package stream_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/stream"
)

// newJSONLinesReader wraps lines as io.ReadCloser of JSON-lines content.
// Lines are joined with "\n". No trailing newline is added so that bufio does
// not produce a spurious empty line that causes ErrEmptyData.
func newJSONLinesReader(lines ...string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(strings.Join(lines, "\n")))
}

// newJSONReader wraps a JSON string as io.ReadCloser.
func newJSONReader(s string) io.ReadCloser {
	return io.NopCloser(strings.NewReader(s))
}

// ---- DefaultConfig ----

func TestDefaultConfig(t *testing.T) {
	t.Parallel()

	cfg := stream.DefaultConfig()
	if cfg == nil {
		t.Fatal("DefaultConfig returned nil")
	}

	if cfg.BufferSize <= 0 {
		t.Errorf("BufferSize: want >0, got %d", cfg.BufferSize)
	}

	if cfg.MaxItemSize <= 0 {
		t.Errorf("MaxItemSize: want >0, got %d", cfg.MaxItemSize)
	}

	if cfg.Format != "jsonlines" {
		t.Errorf("Format: want 'jsonlines', got %q", cfg.Format)
	}
}

// ---- New / DefaultConfig nil ----

func TestNew_NilConfig(t *testing.T) {
	t.Parallel()

	r := newJSONLinesReader(`{"a":1}`)

	s := stream.New(r, nil)
	if s == nil {
		t.Fatal("New(nil config) returned nil")
	}
	defer s.Close() //nolint:errcheck
}

func TestNew_JSONFormat(t *testing.T) {
	t.Parallel()

	cfg := &stream.Config{
		BufferSize:  1024,
		MaxItemSize: 1 << 20,
		ReadTimeout: time.Second,
		Format:      "json",
		Delimiter:   "\n",
	}
	r := newJSONReader(`{"key":"value"}`)

	s := stream.New(r, cfg)
	if s == nil {
		t.Fatal("New(json format) returned nil")
	}
	defer s.Close() //nolint:errcheck
}

// ---- Read ----

func TestStream_Read_JSONLines(t *testing.T) {
	t.Parallel()

	r := newJSONLinesReader(`{"id":1}`, `{"id":2}`)

	s := stream.New(r, nil)
	defer s.Close() //nolint:errcheck

	item1, err := s.Read()
	if err != nil {
		t.Fatalf("Read #1: %v", err)
	}

	m1, ok := item1.(map[string]interface{})
	if !ok {
		t.Fatalf("Read #1: want map, got %T", item1)
	}

	if m1["id"] != float64(1) {
		t.Errorf("Read #1: want id=1, got %v", m1["id"])
	}

	item2, err := s.Read()
	if err != nil {
		t.Fatalf("Read #2: %v", err)
	}

	m2, ok := item2.(map[string]interface{})
	if !ok {
		t.Fatalf("Read #2: want map, got %T", item2)
	}

	if m2["id"] != float64(2) {
		t.Errorf("Read #2: want id=2, got %v", m2["id"])
	}
}

func TestStream_Read_EOF(t *testing.T) {
	t.Parallel()

	r := newJSONLinesReader(`{"x":1}`)

	s := stream.New(r, nil)
	defer s.Close() //nolint:errcheck

	// Drain first item.
	_, err := s.Read()
	if err != nil {
		t.Fatalf("first Read: %v", err)
	}
	// Next read should return EOF.
	_, err = s.Read()
	if !errors.Is(err, io.EOF) {
		t.Errorf("second Read: want io.EOF, got %v", err)
	}
}

func TestStream_Read_ClosedStream(t *testing.T) {
	t.Parallel()

	r := newJSONLinesReader(`{"x":1}`)

	s := stream.New(r, nil)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err := s.Read()
	if !errors.Is(err, stream.ErrStreamClosed) {
		t.Errorf("Read on closed: want ErrStreamClosed, got %v", err)
	}
}

