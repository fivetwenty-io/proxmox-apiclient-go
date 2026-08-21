package http //nolint:testpackage // white-box test: exercises builder internals and middleware directly

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Shared literals for the streaming-upload tests.
const (
	streamTestFieldContent = "content"
	streamTestContentISO   = "iso"
	streamTestFileField    = "filename"
	streamTestFileName     = "img.raw"
)

// patternReader yields a deterministic byte pattern of a fixed total size
// without holding the payload in memory.
type patternReader struct {
	remaining int64
	pos       int64
}

func (p *patternReader) Read(b []byte) (int, error) {
	if p.remaining <= 0 {
		return 0, io.EOF
	}

	n := len(b)
	if int64(n) > p.remaining {
		n = int(p.remaining)
	}

	for i := range n {
		b[i] = byte((p.pos + int64(i)) % 251)
	}

	p.pos += int64(n)
	p.remaining -= int64(n)

	return n, nil
}

// parseMultipartBody parses raw multipart bytes using the boundary from the
// content type, returning form field values and file part contents.
func parseMultipartBody(t *testing.T, contentType string, raw []byte) (map[string]string, map[string][]byte) {
	t.Helper()

	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		t.Fatalf("ParseMediaType(%q): %v", contentType, err)
	}

	fields := map[string]string{}
	files := map[string][]byte{}

	reader := multipart.NewReader(bytes.NewReader(raw), params["boundary"])

	for {
		part, partErr := reader.NextPart()
		if errors.Is(partErr, io.EOF) {
			break
		}

		if partErr != nil {
			t.Fatalf("NextPart: %v", partErr)
		}

		data, readErr := io.ReadAll(part)
		if readErr != nil {
			t.Fatalf("read part %q: %v", part.FormName(), readErr)
		}

		if part.FileName() != "" {
			files[part.FormName()] = data
		} else {
			fields[part.FormName()] = string(data)
		}
	}

	return fields, files
}

// TestBuildBody_SizedFile_StreamsWithExactLength verifies that a size-aware
// file part produces a streamed (non-buffered) multipart body whose declared
// length matches the produced bytes exactly, and that the parts parse back
// with intact field values and file content.
func TestBuildBody_SizedFile_StreamsWithExactLength(t *testing.T) {
	t.Parallel()

	const fileSize = 1 << 20 // 1 MiB

	rb := NewRequestBuilder("POST", "https://pve.example.com:8006/api2/json", "/nodes/pve/storage/local/upload")
	rb.AddFormParam(streamTestFieldContent, streamTestContentISO)
	rb.AddFileWithSize(streamTestFileField, streamTestFileName, &patternReader{remaining: fileSize}, fileSize)

	body, contentType, contentLength, err := rb.BuildBody()
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}

	if _, buffered := body.(*bytes.Buffer); buffered {
		t.Fatal("sized file must produce a streamed body, got *bytes.Buffer")
	}

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read streamed body: %v", err)
	}

	if int64(len(raw)) != contentLength {
		t.Fatalf("declared length = %d, produced %d bytes", contentLength, len(raw))
	}

	fields, files := parseMultipartBody(t, contentType, raw)

	if fields[streamTestFieldContent] != streamTestContentISO {
		t.Errorf("field content = %q, want iso", fields[streamTestFieldContent])
	}

	file := files[streamTestFileField]
	if int64(len(file)) != fileSize {
		t.Fatalf("file part = %d bytes, want %d", len(file), fileSize)
	}

	for i, b := range file {
		if b != byte(i%251) {
			t.Fatalf("file byte %d = %d, want %d", i, b, byte(i%251))
		}
	}
}

// TestAddFile_SizedReaderWrapper_Streams verifies AddFile detects the
// SizedReader wrapper and takes the streaming path.
func TestAddFile_SizedReaderWrapper_Streams(t *testing.T) {
	t.Parallel()

	const fileSize = 4096

	rb := NewRequestBuilder("POST", "https://pve.example.com:8006/api2/json", "/upload")
	rb.AddFile(streamTestFileField, streamTestFileName, NewSizedReader(&patternReader{remaining: fileSize}, fileSize))

	body, contentType, contentLength, err := rb.BuildBody()
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}

	if _, buffered := body.(*bytes.Buffer); buffered {
		t.Fatal("SizedReader must produce a streamed body, got *bytes.Buffer")
	}

	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read streamed body: %v", err)
	}

	if int64(len(raw)) != contentLength {
		t.Fatalf("declared length = %d, produced %d bytes", contentLength, len(raw))
	}

	_, files := parseMultipartBody(t, contentType, raw)
	if got := int64(len(files[streamTestFileField])); got != fileSize {
		t.Fatalf("file part = %d bytes, want %d", got, fileSize)
	}
}

