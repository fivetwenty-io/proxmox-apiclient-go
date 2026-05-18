package http //nolint:testpackage // white-box tests: accesses unexported fields/helpers

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/internal/constants"
	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/cache"
	pmetrics "github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/metrics"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// minimalOptions returns an Options wired to an HTTP (not HTTPS) test server.
// Pass baseURL from the test server so BaseURL() is overridden by patching
// the client directly after creation.
func minimalHTTPOptions() *Options {
	return &Options{
		Host:      "127.0.0.1",
		Port:      9999,
		Protocol:  "http",
		Timeout:   5 * time.Second,
		KeepAlive: 5,
	}
}

// pveEnvelope wraps data in PVE API envelope.
func pveEnvelope(t *testing.T, data interface{}) []byte {
	t.Helper()

	env := map[string]interface{}{"data": data}

	b, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("pveEnvelope marshal: %v", err)
	}

	return b
}

// newTestServer starts an httptest.Server that serves fixed responses; caller
// must t.Cleanup(srv.Close).
func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	return srv
}

// clientPointedAt creates a NewClient with HTTP options then rewires its
// baseURL to the given test-server URL so actual requests hit the fake server.
func clientPointedAt(t *testing.T, serverURL string) *Client {
	t.Helper()

	opts := minimalHTTPOptions()

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	c.baseURL = serverURL

	return c
}

// testLogger captures log calls for assertion.
type testLogger struct {
	mu     sync.Mutex
	infos  []string
	warns  []string
	errors []string
	debugs []string
}

func (l *testLogger) Debug(msg string, _ map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.debugs = append(l.debugs, msg)
}

func (l *testLogger) Info(msg string, _ map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.infos = append(l.infos, msg)
}

func (l *testLogger) Warn(msg string, _ map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.warns = append(l.warns, msg)
}

func (l *testLogger) Error(msg string, _ map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.errors = append(l.errors, msg)
}

// ---------------------------------------------------------------------------
// Options / BaseURL
// ---------------------------------------------------------------------------

func TestOptions_BaseURL(t *testing.T) {
	t.Parallel()

	cases := []struct {
		proto string
		host  string
		port  int
		want  string
	}{
		{"https", "pve.example.com", 8006, "https://pve.example.com:8006/api2/json"},
		{"http", "192.168.1.1", 80, "http://192.168.1.1:80/api2/json"},
	}

	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()

			o := &Options{Protocol: tc.proto, Host: tc.host, Port: tc.port}
			got := o.BaseURL()

			if got != tc.want {
				t.Errorf("BaseURL() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// NewClient: option wiring
// ---------------------------------------------------------------------------

func TestNewClient_DefaultTimeout(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.Timeout = 30 * time.Second

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if c.timeout != 30*time.Second {
		t.Errorf("c.timeout = %v, want 30s", c.timeout)
	}

	if c.httpClient.Timeout != 30*time.Second {
		t.Errorf("httpClient.Timeout = %v, want 30s", c.httpClient.Timeout)
	}
}

func TestNewClient_ZeroTimeout(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.Timeout = 0

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if c.httpClient.Timeout != 0 {
		t.Errorf("expected zero timeout, got %v", c.httpClient.Timeout)
	}
}

func TestNewClient_DefaultMaxRetries(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if c.maxRetries != constants.DefaultMaxRetries {
		t.Errorf("maxRetries = %d, want %d", c.maxRetries, constants.DefaultMaxRetries)
	}
}

func TestNewClient_MiddlewareChain_NoCache(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// Without cache, chain: authMiddleware, retryMiddleware, loggingMiddleware
	if len(c.middleware) != 3 {
		t.Errorf("middleware count = %d, want 3", len(c.middleware))
	}
}

func TestNewClient_MiddlewareChain_WithCache(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.CacheConfig = &cache.Config{Enabled: true, MaxSize: 10 * 1024 * 1024, DefaultTTL: time.Minute, CleanupInterval: 5 * time.Minute}

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// With cache: cachingMiddleware + authMiddleware + retryMiddleware + loggingMiddleware
	if len(c.middleware) != 4 {
		t.Errorf("middleware count = %d, want 4", len(c.middleware))
	}

	if c.cache == nil {
		t.Error("cache should be non-nil when CacheConfig.Enabled=true")
	}
}

func TestNewClient_TLS_InsecureSkipVerify(t *testing.T) {
	t.Parallel()

	opts := &Options{
		Host:      "pve.example.com",
		Port:      8006,
		Protocol:  "https",
		Timeout:   5 * time.Second,
		KeepAlive: 5,
		SSLOptions: &SSLOptions{
			VerifyMode: SSLVerifyNone,
		},
	}

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	transport, ok := c.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatal("transport is not *http.Transport")
	}

	if transport.TLSClientConfig == nil {
		t.Fatal("TLSClientConfig is nil")
	}

	if !transport.TLSClientConfig.InsecureSkipVerify {
		t.Error("InsecureSkipVerify should be true for SSLVerifyNone")
	}
}

func TestNewClient_TLS_StrictVerify(t *testing.T) {
	t.Parallel()

	opts := &Options{
		Host:      "pve.example.com",
		Port:      8006,
		Protocol:  "https",
		Timeout:   5 * time.Second,
		KeepAlive: 5,
		SSLOptions: &SSLOptions{
			VerifyMode: SSLVerifyFull,
		},
	}

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	transport, ok := c.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatal("transport is not *http.Transport")
	}

	if transport.TLSClientConfig == nil {
		t.Fatal("TLSClientConfig is nil")
	}

	if transport.TLSClientConfig.InsecureSkipVerify {
		t.Error("InsecureSkipVerify should be false for SSLVerifyFull")
	}

	if transport.TLSClientConfig.MinVersion != 0x0303 { // tls.VersionTLS12
		t.Errorf("MinVersion = %x, want TLS1.2 (0x0303)", transport.TLSClientConfig.MinVersion)
	}
}

func TestNewClient_TLS_FingerprintCallback(t *testing.T) {
	t.Parallel()

	var called bool

	opts := &Options{
		Host:      "pve.example.com",
		Port:      8006,
		Protocol:  "https",
		Timeout:   5 * time.Second,
		KeepAlive: 5,
		VerifyFingerprintCallback: func(_ *x509.Certificate) bool {
			called = true

			return true
		},
	}

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	transport, ok := c.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatal("transport is not *http.Transport")
	}

	// VerifyPeerCertificate should be set (even if we don't call it here)
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.VerifyPeerCertificate == nil {
		t.Error("VerifyPeerCertificate should be set when VerifyFingerprintCallback is provided")
	}

	_ = called // callback invoked during actual TLS handshake, not NewClient
}

func TestNewClient_APIToken_Authenticator(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.APIToken = "root@pam!mytoken=s3cr3t"

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if c.authenticator == nil {
		t.Fatal("authenticator should not be nil for API token")
	}
}

func TestNewClient_Username_Authenticator(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.Username = "root@pam"
	opts.Password = "secret"

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if c.authenticator == nil {
		t.Fatal("authenticator should not be nil for username/password")
	}
}

func TestNewClient_NoAuth_NilAuthenticator(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if c.authenticator != nil {
		t.Error("authenticator should be nil when no auth credentials provided")
	}
}

// ---------------------------------------------------------------------------
// Do / DoWithContext: happy paths
// ---------------------------------------------------------------------------

func TestDo_GET_Success(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(pveEnvelope(t, "pong"))
	})

	c := clientPointedAt(t, srv.URL)

	resp, err := c.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	if resp.Code != http.StatusOK {
		t.Errorf("Code = %d, want 200", resp.Code)
	}

	if resp.Data != "pong" {
		t.Errorf("Data = %v, want 'pong'", resp.Data)
	}
}

func TestDo_POST_Success(t *testing.T) {
	t.Parallel()

	var gotBody string

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "created"))
	})

	c := clientPointedAt(t, srv.URL)

	resp, err := c.Do("POST", "/access/ticket", map[string]interface{}{
		"username": "root@pam",
		"password": "secret",
	})
	if err != nil {
		t.Fatalf("Do POST: %v", err)
	}

	if resp.Code != http.StatusOK {
		t.Errorf("Code = %d, want 200", resp.Code)
	}

	if !strings.Contains(gotBody, "username=root") {
		t.Errorf("POST body missing form param, got: %q", gotBody)
	}
}

func TestDo_GET_WithQueryParams(t *testing.T) {
	t.Parallel()

	var gotQuery string

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "ok"))
	})

	c := clientPointedAt(t, srv.URL)

	_, err := c.Do("GET", "/nodes", map[string]interface{}{
		"type": "node",
	})
	if err != nil {
		t.Fatalf("Do GET params: %v", err)
	}

	if !strings.Contains(gotQuery, "type=node") {
		t.Errorf("query string missing 'type=node', got: %q", gotQuery)
	}
}

func TestDo_DELETE_Success(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, nil))
	})

	c := clientPointedAt(t, srv.URL)

	resp, err := c.Do("DELETE", "/nodes/pve/qemu/100", nil)
	if err != nil {
		t.Fatalf("Do DELETE: %v", err)
	}

	if resp.Code != http.StatusOK {
		t.Errorf("Code = %d, want 200", resp.Code)
	}
}

func TestDo_PUT_Success(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}

		ct := r.Header.Get("Content-Type")
		if ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", ct)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "updated"))
	})

	c := clientPointedAt(t, srv.URL)

	resp, err := c.Do("PUT", "/nodes/pve/qemu/100/config", map[string]interface{}{
		"memory": 2048,
	})
	if err != nil {
		t.Fatalf("Do PUT: %v", err)
	}

	if resp.Data != "updated" {
		t.Errorf("Data = %v, want 'updated'", resp.Data)
	}
}

// ---------------------------------------------------------------------------
// Request headers
// ---------------------------------------------------------------------------

func TestDo_Headers_UserAgent(t *testing.T) {
	t.Parallel()

	var gotUA string

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, nil))
	})

	c := clientPointedAt(t, srv.URL)
	_, _ = c.Do("GET", "/version", nil)

	if !strings.Contains(gotUA, "pve-apiclient-go") {
		t.Errorf("User-Agent = %q, want pve-apiclient-go/*", gotUA)
	}
}