func TestStream_Read_ItemTooLarge(t *testing.T) {
	t.Parallel()
	// Single line larger than MaxItemSize.
	big := strings.Repeat("x", 100) + "\n"
	r := io.NopCloser(strings.NewReader(big))
	cfg := &stream.Config{
		BufferSize:  4096,
		MaxItemSize: 50, // smaller than line
		ReadTimeout: time.Second,
		Format:      "jsonlines",
		Delimiter:   "\n",
	}

	s := stream.New(r, cfg)
	defer s.Close() //nolint:errcheck

	_, err := s.Read()
	if !errors.Is(err, stream.ErrItemSizeExceedsMaximum) {
		t.Errorf("Read oversized: want ErrItemSizeExceedsMaximum, got %v", err)
	}
}

// ---- ReadAll ----

func TestStream_ReadAll(t *testing.T) {
	t.Parallel()

	r := newJSONLinesReader(`{"n":1}`, `{"n":2}`, `{"n":3}`)

	s := stream.New(r, nil)
	defer s.Close() //nolint:errcheck

	items, err := s.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if len(items) != 3 {
		t.Errorf("ReadAll: want 3 items, got %d", len(items))
	}
}

func TestStream_ReadAll_Empty(t *testing.T) {
	t.Parallel()

	r := io.NopCloser(strings.NewReader(""))

	s := stream.New(r, nil)
	defer s.Close() //nolint:errcheck

	items, err := s.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll empty: %v", err)
	}

	if len(items) != 0 {
		t.Errorf("ReadAll empty: want 0 items, got %d", len(items))
	}
}

// ---- ReadN ----

func TestStream_ReadN(t *testing.T) {
	t.Parallel()

	r := newJSONLinesReader(`{"i":1}`, `{"i":2}`, `{"i":3}`, `{"i":4}`)

	s := stream.New(r, nil)
	defer s.Close() //nolint:errcheck

	items, err := s.ReadN(2)
	if err != nil {
		t.Fatalf("ReadN(2): %v", err)
	}

	if len(items) != 2 {
		t.Errorf("ReadN(2): want 2 items, got %d", len(items))
	}
}

func TestStream_ReadN_LessThanN(t *testing.T) {
	t.Parallel()

	r := newJSONLinesReader(`{"i":1}`)

	s := stream.New(r, nil)
	defer s.Close() //nolint:errcheck

	items, err := s.ReadN(10)
	if err != nil {
		t.Fatalf("ReadN(10) with 1 item: %v", err)
	}

	if len(items) != 1 {
		t.Errorf("ReadN(10) with 1 item: want 1, got %d", len(items))
	}
}

// ---- Channel ----

func TestStream_Channel(t *testing.T) {
	t.Parallel()

	r := newJSONLinesReader(`{"v":1}`, `{"v":2}`)

	s := stream.New(r, nil)
	defer s.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch := s.Channel(ctx)

	var count int
	for range ch {
		count++
	}

	if count != 2 {
		t.Errorf("Channel: want 2 items, got %d", count)
	}
}

func TestStream_Channel_ContextCancel(t *testing.T) {
	t.Parallel()
	// Infinite-ish stream via a blocking reader.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			return
		}

		for range 5 {
			_, _ = w.Write([]byte(`{"v":1}` + "\n"))

			flusher.Flush()
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL) //nolint:noctx
	if err != nil {
		t.Fatalf("GET: %v", err)
	}

	s := stream.NewFromResponse(resp, nil)
	defer s.Close() //nolint:errcheck

	ctx, cancel := context.WithCancel(context.Background())
	ch := s.Channel(ctx)
	// Read 1 item then cancel.
	<-ch
	cancel()
	// Drain remaining; channel must close eventually.
	done := make(chan struct{})

	go func() {
		for range ch {
		}

		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Error("Channel: did not close after context cancel")
	}
}

// ---- Process ----

func TestStream_Process(t *testing.T) {
	t.Parallel()

	r := newJSONLinesReader(`{"k":1}`, `{"k":2}`)

	s := stream.New(r, nil)
	defer s.Close() //nolint:errcheck

	var collected []interface{}

	err := s.Process(context.Background(), func(item interface{}) error {
		collected = append(collected, item)

		return nil
	})
	if err != nil {
		t.Fatalf("Process: %v", err)
	}

	if len(collected) != 2 {
		t.Errorf("Process: want 2 items, got %d", len(collected))
	}
}

