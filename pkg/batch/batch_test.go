package batch_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/batch"
)

const (
	methodGET = "GET"
	pathTest  = "/test"
	pathTest1 = "/test1"
	pathTest2 = "/test2"
)

func TestNewBatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config *batch.Config
	}{
		{
			name:   "default config",
			config: nil,
		},
		{
			name: "custom config",
			config: &batch.Config{
				MaxBatchSize:        50,
				MaxConcurrency:      5,
				Timeout:             2 * time.Minute,
				RetryFailedRequests: false,
				MaxRetries:          0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			batchInstance := batch.New(tt.config)
			if batchInstance == nil {
				t.Fatal("New() returned nil")
			}

			if batchInstance.Size() != 0 {
				t.Errorf("Size() = %d, want 0", batchInstance.Size())
			}
		})
	}
}

func TestBatchAdd(t *testing.T) {
	t.Parallel()

	batchInstance := batch.New(&batch.Config{
		MaxBatchSize:        3,
		MaxConcurrency:      0,
		Timeout:             time.Duration(0),
		RetryFailedRequests: false,
		MaxRetries:          0,
	})

	// Add requests
	for i := range 3 {
		req := &batch.Request{
			ID:      fmt.Sprintf("req-%d", i),
			Method:  methodGET,
			Path:    fmt.Sprintf("/api/test/%d", i),
			Params:  nil,
			Headers: nil,
			Body:    nil,
		}

		err := batchInstance.Add(req)
		if err != nil {
			t.Fatalf("Add() error = %v", err)
		}
	}

	if batchInstance.Size() != 3 {
		t.Errorf("Size() = %d, want 3", batchInstance.Size())
	}

	// Try to exceed limit
	err := batchInstance.Add(&batch.Request{
		ID:      "",
		Method:  methodGET,
		Path:    "/overflow",
		Params:  nil,
		Headers: nil,
		Body:    nil,
	})
	if err == nil {
		t.Fatal("Add() should fail when exceeding MaxBatchSize")
	}
}

func TestBatchAddDuplicate(t *testing.T) {
	t.Parallel()

	batchInstance := batch.New(batch.DefaultConfig())

	req1 := &batch.Request{
		ID:      "duplicate",
		Method:  methodGET,
		Path:    pathTest1,
		Params:  nil,
		Headers: nil,
		Body:    nil,
	}
	req2 := &batch.Request{
		ID:      "duplicate",
		Method:  methodGET,
		Path:    pathTest2,
		Params:  nil,
		Headers: nil,
		Body:    nil,
	}

	err := batchInstance.Add(req1)
	if err != nil {
		t.Fatalf("Add() first request error = %v", err)
	}

	err = batchInstance.Add(req2)
	if err == nil {
		t.Fatal("Add() should fail for duplicate ID")
	}
}

func TestBatchAddMultiple(t *testing.T) {
	t.Parallel()

	batchInstance := batch.New(batch.DefaultConfig())

	requests := []*batch.Request{
		{Method: methodGET, Path: pathTest1},
		{Method: "POST", Path: pathTest2},
		{Method: "PUT", Path: "/test3"},
	}

	err := batchInstance.AddMultiple(requests...)
	if err != nil {
		t.Fatalf("AddMultiple() error = %v", err)
	}

	if batchInstance.Size() != 3 {
		t.Errorf("Size() = %d, want 3", batchInstance.Size())
	}
}

func TestBatchClear(t *testing.T) {
	t.Parallel()

	batchInstance := batch.New(batch.DefaultConfig())

	// Add some requests
	err := batchInstance.Add(&batch.Request{
		ID:      "",
		Method:  methodGET,
		Path:    pathTest,
		Params:  nil,
		Headers: nil,
		Body:    nil,
	})
	if err != nil {
		t.Fatalf("Failed to add request: %v", err)
	}

	err = batchInstance.Add(&batch.Request{
		ID:      "",
		Method:  "POST",
		Path:    pathTest,
		Params:  nil,
		Headers: nil,
		Body:    nil,
	})
	if err != nil {
		t.Fatalf("Failed to add request: %v", err)
	}

	if batchInstance.Size() != 2 {
		t.Errorf("Size() before clear = %d, want 2", batchInstance.Size())
	}

	batchInstance.Clear()

	if batchInstance.Size() != 0 {
		t.Errorf("Size() after clear = %d, want 0", batchInstance.Size())
	}
}

type mockHTTPClient struct {
	responses map[string]*http.Response
	callCount int32
}