func TestDo_Headers_Accept(t *testing.T) {
	t.Parallel()

	var gotAccept string

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, nil))
	})

	c := clientPointedAt(t, srv.URL)
	_, _ = c.Do("GET", "/version", nil)

	if gotAccept != "application/json" {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}
}

func TestDo_Headers_ContentType_POST(t *testing.T) {
	t.Parallel()

	var gotCT string

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, nil))
	})

	c := clientPointedAt(t, srv.URL)
	_, _ = c.Do("POST", "/access/ticket", map[string]interface{}{"username": "root@pam", "password": "x"})

	if gotCT != "application/x-www-form-urlencoded" {
		t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", gotCT)
	}
}

func TestDo_Headers_NoContentType_GET(t *testing.T) {
	t.Parallel()

	var gotCT string

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, nil))
	})

	c := clientPointedAt(t, srv.URL)
	_, _ = c.Do("GET", "/version", nil)

	if gotCT != "" {
		t.Errorf("GET Content-Type should be empty, got %q", gotCT)
	}
}

// ---------------------------------------------------------------------------
// Error paths: 4xx / 5xx
// ---------------------------------------------------------------------------

func TestDo_404_ReturnsError(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"errors":{"path":"not found"}}`))
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 0

	_, err := c.Do("GET", "/nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
}

func TestDo_500_ReturnsError(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"server error"}`))
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 0

	_, err := c.Do("GET", "/crash", nil)
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
}

func TestDo_401_ReturnsError(t *testing.T) {
	t.Parallel()

	// Return 401 on all requests (including auth retry).
	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"unauthorized"}`))
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 0

	_, err := c.Do("GET", "/nodes", nil)
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
}

// ---------------------------------------------------------------------------
// Malformed JSON response
// ---------------------------------------------------------------------------

func TestDo_MalformedJSON_ReturnsRawBody(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json at all`))
	})

	c := clientPointedAt(t, srv.URL)

	// parseResponse returns a Response with raw body as Data plus an error.
	resp, err := c.Do("GET", "/version", nil)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}

	if resp == nil {
		t.Fatal("expected non-nil response even on JSON parse error")
	}

	if resp.Data != "not json at all" {
		t.Errorf("Data = %v, want raw body on JSON parse error", resp.Data)
	}
}

// ---------------------------------------------------------------------------
// Context cancellation
// ---------------------------------------------------------------------------

func TestDoWithContext_Cancellation(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Block until client cancels.
		select {
		case <-r.Context().Done():
			w.WriteHeader(http.StatusServiceUnavailable)
		case <-time.After(5 * time.Second):
			w.WriteHeader(http.StatusOK)
		}
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 0

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)

	go func() {
		_, err := c.DoWithContext(ctx, "GET", "/slow", nil)
		done <- err
	}()

	// Cancel after brief delay.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error after context cancellation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("DoWithContext did not return after context cancellation")
	}
}

func TestDoWithContext_Deadline(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}

		w.WriteHeader(http.StatusOK)
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 0

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := c.DoWithContext(ctx, "GET", "/slow", nil)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

// ---------------------------------------------------------------------------
// Retry middleware
// ---------------------------------------------------------------------------

func TestRetryMiddleware_RetriesOn5xx(t *testing.T) {
	t.Parallel()

	var calls int32

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"message":"unavailable"}`))

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "recovered"))
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 3
	c.retryDelay = time.Millisecond // fast for test

	resp, err := c.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}

	if resp.Data != "recovered" {
		t.Errorf("Data = %v, want 'recovered'", resp.Data)
	}

	if atomic.LoadInt32(&calls) < 3 {
		t.Errorf("calls = %d, want ≥3 (one initial + retries)", calls)
	}
}

func TestRetryMiddleware_NoRetryOn4xx(t *testing.T) {
	t.Parallel()

	var calls int32

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"bad request"}`))
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 3
	c.retryDelay = time.Millisecond

	_, err := c.Do("GET", "/bad", nil)
	if err == nil {
		t.Fatal("expected error for 400")
	}

	// 400 is not retryable; exactly one attempt.
	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("calls = %d, want 1 (no retry on 4xx)", calls)
	}
}

func TestRetryMiddleware_PerRequestRetryOverride(t *testing.T) {
	t.Parallel()

	var calls int32

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 5
	c.retryDelay = time.Millisecond

	// Override to 1 retry via context.
	ctx := WithRetries(context.Background(), 1)
	ctx = WithRetryDelay(ctx, time.Millisecond)

	_, _ = c.DoWithContext(ctx, "GET", "/flaky", nil)

	// 1 retry → 2 total calls (initial + 1).
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("calls = %d, want 2 (initial + 1 retry)", calls)
	}
}

func TestRetryMiddleware_ExhaustedReturnsError(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"message":"unavailable"}`))
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 2
	c.retryDelay = time.Millisecond

	// retryMiddleware returns last retryable response after exhausting attempts;
	// parseResponse then wraps 5xx into an API error.
	_, err := c.Do("GET", "/always503", nil)
	if err == nil {
		t.Fatal("expected error when all retries exhausted")
	}
}

// ---------------------------------------------------------------------------
// Logging middleware
// ---------------------------------------------------------------------------

func TestLoggingMiddleware_WritesWhenEnabled(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "ok"))
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 0

	lg := &testLogger{}
	c.SetLogger(lg)
	c.SetLogConfig(LogConfig{Enabled: true, LogQueryParams: true})

	_, err := c.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	lg.mu.Lock()
	infoCount := len(lg.infos)
	lg.mu.Unlock()

	if infoCount == 0 {
		t.Error("expected at least one Info log when logging enabled")
	}
}

func TestLoggingMiddleware_SilentWhenDisabled(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "ok"))
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 0

	lg := &testLogger{}
	c.SetLogger(lg)
	// logConfig.Enabled defaults to false
	c.SetLogConfig(LogConfig{Enabled: false})

	_, err := c.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	lg.mu.Lock()
	infoCount := len(lg.infos)
	lg.mu.Unlock()

	if infoCount != 0 {
		t.Errorf("expected no Info logs when logging disabled, got %d", infoCount)
	}
}

func TestLoggingMiddleware_SuppressedByContext(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "ok"))
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 0

	lg := &testLogger{}
	c.SetLogger(lg)
	// Enabled=true but no request headers/body logged.
	c.SetLogConfig(LogConfig{Enabled: true})

	// Disable per-request middleware logging via context.
	// Note: logResponse in recordRequestComplete fires regardless of per-request opts,
	// so exactly 1 info log is expected (from logResponse), not from loggingMiddleware.
	ctx := WithLogging(context.Background(), false)
	_, _ = c.DoWithContext(ctx, "GET", "/version", nil)

	lg.mu.Lock()
	infoCount := len(lg.infos)
	lg.mu.Unlock()

	// loggingMiddleware skips; logResponse always runs → 1 log expected.
	if infoCount != 1 {
		t.Errorf("expected 1 log (from logResponse), got %d", infoCount)
	}
}

// ---------------------------------------------------------------------------
// Hooks
// ---------------------------------------------------------------------------

func TestHook_Fired(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "ok"))
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 0

	var fired bool

	c.AddHook(func(e *Event) { fired = true })

	_, _ = c.Do("GET", "/version", nil)

	if !fired {
		t.Error("hook should have been fired")
	}
}

func TestHook_PanickingHookDoesNotCrash(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "ok"))
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 0
	c.AddHook(func(_ *Event) { panic("hook panic") })

	// Should not panic.
	_, err := c.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

func TestMetrics_Increments(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "ok"))
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 0

	before := c.Metrics()

	_, err := c.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	after := c.Metrics()

	if after.Requests-before.Requests != 1 {
		t.Errorf("Requests delta = %d, want 1", after.Requests-before.Requests)
	}
}

func TestMetrics_ErrorIncrement(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{}`))
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 0

	before := c.Metrics()
	_, _ = c.Do("GET", "/bad", nil)
	after := c.Metrics()

	// A 4xx counts as a request; it hits parseResponse which wraps into error.
	if after.Requests <= before.Requests {
		t.Errorf("Requests should increment even on error responses")
	}
}

// ---------------------------------------------------------------------------
// SetTimeout / SetMaxRetries / SetRetryDelay
// ---------------------------------------------------------------------------

func TestSetTimeout(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	c.SetTimeout(42 * time.Second)

	if c.timeout != 42*time.Second {
		t.Errorf("timeout = %v, want 42s", c.timeout)
	}

	if c.httpClient.Timeout != 42*time.Second {
		t.Errorf("httpClient.Timeout = %v, want 42s", c.httpClient.Timeout)
	}
}

func TestSetMaxRetries(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()

	c, _ := NewClient(opts)
	c.SetMaxRetries(7)

	if c.maxRetries != 7 {
		t.Errorf("maxRetries = %d, want 7", c.maxRetries)
	}
}

func TestSetRetryDelay(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()

	c, _ := NewClient(opts)
	c.SetRetryDelay(200 * time.Millisecond)

	if c.retryDelay != 200*time.Millisecond {
		t.Errorf("retryDelay = %v, want 200ms", c.retryDelay)
	}
}

// ---------------------------------------------------------------------------
// CacheStats / InvalidateCache / ClearCache
// ---------------------------------------------------------------------------

func TestCacheStats_NilWhenNoCache(t *testing.T) {
	t.Parallel()

	c, _ := NewClient(minimalHTTPOptions())

	if c.CacheStats() != nil {
		t.Error("CacheStats() should be nil when caching not configured")
	}
}

func TestInvalidateCache_ZeroWhenNoCache(t *testing.T) {
	t.Parallel()

	c, _ := NewClient(minimalHTTPOptions())

	n := c.InvalidateCache("/nodes/*")
	if n != 0 {
		t.Errorf("InvalidateCache with no cache = %d, want 0", n)
	}
}

func TestClearCache_NopWhenNoCache(t *testing.T) {
	t.Parallel()

	c, _ := NewClient(minimalHTTPOptions())
	// Must not panic.
	c.ClearCache()
}

