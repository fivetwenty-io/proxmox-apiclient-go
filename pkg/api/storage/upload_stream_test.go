package storage_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/storage"
	pveclient "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

// patternReader yields a deterministic byte pattern of a fixed total size
// without ever holding the payload in memory.
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

// countingReader counts the bytes read through it.
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)

	return n, err //nolint:wrapcheck // io.Reader contract: pass io.EOF and source errors through verbatim
}

// streamedUploadCapture records what the fake PVE server observed for one upload.
type streamedUploadCapture struct {
	contentLength int64
	transferEnc   []string
	bodyBytes     int64
	contentField  string
	fileName      string
	fileBytes     int64
	fileHash      string
}

// captureUploadHandler returns a handler that consumes a multipart upload
// streamingly (never buffering the file part), recording the request framing
// and file digest into capture.
func captureUploadHandler(t *testing.T, capture *streamedUploadCapture) http.HandlerFunc {
	t.Helper()

	return func(w http.ResponseWriter, r *http.Request) {
		capture.contentLength = r.ContentLength
		capture.transferEnc = r.TransferEncoding

		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			t.Errorf("ParseMediaType: %v", err)
			http.Error(w, "bad content type", http.StatusBadRequest)

			return
		}

		body := &countingReader{r: r.Body}
		if !consumeMultipartParts(t, multipart.NewReader(body, params["boundary"]), capture) {
			http.Error(w, "bad multipart", http.StatusBadRequest)

			return
		}

		// Drain any trailing bytes so the count covers the whole body.
		_, _ = io.Copy(io.Discard, body)
		capture.bodyBytes = body.n

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"UPID:pve:00001234:upload"}`))
	}
}

// consumeMultipartParts walks the multipart parts, hashing the file part and
// keeping the content field, reporting whether parsing succeeded.
func consumeMultipartParts(t *testing.T, reader *multipart.Reader, capture *streamedUploadCapture) bool {
	t.Helper()

	for {
		part, partErr := reader.NextPart()
		if errors.Is(partErr, io.EOF) {
			return true
		}

		if partErr != nil {
			t.Errorf("NextPart: %v", partErr)

			return false
		}

		switch {
		case part.FileName() != "":
			capture.fileName = part.FileName()
			fileHash := sha256.New()

			n, copyErr := io.Copy(fileHash, part)
			if copyErr != nil {
				t.Errorf("read file part: %v", copyErr)
			}

			capture.fileBytes = n
			capture.fileHash = hex.EncodeToString(fileHash.Sum(nil))
		case part.FormName() == "content":
			fieldBytes, _ := io.ReadAll(part)
			capture.contentField = string(fieldBytes)
		}
	}
}

// TestUpload_StreamsLargePayload verifies the public Upload path streams a
// large payload: the request carries an exact Content-Length (never chunked
// transfer-encoding, which PVE rejects with 501), the multipart body parses
// server-side with intact field values and file bytes, and the client never
// buffers anything near the payload in memory.
//
// Deliberately not parallel: the allocation measurement must not include
// other tests' work.
func TestUpload_StreamsLargePayload(t *testing.T) { //nolint:paralleltest // measures process-wide allocations
	const payloadSize = 64 << 20 // 64 MiB

	expectedHash := sha256.New()

	_, hashErr := io.Copy(expectedHash, &patternReader{remaining: payloadSize})
	if hashErr != nil {
		t.Fatalf("hash expected payload: %v", hashErr)
	}

	var capture streamedUploadCapture

	srv := httptest.NewServer(captureUploadHandler(t, &capture))
	defer srv.Close()

	cli, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := storage.New(cli)

	var before, after runtime.MemStats

	runtime.GC()
	runtime.ReadMemStats(&before)

	upid, err := svc.Upload(context.Background(), "node1", "local", "iso",
		"stemcell.img", pveclient.NewSizedReader(&patternReader{remaining: payloadSize}, payloadSize))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	runtime.GC()
	runtime.ReadMemStats(&after)

	if upid != "UPID:pve:00001234:upload" {
		t.Errorf("upid = %q, want the server's UPID", upid)
	}

	assertStreamedUpload(t, &capture, payloadSize, hex.EncodeToString(expectedHash.Sum(nil)))

	// Streaming means allocations stay far below the payload size; the old
	// buffered path allocated well over payloadSize (buffer growth doubles).
	// The bound is generous — a quarter of the payload — to avoid flakes;
	// both client and in-process test server allocations are included.
	allocated := int64(after.TotalAlloc - before.TotalAlloc)
	if allocated >= payloadSize/4 {
		t.Errorf("allocated %d bytes during a %d-byte upload; streaming must not buffer the payload", allocated, int64(payloadSize))
	}
}

// assertStreamedUpload checks the framing and content the server captured for
// a streamed upload of payloadSize bytes with the given digest.
func assertStreamedUpload(t *testing.T, capture *streamedUploadCapture, payloadSize int64, wantHash string) {
	t.Helper()

	if len(capture.transferEnc) != 0 {
		t.Errorf("Transfer-Encoding = %v, want none (PVE rejects chunked with 501)", capture.transferEnc)
	}

	if capture.contentLength <= payloadSize {
		t.Errorf("Content-Length = %d, want > %d (payload plus multipart framing)", capture.contentLength, payloadSize)
	}

	if capture.bodyBytes != capture.contentLength {
		t.Errorf("body bytes on the wire = %d, Content-Length declared %d", capture.bodyBytes, capture.contentLength)
	}

	if capture.contentField != "iso" {
		t.Errorf("content field = %q, want iso", capture.contentField)
	}

	if capture.fileName != "stemcell.img" {
		t.Errorf("file name = %q, want stemcell.img", capture.fileName)
	}

	if capture.fileBytes != payloadSize {
		t.Errorf("file part = %d bytes, want %d", capture.fileBytes, payloadSize)
	}

	if capture.fileHash != wantHash {
		t.Errorf("file hash = %s, want %s (payload corrupted in transit)", capture.fileHash, wantHash)
	}
}

// TestUpload_WithHostRoutesToOwningNode verifies the public chain honors a
// per-request host override, so an upload can target the node that owns a
// local storage while the client stays pointed at the cluster base host.
func TestUpload_WithHostRoutesToOwningNode(t *testing.T) {
	t.Parallel()

	var baseCalls, nodeCalls int32

	baseSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&baseCalls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"UPID:base:upload"}`))
	}))
	defer baseSrv.Close()

	nodeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&nodeCalls, 1)

		_, _ = io.Copy(io.Discard, r.Body)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"UPID:node2:upload"}`))
	}))
	defer nodeSrv.Close()

	cli, err := pveclient.NewClient(optsFromServerURL(baseSrv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := storage.New(cli)

	nodeURL, err := url.Parse(nodeSrv.URL)
	if err != nil {
		t.Fatalf("parse node URL: %v", err)
	}

	ctx := pveclient.WithHost(context.Background(), nodeURL.Host)

	upid, err := svc.Upload(ctx, "node2", "local", "iso", "img.raw",
		pveclient.NewSizedReader(&patternReader{remaining: 4096}, 4096))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}

	if upid != "UPID:node2:upload" {
		t.Errorf("upid = %q, want the owning node's UPID", upid)
	}

	if got := atomic.LoadInt32(&nodeCalls); got != 1 {
		t.Errorf("node calls = %d, want 1", got)
	}

	if got := atomic.LoadInt32(&baseCalls); got != 0 {
		t.Errorf("base calls = %d, want 0", got)
	}
}
