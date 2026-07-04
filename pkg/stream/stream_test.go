package stream_test

import (
	"bytes"
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

	reader := newJSONLinesReader(`{"id":1}`, `{"id":2}`)

	strm := stream.New(reader, nil)
	defer strm.Close() //nolint:errcheck

	item1, err := strm.Read()
	if err != nil {
		t.Fatalf("Read #1: %v", err)
	}

	map1, isMap := item1.(map[string]interface{})
	if !isMap {
		t.Fatalf("Read #1: want map, got %T", item1)
	}

	if map1["id"] != float64(1) {
		t.Errorf("Read #1: want id=1, got %v", map1["id"])
	}

	item2, err := strm.Read()
	if err != nil {
		t.Fatalf("Read #2: %v", err)
	}

	map2, isMap2 := item2.(map[string]interface{})
	if !isMap2 {
		t.Fatalf("Read #2: want map, got %T", item2)
	}

	if map2["id"] != float64(2) {
		t.Errorf("Read #2: want id=2, got %v", map2["id"])
	}
}

func TestStream_Read_EOF(t *testing.T) {
	t.Parallel()

	reader := newJSONLinesReader(`{"x":1}`)

	strm := stream.New(reader, nil)
	defer strm.Close() //nolint:errcheck

	// Drain first item.
	_, err := strm.Read()
	if err != nil {
		t.Fatalf("first Read: %v", err)
	}
	// Next read should return EOF.
	_, err = strm.Read()
	if !errors.Is(err, io.EOF) {
		t.Errorf("second Read: want io.EOF, got %v", err)
	}
}

func TestStream_Read_ClosedStream(t *testing.T) {
	t.Parallel()

	reader := newJSONLinesReader(`{"x":1}`)

	strm := stream.New(reader, nil)

	err := strm.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err = strm.Read()
	if !errors.Is(err, stream.ErrStreamClosed) {
		t.Errorf("Read on closed: want ErrStreamClosed, got %v", err)
	}
}

func TestStream_Read_ItemTooLarge(t *testing.T) {
	t.Parallel()
	// Single line larger than MaxItemSize.
	bigLine := strings.Repeat("x", 100) + "\n"
	reader := io.NopCloser(strings.NewReader(bigLine))
	cfg := &stream.Config{
		BufferSize:  4096,
		MaxItemSize: 50, // smaller than line
		ReadTimeout: time.Second,
		Format:      "jsonlines",
		Delimiter:   "\n",
	}

	strm := stream.New(reader, cfg)
	defer strm.Close() //nolint:errcheck

	_, err := strm.Read()
	if !errors.Is(err, stream.ErrItemSizeExceedsMaximum) {
		t.Errorf("Read oversized: want ErrItemSizeExceedsMaximum, got %v", err)
	}
}

// ---- ReadAll ----

func TestStream_ReadAll(t *testing.T) {
	t.Parallel()

	reader := newJSONLinesReader(`{"n":1}`, `{"n":2}`, `{"n":3}`)

	strm := stream.New(reader, nil)
	defer strm.Close() //nolint:errcheck

	items, err := strm.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	if len(items) != 3 {
		t.Errorf("ReadAll: want 3 items, got %d", len(items))
	}
}

func TestStream_ReadAll_Empty(t *testing.T) {
	t.Parallel()

	reader := io.NopCloser(strings.NewReader(""))

	strm := stream.New(reader, nil)
	defer strm.Close() //nolint:errcheck

	items, err := strm.ReadAll()
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

	reader := newJSONLinesReader(`{"i":1}`, `{"i":2}`, `{"i":3}`, `{"i":4}`)

	strm := stream.New(reader, nil)
	defer strm.Close() //nolint:errcheck

	items, err := strm.ReadN(2)
	if err != nil {
		t.Fatalf("ReadN(2): %v", err)
	}

	if len(items) != 2 {
		t.Errorf("ReadN(2): want 2 items, got %d", len(items))
	}
}