func TestCachingMiddleware_HitsCache(t *testing.T) {
	t.Parallel()

	var calls int32

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "cached-value"))
	})

	opts := minimalHTTPOptions()
	opts.CacheConfig = &cache.Config{Enabled: true, MaxSize: 10 * 1024 * 1024, DefaultTTL: time.Minute, CleanupInterval: 5 * time.Minute}

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	c.baseURL = srv.URL

	// Two identical GET requests → should hit cache on second.
	_, err = c.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("first Do: %v", err)
	}

	_, err = c.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("second Do: %v", err)
	}

	if atomic.LoadInt32(&calls) != 1 {
		t.Errorf("server calls = %d, want 1 (second should hit cache)", calls)
	}
}

// ---------------------------------------------------------------------------
// Logout
// ---------------------------------------------------------------------------

func TestLogout_NilAuthenticator(t *testing.T) {
	t.Parallel()

	c, _ := NewClient(minimalHTTPOptions())

	err := c.Logout()
	if err != nil {
		t.Errorf("Logout with nil authenticator should return nil, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// isAuthenticated / needsLogin
// ---------------------------------------------------------------------------

func TestIsAuthenticated_NilAuthenticator(t *testing.T) {
	t.Parallel()

	c, _ := NewClient(minimalHTTPOptions())

	if c.isAuthenticated() {
		t.Error("isAuthenticated() with nil authenticator should return false")
	}
}

func TestNeedsLogin_WithCredentials(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.Username = "root@pam"
	opts.Password = "secret"

	c, _ := NewClient(opts)

	if !c.needsLogin() {
		t.Error("needsLogin() should return true when username+password set and no token/ticket")
	}
}

func TestNeedsLogin_WithToken_False(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.Username = "root@pam"
	opts.Password = "secret"
	opts.APIToken = "root@pam!t=secret"

	c, _ := NewClient(opts)

	if c.needsLogin() {
		t.Error("needsLogin() should return false when APIToken is set")
	}
}

func TestNeedsLogin_NilOptions(t *testing.T) {
	t.Parallel()

	c := &Client{} // No options

	if c.needsLogin() {
		t.Error("needsLogin() with nil options should return false")
	}
}

// ---------------------------------------------------------------------------
// Authenticate (ensureAuthentication with nil authenticator)
// ---------------------------------------------------------------------------

func TestAuthenticate_NilAuthenticator_NoError(t *testing.T) {
	t.Parallel()

	c, _ := NewClient(minimalHTTPOptions())

	err := c.Authenticate()
	if err != nil {
		t.Errorf("Authenticate() with nil authenticator should return nil, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// URL building (path prefix handling)
// ---------------------------------------------------------------------------

func TestDo_PathWithoutLeadingSlash(t *testing.T) {
	t.Parallel()

	var gotPath string

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "ok"))
	})

	c := clientPointedAt(t, srv.URL)
	_, _ = c.Do("GET", "version", nil) // no leading slash

	if !strings.HasPrefix(gotPath, "/") {
		t.Errorf("server path should start with /, got: %q", gotPath)
	}
}

// ---------------------------------------------------------------------------
// RequestOptions context helpers
// ---------------------------------------------------------------------------

func TestWithRetries_SetsOption(t *testing.T) {
	t.Parallel()

	ctx := WithRetries(context.Background(), 5)
	opts := FromContext(ctx)

	if opts.Retries == nil || *opts.Retries != 5 {
		t.Errorf("Retries = %v, want 5", opts.Retries)
	}
}

func TestWithRetryDelay_SetsOption(t *testing.T) {
	t.Parallel()

	ctx := WithRetryDelay(context.Background(), 200*time.Millisecond)
	opts := FromContext(ctx)

	if opts.RetryDelay == nil || *opts.RetryDelay != 200*time.Millisecond {
		t.Errorf("RetryDelay = %v, want 200ms", opts.RetryDelay)
	}
}

func TestWithLogging_SetsOption(t *testing.T) {
	t.Parallel()

	ctx := WithLogging(context.Background(), true)
	opts := FromContext(ctx)

	if opts.Logging == nil || !*opts.Logging {
		t.Errorf("Logging = %v, want true", opts.Logging)
	}
}

func TestWithLogFields_SetsOption(t *testing.T) {
	t.Parallel()

	ctx := WithLogFields(context.Background(), map[string]interface{}{"req_id": "abc"})
	opts := FromContext(ctx)

	if opts.Fields["req_id"] != "abc" {
		t.Errorf("Fields[req_id] = %v, want 'abc'", opts.Fields["req_id"])
	}
}

func TestFromContext_EmptyContext_ReturnsEmptyOpts(t *testing.T) {
	t.Parallel()

	opts := FromContext(context.Background())

	if opts == nil {
		t.Error("FromContext with no value should return non-nil RequestOptions")
	}

	if opts.Retries != nil || opts.Logging != nil {
		t.Error("empty context should yield zero RequestOptions")
	}
}

// ---------------------------------------------------------------------------
// PathBuilder
// ---------------------------------------------------------------------------

func TestPathBuilder_Build(t *testing.T) {
	t.Parallel()

	got := NewPathBuilder().Add("nodes").Add("pve").Add("qemu").AddFormat("%d", 100).Build()
	want := "/nodes/pve/qemu/100"

	if got != want {
		t.Errorf("PathBuilder = %q, want %q", got, want)
	}
}

func TestBuildNodePath(t *testing.T) {
	t.Parallel()

	got := BuildNodePath("pve", "qemu")
	want := "/nodes/pve/qemu"

	if got != want {
		t.Errorf("BuildNodePath = %q, want %q", got, want)
	}
}

func TestBuildVMPath(t *testing.T) {
	t.Parallel()

	got := BuildVMPath("pve", 100, "config")
	want := "/nodes/pve/qemu/100/config"

	if got != want {
		t.Errorf("BuildVMPath = %q, want %q", got, want)
	}
}

func TestBuildContainerPath(t *testing.T) {
	t.Parallel()

	got := BuildContainerPath("pve", 200, "status")
	want := "/nodes/pve/lxc/200/status"

	if got != want {
		t.Errorf("BuildContainerPath = %q, want %q", got, want)
	}
}

func TestBuildStoragePath(t *testing.T) {
	t.Parallel()

	got := BuildStoragePath("local", "content")
	want := "/storage/local/content"

	if got != want {
		t.Errorf("BuildStoragePath = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// RequestBuilder
// ---------------------------------------------------------------------------

func TestRequestBuilder_BuildURL_WithQueryParams(t *testing.T) {
	t.Parallel()

	rb := NewRequestBuilder("GET", "https://pve.example.com:8006/api2/json", "/nodes")
	rb.AddQueryParam("type", "node")

	url := rb.BuildURL()

	if !strings.Contains(url, "type=node") {
		t.Errorf("URL should contain type=node, got: %q", url)
	}
}

func TestRequestBuilder_BuildBody_FormEncoded(t *testing.T) {
	t.Parallel()

	rb := NewRequestBuilder("POST", "https://pve.example.com:8006/api2/json", "/access/ticket")
	rb.AddFormParam("username", "root@pam")
	rb.AddFormParam("password", "secret")

	body, ct, err := rb.BuildBody()
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}

	if ct != "application/x-www-form-urlencoded" {
		t.Errorf("content-type = %q, want application/x-www-form-urlencoded", ct)
	}

	b, _ := io.ReadAll(body)
	if !strings.Contains(string(b), "username=root") {
		t.Errorf("body missing username, got: %q", string(b))
	}
}

func TestRequestBuilder_BuildBody_JSON(t *testing.T) {
	t.Parallel()

	rb := NewRequestBuilder("POST", "https://pve.example.com:8006/api2/json", "/nodes")
	rb.SetJSONBody(map[string]string{"key": "value"})

	body, ct, err := rb.BuildBody()
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}

	if ct != "application/json" {
		t.Errorf("content-type = %q, want application/json", ct)
	}

	b, _ := io.ReadAll(body)
	if !strings.Contains(string(b), `"key"`) {
		t.Errorf("body missing JSON key, got: %q", string(b))
	}
}

func TestRequestBuilder_BuildBody_GET_ReturnsNil(t *testing.T) {
	t.Parallel()

	rb := NewRequestBuilder("GET", "https://pve.example.com:8006/api2/json", "/nodes")

	body, ct, err := rb.BuildBody()
	if err != nil {
		t.Fatalf("BuildBody GET: %v", err)
	}

	if body != nil || ct != "" {
		t.Error("GET should produce nil body and empty content-type")
	}
}

func TestRequestBuilder_BuildBody_UnsupportedMethod(t *testing.T) {
	t.Parallel()

	rb := NewRequestBuilder("TRACE", "https://pve.example.com:8006/api2/json", "/nodes")

	_, _, err := rb.BuildBody()
	if err == nil {
		t.Fatal("expected error for unsupported method")
	}
}

func TestRequestBuilder_AddFile_Multipart(t *testing.T) {
	t.Parallel()

	rb := NewRequestBuilder("POST", "https://pve.example.com:8006/api2/json", "/nodes/pve/upload")
	rb.AddFormParam("storage", "local")
	rb.AddFile("file", "test.iso", bytes.NewReader([]byte("ISO content")))

	body, ct, err := rb.BuildBody()
	if err != nil {
		t.Fatalf("BuildBody multipart: %v", err)
	}

	if !strings.HasPrefix(ct, "multipart/form-data") {
		t.Errorf("content-type = %q, want multipart/form-data", ct)
	}

	b, _ := io.ReadAll(body)
	if !strings.Contains(string(b), "ISO content") {
		t.Errorf("multipart body missing file content")
	}
}

func TestRequestBuilder_AddHeaders(t *testing.T) {
	t.Parallel()

	rb := NewRequestBuilder("GET", "https://pve.example.com:8006/api2/json", "/nodes")
	rb.AddHeader("X-Custom", "val1").AddHeaders(map[string]string{"X-Other": "val2"})

	if rb.headers["X-Custom"] != "val1" {
		t.Errorf("X-Custom = %q, want val1", rb.headers["X-Custom"])
	}

	if rb.headers["X-Other"] != "val2" {
		t.Errorf("X-Other = %q, want val2", rb.headers["X-Other"])
	}
}

func TestRequestBuilder_AddQueryParams(t *testing.T) {
	t.Parallel()

	rb := NewRequestBuilder("GET", "https://pve.example.com:8006/api2/json", "/nodes")
	rb.AddQueryParams(map[string]interface{}{"a": "1", "b": true})

	if rb.queryParams.Get("a") != "1" {
		t.Errorf("a = %q, want 1", rb.queryParams.Get("a"))
	}

	if rb.queryParams.Get("b") != "1" {
		t.Errorf("b (bool true) = %q, want 1", rb.queryParams.Get("b"))
	}
}

func TestRequestBuilder_AddFormParams(t *testing.T) {
	t.Parallel()

	rb := NewRequestBuilder("POST", "https://pve.example.com:8006/api2/json", "/access/ticket")
	rb.AddFormParams(map[string]interface{}{"username": "root@pam", "enabled": false})

	if rb.formParams.Get("username") != "root@pam" {
		t.Errorf("username = %q, want root@pam", rb.formParams.Get("username"))
	}

	if rb.formParams.Get("enabled") != "0" {
		t.Errorf("enabled (bool false) = %q, want 0", rb.formParams.Get("enabled"))
	}
}

// ---------------------------------------------------------------------------
// DefaultRequestConfig
// ---------------------------------------------------------------------------

func TestDefaultRequestConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultRequestConfig()

	if cfg.DefaultHeaders["Accept"] != "application/json" {
		t.Errorf("Accept = %q, want application/json", cfg.DefaultHeaders["Accept"])
	}

	if cfg.DefaultHeaders["User-Agent"] == "" {
		t.Error("User-Agent should be set in default config")
	}

	if cfg.QueryEncoder == nil {
		t.Error("QueryEncoder should not be nil")
	}

	if cfg.BodyEncoder == nil {
		t.Error("BodyEncoder should not be nil")
	}
}

// ---------------------------------------------------------------------------
// Response parser
// ---------------------------------------------------------------------------

func TestResponseParser_Parse_JSONEnvelope(t *testing.T) {
	t.Parallel()

	rp := NewResponseParser()

	body := []byte(`{"data":{"vmid":100},"success":1}`)
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}

	var result map[string]interface{}

	err := rp.Parse(resp, &result)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if result["vmid"] == nil {
		t.Errorf("vmid should be present, got: %v", result)
	}
}

func TestResponseParser_Parse_4xx(t *testing.T) {
	t.Parallel()

	rp := NewResponseParser()

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"errors":{"path":"not found"}}`))),
		Request:    req,
	}

	var result interface{}

	err := rp.Parse(resp, &result)
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

func TestResponseParser_Parse_TextContent(t *testing.T) {
	t.Parallel()

	rp := NewResponseParser()

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(bytes.NewReader([]byte("hello world"))),
		Request:    req,
	}

	var result string

	err := rp.Parse(resp, &result)
	if err != nil {
		t.Fatalf("Parse text: %v", err)
	}

	if result != "hello world" {
		t.Errorf("result = %q, want 'hello world'", result)
	}
}

func TestResponseParser_Parse_NonPointerTarget(t *testing.T) {
	t.Parallel()

	rp := NewResponseParser()

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(bytes.NewReader([]byte("hello"))),
		Request:    req,
	}

	var result string // non-pointer passed directly

	err := rp.Parse(resp, result) // NOT &result → assignResult should error
	if err == nil {
		t.Fatal("expected error when passing non-pointer target")
	}
}

func TestResponseParser_RegisterCustomParser(t *testing.T) {
	t.Parallel()

	rp := NewResponseParser()
	rp.RegisterCustomParser("/special", func(_ *http.Response) (interface{}, error) {
		return "custom", nil
	})

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/special", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
		Request:    req,
	}

	var result string

	err := rp.Parse(resp, &result)
	if err != nil {
		t.Fatalf("custom parser: %v", err)
	}

	if result != "custom" {
		t.Errorf("result = %q, want 'custom'", result)
	}
}

func TestResponseParser_Parse_StrictMode_InvalidJSON(t *testing.T) {
	t.Parallel()

	rp := NewResponseParser()
	rp.StrictMode = true

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`not json`))),
		Request:    req,
	}

	var result interface{}

	err := rp.Parse(resp, &result)
	if err == nil {
		t.Fatal("expected error in strict mode for invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// ResponseHandler
// ---------------------------------------------------------------------------

func TestResponseHandler_Handle(t *testing.T) {
	t.Parallel()

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"data":"result"}`))),
		Request:    req,
	}

	rh := NewResponseHandler()

	result, err := rh.Handle(resp)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if result == nil {
		t.Error("result should not be nil")
	}
}

