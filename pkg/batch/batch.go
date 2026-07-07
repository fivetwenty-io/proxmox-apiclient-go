// Package batch provides request batching functionality for the PVE API
// client: collect Requests into a Batch, then run them concurrently (with a
// configurable concurrency limit and per-request retry) via an Executor.
//
// This is a standalone opt-in utility: it is not wired into pkg/client.Client
// and pkg/client.Client does not use it internally. Construct a batch.Batch
// and batch.Executor directly, using an *http.Client (or anything
// implementing batch.HTTPClient) as the executor's transport, when you want
// to fan out multiple independent HTTP calls together.
package batch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/constants"
)

var (
	ErrBatchSizeLimitReached    = errors.New("batch size limit reached")
	ErrDuplicateRequestID       = errors.New("duplicate request ID")
	ErrBatchStoppedHighFailures = errors.New("batch execution stopped due to high failure rate")
)

// Request represents a single request in a batch.
type Request struct {
	ID      string                 `json:"id"`
	Method  string                 `json:"method"`
	Path    string                 `json:"path"`
	Params  map[string]interface{} `json:"params,omitempty"`
	Headers map[string]string      `json:"headers,omitempty"`
	Body    interface{}            `json:"body,omitempty"`
}

// Response represents a single response in a batch.
type Response struct {
	ID         string          `json:"id"`
	StatusCode int             `json:"status_code"`
	Headers    http.Header     `json:"headers,omitempty"`
	Body       json.RawMessage `json:"body,omitempty"`
	Error      string          `json:"error,omitempty"`
	Duration   time.Duration   `json:"duration,omitempty"`
}

// Result represents the result of a batch execution.
type Result struct {
	Responses    map[string]*Response
	TotalTime    time.Duration
	SuccessCount int
	FailureCount int
}

// Batch manages a collection of requests to be executed together.
type Batch struct {
	requests  []*Request
	responses map[string]*Response
	config    *Config
	mu        sync.Mutex
}

// Config represents batch configuration.
type Config struct {
	// MaxBatchSize is the maximum number of requests in a batch.
	MaxBatchSize int

	// MaxConcurrency is the maximum number of concurrent requests.
	MaxConcurrency int

	// Timeout is the maximum time to wait for batch completion.
	Timeout time.Duration

	// RetryFailedRequests indicates whether to retry failed requests.
	RetryFailedRequests bool

	// MaxRetries is the maximum number of retries for failed requests.
	MaxRetries int
}

// DefaultConfig returns the default batch configuration.
func DefaultConfig() *Config {
	return &Config{
		MaxBatchSize:        constants.DefaultMaxBatchSize,
		MaxConcurrency:      constants.DefaultMaxConcurrency,
		Timeout:             constants.BatchTimeout(),
		RetryFailedRequests: true,
		MaxRetries:          constants.DefaultMaxRetries,
	}
}

// New creates a new batch with the given configuration.
func New(config *Config) *Batch {
	if config == nil {
		config = DefaultConfig()
	}

	return &Batch{
		requests:  make([]*Request, 0),
		responses: make(map[string]*Response),
		config:    config,
		mu:        sync.Mutex{},
	}
}

// Add adds a request to the batch.
func (b *Batch) Add(req *Request) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if len(b.requests) >= b.config.MaxBatchSize {
		return fmt.Errorf("%w (%d)", ErrBatchSizeLimitReached, b.config.MaxBatchSize)
	}

	if req.ID == "" {
		req.ID = fmt.Sprintf("req-%d-%d", time.Now().Unix(), len(b.requests))
	}

	// Check for duplicate ID
	for _, existing := range b.requests {
		if existing.ID == req.ID {
			return fmt.Errorf("%w: %s", ErrDuplicateRequestID, req.ID)
		}
	}

	b.requests = append(b.requests, req)

	return nil
}

// AddMultiple adds multiple requests to the batch.
func (b *Batch) AddMultiple(requests ...*Request) error {
	for _, req := range requests {
		err := b.Add(req)
		if err != nil {
			return err
		}
	}

	return nil
}