func (m *mockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	atomic.AddInt32(&m.callCount, 1)

	// Return predefined response if exists
	if resp, ok := m.responses[req.URL.Path]; ok {
		return resp, nil
	}

	// Default response
	return &http.Response{
		Status:           "",
		StatusCode:       http.StatusOK,
		Proto:            "",
		ProtoMajor:       0,
		ProtoMinor:       0,
		Header:           make(http.Header),
		Body:             http.NoBody,
		ContentLength:    0,
		TransferEncoding: nil,
		Close:            false,
		Uncompressed:     false,
		Trailer:          nil,
		Request:          nil,
		TLS:              nil,
	}, nil
}

func createTestBatch(t *testing.T, requests []string) *batch.Batch {
	t.Helper()

	batchInstance := batch.New(batch.DefaultConfig())
	for i, path := range requests {
		err := batchInstance.Add(&batch.Request{
			ID:      strconv.Itoa(i + 1),
			Method:  methodGET,
			Path:    path,
			Params:  nil,
			Headers: nil,
			Body:    nil,
		})
		if err != nil {
			t.Fatalf("Failed to add request: %v", err)
		}
	}

	return batchInstance
}

func createMockExecutor() *batch.Executor {
	client := &mockHTTPClient{
		responses: make(map[string]*http.Response),
		callCount: 0,
	}

	return batch.NewExecutor(client, batch.DefaultConfig())
}

func TestExecutor(t *testing.T) {
	t.Parallel()

	executor := createMockExecutor()
	batchInstance := createTestBatch(t, []string{pathTest1, pathTest2, "/test3"})

	ctx := context.Background()

	result, err := executor.Execute(ctx, batchInstance)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.SuccessCount != 3 {
		t.Errorf("SuccessCount = %d, want 3", result.SuccessCount)
	}

	if result.FailureCount != 0 {
		t.Errorf("FailureCount = %d, want 0", result.FailureCount)
	}

	if len(result.Responses) != 3 {
		t.Errorf("len(Responses) = %d, want 3", len(result.Responses))
	}
}

func TestExecutorWithCallback(t *testing.T) {
	t.Parallel()

	client := &mockHTTPClient{
		responses: make(map[string]*http.Response),
		callCount: 0,
	}

	executor := batch.NewExecutor(client, batch.DefaultConfig())

	batchInstance := batch.New(batch.DefaultConfig())

	err := batchInstance.Add(&batch.Request{
		ID:      "1",
		Method:  methodGET,
		Path:    pathTest,
		Params:  nil,
		Headers: nil,
		Body:    nil,
	})
	if err != nil {
		t.Fatalf("Failed to add request: %v", err)
	}

	callbackCalled := false
	callback := func(req *batch.Request, resp *batch.Response) {
		callbackCalled = true

		if req.ID != resp.ID {
			t.Errorf("Callback: req.ID = %s, resp.ID = %s", req.ID, resp.ID)
		}
	}

	ctx := context.Background()

	_, err = executor.ExecuteWithCallback(ctx, batchInstance, callback)
	if err != nil {
		t.Fatalf("ExecuteWithCallback() error = %v", err)
	}

	if !callbackCalled {
		t.Error("Callback was not called")
	}
}

func setupConcurrencyTestServer(t *testing.T, maxConcurrent, currentConcurrent *int32) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		current := atomic.AddInt32(currentConcurrent, 1)

		for {
			maxVal := atomic.LoadInt32(maxConcurrent)
			if current <= maxVal || atomic.CompareAndSwapInt32(maxConcurrent, maxVal, current) {
				break
			}
		}

		time.Sleep(50 * time.Millisecond)
		atomic.AddInt32(currentConcurrent, -1)

		err := json.NewEncoder(writer).Encode(map[string]string{"status": "ok"})
		if err != nil {
			t.Errorf("Failed to encode response: %v", err)
		}
	}))
}

func createConcurrencyTestBatch(t *testing.T, serverURL string, requestCount int) *batch.Batch {
	t.Helper()

	config := &batch.Config{
		MaxBatchSize:        10,
		MaxConcurrency:      3,
		Timeout:             5 * time.Second,
		RetryFailedRequests: false,
		MaxRetries:          0,
	}
	batchInstance := batch.New(config)

	for i := range requestCount {
		err := batchInstance.Add(&batch.Request{
			ID:      fmt.Sprintf("req-%d", i),
			Method:  methodGET,
			Path:    serverURL,
			Params:  nil,
			Headers: nil,
			Body:    nil,
		})
		if err != nil {
			t.Fatalf("Failed to add request: %v", err)
		}
	}

	return batchInstance
}