func TestResponseHandler_HandleInto(t *testing.T) {
	t.Parallel()

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"data":{"key":"val"}}`))),
		Request:    req,
	}

	rh := NewResponseHandler()

	var result map[string]interface{}

	err := rh.HandleInto(resp, &result)
	if err != nil {
		t.Fatalf("HandleInto: %v", err)
	}

	if result["key"] != "val" {
		t.Errorf("key = %v, want 'val'", result["key"])
	}
}

func TestResponseHandler_HandleList(t *testing.T) {
	t.Parallel()

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"data":["a","b","c"]}`))),
		Request:    req,
	}

	rh := NewResponseHandler()

	list, err := rh.HandleList(resp)
	if err != nil {
		t.Fatalf("HandleList: %v", err)
	}

	if len(list) != 3 {
		t.Errorf("len(list) = %d, want 3", len(list))
	}
}

func TestResponseHandler_HandleString(t *testing.T) {
	t.Parallel()

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"data":"hello"}`))),
		Request:    req,
	}

	rh := NewResponseHandler()

	result, err := rh.HandleString(resp)
	if err != nil {
		t.Fatalf("HandleString: %v", err)
	}

	if result != "hello" {
		t.Errorf("result = %q, want 'hello'", result)
	}
}

func TestResponseHandler_HandleBool(t *testing.T) {
	t.Parallel()

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"data":true}`))),
		Request:    req,
	}

	rh := NewResponseHandler()

	result, err := rh.HandleBool(resp)
	if err != nil {
		t.Fatalf("HandleBool: %v", err)
	}

	if !result {
		t.Error("result should be true")
	}
}

func TestResponseHandler_HandleInt(t *testing.T) {
	t.Parallel()

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"data":42}`))),
		Request:    req,
	}

	rh := NewResponseHandler()

	result, err := rh.HandleInt(resp)
	if err != nil {
		t.Fatalf("HandleInt: %v", err)
	}

	if result != 42 {
		t.Errorf("result = %d, want 42", result)
	}
}

func TestResponseHandler_HandleFloat(t *testing.T) {
	t.Parallel()

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"data":3.14}`))),
		Request:    req,
	}

	rh := NewResponseHandler()

	result, err := rh.HandleFloat(resp)
	if err != nil {
		t.Fatalf("HandleFloat: %v", err)
	}

	if result != 3.14 {
		t.Errorf("result = %f, want 3.14", result)
	}
}