// TestAddFile_OSFile_DetectsSize verifies AddFile stats an *os.File and takes
// the streaming path with the file's remaining size.
func TestAddFile_OSFile_DetectsSize(t *testing.T) {
	t.Parallel()

	content := strings.Repeat("stemcell-bytes.", 1024)
	path := filepath.Join(t.TempDir(), streamTestFileName)

	writeErr := os.WriteFile(path, []byte(content), 0o600)
	if writeErr != nil {
		t.Fatalf("WriteFile: %v", writeErr)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	t.Cleanup(func() { _ = f.Close() })

	rb := NewRequestBuilder("POST", "https://pve.example.com:8006/api2/json", "/upload")
	rb.AddFile(streamTestFileField, streamTestFileName, f)

	body, contentType, contentLength, buildErr := rb.BuildBody()
	if buildErr != nil {
		t.Fatalf("BuildBody: %v", buildErr)
	}

	if _, buffered := body.(*bytes.Buffer); buffered {
		t.Fatal("*os.File must produce a streamed body, got *bytes.Buffer")
	}

	raw, readErr := io.ReadAll(body)
	if readErr != nil {
		t.Fatalf("read streamed body: %v", readErr)
	}

	if int64(len(raw)) != contentLength {
		t.Fatalf("declared length = %d, produced %d bytes", contentLength, len(raw))
	}

	_, files := parseMultipartBody(t, contentType, raw)
	if string(files[streamTestFileField]) != content {
		t.Fatal("file part content does not match the file on disk")
	}
}

// TestAddFile_UnsizedReader_Buffers verifies a plain reader with no
// determinable size keeps the historical buffered behavior.
func TestAddFile_UnsizedReader_Buffers(t *testing.T) {
	t.Parallel()

	rb := NewRequestBuilder("POST", "https://pve.example.com:8006/api2/json", "/upload")
	rb.AddFile(streamTestFileField, streamTestFileName, &patternReader{remaining: 64})

	body, _, contentLength, err := rb.BuildBody()
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}

	buffer, buffered := body.(*bytes.Buffer)
	if !buffered {
		t.Fatalf("unsized reader must produce a buffered body, got %T", body)
	}

	if int64(buffer.Len()) != contentLength {
		t.Fatalf("declared length = %d, buffer holds %d bytes", contentLength, buffer.Len())
	}
}

// TestBuildBody_ShortSizedReader_FailsRead verifies a reader that yields fewer
// bytes than it declared surfaces ErrFileShorterThanDeclared to the body
// consumer instead of silently truncating a body with a larger Content-Length.
func TestBuildBody_ShortSizedReader_FailsRead(t *testing.T) {
	t.Parallel()

	rb := NewRequestBuilder("POST", "https://pve.example.com:8006/api2/json", "/upload")
	rb.AddFileWithSize(streamTestFileField, streamTestFileName, &patternReader{remaining: 10}, 100)

	body, _, _, err := rb.BuildBody()
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}

	_, readErr := io.ReadAll(body)
	if !errors.Is(readErr, ErrFileShorterThanDeclared) {
		t.Fatalf("read error = %v, want ErrFileShorterThanDeclared", readErr)
	}
}

// TestUploadWithContext_StreamedBody_ExactContentLengthNoChunking verifies the
// full upload path sends a streamed body with an explicit Content-Length that
// matches the bytes on the wire, and never falls back to chunked
// transfer-encoding (PVE's proxy rejects it with 501).
func TestUploadWithContext_StreamedBody_ExactContentLengthNoChunking(t *testing.T) {
	t.Parallel()

	const fileSize = 256 << 10 // 256 KiB

	var (
		gotContentLength  int64
		gotTransferChunks []string
		gotBodyBytes      int64
	)

	srv := newTestServer(t, func(writer http.ResponseWriter, r *http.Request) {
		gotContentLength = r.ContentLength
		gotTransferChunks = r.TransferEncoding

		n, _ := io.Copy(io.Discard, r.Body)
		gotBodyBytes = n

		writer.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = writer.Write(pveEnvelope(t, "UPID:pve:0000:upload"))
	})

	client := clientPointedAt(t, srv.URL)

	resp, err := client.UploadWithContext(
		context.Background(),
		"/nodes/pve/storage/local/upload",
		map[string]string{streamTestFieldContent: streamTestContentISO},
		streamTestFileField,
		streamTestFileName,
		NewSizedReader(&patternReader{remaining: fileSize}, fileSize),
	)
	if err != nil {
		t.Fatalf("UploadWithContext: %v", err)
	}

	if resp.Data != "UPID:pve:0000:upload" {
		t.Errorf("Data = %v, want UPID", resp.Data)
	}

	if len(gotTransferChunks) != 0 {
		t.Errorf("Transfer-Encoding = %v, want none (PVE rejects chunked with 501)", gotTransferChunks)
	}

	if gotContentLength <= fileSize {
		t.Errorf("Content-Length = %d, want > %d (file plus multipart framing)", gotContentLength, fileSize)
	}

	if gotBodyBytes != gotContentLength {
		t.Errorf("body bytes on the wire = %d, Content-Length declared %d", gotBodyBytes, gotContentLength)
	}
}