func TestExecutorConcurrency(t *testing.T) {
	t.Parallel()

	var maxConcurrent, currentConcurrent int32

	server := setupConcurrencyTestServer(t, &maxConcurrent, &currentConcurrent)
	defer server.Close()

	client := &http.Client{}
	config := &batch.Config{
		MaxBatchSize:        10,
		MaxConcurrency:      3,
		Timeout:             5 * time.Second,
		RetryFailedRequests: false,
		MaxRetries:          0,
	}
	executor := batch.NewExecutor(client, config)
	batchInstance := createConcurrencyTestBatch(t, server.URL, 10)

	ctx := context.Background()

	result, err := executor.Execute(ctx, batchInstance)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if result.SuccessCount != 10 {
		t.Errorf("SuccessCount = %d, want 10", result.SuccessCount)
	}

	if maxConcurrent > 3 {
		t.Errorf("Max concurrent requests = %d, want <= 3", maxConcurrent)
	}
}

func TestExecutorTimeout(t *testing.T) {
	t.Parallel()
	// Create slow server
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		time.Sleep(200 * time.Millisecond)
		writer.WriteHeader(http.StatusOK)

		err := json.NewEncoder(writer).Encode(map[string]string{"status": "ok"})
		if err != nil {
			t.Errorf("Failed to encode response: %v", err)
		}
	}))
	defer server.Close()

	client := &http.Client{
		Transport:     nil,
		CheckRedirect: nil,
		Jar:           nil,
		Timeout:       time.Duration(0),
	}
	config := &batch.Config{
		MaxBatchSize:        10,
		MaxConcurrency:      1,
		Timeout:             100 * time.Millisecond,
		RetryFailedRequests: false,
		MaxRetries:          0,
	}
	executor := batch.NewExecutor(client, config)

	batchInstance := batch.New(config)

	err := batchInstance.Add(&batch.Request{
		ID:      "1",
		Method:  methodGET,
		Path:    server.URL,
		Params:  nil,
		Headers: nil,
		Body:    nil,
	})
	if err != nil {
		t.Fatalf("Failed to add request: %v", err)
	}

	ctx := context.Background()

	var result *batch.Result

	result, err = executor.Execute(ctx, batchInstance)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Should have failed due to timeout
	if result.FailureCount != 1 {
		t.Errorf("FailureCount = %d, want 1", result.FailureCount)
	}
}

func TestBuilder(t *testing.T) {
	t.Parallel()

	builder := batch.NewBuilder(batch.DefaultConfig())

	batchInstance := builder.
		AddRequest(methodGET, pathTest1).
		AddRequest("POST", pathTest2).
		AddRequestWithParams("PUT", "/test3", map[string]interface{}{
			"key": "value",
		}).
		Build()

	if batchInstance.Size() != 3 {
		t.Errorf("Size() = %d, want 3", batchInstance.Size())
	}
}

func createPipelineBatch(t *testing.T, batchPrefix string, paths []string) *batch.Batch {
	t.Helper()

	batchInstance := batch.New(batch.DefaultConfig())
	for i, path := range paths {
		err := batchInstance.Add(&batch.Request{
			ID:      fmt.Sprintf("%s-%d", batchPrefix, i+1),
			Method:  methodGET,
			Path:    path,
			Params:  nil,
			Headers: nil,
			Body:    nil,
		})
		if err != nil {
			t.Fatalf("Failed to add request: %v", err)
		}
	}

	return batchInstance
}

func TestPipeline(t *testing.T) {
	t.Parallel()

	executor := createMockExecutor()
	pipeline := batch.NewPipeline(executor)

	batch1 := createPipelineBatch(t, "1", []string{"/batch1/test1", "/batch1/test2"})
	batch2 := createPipelineBatch(t, "2", []string{"/batch2/test1", "/batch2/test2"})

	pipeline.AddBatch(batch1).AddBatch(batch2)

	ctx := context.Background()

	results, err := pipeline.Execute(ctx)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	if len(results) != 2 {
		t.Errorf("len(results) = %d, want 2", len(results))
	}

	if results[0].SuccessCount != 2 {
		t.Errorf("Batch 1 SuccessCount = %d, want 2", results[0].SuccessCount)
	}

	// Check second batch results
	if results[1].SuccessCount != 2 {
		t.Errorf("Batch 2 SuccessCount = %d, want 2", results[1].SuccessCount)
	}
}