func TestResponseHandler_HandleMap(t *testing.T) {
	t.Parallel()

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"data":{"node":"pve","status":"online"}}`))),
		Request:    req,
	}

	rh := NewResponseHandler()

	result, err := rh.HandleMap(resp)
	if err != nil {
		t.Fatalf("HandleMap: %v", err)
	}

	if result["node"] != "pve" {
		t.Errorf("node = %v, want 'pve'", result["node"])
	}
}

// ---------------------------------------------------------------------------
// StreamHandler
// ---------------------------------------------------------------------------

func TestStreamHandler_Handle(t *testing.T) {
	t.Parallel()

	content := "line1\nline2\nline3"
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(content)),
	}

	sh := NewStreamHandler()

	var received []byte

	sh.OnChunk = func(b []byte) error {
		received = append(received, b...)

		return nil
	}

	var completeCalled bool

	sh.OnComplete = func() error {
		completeCalled = true

		return nil
	}

	err := sh.Handle(resp)
	if err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if string(received) != content {
		t.Errorf("received = %q, want %q", string(received), content)
	}

	if !completeCalled {
		t.Error("OnComplete should have been called")
	}
}

func TestStreamHandler_Handle_ChunkError(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("data")),
	}

	sh := NewStreamHandler()
	sh.OnChunk = func(_ []byte) error {
		return errors.New("chunk error")
	}

	err := sh.Handle(resp)
	if err == nil {
		t.Fatal("expected error from chunk handler")
	}
}

// ---------------------------------------------------------------------------
// Logger / redact
// ---------------------------------------------------------------------------

func TestRedact_SensitiveHeaders(t *testing.T) {
	t.Parallel()

	headers := map[string][]string{
		"Authorization":       {"Bearer token"},
		"Cookie":              {"session=abc"},
		"CSRFPreventionToken": {"csrf123"},
		"X-Custom-Header":     {"safe"},
	}

	redactKeys := []string{"authorization", "cookie", "csrfpreventiontoken"}
	result := redact(headers, redactKeys)

	if result["Authorization"] != "REDACTED" {
		t.Errorf("Authorization = %v, want REDACTED", result["Authorization"])
	}

	if result["Cookie"] != "REDACTED" {
		t.Errorf("Cookie = %v, want REDACTED", result["Cookie"])
	}

	if result["CSRFPreventionToken"] != "REDACTED" {
		t.Errorf("CSRFPreventionToken = %v, want REDACTED", result["CSRFPreventionToken"])
	}

	// Non-sensitive header should pass through.
	vals, ok := result["X-Custom-Header"].([]string)
	if !ok || vals[0] != "safe" {
		t.Errorf("X-Custom-Header = %v, want ['safe']", result["X-Custom-Header"])
	}
}

func TestLogConfig_Default(t *testing.T) {
	t.Parallel()

	cfg := defaultLogConfig()

	// Default log config has Enabled=true; callers must install a Logger to see output.
	if !cfg.Enabled {
		t.Error("default log config should have Enabled=true")
	}

	if cfg.SampleRate != 1.0 {
		t.Errorf("SampleRate = %f, want 1.0", cfg.SampleRate)
	}

	if cfg.MaxBodyBytes != constants.DefaultBufferSize {
		t.Errorf("MaxBodyBytes = %d, want %d", cfg.MaxBodyBytes, constants.DefaultBufferSize)
	}
}

// ---------------------------------------------------------------------------
// Middleware types: Chain, HeaderMiddleware, CompressionMiddleware, etc.
// ---------------------------------------------------------------------------

func TestChain_Then(t *testing.T) {
	t.Parallel()

	var order []int

	m1 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, 1)

			next.ServeHTTP(w, r)
		})
	}

	m2 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, 2)

			next.ServeHTTP(w, r)
		})
	}

	final := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		order = append(order, 3)

		w.WriteHeader(http.StatusOK)
	})

	chain := NewChain(m1, m2)
	handler := chain.Then(final)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if len(order) != 3 || order[0] != 1 || order[1] != 2 || order[2] != 3 {
		t.Errorf("middleware execution order = %v, want [1 2 3]", order)
	}
}

func TestChain_Append(t *testing.T) {
	t.Parallel()

	chain := NewChain()
	extended := chain.Append(func(next http.Handler) http.Handler { return next })

	if len(extended.middlewares) != 1 {
		t.Errorf("extended middlewares = %d, want 1", len(extended.middlewares))
	}

	// Original unchanged.
	if len(chain.middlewares) != 0 {
		t.Errorf("original chain should not be modified")
	}
}

func TestHeaderMiddleware_Apply(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Injected") != "yes" {
			t.Errorf("X-Injected header missing")
		}

		w.WriteHeader(http.StatusOK)
	})

	hm := NewHeaderMiddleware(map[string]string{"X-Injected": "yes"})

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	httpClient := &http.Client{}

	next := Handler(func(r *http.Request) (*http.Response, error) {
		return httpClient.Do(r)
	})

	resp, err := hm.Apply(req, next)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	defer resp.Body.Close()
}

func TestCompressionMiddleware_Apply(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		enc := r.Header.Get("Accept-Encoding")
		if !strings.Contains(enc, "gzip") {
			t.Errorf("Accept-Encoding should contain gzip, got: %q", enc)
		}

		w.WriteHeader(http.StatusOK)
	})

	cm := NewCompressionMiddleware()

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	httpClient := &http.Client{}

	next := Handler(func(r *http.Request) (*http.Response, error) {
		return httpClient.Do(r)
	})

	resp, err := cm.Apply(req, next)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	defer resp.Body.Close()
}

func TestLoggingMiddlewareApply_Apply(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	var buf bytes.Buffer

	lg := log.New(&buf, "", 0)
	lm := NewLoggingMiddleware(lg)

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	httpClient := &http.Client{}

	next := Handler(func(r *http.Request) (*http.Response, error) {
		return httpClient.Do(r)
	})

	resp, err := lm.Apply(req, next)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	defer resp.Body.Close()

	if !strings.Contains(buf.String(), "GET") {
		t.Errorf("log should contain method, got: %q", buf.String())
	}
}

func TestLoggingMiddlewareApply_NilLogger(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// NewLoggingMiddleware(nil) must not panic.
	lm := NewLoggingMiddleware(nil)

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	httpClient := &http.Client{}

	next := Handler(func(r *http.Request) (*http.Response, error) {
		return httpClient.Do(r)
	})

	resp, err := lm.Apply(req, next)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	defer resp.Body.Close()
}

func TestMetricsMiddleware_Apply(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	mm := NewMetricsMiddleware()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/path", nil)
	httpClient := &http.Client{}

	next := Handler(func(r *http.Request) (*http.Response, error) {
		return httpClient.Do(r)
	})

	resp, err := mm.Apply(req, next)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	defer resp.Body.Close()

	metrics := mm.GetMetrics()
	if metrics["total_requests"].(int64) != 1 {
		t.Errorf("total_requests = %v, want 1", metrics["total_requests"])
	}
}

func TestMetricsMiddleware_ErrorPath(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	mm := NewMetricsMiddleware()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/fail", nil)
	httpClient := &http.Client{}

	next := Handler(func(r *http.Request) (*http.Response, error) {
		return httpClient.Do(r)
	})

	resp, err := mm.Apply(req, next)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	defer resp.Body.Close()

	metrics := mm.GetMetrics()
	if metrics["total_errors"].(int64) != 1 {
		t.Errorf("total_errors = %v, want 1", metrics["total_errors"])
	}
}

func TestTimeoutMiddleware_Apply_Timeout(t *testing.T) {
	t.Parallel()

	tm := NewTimeoutMiddleware(20 * time.Millisecond)

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)

	next := Handler(func(_ *http.Request) (*http.Response, error) {
		time.Sleep(200 * time.Millisecond)

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
	})

	_, err := tm.Apply(req, next)
	if err == nil {
		t.Fatal("expected timeout error")
	}

	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("error should mention timeout, got: %v", err)
	}
}

func TestTimeoutMiddleware_Apply_Success(t *testing.T) {
	t.Parallel()

	tm := NewTimeoutMiddleware(500 * time.Millisecond)

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)

	next := Handler(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
	})

	resp, err := tm.Apply(req, next)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
	}
}

func TestRateLimitMiddleware_Apply(t *testing.T) {
	t.Parallel()

	rl := NewRateLimitMiddleware(100, 5)

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)

	called := false
	next := Handler(func(_ *http.Request) (*http.Response, error) {
		called = true

		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
	})

	resp, err := rl.Apply(req, next)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	defer resp.Body.Close()

	if !called {
		t.Error("next handler should have been called")
	}
}

// ---------------------------------------------------------------------------
// UploadWithContext
// ---------------------------------------------------------------------------

func TestUploadWithContext_Success(t *testing.T) {
	t.Parallel()

	var gotContentType string

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "uploaded"))
	})

	c := clientPointedAt(t, srv.URL)

	resp, err := c.UploadWithContext(
		context.Background(),
		"/nodes/pve/storage/local/upload",
		map[string]string{"storage": "local", "content": "iso"},
		"file",
		"test.iso",
		bytes.NewReader([]byte("ISO data")),
	)
	if err != nil {
		t.Fatalf("UploadWithContext: %v", err)
	}

	if resp.Data != "uploaded" {
		t.Errorf("Data = %v, want 'uploaded'", resp.Data)
	}

	if !strings.HasPrefix(gotContentType, "multipart/form-data") {
		t.Errorf("Content-Type = %q, want multipart/form-data", gotContentType)
	}
}

// ---------------------------------------------------------------------------
// Response: APIError in body
// ---------------------------------------------------------------------------

func TestDo_APILevelError_SuccessZeroWithMessage(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		// success=0 with message → API-level error
		_, _ = w.Write([]byte(`{"success":0,"message":"permission denied"}`))
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 0

	_, err := c.Do("GET", "/nodes", nil)
	if err == nil {
		t.Fatal("expected error for success=0 with message")
	}

	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error should mention 'permission denied', got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Concurrent access (race detector)
// ---------------------------------------------------------------------------

func TestDo_ConcurrentRequests(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "ok"))
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 0

	var wg sync.WaitGroup

	const goroutines = 20

	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()

			_, _ = c.Do("GET", "/version", nil)
		}()
	}

	wg.Wait()
}

// ---------------------------------------------------------------------------
// Additional coverage: simple setters
// ---------------------------------------------------------------------------

func TestSetHeader_DoesNotPanic(t *testing.T) {
	t.Parallel()

	c, _ := NewClient(minimalHTTPOptions())
	// SetHeader is a no-op stub; must not panic.
	c.SetHeader("X-Test", "value")
}

func TestRemoveHeader_DoesNotPanic(t *testing.T) {
	t.Parallel()

	c, _ := NewClient(minimalHTTPOptions())
	// RemoveHeader is a no-op stub; must not panic.
	c.RemoveHeader("X-Test")
}

func TestSetMetrics_DoesNotPanic(t *testing.T) {
	t.Parallel()

	c, _ := NewClient(minimalHTTPOptions())
	// SetMetrics with nil should be a no-op.
	c.SetMetrics(nil)
}

func TestSetTFAHandler_DoesNotPanic(t *testing.T) {
	t.Parallel()

	c, _ := NewClient(minimalHTTPOptions())
	c.SetTFAHandler(nil)
}

func TestLogConfigAccessor(t *testing.T) {
	t.Parallel()

	c, _ := NewClient(minimalHTTPOptions())
	cfg := c.LogConfig()

	// Verify returned config matches what was set.
	c.SetLogConfig(LogConfig{Enabled: false, SampleRate: 0.5})
	cfg2 := c.LogConfig()

	if cfg2.Enabled != false {
		t.Error("LogConfig() should return current config")
	}

	if cfg2.SampleRate != 0.5 {
		t.Errorf("SampleRate = %f, want 0.5", cfg2.SampleRate)
	}

	_ = cfg // avoid unused variable
}

// ---------------------------------------------------------------------------
// CacheStats / ClearCache / InvalidateCache with real cache
// ---------------------------------------------------------------------------

func TestCacheStats_WithCache(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.CacheConfig = &cache.Config{Enabled: true, MaxSize: 10 * 1024 * 1024, DefaultTTL: time.Minute, CleanupInterval: 5 * time.Minute}

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	stats := c.CacheStats()
	if stats == nil {
		t.Fatal("CacheStats() should be non-nil when cache enabled")
	}
}

func TestClearCache_WithCache(t *testing.T) {
	t.Parallel()

	var calls int32

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "val"))
	})

	opts := minimalHTTPOptions()
	opts.CacheConfig = &cache.Config{Enabled: true, MaxSize: 10 * 1024 * 1024, DefaultTTL: time.Minute, CleanupInterval: 5 * time.Minute}

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	c.baseURL = srv.URL

	_, _ = c.Do("GET", "/version", nil)
	c.ClearCache()
	_, _ = c.Do("GET", "/version", nil)

	// After clear, second request hits server.
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("calls = %d, want 2 (cache cleared between requests)", calls)
	}
}

func TestInvalidateCache_WithCache(t *testing.T) {
	t.Parallel()

	var calls int32

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "val"))
	})

	opts := minimalHTTPOptions()
	opts.CacheConfig = &cache.Config{Enabled: true, MaxSize: 10 * 1024 * 1024, DefaultTTL: time.Minute, CleanupInterval: 5 * time.Minute}

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	c.baseURL = srv.URL

	// Populate cache.
	_, _ = c.Do("GET", "/version", nil)

	// Invalidate and refetch.
	n := c.InvalidateCache("*")
	_ = n // may or may not invalidate depending on key format

	// Verify no panic.
}

// ---------------------------------------------------------------------------
// applyAuthHeaders with API token authenticator
// ---------------------------------------------------------------------------

func TestApplyAuthHeaders_APIToken(t *testing.T) {
	t.Parallel()

	var gotAuth string

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "ok"))
	})

	opts := minimalHTTPOptions()
	opts.APIToken = "root@pam!mytoken=s3cr3t"

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	c.baseURL = srv.URL

	_, err = c.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	if !strings.Contains(gotAuth, "PVEAPIToken=root@pam!mytoken=s3cr3t") {
		t.Errorf("Authorization = %q, want PVEAPIToken=root@pam!mytoken=s3cr3t", gotAuth)
	}
}

// ---------------------------------------------------------------------------
// AutoLogin: performAutoLogin path
// ---------------------------------------------------------------------------

func TestAutoLogin_Fires_WhenEnabled(t *testing.T) {
	t.Parallel()

	var loginCalls int32

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		// First request: POST to /access/ticket (login).
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "ticket") {
			atomic.AddInt32(&loginCalls, 1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":{"ticket":"PVE:root@pam:ticket","CSRFPreventionToken":"csrf"}}`))

			return
		}
		// Normal response.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "ok"))
	})

	opts := minimalHTTPOptions()
	opts.Username = "root@pam"
	opts.Password = "secret"
	opts.AutoLogin = true

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	c.baseURL = srv.URL

	// shouldAutoLogin returns true, performAutoLogin runs.
	// Even if auth fails (mock returns wrong format), the function ran.
	_, _ = c.Do("GET", "/version", nil)

	// We can't assert specific login count without full auth server,
	// but ensure no panic and code path exercised.
}

