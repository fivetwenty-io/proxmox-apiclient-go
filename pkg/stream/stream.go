package stream

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/constants"
)

const formatJSONLines = "jsonlines"

var (
	ErrStreamClosed           = errors.New("stream is closed")
	ErrItemSizeExceedsMaximum = errors.New("item size exceeds maximum")
	ErrEmptyData              = errors.New("no data to decode")
	ErrNilItem                = errors.New("item is nil")
)

// Stream represents a streaming response handler.
type Stream struct {
	reader    io.ReadCloser
	decoder   Decoder
	buffer    *bufio.Reader
	config    *Config
	closed    bool
	mu        sync.RWMutex
	metrics   *streamMetrics
	errorChan chan error
}

// Config represents stream configuration.
type Config struct {
	// BufferSize is the size of the read buffer.
	BufferSize int

	// MaxItemSize is the maximum size of a single item.
	MaxItemSize int

	// ReadTimeout is the timeout for read operations.
	ReadTimeout time.Duration

	// Format is the expected stream format (json, jsonlines, csv).
	Format string

	// Delimiter is used for delimited formats.
	Delimiter string
}

// DefaultConfig returns the default stream configuration.
func DefaultConfig() *Config {
	return &Config{
		BufferSize:  constants.LargeBufferSize,
		MaxItemSize: constants.StreamMaxItemSize, // 1MB
		ReadTimeout: constants.DefaultClientTimeout(),
		Format:      formatJSONLines,
		Delimiter:   "\n",
	}
}

// Metrics contains stream metrics.
type Metrics struct {
	ItemsRead    int64
	BytesRead    int64
	ErrorCount   int64
	ReadTime     time.Duration
	LastReadTime time.Time
}

// streamMetrics is the internal, mutex-guarded metrics state. Metrics (the
// embedded value) is the pure-data snapshot returned by Stream.Metrics, so
// callers copy a snapshot without copying a lock.
type streamMetrics struct {
	Metrics

	mu sync.RWMutex
}

// Decoder interface for decoding streamed data.
type Decoder interface {
	Decode(data []byte) (interface{}, error)
	SupportsPartial() bool
}

// JSONDecoder decodes JSON data.
type JSONDecoder struct{}

func (d *JSONDecoder) Decode(data []byte) (interface{}, error) {
	var result interface{}

	err := json.Unmarshal(data, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON data: %w", err)
	}

	return result, nil
}

func (d *JSONDecoder) SupportsPartial() bool {
	return false
}

// JSONLinesDecoder decodes JSON Lines format.
type JSONLinesDecoder struct{}

func (d *JSONLinesDecoder) Decode(data []byte) (interface{}, error) {
	// Trim whitespace
	data = []byte(strings.TrimSpace(string(data)))
	if len(data) == 0 {
		return nil, ErrEmptyData
	}

	var result interface{}

	err := json.Unmarshal(data, &result)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal JSON Lines data: %w", err)
	}

	return result, nil
}

func (d *JSONLinesDecoder) SupportsPartial() bool {
	return true
}

// New creates a new stream from an io.ReadCloser.
func New(reader io.ReadCloser, config *Config) *Stream {
	if config == nil {
		config = DefaultConfig()
	}

	// Select decoder based on format
	var decoder Decoder

	switch config.Format {
	case "json":
		decoder = &JSONDecoder{}
	case formatJSONLines:
		decoder = &JSONLinesDecoder{}
	default:
		decoder = &JSONLinesDecoder{}
	}

	stream := &Stream{
		reader:    reader,
		decoder:   decoder,
		buffer:    bufio.NewReaderSize(reader, config.BufferSize),
		config:    config,
		metrics:   &streamMetrics{},
		errorChan: make(chan error, 1),
	}

	return stream
}

// NewFromResponse creates a stream from an HTTP response.
func NewFromResponse(resp *http.Response, config *Config) *Stream {
	return New(resp.Body, config)
}

// Read reads the next item from the stream.
func (s *Stream) Read() (interface{}, error) {
	err := s.checkStreamState()
	if err != nil {
		return nil, err
	}

	start := time.Now()
	defer s.updateReadMetrics(start)

	data, err := s.readStreamData()
	if err != nil {
		return nil, err
	}

	err = s.validateDataSize(data)
	if err != nil {
		return nil, err
	}

	s.updateBytesRead(int64(len(data)))

	item, err := s.decodeStreamData(data)
	if err != nil {
		return nil, err
	}

	s.updateItemsRead()

	return item, nil
}