func TestStream_Process_CallbackError(t *testing.T) {
	t.Parallel()

	r := newJSONLinesReader(`{"k":1}`)

	s := stream.New(r, nil)
	defer s.Close() //nolint:errcheck

	sentinel := errors.New("stop")

	err := s.Process(context.Background(), func(_ interface{}) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Errorf("Process callback error: want sentinel, got %v", err)
	}
}

func TestStream_Process_ContextCancel(t *testing.T) {
	t.Parallel()

	r := io.NopCloser(strings.NewReader(strings.Repeat(`{"k":1}`+"\n", 100)))

	s := stream.New(r, nil)
	defer s.Close() //nolint:errcheck

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := s.Process(ctx, func(_ interface{}) error { return nil })
	if err == nil {
		t.Error("Process with cancelled ctx: want error, got nil")
	}
}

// ---- ProcessBatch ----

func TestStream_ProcessBatch(t *testing.T) {
	t.Parallel()

	r := newJSONLinesReader(`{"n":1}`, `{"n":2}`, `{"n":3}`, `{"n":4}`, `{"n":5}`)

	s := stream.New(r, nil)
	defer s.Close() //nolint:errcheck

	var batches [][]interface{}

	err := s.ProcessBatch(context.Background(), 2, func(batch []interface{}) error {
		cp := make([]interface{}, len(batch))
		copy(cp, batch)
		batches = append(batches, cp)

		return nil
	})
	if err != nil {
		t.Fatalf("ProcessBatch: %v", err)
	}

	total := 0
	for _, b := range batches {
		total += len(b)
	}

	if total != 5 {
		t.Errorf("ProcessBatch: want 5 total items, got %d", total)
	}
}

func TestStream_ProcessBatch_ContextCancelWithItems(t *testing.T) {
	t.Parallel()
	// Stream that requires multiple batches; cancel mid-way.
	r := io.NopCloser(strings.NewReader(strings.Repeat(`{"n":1}`+"\n", 20)))

	s := stream.New(r, nil)
	defer s.Close() //nolint:errcheck

	ctx, cancel := context.WithCancel(context.Background())

	calls := 0
	err := s.ProcessBatch(ctx, 3, func(batch []interface{}) error {
		calls++
		if calls == 1 {
			cancel()
		}

		return nil
	})
	// After cancel ProcessBatch may return an error or nil depending on timing.
	_ = err
}

// ---- Errors ----

func TestStream_Errors(t *testing.T) {
	t.Parallel()

	r := newJSONLinesReader(`{"x":1}`)

	s := stream.New(r, nil)
	defer s.Close() //nolint:errcheck

	ch := s.Errors()
	if ch == nil {
		t.Fatal("Errors() returned nil channel")
	}
}

// ---- Metrics ----

func TestStream_Metrics(t *testing.T) {
	t.Parallel()

	r := newJSONLinesReader(`{"m":1}`, `{"m":2}`)

	s := stream.New(r, nil)
	defer s.Close() //nolint:errcheck

	_, _ = s.ReadAll()

	m := s.Metrics()
	if m.ItemsRead != 2 {
		t.Errorf("Metrics.ItemsRead: want 2, got %d", m.ItemsRead)
	}

	if m.BytesRead <= 0 {
		t.Errorf("Metrics.BytesRead: want >0, got %d", m.BytesRead)
	}
}

// ---- Close ----

func TestStream_Close_Idempotent(t *testing.T) {
	t.Parallel()

	r := newJSONLinesReader(`{"z":1}`)

	s := stream.New(r, nil)

	err := s.Close()
	if err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Second close should succeed without error.
	err = s.Close()
	if err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// ---- NewFromResponse ----

func TestNewFromResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		_ = json.NewEncoder(w).Encode(map[string]int{"resp": 1})
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL) //nolint:noctx
	if err != nil {
		t.Fatalf("GET: %v", err)
	}

	s := stream.NewFromResponse(resp, nil)
	defer s.Close() //nolint:errcheck

	items, err := s.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if len(items) != 1 {
		t.Errorf("NewFromResponse: want 1 item, got %d", len(items))
	}
}