// Size returns the number of requests in the batch.
func (b *Batch) Size() int {
	b.mu.Lock()
	defer b.mu.Unlock()

	return len(b.requests)
}

// Clear removes all requests from the batch.
func (b *Batch) Clear() {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.requests = make([]*Request, 0)
	b.responses = make(map[string]*Response)
}

// Executor handles the execution of batches.
type Executor struct {
	client HTTPClient
	config *Config
}

// HTTPClient interface for executing HTTP requests.
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// NewExecutor creates a new batch executor.
func NewExecutor(client HTTPClient, config *Config) *Executor {
	if config == nil {
		config = DefaultConfig()
	}

	return &Executor{
		client: client,
		config: config,
	}
}

// Execute executes a batch of requests.
func (e *Executor) Execute(ctx context.Context, batch *Batch) (*Result, error) {
	return e.ExecuteWithCallback(ctx, batch, nil)
}

// CallbackFunc is called after each request completes.
type CallbackFunc func(req *Request, resp *Response)

// ExecuteWithCallback executes a batch with a callback for each response.
func (e *Executor) ExecuteWithCallback(ctx context.Context, batch *Batch, callback CallbackFunc) (*Result, error) {
	if batch.Size() == 0 {
		return e.createEmptyResult(), nil
	}

	ctx, cancel := e.createContextWithTimeout(ctx)
	if cancel != nil {
		defer cancel()
	}

	start := time.Now()
	result := e.createEmptyResult()

	sem := make(chan struct{}, e.config.MaxConcurrency)

	var (
		waitGroup sync.WaitGroup
		mutex     sync.Mutex
	)

	for _, req := range batch.requests {
		// Acquire a concurrency slot before spawning so a large batch does not
		// create one goroutine per request up front; block here instead.
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			e.handleContextCancellation(ctx, req, result, &mutex)

			continue
		}

		waitGroup.Add(1)

		go func(request *Request) {
			defer waitGroup.Done()
			defer func() { <-sem }()

			e.processRequest(ctx, request, result, &mutex, callback)
		}(req)
	}

	waitGroup.Wait()

	result.TotalTime = time.Since(start)

	return result, nil
}

func (e *Executor) createEmptyResult() *Result {
	return &Result{
		Responses:    make(map[string]*Response),
		TotalTime:    time.Duration(0),
		SuccessCount: 0,
		FailureCount: 0,
	}
}

func (e *Executor) createContextWithTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if e.config.Timeout > 0 {
		return context.WithTimeout(ctx, e.config.Timeout)
	}

	return ctx, nil
}

func (e *Executor) processRequest(ctx context.Context, request *Request, result *Result, mutex *sync.Mutex, callback CallbackFunc) {
	resp := e.executeWithRetry(ctx, request)

	mutex.Lock()

	result.Responses[request.ID] = resp
	if resp.Error == "" {
		result.SuccessCount++
	} else {
		result.FailureCount++
	}

	mutex.Unlock()

	if callback != nil {
		callback(request, resp)
	}
}

func (e *Executor) handleContextCancellation(ctx context.Context, request *Request, result *Result, mutex *sync.Mutex) {
	mutex.Lock()

	result.Responses[request.ID] = &Response{
		ID:         request.ID,
		StatusCode: 0,
		Headers:    nil,
		Body:       nil,
		Error:      ctx.Err().Error(),
		Duration:   time.Duration(0),
	}
	result.FailureCount++

	mutex.Unlock()
}

func (e *Executor) executeWithRetry(ctx context.Context, req *Request) *Response {
	var lastResp *Response

	maxAttempts := 1
	if e.config.RetryFailedRequests {
		maxAttempts = e.config.MaxRetries + 1
	}

	for attempt := range maxAttempts {
		if attempt > 0 {
			// Exponential backoff
			backoff := time.Duration(attempt*attempt) * time.Second
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return &Response{
					ID:         req.ID,
					StatusCode: 0,
					Headers:    nil,
					Body:       nil,
					Error:      ctx.Err().Error(),
					Duration:   time.Duration(0),
				}
			}
		}

		resp := e.executeRequest(ctx, req)
		lastResp = resp

		// Success or non-retryable error
		if resp.Error == "" || !isRetryable(resp.StatusCode) {
			return resp
		}
	}

	return lastResp
}