// ReadAll reads all items from the stream.
func (s *Stream) ReadAll() ([]interface{}, error) {
	var items []interface{}

	for {
		item, err := s.Read()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return items, err
		}

		if item != nil {
			items = append(items, item)
		}
	}

	return items, nil
}

// ReadN reads up to n items from the stream.
func (s *Stream) ReadN(n int) ([]interface{}, error) {
	items := make([]interface{}, 0, n)

	for range n {
		item, err := s.Read()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return items, err
		}

		if item != nil {
			items = append(items, item)
		}
	}

	return items, nil
}

// Channel returns a channel that yields items from the stream.
func (s *Stream) Channel(ctx context.Context) <-chan interface{} {
	channel := make(chan interface{})

	go func() {
		defer close(channel)

		for {
			select {
			case <-ctx.Done():
				return
			default:
				item, err := s.Read()
				if errors.Is(err, io.EOF) {
					return
				}

				if err != nil {
					s.trySendError(err)

					return
				}

				if item != nil {
					select {
					case channel <- item:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()

	return channel
}

// Process processes items with a callback function.
func (s *Stream) Process(ctx context.Context, processFunc func(interface{}) error) error {
	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("context cancelled during processing: %w", ctx.Err())
		default:
			item, err := s.Read()
			if errors.Is(err, io.EOF) {
				return nil
			}

			if err != nil {
				return fmt.Errorf("failed to read item for processing: %w", err)
			}

			if item != nil {
				err := processFunc(item)
				if err != nil {
					return err
				}
			}
		}
	}
}

// ProcessBatch processes items in batches.
func (s *Stream) ProcessBatch(ctx context.Context, batchSize int, processFunc func([]interface{}) error) error {
	batch := make([]interface{}, 0, batchSize)

	for {
		select {
		case <-ctx.Done():
			// Process remaining batch
			if len(batch) > 0 {
				return processFunc(batch)
			}

			return fmt.Errorf("context cancelled during batch processing: %w", ctx.Err())
		default:
			item, err := s.Read()
			if errors.Is(err, io.EOF) {
				// Process final batch
				if len(batch) > 0 {
					return processFunc(batch)
				}

				return nil
			}

			if err != nil {
				return fmt.Errorf("failed to process batch item: %w", err)
			}

			if item != nil {
				batch = append(batch, item)
				if len(batch) >= batchSize {
					err := processFunc(batch)
					if err != nil {
						return err
					}

					batch = make([]interface{}, 0, batchSize)
				}
			}
		}
	}
}

// Errors returns the error channel.
func (s *Stream) Errors() <-chan error {
	return s.errorChan
}

// Metrics returns the current stream metrics.
func (s *Stream) Metrics() Metrics {
	s.metrics.mu.RLock()
	defer s.metrics.mu.RUnlock()

	return s.metrics.Metrics
}

// Close closes the stream.
func (s *Stream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	s.closed = true
	close(s.errorChan)

	err := s.reader.Close()
	if err != nil {
		return fmt.Errorf("failed to close stream reader: %w", err)
	}

	return nil
}

func (s *Stream) checkStreamState() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return ErrStreamClosed
	}

	return nil
}

func (s *Stream) updateReadMetrics(start time.Time) {
	s.metrics.mu.Lock()
	defer s.metrics.mu.Unlock()

	s.metrics.ReadTime += time.Since(start)
	s.metrics.LastReadTime = time.Now()
}

func (s *Stream) readStreamData() ([]byte, error) {
	if s.config.Format == formatJSONLines || s.decoder.SupportsPartial() {
		return s.readStreamLine()
	}

	return s.readStreamAll()
}

func (s *Stream) readStreamLine() ([]byte, error) {
	data, err := s.buffer.ReadBytes('\n')
	if err == nil {
		return data, nil
	}

	if !errors.Is(err, io.EOF) {
		s.recordError(err)

		return nil, fmt.Errorf("failed to read stream line: %w", err)
	}

	// io.EOF with no data means the stream is exhausted.
	if len(data) == 0 {
		return nil, io.EOF
	}

	return data, nil
}

func (s *Stream) readStreamAll() ([]byte, error) {
	data, err := io.ReadAll(s.buffer)
	if err != nil {
		s.recordError(err)

		return nil, fmt.Errorf("failed to read stream content: %w", err)
	}

	return data, nil
}

func (s *Stream) validateDataSize(data []byte) error {
	if len(data) > s.config.MaxItemSize {
		err := fmt.Errorf("%w: %d exceeds maximum %d", ErrItemSizeExceedsMaximum, len(data), s.config.MaxItemSize)
		s.recordError(err)

		return err
	}

	return nil
}

func (s *Stream) updateBytesRead(bytes int64) {
	s.metrics.mu.Lock()
	defer s.metrics.mu.Unlock()

	s.metrics.BytesRead += bytes
}

func (s *Stream) decodeStreamData(data []byte) (interface{}, error) {
	item, err := s.decoder.Decode(data)
	if err != nil {
		s.recordError(err)

		return nil, fmt.Errorf("failed to decode stream data: %w", err)
	}

	return item, nil
}

func (s *Stream) updateItemsRead() {
	s.metrics.mu.Lock()
	defer s.metrics.mu.Unlock()

	s.metrics.ItemsRead++
}

func (s *Stream) recordError(err error) {
	s.metrics.mu.Lock()
	s.metrics.ErrorCount++
	s.metrics.mu.Unlock()

	s.trySendError(err)
}

// trySendError performs a non-blocking send on errorChan while holding the read
// lock and only when the stream is open. Close acquires the write lock before
// closing errorChan, so the read lock here guarantees the channel cannot be
// closed mid-send, which would otherwise panic.
func (s *Stream) trySendError(err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.closed {
		return
	}

	select {
	case s.errorChan <- err:
	default:
	}
}

// Reader adapts a Stream to the io.Reader interface. Each stream item is
// marshaled to JSON and emitted followed by a newline, so a Reader's output
// is a valid JSON Lines byte stream regardless of the underlying Stream's
// configured Format; callers who need the original wire format should read
// from the Stream directly instead of through Reader.
type Reader struct {
	stream    *Stream
	remainder []byte // undelivered bytes from the most recently marshaled item
	err       error  // sticky terminal error once the stream is exhausted or fails
}

// NewReader creates a new stream reader.
func NewReader(stream *Stream) *Reader {
	return &Reader{stream: stream}
}

// Read implements io.Reader. It satisfies the io.Reader contract: when the
// caller's buffer is smaller than the next marshaled item, the remainder is
// buffered internally and served on subsequent calls before the next item is
// pulled from the stream, so no bytes are ever dropped.
func (r *Reader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}

	if len(r.remainder) == 0 {
		if r.err != nil {
			return 0, r.err
		}

		item, err := r.stream.Read()
		if err != nil {
			r.err = err

			return 0, err
		}

		data, err := json.Marshal(item)
		if err != nil {
			return 0, fmt.Errorf("failed to marshal item to JSON: %w", err)
		}

		data = append(data, '\n')
		r.remainder = data
	}

	n := copy(buffer, r.remainder)
	r.remainder = r.remainder[n:]

	return n, nil
}

// Transform applies a transformation function to stream items.
type Transform struct {
	stream *Stream
	fn     func(interface{}) (interface{}, error)
}

// NewTransform creates a new transform stream.
func NewTransform(stream *Stream, fn func(interface{}) (interface{}, error)) *Transform {
	return &Transform{
		stream: stream,
		fn:     fn,
	}
}

// Read reads and transforms the next item.
func (t *Transform) Read() (interface{}, error) {
	item, err := t.stream.Read()
	if err != nil {
		return nil, err
	}

	if item == nil {
		return nil, ErrNilItem
	}

	return t.fn(item)
}

// Filter filters stream items based on a predicate.
type Filter struct {
	stream    *Stream
	predicate func(interface{}) bool
}

// NewFilter creates a new filter stream.
func NewFilter(stream *Stream, predicate func(interface{}) bool) *Filter {
	return &Filter{
		stream:    stream,
		predicate: predicate,
	}
}

// Read reads the next item that matches the filter.
func (f *Filter) Read() (interface{}, error) {
	for {
		item, err := f.stream.Read()
		if err != nil {
			return nil, err
		}

		if item != nil && f.predicate(item) {
			return item, nil
		}
	}
}