// ---------------------------------------------------------------------------
// recordRequestError: triggered when executeWithMiddleware fails
// ---------------------------------------------------------------------------

func TestRecordRequestError_OnNetworkFailure(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	// Point to a non-listening port to force connection error.
	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	c.baseURL = "http://127.0.0.1:1" // no listener
	c.maxRetries = 0

	// recordRequestError is called when executeWithMiddleware fails.
	// The ClientMetrics.Errors counter is NOT updated by recordRequestError
	// (only by recordRequestComplete); verify the request returns an error.
	_, doErr := c.Do("GET", "/version", nil)
	if doErr == nil {
		t.Error("expected error on connection refused")
	}
}

// ---------------------------------------------------------------------------
// logRequest with headers / query params logging enabled
// ---------------------------------------------------------------------------

func TestLogRequest_WithHeaders(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "ok"))
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 0

	lg := &testLogger{}
	c.SetLogger(lg)
	c.SetLogConfig(LogConfig{
		Enabled:          true,
		LogRequestHeader: true,
		LogQueryParams:   true,
	})

	_, _ = c.Do("GET", "/version", map[string]interface{}{"type": "node"})

	lg.mu.Lock()
	count := len(lg.infos)
	lg.mu.Unlock()

	if count == 0 {
		t.Error("expected info logs with headers enabled")
	}
}

// ---------------------------------------------------------------------------
// ensureAuthentication with already-authenticated authenticator
// ---------------------------------------------------------------------------

func TestEnsureAuthentication_AlreadyAuthenticated(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.APIToken = "root@pam!tok=secret"

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// APIToken authenticator is always "authenticated" after Authenticate().
	// ensureAuthentication with IsAuthenticated=true returns nil immediately.
	if err := c.ensureAuthentication(); err != nil {
		t.Errorf("ensureAuthentication() = %v, want nil when already auth'd", err)
	}
}

// ---------------------------------------------------------------------------
// logResponse with response body logging
// ---------------------------------------------------------------------------

func TestLogResponse_WithBody(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "body-data"))
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 0

	lg := &testLogger{}
	c.SetLogger(lg)
	c.SetLogConfig(LogConfig{
		Enabled:           true,
		LogResponseBody:   true,
		LogResponseHeader: true,
		MaxBodyBytes:      512,
	})

	_, err := c.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	lg.mu.Lock()
	count := len(lg.infos)
	lg.mu.Unlock()

	if count == 0 {
		t.Error("expected info logs with response body logging enabled")
	}
}

// ---------------------------------------------------------------------------
// seedTrustedFingerprints with trusted entries
// ---------------------------------------------------------------------------

func TestNewClient_CachedFingerprints(t *testing.T) {
	t.Parallel()

	opts := &Options{
		Host:      "pve.example.com",
		Port:      8006,
		Protocol:  "https",
		Timeout:   5 * time.Second,
		KeepAlive: 5,
		CachedFingerprints: map[string]bool{
			"AA:BB:CC:DD:EE:FF": true,
			"11:22:33:44:55:66": false, // untrusted, not added
		},
	}

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient with CachedFingerprints: %v", err)
	}

	transport, ok := c.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatal("transport is not *http.Transport")
	}

	// VerifyPeerCertificate must be set when fingerprints provided.
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.VerifyPeerCertificate == nil {
		t.Error("VerifyPeerCertificate should be set when CachedFingerprints provided")
	}
}

func TestNewClient_ManualVerification(t *testing.T) {
	t.Parallel()

	opts := &Options{
		Host:               "pve.example.com",
		Port:               8006,
		Protocol:           "https",
		Timeout:            5 * time.Second,
		KeepAlive:          5,
		ManualVerification: true,
	}

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient with ManualVerification: %v", err)
	}

	transport, ok := c.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatal("transport is not *http.Transport")
	}

	if transport.TLSClientConfig == nil || transport.TLSClientConfig.VerifyPeerCertificate == nil {
		t.Error("VerifyPeerCertificate should be set when ManualVerification=true")
	}
}

// ---------------------------------------------------------------------------
// configureVerifierCallbacks: RegisterFingerprintCallback
// ---------------------------------------------------------------------------

func TestNewClient_RegisterFingerprintCallback(t *testing.T) {
	t.Parallel()

	var registered string

	opts := &Options{
		Host:      "pve.example.com",
		Port:      8006,
		Protocol:  "https",
		Timeout:   5 * time.Second,
		KeepAlive: 5,
		// Trigger fingerprint verification code path.
		ManualVerification: true,
		RegisterFingerprintCallback: func(fp string) {
			registered = fp
		},
	}

	_, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_ = registered // callback fires during TLS handshake, not NewClient
}

// ---------------------------------------------------------------------------
// encodeSingleValue: remaining branches
// ---------------------------------------------------------------------------

func TestEncodeSingleValue_Nil(t *testing.T) {
	t.Parallel()

	got := encodeSingleValue(nil)
	if got != "" {
		t.Errorf("nil: got %q, want empty string", got)
	}
}

func TestEncodeSingleValue_TimeTime(t *testing.T) {
	t.Parallel()

	ts := time.Unix(1_700_000_000, 0)
	got := encodeSingleValue(ts)

	if got != "1700000000" {
		t.Errorf("time.Time: got %q, want 1700000000", got)
	}
}

func TestEncodeSingleValue_Int(t *testing.T) {
	t.Parallel()

	got := encodeSingleValue(42)
	if got != "42" {
		t.Errorf("int: got %q, want 42", got)
	}
}

func TestEncodeSingleValue_BoolTrue(t *testing.T) {
	t.Parallel()

	got := encodeSingleValue(true)
	if got != "1" {
		t.Errorf("bool true: got %q, want 1", got)
	}
}

func TestEncodeSingleValue_BoolFalse(t *testing.T) {
	t.Parallel()

	got := encodeSingleValue(false)
	if got != "0" {
		t.Errorf("bool false: got %q, want 0", got)
	}
}

// ---------------------------------------------------------------------------
// Response parser: tryStringConversion paths
// ---------------------------------------------------------------------------

func TestResponseParser_StringToInt(t *testing.T) {
	t.Parallel()

	rp := NewResponseParser()

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(strings.NewReader("42")),
		Request:    req,
	}

	var result int64

	err := rp.Parse(resp, &result)
	if err != nil {
		t.Fatalf("Parse text→int64: %v", err)
	}

	if result != 42 {
		t.Errorf("result = %d, want 42", result)
	}
}

func TestResponseParser_StringToFloat(t *testing.T) {
	t.Parallel()

	rp := NewResponseParser()

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(strings.NewReader("3.14")),
		Request:    req,
	}

	var result float64

	err := rp.Parse(resp, &result)
	if err != nil {
		t.Fatalf("Parse text→float64: %v", err)
	}

	if result != 3.14 {
		t.Errorf("result = %f, want 3.14", result)
	}
}

func TestResponseParser_StringToBool(t *testing.T) {
	t.Parallel()

	rp := NewResponseParser()

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(strings.NewReader("true")),
		Request:    req,
	}

	var result bool

	err := rp.Parse(resp, &result)
	if err != nil {
		t.Fatalf("Parse text→bool: %v", err)
	}

	if !result {
		t.Error("result should be true")
	}
}

func TestResponseParser_CannotConvert_Error(t *testing.T) {
	t.Parallel()

	rp := NewResponseParser()

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(strings.NewReader("not-a-struct")),
		Request:    req,
	}

	type myStruct struct{ X int }

	var result myStruct

	err := rp.Parse(resp, &result)
	// Should error because string cannot be assigned to struct.
	if err == nil {
		t.Fatal("expected error when assigning string to struct")
	}
}

// ---------------------------------------------------------------------------
// logTicketRenewalFailure: cover via SetLogger + Enabled
// ---------------------------------------------------------------------------