func TestStream_ReadN_LessThanN(t *testing.T) {
	t.Parallel()

	reader := newJSONLinesReader(`{"i":1}`)

	strm := stream.New(reader, nil)
	defer strm.Close() //nolint:errcheck

	items, err := strm.ReadN(10)
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

	reader := newJSONLinesReader(`{"v":1}`, `{"v":2}`)

	strm := stream.New(reader, nil)
	defer strm.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	itemChan := strm.Channel(ctx)

	var count int
	for range itemChan {
		count++
	}

	if count != 2 {
		t.Errorf("Channel: want 2 items, got %d", count)
	}
}

func TestStream_Channel_ContextCancel(t *testing.T) {
	t.Parallel()
	// Infinite-ish stream via a blocking reader.
	srv := httptest.NewServer(http.HandlerFunc(func(respWriter http.ResponseWriter, _ *http.Request) {
		flusher, ok := respWriter.(http.Flusher)
		if !ok {
			return
		}

		for range 5 {
			_, _ = respWriter.Write([]byte(`{"v":1}` + "\n"))

			flusher.Flush()
			time.Sleep(20 * time.Millisecond)
		}
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL) //nolint:noctx
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	strm := stream.NewFromResponse(resp, nil)
	defer strm.Close() //nolint:errcheck

	ctx, cancel := context.WithCancel(context.Background())
	itemChan := strm.Channel(ctx)
	// Read 1 item then cancel.
	<-itemChan
	cancel()
	// Drain remaining; channel must close eventually.
	done := make(chan struct{})

	go func() {
		for range itemChan {
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

	reader := newJSONLinesReader(`{"k":1}`, `{"k":2}`)

	strm := stream.New(reader, nil)
	defer strm.Close() //nolint:errcheck

	var collected []interface{}

	err := strm.Process(context.Background(), func(item interface{}) error {
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

var errTestStop = errors.New("stop")

func TestStream_Process_CallbackError(t *testing.T) {
	t.Parallel()

	reader := newJSONLinesReader(`{"k":1}`)

	strm := stream.New(reader, nil)
	defer strm.Close() //nolint:errcheck

	err := strm.Process(context.Background(), func(_ interface{}) error {
		return errTestStop
	})
	if !errors.Is(err, errTestStop) {
		t.Errorf("Process callback error: want sentinel, got %v", err)
	}
}

func TestStream_Process_ContextCancel(t *testing.T) {
	t.Parallel()

	reader := io.NopCloser(strings.NewReader(strings.Repeat(`{"k":1}`+"\n", 100)))

	strm := stream.New(reader, nil)
	defer strm.Close() //nolint:errcheck

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := strm.Process(ctx, func(_ interface{}) error { return nil })
	if err == nil {
		t.Error("Process with cancelled ctx: want error, got nil")
	}
}

// ---- ProcessBatch ----

func TestStream_ProcessBatch(t *testing.T) {
	t.Parallel()

	reader := newJSONLinesReader(`{"n":1}`, `{"n":2}`, `{"n":3}`, `{"n":4}`, `{"n":5}`)

	strm := stream.New(reader, nil)
	defer strm.Close() //nolint:errcheck

	var batches [][]interface{}

	err := strm.ProcessBatch(context.Background(), 2, func(batch []interface{}) error {
		cp := make([]interface{}, len(batch))
		copy(cp, batch)
		batches = append(batches, cp)

		return nil
	})
	if err != nil {
		t.Fatalf("ProcessBatch: %v", err)
	}

	total := 0
	for _, batchItem := range batches {
		total += len(batchItem)
	}

	if total != 5 {
		t.Errorf("ProcessBatch: want 5 total items, got %d", total)
	}
}

func TestStream_ProcessBatch_ContextCancelWithItems(t *testing.T) {
	t.Parallel()
	// Stream that requires multiple batches; cancel mid-way.
	reader := io.NopCloser(strings.NewReader(strings.Repeat(`{"n":1}`+"\n", 20)))

	strm := stream.New(reader, nil)
	defer strm.Close() //nolint:errcheck

	ctx, cancel := context.WithCancel(context.Background())

	calls := 0
	err := strm.ProcessBatch(ctx, 3, func(batch []interface{}) error {
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

	reader := newJSONLinesReader(`{"x":1}`)

	strm := stream.New(reader, nil)
	defer strm.Close() //nolint:errcheck

	errChan := strm.Errors()
	if errChan == nil {
		t.Fatal("Errors() returned nil channel")
	}
}

// ---- Metrics ----

func TestStream_Metrics(t *testing.T) {
	t.Parallel()

	reader := newJSONLinesReader(`{"m":1}`, `{"m":2}`)

	strm := stream.New(reader, nil)
	defer strm.Close() //nolint:errcheck

	_, _ = strm.ReadAll()

	metrics := strm.Metrics()
	if metrics.ItemsRead != 2 {
		t.Errorf("Metrics.ItemsRead: want 2, got %d", metrics.ItemsRead)
	}

	if metrics.BytesRead <= 0 {
		t.Errorf("Metrics.BytesRead: want >0, got %d", metrics.BytesRead)
	}
}

// ---- Close ----

func TestStream_Close_Idempotent(t *testing.T) {
	t.Parallel()

	reader := newJSONLinesReader(`{"z":1}`)

	strm := stream.New(reader, nil)

	err := strm.Close()
	if err != nil {
		t.Fatalf("first Close: %v", err)
	}
	// Second close should succeed without error.
	err = strm.Close()
	if err != nil {
		t.Errorf("second Close: %v", err)
	}
}

// ---- NewFromResponse ----

func TestNewFromResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.Header().Set("Content-Type", "application/x-ndjson")
		_ = json.NewEncoder(respWriter).Encode(map[string]int{"resp": 1})
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL) //nolint:noctx
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	strm := stream.NewFromResponse(resp, nil)
	defer strm.Close() //nolint:errcheck

	items, err := strm.ReadAll()
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

	rawReader := newJSONLinesReader(`{"r":1}`)

	strm := stream.New(rawReader, nil)
	defer strm.Close() //nolint:errcheck

	strmReader := stream.NewReader(strm)
	buf := make([]byte, 1024)

	bytesRead, err := strmReader.Read(buf)
	if err != nil {
		t.Fatalf("Reader.Read: %v", err)
	}

	if bytesRead == 0 {
		t.Error("Reader.Read: want n>0")
	}
}

// TestReader_Read_SmallBuffer_IOCopyReconstructsExactBytes drives Reader.Read
// through io.CopyBuffer with a buffer far smaller than a single marshaled
// item, forcing every item to be served across several Read calls. This
// exercises the internal remainder buffering that keeps Reader from silently
// dropping bytes when the caller's buffer is undersized.
func TestReader_Read_SmallBuffer_IOCopyReconstructsExactBytes(t *testing.T) {
	t.Parallel()

	reader := newJSONLinesReader(`{"id":1}`, `{"id":2}`, `{"id":3}`)

	strm := stream.New(reader, nil)
	defer strm.Close() //nolint:errcheck

	strmReader := stream.NewReader(strm)

	var out bytes.Buffer

	// 3-byte copy buffer: smaller than any marshaled item, so io.CopyBuffer
	// must call Read many times per item.
	n, err := io.CopyBuffer(&out, strmReader, make([]byte, 3))
	if err != nil {
		t.Fatalf("io.CopyBuffer: %v", err)
	}

	want := "{\"id\":1}\n{\"id\":2}\n{\"id\":3}\n"
	if out.String() != want {
		t.Errorf("reconstructed bytes: want %q, got %q", want, out.String())
	}

	if int(n) != len(want) {
		t.Errorf("bytes copied: want %d, got %d", len(want), n)
	}
}

// TestReader_Read_IOReadFull_ExactLength reads the whole known-length output
// in a single io.ReadFull call. io.ReadFull loops calling Read until the
// destination is full or an error occurs, so this confirms Reader never
// returns fewer total bytes than were marshaled even when individual Read
// calls return partial data.
func TestReader_Read_IOReadFull_ExactLength(t *testing.T) {
	t.Parallel()

	reader := newJSONLinesReader(`{"a":1}`, `{"b":2}`, `{"c":3}`)

	strm := stream.New(reader, nil)
	defer strm.Close() //nolint:errcheck

	strmReader := stream.NewReader(strm)

	want := "{\"a\":1}\n{\"b\":2}\n{\"c\":3}\n"
	buf := make([]byte, len(want))

	n, err := io.ReadFull(strmReader, buf)
	if err != nil {
		t.Fatalf("io.ReadFull: %v", err)
	}

	if n != len(want) {
		t.Fatalf("io.ReadFull: want n=%d, got %d", len(want), n)
	}

	if string(buf) != want {
		t.Errorf("reconstructed bytes: want %q, got %q", want, string(buf))
	}

	// Stream is now exhausted; a further Read must report io.EOF.
	extra := make([]byte, 1)

	_, err = strmReader.Read(extra)
	if !errors.Is(err, io.EOF) {
		t.Errorf("Read after exhaustion: want io.EOF, got %v", err)
	}
}

// TestReader_Read_PartialThenRemainder manually drives Read with a buffer
// smaller than one item to check that the second call resumes mid-item
// (rather than fetching a new item) and that byte order is preserved.
func TestReader_Read_PartialThenRemainder(t *testing.T) {
	t.Parallel()

	reader := newJSONLinesReader(`{"id":1}`)

	strm := stream.New(reader, nil)
	defer strm.Close() //nolint:errcheck

	strmReader := stream.NewReader(strm)

	want := "{\"id\":1}\n"

	first := make([]byte, 4)

	firstReadLen, err := strmReader.Read(first)
	if err != nil {
		t.Fatalf("first Read: %v", err)
	}

	if firstReadLen != 4 {
		t.Fatalf("first Read: want n=4, got %d", firstReadLen)
	}

	rest := make([]byte, 64)

	secondReadLen, err := strmReader.Read(rest)
	if err != nil {
		t.Fatalf("second Read: %v", err)
	}

	got := string(first[:firstReadLen]) + string(rest[:secondReadLen])
	if got != want {
		t.Errorf("reconstructed bytes: want %q, got %q", want, got)
	}

	// Item and stream are both exhausted now.
	thirdReadLen, err := strmReader.Read(rest)
	if thirdReadLen != 0 || !errors.Is(err, io.EOF) {
		t.Errorf("Read after exhaustion: want (0, io.EOF), got (%d, %v)", thirdReadLen, err)
	}
}

func TestReader_Read_EOF(t *testing.T) {
	t.Parallel()

	rawReader := io.NopCloser(strings.NewReader(""))

	strm := stream.New(rawReader, nil)
	defer strm.Close() //nolint:errcheck

	strmReader := stream.NewReader(strm)
	buf := make([]byte, 64)

	_, err := strmReader.Read(buf)
	if err == nil {
		t.Error("Reader.Read on empty stream: want error, got nil")
	}
}

// ---- Transform ----

var errBadType = errors.New("bad type")

func TestTransform_Read(t *testing.T) {
	t.Parallel()

	reader := newJSONLinesReader(`{"n":5}`)

	strm := stream.New(reader, nil)
	defer strm.Close() //nolint:errcheck

	transform := stream.NewTransform(strm, func(item interface{}) (interface{}, error) {
		itemMap, isMap := item.(map[string]interface{})
		if !isMap {
			return nil, errBadType
		}

		nVal, isFloat := itemMap["n"].(float64)
		if !isFloat {
			return nil, errBadType
		}

		itemMap["doubled"] = nVal * 2

		return itemMap, nil
	})

	result, err := transform.Read()
	if err != nil {
		t.Fatalf("Transform.Read: %v", err)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		t.Fatalf("Transform.Read: want map, got %T", result)
	}

	if resultMap["doubled"] != float64(10) {
		t.Errorf("Transform.Read: want doubled=10, got %v", resultMap["doubled"])
	}
}

func TestTransform_Read_NilItem(t *testing.T) {
	t.Parallel()
	// Empty line → JSONLinesDecoder returns ErrEmptyData, not nil item.
	// To get a nil item we need a stream that returns nil without error —
	// the only path is when decoder returns nil, nil. That path isn't reachable
	// through normal JSON; test nil item via a closed stream instead.
	reader := newJSONLinesReader(`{"n":1}`)

	strm := stream.New(reader, nil)

	err := strm.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	transform := stream.NewTransform(strm, func(item interface{}) (interface{}, error) {
		return item, nil
	})

	_, err = transform.Read()
	if err == nil {
		t.Error("Transform.Read on closed stream: want error, got nil")
	}
}

// ---- Filter ----

func TestFilter_Read(t *testing.T) {
	t.Parallel()

	reader := newJSONLinesReader(`{"v":1}`, `{"v":2}`, `{"v":3}`)

	strm := stream.New(reader, nil)
	defer strm.Close() //nolint:errcheck

	filter := stream.NewFilter(strm, func(item interface{}) bool {
		itemMap, isMap := item.(map[string]interface{})
		if !isMap {
			return false
		}

		vVal, isFloat := itemMap["v"].(float64)
		if !isFloat {
			return false
		}

		return vVal > 1
	})

	item1, err := filter.Read()
	if err != nil {
		t.Fatalf("Filter.Read #1: %v", err)
	}

	map1, _ := item1.(map[string]interface{})
	if map1["v"] != float64(2) {
		t.Errorf("Filter: want v=2, got %v", map1["v"])
	}

	item2, err := filter.Read()
	if err != nil {
		t.Fatalf("Filter.Read #2: %v", err)
	}

	map2, _ := item2.(map[string]interface{})
	if map2["v"] != float64(3) {
		t.Errorf("Filter: want v=3, got %v", map2["v"])
	}
}

func TestFilter_Read_EOF_NoMatch(t *testing.T) {
	t.Parallel()

	reader := newJSONLinesReader(`{"v":1}`)

	strm := stream.New(reader, nil)
	defer strm.Close() //nolint:errcheck

	// Filter rejects everything → hits EOF.
	filter := stream.NewFilter(strm, func(_ interface{}) bool { return false })

	_, err := filter.Read()
	if !errors.Is(err, io.EOF) {
		t.Errorf("Filter no match: want io.EOF, got %v", err)
	}
}

// errReadBoom is the sentinel returned by erroringReader.
var errReadBoom = errors.New("read boom")

// erroringReader always fails Read with a non-EOF error, driving the stream's
// error-reporting path on every read.
type erroringReader struct{}

func (erroringReader) Read([]byte) (int, error) { return 0, errReadBoom }
func (erroringReader) Close() error             { return nil }

// TestStream_ConcurrentCloseAndErrorSend exercises Close racing against the
// error-reporting send under the race detector. Previously Close closed
// errorChan while Read/Channel could still send to it, panicking on a send to a
// closed channel.
func TestStream_ConcurrentCloseAndErrorSend(t *testing.T) {
	t.Parallel()

	strm := stream.New(erroringReader{}, nil)

	done := make(chan struct{})

	go func() {
		defer close(done)

		for range 200 {
			_, _ = strm.Read()
		}
	}()

	// Drain errors so the buffered channel does not stay full.
	go func() {
		for range strm.Errors() {
		}
	}()

	_ = strm.Close()

	<-done
}
