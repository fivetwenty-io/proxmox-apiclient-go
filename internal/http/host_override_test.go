package http //nolint:testpackage // white-box test: accesses unexported client fields directly

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/cache"
)

// Shared literals for the host-override tests.
const (
	testDataBase        = "base"
	testDataNode        = "node"
	testOverrideNoPort  = "pve2.example.com"
	testBaseURLWithPort = "https://pve1.example.com:8006/api2/json"
)

// newEnvelopeCountingServer returns a test server that answers every request
// with a PVE envelope carrying data, counting the calls it receives.
func newEnvelopeCountingServer(t *testing.T, data string, calls *int32) *httptest.Server {
	t.Helper()

	return newTestServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(calls, 1)
		writer.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = writer.Write(pveEnvelope(t, data))
	})
}

// TestWithHost_RoutesRequestToOverriddenHost verifies a request carrying a
// WithHost override is sent to the override, not the configured base host.
func TestWithHost_RoutesRequestToOverriddenHost(t *testing.T) {
	t.Parallel()

	var baseCalls, nodeCalls int32

	baseSrv := newEnvelopeCountingServer(t, testDataBase, &baseCalls)
	nodeSrv := newEnvelopeCountingServer(t, testDataNode, &nodeCalls)

	client := clientPointedAt(t, baseSrv.URL)

	nodeURL, err := url.Parse(nodeSrv.URL)
	if err != nil {
		t.Fatalf("parse node URL: %v", err)
	}

	ctx := WithHost(context.Background(), nodeURL.Host)

	resp, err := client.DoWithContext(ctx, "GET", "/version", nil)
	if err != nil {
		t.Fatalf("DoWithContext: %v", err)
	}

	if resp.Data != testDataNode {
		t.Errorf("Data = %v, want the overridden node's response", resp.Data)
	}

	if got := atomic.LoadInt32(&nodeCalls); got != 1 {
		t.Errorf("node calls = %d, want 1", got)
	}

	if got := atomic.LoadInt32(&baseCalls); got != 0 {
		t.Errorf("base calls = %d, want 0", got)
	}
}

// TestWithHost_UploadRoutesToOverriddenHost verifies the upload request path
// honors the host override too (the cross-node upload case).
func TestWithHost_UploadRoutesToOverriddenHost(t *testing.T) {
	t.Parallel()

	var baseCalls, nodeCalls int32

	baseSrv := newEnvelopeCountingServer(t, testDataBase, &baseCalls)

	nodeSrv := newTestServer(t, func(writer http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&nodeCalls, 1)

		_, _ = io.Copy(io.Discard, r.Body)

		writer.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = writer.Write(pveEnvelope(t, "UPID:node2:upload"))
	})

	client := clientPointedAt(t, baseSrv.URL)

	nodeURL, err := url.Parse(nodeSrv.URL)
	if err != nil {
		t.Fatalf("parse node URL: %v", err)
	}

	ctx := WithHost(context.Background(), nodeURL.Host)

	resp, err := client.UploadWithContext(ctx, "/nodes/node2/storage/local/upload",
		map[string]string{streamTestFieldContent: streamTestContentISO}, streamTestFileField, streamTestFileName,
		NewSizedReader(strings.NewReader("payload"), int64(len("payload"))))
	if err != nil {
		t.Fatalf("UploadWithContext: %v", err)
	}

	if resp.Data != "UPID:node2:upload" {
		t.Errorf("Data = %v, want the overridden node's UPID", resp.Data)
	}

	if got := atomic.LoadInt32(&nodeCalls); got != 1 {
		t.Errorf("node calls = %d, want 1", got)
	}

	if got := atomic.LoadInt32(&baseCalls); got != 0 {
		t.Errorf("base calls = %d, want 0", got)
	}
}