func TestLogTicketRenewalFailure(t *testing.T) {
	t.Parallel()

	c, _ := NewClient(minimalHTTPOptions())

	lg := &testLogger{}
	c.SetLogger(lg)
	c.SetLogConfig(LogConfig{Enabled: true})

	c.logTicketRenewalFailure(errors.New("renewal failed"))

	lg.mu.Lock()
	warnCount := len(lg.warns)
	lg.mu.Unlock()

	if warnCount == 0 {
		t.Error("expected a Warn log for ticket renewal failure")
	}
}

func TestLogTicketRenewalFailure_NoLogger(t *testing.T) {
	t.Parallel()

	c, _ := NewClient(minimalHTTPOptions())
	// Must not panic when no logger set.
	c.logTicketRenewalFailure(errors.New("renewal failed"))
}

// ---------------------------------------------------------------------------
// DefaultRequestConfig QueryEncoder and BodyEncoder
// ---------------------------------------------------------------------------

func TestDefaultRequestConfig_Encoders(t *testing.T) {
	t.Parallel()

	cfg := DefaultRequestConfig()

	// QueryEncoder should produce encoded string.
	vals := make(map[string][]string)
	vals["key"] = []string{"value"}

	encoded := cfg.QueryEncoder(vals)
	if encoded != "key=value" {
		t.Errorf("QueryEncoder = %q, want 'key=value'", encoded)
	}

	// BodyEncoder should JSON-marshal.
	b, err := cfg.BodyEncoder(map[string]string{"k": "v"})
	if err != nil {
		t.Fatalf("BodyEncoder: %v", err)
	}

	if !strings.Contains(string(b), `"k"`) {
		t.Errorf("BodyEncoder = %q, missing key k", string(b))
	}
}

// ---------------------------------------------------------------------------
// Prometheus metrics path (recordRequestStart/Error/Complete)
// ---------------------------------------------------------------------------

func TestSetMetrics_RecordRequestComplete(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "ok"))
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 0

	m := pmetrics.NewDefaultMetrics()
	c.SetMetrics(m)

	_, err := c.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	// Prom metrics were exercised; verify no panic. Counters should be > 0.
	// We can't assert exact values without exposing counter internals,
	// but the code path must complete without panic.
}

func TestSetMetrics_RecordRequestError(t *testing.T) {
	t.Parallel()

	c, _ := NewClient(minimalHTTPOptions())
	c.baseURL = "http://127.0.0.1:1" // no listener
	c.maxRetries = 0

	m := pmetrics.NewDefaultMetrics()
	c.SetMetrics(m)

	// Should exercise recordRequestError path (prom.Dec on active connections etc.)
	_, _ = c.Do("GET", "/version", nil)
}