func (e *Executor) executeRequest(ctx context.Context, req *Request) *Response {
	start := time.Now()

	// Build HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.Path, nil)
	if err != nil {
		return &Response{
			ID:         req.ID,
			StatusCode: 0,
			Headers:    nil,
			Body:       nil,
			Error:      err.Error(),
			Duration:   time.Since(start),
		}
	}

	// Add headers
	for k, v := range req.Headers {
		httpReq.Header.Set(k, v)
	}

	// Execute request
	httpResp, err := e.client.Do(httpReq)
	if err != nil {
		return &Response{
			ID:         req.ID,
			StatusCode: 0,
			Headers:    nil,
			Body:       nil,
			Error:      err.Error(),
			Duration:   time.Since(start),
		}
	}

	defer func() {
		_ = httpResp.Body.Close()
	}()

	// Read response body
	var body json.RawMessage

	err = json.NewDecoder(httpResp.Body).Decode(&body)
	if err != nil {
		if errors.Is(err, io.EOF) {
			// Empty body (e.g. 204 No Content): not an error.
			body = json.RawMessage("null")
		} else {
			// Body present but not valid JSON. Surface it as an error rather
			// than silently counting a malformed response as a success.
			return &Response{
				ID:         req.ID,
				StatusCode: httpResp.StatusCode,
				Headers:    httpResp.Header,
				Body:       nil,
				Error:      fmt.Sprintf("failed to decode response body: %v", err),
				Duration:   time.Since(start),
			}
		}
	}

	return &Response{
		ID:         req.ID,
		StatusCode: httpResp.StatusCode,
		Headers:    httpResp.Header,
		Body:       body,
		Error:      "",
		Duration:   time.Since(start),
	}
}

func isRetryable(statusCode int) bool {
	// Retry on 5xx errors and specific 4xx errors
	return statusCode >= 500 || statusCode == 429 || statusCode == 408
}

// Builder provides a fluent interface for building batches.
type Builder struct {
	batch *Batch
}

// NewBuilder creates a new batch builder.
func NewBuilder(config *Config) *Builder {
	return &Builder{
		batch: New(config),
	}
}

// AddRequest adds a request to the batch being built.
func (b *Builder) AddRequest(method, path string) *Builder {
	_ = b.batch.Add(&Request{
		ID:      "",
		Method:  method,
		Path:    path,
		Params:  nil,
		Headers: nil,
		Body:    nil,
	})

	return b
}

// AddRequestWithParams adds a request with parameters.
func (b *Builder) AddRequestWithParams(method, path string, params map[string]interface{}) *Builder {
	_ = b.batch.Add(&Request{
		ID:      "",
		Method:  method,
		Path:    path,
		Params:  params,
		Headers: nil,
		Body:    nil,
	})

	return b
}

// Build returns the built batch.
func (b *Builder) Build() *Batch {
	return b.batch
}

// Pipeline allows chaining multiple batches.
type Pipeline struct {
	batches  []*Batch
	executor *Executor
}

// NewPipeline creates a new pipeline.
func NewPipeline(executor *Executor) *Pipeline {
	return &Pipeline{
		batches:  make([]*Batch, 0),
		executor: executor,
	}
}

// AddBatch adds a batch to the pipeline.
func (p *Pipeline) AddBatch(batch *Batch) *Pipeline {
	p.batches = append(p.batches, batch)

	return p
}

// Execute executes all batches in the pipeline sequentially.
func (p *Pipeline) Execute(ctx context.Context) ([]*Result, error) {
	results := make([]*Result, 0, len(p.batches))

	for _, batch := range p.batches {
		result, err := p.executor.Execute(ctx, batch)
		if err != nil {
			return results, err
		}

		results = append(results, result)

		// Stop if too many failures
		if result.FailureCount > result.SuccessCount {
			return results, ErrBatchStoppedHighFailures
		}
	}

	return results, nil
}