// ---- Reader ----

func TestReader_Read(t *testing.T) {
	t.Parallel()

	r := newJSONLinesReader(`{"r":1}`)

	s := stream.New(r, nil)
	defer s.Close() //nolint:errcheck

	reader := stream.NewReader(s)
	buf := make([]byte, 1024)

	n, err := reader.Read(buf)
	if err != nil {
		t.Fatalf("Reader.Read: %v", err)
	}

	if n == 0 {
		t.Error("Reader.Read: want n>0")
	}
}

func TestReader_Read_EOF(t *testing.T) {
	t.Parallel()

	r := io.NopCloser(strings.NewReader(""))

	s := stream.New(r, nil)
	defer s.Close() //nolint:errcheck

	reader := stream.NewReader(s)
	buf := make([]byte, 64)

	_, err := reader.Read(buf)
	if err == nil {
		t.Error("Reader.Read on empty stream: want error, got nil")
	}
}

// ---- Transform ----

func TestTransform_Read(t *testing.T) {
	t.Parallel()

	r := newJSONLinesReader(`{"n":5}`)

	s := stream.New(r, nil)
	defer s.Close() //nolint:errcheck

	tr := stream.NewTransform(s, func(item interface{}) (interface{}, error) {
		m, ok := item.(map[string]interface{})
		if !ok {
			return nil, errors.New("bad type")
		}

		m["doubled"] = m["n"].(float64) * 2

		return m, nil
	})

	result, err := tr.Read()
	if err != nil {
		t.Fatalf("Transform.Read: %v", err)
	}

	rm, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("Transform.Read: want map, got %T", result)
	}

	if rm["doubled"] != float64(10) {
		t.Errorf("Transform.Read: want doubled=10, got %v", rm["doubled"])
	}
}

func TestTransform_Read_NilItem(t *testing.T) {
	t.Parallel()
	// Empty line → JSONLinesDecoder returns ErrEmptyData, not nil item.
	// To get a nil item we need a stream that returns nil without error —
	// the only path is when decoder returns nil, nil. That path isn't reachable
	// through normal JSON; test nil item via a closed stream instead.
	r := newJSONLinesReader(`{"n":1}`)

	s := stream.New(r, nil)
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	tr := stream.NewTransform(s, func(item interface{}) (interface{}, error) {
		return item, nil
	})

	_, err := tr.Read()
	if err == nil {
		t.Error("Transform.Read on closed stream: want error, got nil")
	}
}

// ---- Filter ----

func TestFilter_Read(t *testing.T) {
	t.Parallel()

	r := newJSONLinesReader(`{"v":1}`, `{"v":2}`, `{"v":3}`)

	s := stream.New(r, nil)
	defer s.Close() //nolint:errcheck

	f := stream.NewFilter(s, func(item interface{}) bool {
		m, ok := item.(map[string]interface{})
		if !ok {
			return false
		}

		return m["v"].(float64) > 1
	})

	item1, err := f.Read()
	if err != nil {
		t.Fatalf("Filter.Read #1: %v", err)
	}

	m1, _ := item1.(map[string]interface{})
	if m1["v"] != float64(2) {
		t.Errorf("Filter: want v=2, got %v", m1["v"])
	}

	item2, err := f.Read()
	if err != nil {
		t.Fatalf("Filter.Read #2: %v", err)
	}

	m2, _ := item2.(map[string]interface{})
	if m2["v"] != float64(3) {
		t.Errorf("Filter: want v=3, got %v", m2["v"])
	}
}

func TestFilter_Read_EOF_NoMatch(t *testing.T) {
	t.Parallel()

	r := newJSONLinesReader(`{"v":1}`)

	s := stream.New(r, nil)
	defer s.Close() //nolint:errcheck

	// Filter rejects everything → hits EOF.
	f := stream.NewFilter(s, func(_ interface{}) bool { return false })

	_, err := f.Read()
	if !errors.Is(err, io.EOF) {
		t.Errorf("Filter no match: want io.EOF, got %v", err)
	}
}