// bodyReadTracker counts reads so a test can prove middleware did not consume
// a request body.
type bodyReadTracker struct {
	reads int32
	data  io.Reader
}

func (b *bodyReadTracker) Read(p []byte) (int, error) {
	atomic.AddInt32(&b.reads, 1)

	return b.data.Read(p) //nolint:wrapcheck // io.Reader contract: pass io.EOF and source errors through verbatim
}

// TestRetryMiddleware_SkipsCaptureWhenRetryImpossible verifies the retry
// middleware does not buffer (or even touch) a streaming body on a request
// that can never be retried: a POST without the force-retry opt-in.
func TestRetryMiddleware_SkipsCaptureWhenRetryImpossible(t *testing.T) {
	t.Parallel()

	client, err := NewClient(minimalHTTPOptions())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.maxRetries = 3

	tracker := &bodyReadTracker{data: strings.NewReader("streamed payload")}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://pve.example.com/upload", tracker)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}

	// Simulate a streamed upload: explicit length, no GetBody.
	req.ContentLength = 999

	resp, err := client.retryMiddleware(req, func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	if err != nil {
		t.Fatalf("retryMiddleware: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := atomic.LoadInt32(&tracker.reads); got != 0 {
		t.Errorf("middleware read the body %d time(s); a never-retried request must not be re-buffered", got)
	}

	if req.ContentLength != 999 {
		t.Errorf("ContentLength = %d, middleware must not overwrite the declared 999", req.ContentLength)
	}

	if req.GetBody != nil {
		t.Error("GetBody must stay nil for a streamed body")
	}
}

// TestRetryMiddleware_CapturesWhenForceRetryOptedIn verifies the capture guard
// keys on retry eligibility: a POST that explicitly opted in to retries still
// gets its body buffered up front so every attempt resends it intact.
func TestRetryMiddleware_CapturesWhenForceRetryOptedIn(t *testing.T) {
	t.Parallel()

	client, err := NewClient(minimalHTTPOptions())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	tracker := &bodyReadTracker{data: strings.NewReader("form body")}

	ctx := WithForceRetry(context.Background(), true)
	ctx = WithRetries(ctx, 2)
	ctx = WithRetryDelay(ctx, time.Millisecond)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://pve.example.com/qemu", tracker)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}

	resp, err := client.retryMiddleware(req, func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})
	if err != nil {
		t.Fatalf("retryMiddleware: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got := atomic.LoadInt32(&tracker.reads); got == 0 {
		t.Error("middleware must buffer the body up front when the request is retry-eligible")
	}

	if req.ContentLength != int64(len("form body")) {
		t.Errorf("ContentLength = %d, want %d from the buffered body", req.ContentLength, len("form body"))
	}
}

// TestHandleAuthRetry_NonRewindableBody_Returns401 verifies that a 401 on a
// streamed (non-rewindable) body surfaces the original 401 instead of
// replaying a drained body against its declared Content-Length.
func TestHandleAuthRetry_NonRewindableBody_Returns401(t *testing.T) {
	t.Parallel()

	const fileSize = 1024

	var calls int32

	srv := newTestServer(t, func(writer http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)

		_, _ = io.Copy(io.Discard, r.Body)

		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"message":"ticket rejected"}`))
	})

	opts := minimalHTTPOptions()
	// A locally-valid ticket so the client is a re-auth-capable ticket
	// authenticator and the request reaches the server on the first attempt.
	opts.Ticket = fmt.Sprintf("PVE:root@pam:%08X::sig", time.Now().Unix())

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.baseURL = srv.URL

	_, err = client.UploadWithContext(
		context.Background(),
		"/nodes/pve/storage/local/upload",
		map[string]string{streamTestFieldContent: streamTestContentISO},
		streamTestFileField,
		streamTestFileName,
		NewSizedReader(&patternReader{remaining: fileSize}, fileSize),
	)
	if err == nil {
		t.Fatal("expected the 401 to surface as an error")
	}

	if !strings.Contains(err.Error(), "ticket rejected") {
		t.Errorf("error = %v, want the original 401 message", err)
	}

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (a drained streamed body must not be replayed)", got)
	}
}