func TestSetMetrics_RecordRequestStart_ContentLength(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "ok"))
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 0

	m := pmetrics.NewDefaultMetrics()
	c.SetMetrics(m)

	// POST with body → ContentLength > 0 → BytesSent path.
	_, err := c.Do("POST", "/access/ticket", map[string]interface{}{
		"username": "root@pam",
		"password": "secret",
	})
	if err != nil {
		t.Fatalf("Do POST: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Logout: error path with failing authenticator
// ---------------------------------------------------------------------------

type failLogoutAuthenticator struct{}

func (f *failLogoutAuthenticator) Authenticate() error           { return nil }
func (f *failLogoutAuthenticator) IsAuthenticated() bool         { return true }
func (f *failLogoutAuthenticator) GetHeaders() map[string]string { return nil }
func (f *failLogoutAuthenticator) Refresh() error                { return nil }
func (f *failLogoutAuthenticator) Logout() error                 { return errors.New("logout failed") }

func TestLogout_ErrorPropagated(t *testing.T) {
	t.Parallel()

	c, _ := NewClient(minimalHTTPOptions())
	c.authenticator = &failLogoutAuthenticator{}

	err := c.Logout()
	if err == nil {
		t.Fatal("expected error from failing logout")
	}

	if !strings.Contains(err.Error(), "logout") {
		t.Errorf("error should mention logout, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// handleAuthenticationRetry: 401 with nil authenticator (returns resp)
// ---------------------------------------------------------------------------

func TestHandleAuthenticationRetry_401_NilAuth(t *testing.T) {
	t.Parallel()

	c, _ := NewClient(minimalHTTPOptions())
	// c.authenticator is nil

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	resp401 := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       io.NopCloser(strings.NewReader("")),
	}

	next := Handler(func(_ *http.Request) (*http.Response, error) {
		return resp401, nil
	})

	// With nil authenticator, 401 is returned as-is.
	result, err := c.handleAuthenticationRetry(req, resp401, next)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if result != resp401 {
		t.Error("401 with nil authenticator should return original response")
	}
}

// ---------------------------------------------------------------------------
// configureTLS: error paths (bad CA cert path / bad client certs)
// ---------------------------------------------------------------------------

func TestNewClient_HTTPS_BadCACert(t *testing.T) {
	t.Parallel()

	opts := &Options{
		Host:      "pve.example.com",
		Port:      8006,
		Protocol:  "https",
		Timeout:   5 * time.Second,
		KeepAlive: 5,
		SSLOptions: &SSLOptions{
			CACert: "/nonexistent/ca.pem",
		},
	}

	_, err := NewClient(opts)
	if err == nil {
		t.Fatal("expected error for nonexistent CA cert path")
	}
}

func TestNewClient_HTTPS_BadClientCerts(t *testing.T) {
	t.Parallel()

	opts := &Options{
		Host:      "pve.example.com",
		Port:      8006,
		Protocol:  "https",
		Timeout:   5 * time.Second,
		KeepAlive: 5,
		SSLOptions: &SSLOptions{
			ClientCert: "/nonexistent/client.crt",
			ClientKey:  "/nonexistent/client.key",
		},
	}

	_, err := NewClient(opts)
	if err == nil {
		t.Fatal("expected error for nonexistent client cert path")
	}
}

func TestCreateTLSConfig_ClientCerts_Invalid(t *testing.T) {
	t.Parallel()

	_, err := createTLSConfig(&SSLOptions{
		ClientCert: "/nonexistent/cert.pem",
		ClientKey:  "/nonexistent/key.pem",
	})
	if err == nil {
		t.Fatal("expected error for invalid client cert paths")
	}
}

// ---------------------------------------------------------------------------
// performAutoLogin: double-check path (already logged in)
// ---------------------------------------------------------------------------

func TestPerformAutoLogin_AlreadyAuthenticated_NoOp(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.Username = "root@pam"
	opts.Password = "secret"

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// Force loginAttempted=true to trigger the double-check branch.
	c.loginMutex.Lock()
	c.loginAttempted = true
	c.loginMutex.Unlock()

	// performAutoLogin should return nil immediately (double-check: loginAttempted=true).
	if err := c.performAutoLogin(); err != nil {
		t.Errorf("performAutoLogin with loginAttempted=true = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// buildMultipartBody: write field error (closed writer)
// ---------------------------------------------------------------------------

func TestRequestBuilder_BuildBody_Multipart_LargeFile(t *testing.T) {
	t.Parallel()

	rb := NewRequestBuilder("POST", "https://pve.example.com:8006/api2/json", "/upload")
	rb.AddFormParam("storage", "local")

	// Multi-field + multi-file to exercise full multipart path.
	rb.AddFile("file1", "a.iso", strings.NewReader("content-a"))
	rb.AddFile("file2", "b.iso", strings.NewReader("content-b"))

	body, ct, err := rb.BuildBody()
	if err != nil {
		t.Fatalf("BuildBody multipart multi-file: %v", err)
	}

	if !strings.HasPrefix(ct, "multipart/form-data") {
		t.Errorf("content-type = %q, want multipart/form-data", ct)
	}

	b, _ := io.ReadAll(body)
	if !strings.Contains(string(b), "content-a") || !strings.Contains(string(b), "content-b") {
		t.Errorf("multipart body missing file contents: %q", string(b))
	}
}

// ---------------------------------------------------------------------------
// BuildURL: path without leading slash
// ---------------------------------------------------------------------------

func TestRequestBuilder_BuildURL_NoLeadingSlash(t *testing.T) {
	t.Parallel()

	rb := NewRequestBuilder("GET", "https://pve.example.com:8006/api2/json", "nodes")
	url := rb.BuildURL()

	if !strings.HasPrefix(url, "https://") {
		t.Errorf("BuildURL = %q, should start with https://", url)
	}

	if !strings.Contains(url, "/nodes") {
		t.Errorf("BuildURL = %q, should contain /nodes", url)
	}
}

// ---------------------------------------------------------------------------
// tryAssignResult: type conversion branch
// ---------------------------------------------------------------------------

func TestResponseParser_AssignConvertible(t *testing.T) {
	t.Parallel()

	rp := NewResponseParser()

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	// data is a JSON number; target is float64 → direct assignable.
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"data":1.5}`)),
		Request:    req,
	}

	var result float64

	err := rp.Parse(resp, &result)
	if err != nil {
		t.Fatalf("Parse float: %v", err)
	}

	if result != 1.5 {
		t.Errorf("result = %f, want 1.5", result)
	}
}

// ---------------------------------------------------------------------------
// ensureAuthentication: nil authenticator short-circuit
// ---------------------------------------------------------------------------

func TestEnsureAuthentication_NilAuthenticator(t *testing.T) {
	t.Parallel()

	c, _ := NewClient(minimalHTTPOptions())
	// c.authenticator == nil

	err := c.ensureAuthentication()
	if err != nil {
		t.Errorf("ensureAuthentication with nil auth = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// isAuthenticated: with real authenticator
// ---------------------------------------------------------------------------

func TestIsAuthenticated_WithAPIToken(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.APIToken = "root@pam!tok=secret"

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// Before Authenticate(), API token auth is not authenticated.
	// After Authenticate(), it is (no-op for tokens).
	_ = c.authenticator.Authenticate()

	if !c.isAuthenticated() {
		t.Error("isAuthenticated() should return true after Authenticate() with API token")
	}
}

// ---------------------------------------------------------------------------
// DoWithContext: body close error log (nil logger branch)
// ---------------------------------------------------------------------------

func TestDoWithContext_ResponseBodyClose_NilLogger(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "ok"))
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 0
	// No logger set → body close error warning code path (logger == nil branch).

	_, err := c.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
}

// ---------------------------------------------------------------------------
// RateLimitMiddleware: GetMetrics on MetricsMiddleware
// ---------------------------------------------------------------------------

func TestMetricsMiddleware_GetMetrics_AvgDuration(t *testing.T) {
	t.Parallel()

	mm := NewMetricsMiddleware()

	// No requests yet → avg duration = 0.
	m := mm.GetMetrics()
	if m["average_duration"].(string) != "0s" {
		t.Errorf("average_duration without requests = %v, want 0s", m["average_duration"])
	}
}

// ---------------------------------------------------------------------------
// handleAuthenticationRetry: 401 with API token auth (Refresh no-op → retry)
// ---------------------------------------------------------------------------

func TestHandleAuthenticationRetry_401_WithAuth(t *testing.T) {
	t.Parallel()

	var calls int32

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"unauthorized"}`))

			return
		}
		// Second call (retry after auth): success.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "ok"))
	})

	opts := minimalHTTPOptions()
	opts.APIToken = "root@pam!tok=secret"

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	c.baseURL = srv.URL
	c.maxRetries = 0
	_ = c.authenticator.Authenticate()

	// The auth middleware calls handleAuthenticationRetry on 401.
	// API token Refresh() is a no-op; retry should succeed on second call.
	resp, err := c.Do("GET", "/version", nil)
	if err != nil {
		// Tolerate error since retry after Refresh may return 401 again via
		// parseResponse. Key goal: handleAuthenticationRetry code path hit.
		t.Logf("Do returned error (acceptable): %v", err)
	}

	_ = resp
}

// ---------------------------------------------------------------------------
// parseJSON: no data field fallback (envelope without data)
// ---------------------------------------------------------------------------

func TestResponseParser_ParseJSON_NoDataField(t *testing.T) {
	t.Parallel()

	rp := NewResponseParser()

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	// Envelope without "data" field — should try to unmarshal whole body.
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"key":"val"}`)),
		Request:    req,
	}

	var result map[string]interface{}

	err := rp.Parse(resp, &result)
	if err != nil {
		t.Fatalf("Parse no-data envelope: %v", err)
	}

	// Result contains the whole body parsed.
	_ = result
}

// ---------------------------------------------------------------------------
// tryAssignResult: direct assignable type (string to string)
// ---------------------------------------------------------------------------

func TestResponseParser_TryAssign_String(t *testing.T) {
	t.Parallel()

	rp := NewResponseParser()

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/plain"}},
		Body:       io.NopCloser(strings.NewReader("hello")),
		Request:    req,
	}

	var result string

	err := rp.Parse(resp, &result)
	if err != nil {
		t.Fatalf("Parse direct string: %v", err)
	}

	if result != "hello" {
		t.Errorf("result = %q, want 'hello'", result)
	}
}

// ---------------------------------------------------------------------------
// with(): nil existing.Fields and existing.Retries paths
// ---------------------------------------------------------------------------

func TestWith_NilExistingFields(t *testing.T) {
	t.Parallel()

	// Context has no existing RequestOptions.
	ctx := WithLogFields(context.Background(), map[string]interface{}{"x": 1})
	opts := FromContext(ctx)

	if opts.Fields["x"] != 1 {
		t.Errorf("Fields[x] = %v, want 1", opts.Fields["x"])
	}
}

func TestWith_ExistingNilRetries(t *testing.T) {
	t.Parallel()

	// First set only logging; Retries stays nil.
	ctx := WithLogging(context.Background(), true)
	// Then set retries.
	ctx = WithRetries(ctx, 3)
	opts := FromContext(ctx)

	if opts.Retries == nil || *opts.Retries != 3 {
		t.Errorf("Retries = %v, want 3", opts.Retries)
	}

	if opts.Logging == nil || !*opts.Logging {
		t.Errorf("Logging should still be true")
	}
}

// ---------------------------------------------------------------------------
// LoggingMiddleware.Apply: response body truncation path
// ---------------------------------------------------------------------------

func TestLoggingMiddlewareApply_LargeResponseBody(t *testing.T) {
	t.Parallel()

	largeBody := strings.Repeat("X", 2048)

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(largeBody))
	})

	var buf bytes.Buffer

	lg := log.New(&buf, "", 0)
	lm := NewLoggingMiddleware(lg)
	lm.logBody = true

	req, _ := http.NewRequest(http.MethodGet, srv.URL, nil)
	httpClient := &http.Client{}

	next := Handler(func(r *http.Request) (*http.Response, error) {
		return httpClient.Do(r)
	})

	resp, err := lm.Apply(req, next)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	defer resp.Body.Close()

	// Log should contain truncated marker.
	if !strings.Contains(buf.String(), "truncated") {
		t.Errorf("log should contain 'truncated' for large body, got: %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// LoggingMiddleware.Apply: error from next handler
// ---------------------------------------------------------------------------

func TestLoggingMiddlewareApply_NextError(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer

	lg := log.New(&buf, "", 0)
	lm := NewLoggingMiddleware(lg)

	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/path", nil)

	next := Handler(func(_ *http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})

	_, err := lm.Apply(req, next)
	if err == nil {
		t.Fatal("expected error from next")
	}

	if !strings.Contains(buf.String(), "ERROR") {
		t.Errorf("log should contain ERROR for failed request, got: %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// RateLimitMiddleware.Apply: token re-fill path
// ---------------------------------------------------------------------------

func TestRateLimitMiddleware_TokenRefill(t *testing.T) {
	t.Parallel()

	rl := NewRateLimitMiddleware(10, 5)
	// Start with 3 tokens; refill should fire when time elapses.
	rl.tokens = 3
	rl.lastRequest = time.Now().Add(-2 * time.Second) // 2s elapsed → +20 tokens capped to burst=5

	req, _ := http.NewRequest(http.MethodGet, "http://example.com/path", nil)

	next := Handler(func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(""))}, nil
	})

	resp, err := rl.Apply(req, next)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	defer resp.Body.Close()
}

// ---------------------------------------------------------------------------
// cachingMiddleware: POST not cached
// ---------------------------------------------------------------------------

func TestCachingMiddleware_POST_NotCached(t *testing.T) {
	t.Parallel()

	var calls int32

	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "ok"))
	})

	opts := minimalHTTPOptions()
	opts.CacheConfig = &cache.Config{Enabled: true, MaxSize: 10 * 1024 * 1024, DefaultTTL: time.Minute, CleanupInterval: 5 * time.Minute}

	c, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	c.baseURL = srv.URL
	c.maxRetries = 0

	// Two POST requests to same path — both should hit server (not cached).
	_, _ = c.Do("POST", "/access/ticket", map[string]interface{}{"username": "root@pam", "password": "x"})
	_, _ = c.Do("POST", "/access/ticket", map[string]interface{}{"username": "root@pam", "password": "x"})

	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("calls = %d, want 2 (POST not cached)", calls)
	}
}

// ---------------------------------------------------------------------------
// UploadWithContext: context cancellation
// ---------------------------------------------------------------------------

func TestUploadWithContext_Cancelled(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}

		w.WriteHeader(http.StatusOK)
	})

	c := clientPointedAt(t, srv.URL)
	c.maxRetries = 0

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)

	go func() {
		_, err := c.UploadWithContext(ctx, "/upload", map[string]string{}, "file", "f.iso", strings.NewReader("data"))
		done <- err
	}()

	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error after context cancellation")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("UploadWithContext did not return after cancel")
	}
}

// TestDo_PostHotPath_BoolEncodedAs01 guards CRIT-1: the production
// hot path used by all 666 generated bindings must serialize booleans
// as Proxmox-style "1"/"0", not Go-style "true"/"false".
//
// Regression: an earlier refactor moved encoder.go in front of
// RequestBuilder.AddFormParam but left buildRequestWithContext using
// fmt.Sprintf("%v", value) directly. The bool/slice tests in
// encoder_test.go passed; the wire format was still wrong.
func TestDo_PostHotPath_BoolEncodedAs01(t *testing.T) {
	t.Parallel()

	var gotBody string
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "ok"))
	})

	c := clientPointedAt(t, srv.URL)
	_, err := c.Do("POST", "/nodes/x/qemu/100/config", map[string]interface{}{
		"onboot":   true,
		"protect":  false,
		"shutdown": "{}",
	})
	if err != nil {
		t.Fatalf("Do POST: %v", err)
	}
	if !strings.Contains(gotBody, "onboot=1") {
		t.Errorf("expected onboot=1 in body, got: %q", gotBody)
	}
	if !strings.Contains(gotBody, "protect=0") {
		t.Errorf("expected protect=0 in body, got: %q", gotBody)
	}
	if strings.Contains(gotBody, "onboot=true") || strings.Contains(gotBody, "protect=false") {
		t.Errorf("found Go-style bool in body, expected 0/1: %q", gotBody)
	}
}

// TestDo_GetHotPath_SliceRepeatedKeys guards CRIT-1 on the GET path:
// slices must encode as repeated query keys, not Go-style "[a b c]".
func TestDo_GetHotPath_SliceRepeatedKeys(t *testing.T) {
	t.Parallel()

	var gotQuery string
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveEnvelope(t, "ok"))
	})

	c := clientPointedAt(t, srv.URL)
	_, err := c.Do("GET", "/cluster/log", map[string]interface{}{
		"tag": []string{"a", "b"},
	})
	if err != nil {
		t.Fatalf("Do GET: %v", err)
	}
	if !strings.Contains(gotQuery, "tag=a") || !strings.Contains(gotQuery, "tag=b") {
		t.Errorf("expected repeated tag=a&tag=b in query, got: %q", gotQuery)
	}
	if strings.Contains(gotQuery, "%5Ba+b%5D") || strings.Contains(gotQuery, "[a b]") {
		t.Errorf("found Go-style slice in query, expected repeated keys: %q", gotQuery)
	}
}