// TestWithHost_FingerprintPinning_FailsFast verifies a host override is
// rejected up front when TLS fingerprint pinning is enabled: the pin is
// verified against the configured base host only, so honoring the override
// would silently validate the wrong host's pin.
func TestWithHost_FingerprintPinning_FailsFast(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.CachedFingerprints = map[string]bool{"AA:BB:CC": true}

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx := WithHost(context.Background(), "node2.example.com:8006")

	_, err = client.DoWithContext(ctx, "GET", "/version", nil)
	if !errors.Is(err, ErrHostOverrideFingerprint) {
		t.Errorf("Do error = %v, want ErrHostOverrideFingerprint", err)
	}

	_, err = client.UploadWithContext(ctx, "/nodes/n2/storage/local/upload",
		map[string]string{streamTestFieldContent: streamTestContentISO}, streamTestFileField, streamTestFileName,
		strings.NewReader("x"))
	if !errors.Is(err, ErrHostOverrideFingerprint) {
		t.Errorf("Upload error = %v, want ErrHostOverrideFingerprint", err)
	}
}

// TestWithHost_CacheKeyedOnOverriddenURL verifies a host-overridden GET is
// cached under the overridden URL: repeating it is served from cache, and it
// never poisons the base host's entry for the same path.
func TestWithHost_CacheKeyedOnOverriddenURL(t *testing.T) {
	t.Parallel()

	var baseCalls, nodeCalls int32

	baseSrv := newEnvelopeCountingServer(t, testDataBase, &baseCalls)
	nodeSrv := newEnvelopeCountingServer(t, testDataNode, &nodeCalls)

	opts := minimalHTTPOptions()
	opts.CacheConfig = &cache.Config{Enabled: true, MaxSize: 10 * 1024 * 1024, DefaultTTL: time.Minute, CleanupInterval: 5 * time.Minute}

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.baseURL = baseSrv.URL

	t.Cleanup(func() { _ = client.Close() })

	nodeURL, err := url.Parse(nodeSrv.URL)
	if err != nil {
		t.Fatalf("parse node URL: %v", err)
	}

	ctx := WithHost(context.Background(), nodeURL.Host)

	for range 2 {
		resp, doErr := client.DoWithContext(ctx, "GET", "/version", nil)
		if doErr != nil {
			t.Fatalf("overridden GET: %v", doErr)
		}

		if resp.Data != testDataNode {
			t.Fatalf("Data = %v, want node", resp.Data)
		}
	}

	if got := atomic.LoadInt32(&nodeCalls); got != 1 {
		t.Errorf("node calls = %d, want 1 (second overridden GET must be served from cache)", got)
	}

	resp, err := client.DoWithContext(context.Background(), "GET", "/version", nil)
	if err != nil {
		t.Fatalf("base GET: %v", err)
	}

	if resp.Data != testDataBase {
		t.Errorf("Data = %v, want base (override must not poison the base host's cache entry)", resp.Data)
	}

	if got := atomic.LoadInt32(&baseCalls); got != 1 {
		t.Errorf("base calls = %d, want 1", got)
	}
}

// TestApplyHostOverride covers the port-carrying rules: an override without a
// port keeps the URL's port, an override with a port replaces both, and bare
// IPv6 literals are bracketed.
func TestApplyHostOverride(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		urlIn    string
		override string
		wantHost string
	}{
		{"keeps port", testBaseURLWithPort + "/version", testOverrideNoPort, "pve2.example.com:8006"},
		{"replaces host and port", testBaseURLWithPort, "pve2.example.com:9006", "pve2.example.com:9006"},
		{"no port anywhere", "https://pve1.example.com/api2/json", testOverrideNoPort, testOverrideNoPort},
		{"ipv6 keeps port", testBaseURLWithPort, "fd00::2", "[fd00::2]:8006"},
		{"ipv6 with port", testBaseURLWithPort, "[fd00::2]:9006", "[fd00::2]:9006"},
		{"ipv6 no port anywhere", "https://pve1.example.com/api2/json", "fd00::2", "[fd00::2]"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			parsed, err := url.Parse(tc.urlIn)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			applyHostOverride(parsed, tc.override)

			if parsed.Host != tc.wantHost {
				t.Errorf("host = %q, want %q", parsed.Host, tc.wantHost)
			}
		})
	}
}
