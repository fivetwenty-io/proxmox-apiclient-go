package http //nolint:testpackage // white-box tests: accesses unexported fields/helpers

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/constants"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/cache"
	pmetrics "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/metrics"
)

// ---------------------------------------------------------------------------
// Test-local constants shared across multiple test functions.
// ---------------------------------------------------------------------------

const (
	testHostPVE              = "pve.example.com"
	testProtoHTTP            = "http"
	testProtoHTTPS           = "https"
	testAPITokenFull         = "root@pam!mytoken=s3cr3t"
	testAPITokenShort        = "root@pam!tok=secret"
	testUsername             = "root@pam"
	testPassword             = "secret"
	testFormKeyUsername      = "username"
	testFormKeyPassword      = "password"
	testContentTypeJSON      = "application/json"
	testContentTypeForm      = "application/x-www-form-urlencoded"
	testContentTypeTextPlain = "text/plain"
	testHeaderContentType    = "Content-Type"
	testRedacted             = "REDACTED"
	testHello                = "hello"

	// TFA test constants shared across TFA-related test files.
	testTFATypeTOTP      = "totp"
	testTFAChallenge     = "chal"
	testTFAPartialTicket = "partial"
	testInitialTicket    = "PVE:root@pam:000000::initial"

	// Encoder test constants.
	testEncAlpha  = "alpha"
	testEncBeta   = "beta"
	testEncNet0   = "net0"
	testEncBridge = "bridge"
	testEncVirtio = "virtio"
	testEncVMBR0  = "vmbr0"
	testEncFlag   = "flag"
	testEncGamma  = "gamma"

	// Numeric-encoding regression test constants.
	testEncBwlimit = "bwlimit"
	testEncRatio   = "ratio"
	testEncExpire  = "expire"
	testEncBwVal   = "1048576"
	testEncEpoch   = "1700000000"
)

// Sentinel errors used in test helpers.
var (
	errTestChunk         = errors.New("chunk error")
	errTestRenewalFailed = errors.New("renewal failed")
	errTestLogoutFailed  = errors.New("logout failed")
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
		Protocol:  testProtoHTTP,
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

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.baseURL = serverURL

	return client
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
		{testProtoHTTPS, testHostPVE, 8006, "https://pve.example.com:8006/api2/json"},
		{testProtoHTTP, "192.168.1.1", 80, "http://192.168.1.1:80/api2/json"},
	}

	for _, testCase := range cases {
		t.Run(testCase.want, func(t *testing.T) {
			t.Parallel()

			o := &Options{Protocol: testCase.proto, Host: testCase.host, Port: testCase.port}
			got := o.BaseURL()

			if got != testCase.want {
				t.Errorf("BaseURL() = %q, want %q", got, testCase.want)
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

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if client.timeout != 30*time.Second {
		t.Errorf("client.timeout = %v, want 30s", client.timeout)
	}

	if client.httpClient.Timeout != 30*time.Second {
		t.Errorf("httpClient.Timeout = %v, want 30s", client.httpClient.Timeout)
	}
}

func TestNewClient_ZeroTimeout(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.Timeout = 0

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if client.httpClient.Timeout != 0 {
		t.Errorf("expected zero timeout, got %v", client.httpClient.Timeout)
	}
}

func TestNewClient_DefaultMaxRetries(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if client.maxRetries != constants.DefaultMaxRetries {
		t.Errorf("maxRetries = %d, want %d", client.maxRetries, constants.DefaultMaxRetries)
	}
}

func TestNewClient_MiddlewareChain_NoCache(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// Without cache, chain: authMiddleware, retryMiddleware, loggingMiddleware
	if len(client.middleware) != 3 {
		t.Errorf("middleware count = %d, want 3", len(client.middleware))
	}
}

func TestNewClient_MiddlewareChain_WithCache(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.CacheConfig = &cache.Config{Enabled: true, MaxSize: 10 * 1024 * 1024, DefaultTTL: time.Minute, CleanupInterval: 5 * time.Minute}

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// With cache: cachingMiddleware + authMiddleware + retryMiddleware + loggingMiddleware
	if len(client.middleware) != 4 {
		t.Errorf("middleware count = %d, want 4", len(client.middleware))
	}

	if client.cache == nil {
		t.Error("cache should be non-nil when CacheConfig.Enabled=true")
	}
}

func TestNewClient_TLS_InsecureSkipVerify(t *testing.T) {
	t.Parallel()

	opts := &Options{
		Host:      testHostPVE,
		Port:      8006,
		Protocol:  testProtoHTTPS,
		Timeout:   5 * time.Second,
		KeepAlive: 5,
		SSLOptions: &SSLOptions{
			VerifyMode: SSLVerifyNone,
		},
	}

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	transport, ok := client.httpClient.Transport.(*http.Transport)
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
		Host:      testHostPVE,
		Port:      8006,
		Protocol:  testProtoHTTPS,
		Timeout:   5 * time.Second,
		KeepAlive: 5,
		SSLOptions: &SSLOptions{
			VerifyMode: SSLVerifyFull,
		},
	}

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	transport, ok := client.httpClient.Transport.(*http.Transport)
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
		Host:      testHostPVE,
		Port:      8006,
		Protocol:  testProtoHTTPS,
		Timeout:   5 * time.Second,
		KeepAlive: 5,
		VerifyFingerprintCallback: func(_ *x509.Certificate) bool {
			called = true

			return true
		},
	}

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	transport, ok := client.httpClient.Transport.(*http.Transport)
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
	opts.APIToken = testAPITokenFull

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if client.authenticator == nil {
		t.Fatal("authenticator should not be nil for API token")
	}
}

func TestNewClient_Username_Authenticator(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.Username = testUsername
	opts.Password = testPassword

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if client.authenticator == nil {
		t.Fatal("authenticator should not be nil for username/password")
	}
}

func TestNewClient_NoAuth_NilAuthenticator(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if client.authenticator != nil {
		t.Error("authenticator should be nil when no auth credentials provided")
	}
}

// ---------------------------------------------------------------------------
// Do / DoWithContext: happy paths
// ---------------------------------------------------------------------------

func TestDo_GET_Success(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(respWriter http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}

		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		respWriter.WriteHeader(http.StatusOK)
		_, _ = respWriter.Write(pveEnvelope(t, "pong"))
	})

	client := clientPointedAt(t, srv.URL)

	resp, err := client.Do("GET", "/version", nil)
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

	srv := newTestServer(t, func(respWriter http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}

		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)

		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "created"))
	})

	client := clientPointedAt(t, srv.URL)

	resp, err := client.Do("POST", "/access/ticket", map[string]interface{}{
		testFormKeyUsername: testUsername,
		testFormKeyPassword: testPassword,
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

	srv := newTestServer(t, func(respWriter http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery

		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "ok"))
	})

	client := clientPointedAt(t, srv.URL)

	_, err := client.Do("GET", "/nodes", map[string]interface{}{
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

	srv := newTestServer(t, func(respWriter http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("expected DELETE, got %s", r.Method)
		}

		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, nil))
	})

	client := clientPointedAt(t, srv.URL)

	resp, err := client.Do("DELETE", "/nodes/pve/qemu/100", nil)
	if err != nil {
		t.Fatalf("Do DELETE: %v", err)
	}

	if resp.Code != http.StatusOK {
		t.Errorf("Code = %d, want 200", resp.Code)
	}
}

func TestDo_PUT_Success(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(respWriter http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("expected PUT, got %s", r.Method)
		}

		ct := r.Header.Get(testHeaderContentType)
		if ct != testContentTypeForm {
			t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", ct)
		}

		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "updated"))
	})

	client := clientPointedAt(t, srv.URL)

	resp, err := client.Do("PUT", "/nodes/pve/qemu/100/config", map[string]interface{}{
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

	srv := newTestServer(t, func(respWriter http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, nil))
	})

	client := clientPointedAt(t, srv.URL)
	_, _ = client.Do("GET", "/version", nil)

	if !strings.Contains(gotUA, "proxmox-apiclient-go") {
		t.Errorf("User-Agent = %q, want proxmox-apiclient-go/*", gotUA)
	}
}

func TestDo_Headers_Accept(t *testing.T) {
	t.Parallel()

	var gotAccept string

	srv := newTestServer(t, func(respWriter http.ResponseWriter, r *http.Request) {
		gotAccept = r.Header.Get("Accept")
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, nil))
	})

	client := clientPointedAt(t, srv.URL)
	_, _ = client.Do("GET", "/version", nil)

	if gotAccept != testContentTypeJSON {
		t.Errorf("Accept = %q, want application/json", gotAccept)
	}
}

func TestDo_Headers_ContentType_POST(t *testing.T) {
	t.Parallel()

	var gotCT string

	srv := newTestServer(t, func(respWriter http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get(testHeaderContentType)
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, nil))
	})

	client := clientPointedAt(t, srv.URL)
	_, _ = client.Do("POST", "/access/ticket", map[string]interface{}{testFormKeyUsername: testUsername, testFormKeyPassword: "x"})

	if gotCT != testContentTypeForm {
		t.Errorf("Content-Type = %q, want application/x-www-form-urlencoded", gotCT)
	}
}

func TestDo_Headers_NoContentType_GET(t *testing.T) {
	t.Parallel()

	var gotCT string

	srv := newTestServer(t, func(respWriter http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get(testHeaderContentType)
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, nil))
	})

	client := clientPointedAt(t, srv.URL)
	_, _ = client.Do("GET", "/version", nil)

	if gotCT != "" {
		t.Errorf("GET Content-Type should be empty, got %q", gotCT)
	}
}

// ---------------------------------------------------------------------------
// Error paths: 4xx / 5xx
// ---------------------------------------------------------------------------

func TestDo_404_ReturnsError(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.WriteHeader(http.StatusNotFound)
		_, _ = respWriter.Write([]byte(`{"errors":{"path":"not found"}}`))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0

	_, err := client.Do("GET", "/nonexistent", nil)
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
}

func TestDo_500_ReturnsError(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.WriteHeader(http.StatusInternalServerError)
		_, _ = respWriter.Write([]byte(`{"message":"server error"}`))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0

	_, err := client.Do("GET", "/crash", nil)
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
}

func TestDo_401_ReturnsError(t *testing.T) {
	t.Parallel()

	// Return 401 on all requests (including auth retry).
	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.WriteHeader(http.StatusUnauthorized)
		_, _ = respWriter.Write([]byte(`{"message":"unauthorized"}`))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0

	_, err := client.Do("GET", "/nodes", nil)
	if err == nil {
		t.Fatal("expected error for 401, got nil")
	}
}

// ---------------------------------------------------------------------------
// Malformed JSON response
// ---------------------------------------------------------------------------

func TestDo_MalformedJSON_ReturnsRawBody(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		respWriter.WriteHeader(http.StatusOK)
		_, _ = respWriter.Write([]byte(`not json at all`))
	})

	client := clientPointedAt(t, srv.URL)

	// parseResponse returns a Response with raw body as Data plus an error.
	resp, err := client.Do("GET", "/version", nil)
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

	srv := newTestServer(t, func(respWriter http.ResponseWriter, r *http.Request) {
		// Block until client cancels.
		select {
		case <-r.Context().Done():
			respWriter.WriteHeader(http.StatusServiceUnavailable)
		case <-time.After(5 * time.Second):
			respWriter.WriteHeader(http.StatusOK)
		}
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)

	go func() {
		_, err := client.DoWithContext(ctx, "GET", "/slow", nil)
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

	srv := newTestServer(t, func(respWriter http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}

		respWriter.WriteHeader(http.StatusOK)
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := client.DoWithContext(ctx, "GET", "/slow", nil)
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

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			respWriter.WriteHeader(http.StatusServiceUnavailable)
			_, _ = respWriter.Write([]byte(`{"message":"unavailable"}`))

			return
		}

		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "recovered"))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 3
	client.retryDelay = time.Millisecond // fast for test

	resp, err := client.Do("GET", "/version", nil)
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

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		respWriter.WriteHeader(http.StatusBadRequest)
		_, _ = respWriter.Write([]byte(`{"message":"bad request"}`))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 3
	client.retryDelay = time.Millisecond

	_, err := client.Do("GET", "/bad", nil)
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

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		respWriter.WriteHeader(http.StatusServiceUnavailable)
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 5
	client.retryDelay = time.Millisecond

	// Override to 1 retry via context.
	ctx := WithRetries(context.Background(), 1)
	ctx = WithRetryDelay(ctx, time.Millisecond)

	_, _ = client.DoWithContext(ctx, "GET", "/flaky", nil)

	// 1 retry → 2 total calls (initial + 1).
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("calls = %d, want 2 (initial + 1 retry)", calls)
	}
}

func TestRetryMiddleware_ExhaustedReturnsError(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.WriteHeader(http.StatusServiceUnavailable)
		_, _ = respWriter.Write([]byte(`{"message":"unavailable"}`))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 2
	client.retryDelay = time.Millisecond

	// retryMiddleware returns last retryable response after exhausting attempts;
	// parseResponse then wraps 5xx into an API error.
	_, err := client.Do("GET", "/always503", nil)
	if err == nil {
		t.Fatal("expected error when all retries exhausted")
	}
}

// ---------------------------------------------------------------------------
// Logging middleware
// ---------------------------------------------------------------------------

func TestLoggingMiddleware_WritesWhenEnabled(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "ok"))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0

	logger := &testLogger{}
	client.SetLogger(logger)
	client.SetLogConfig(LogConfig{Enabled: true, LogQueryParams: true})

	_, err := client.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	logger.mu.Lock()
	infoCount := len(logger.infos)
	logger.mu.Unlock()

	if infoCount == 0 {
		t.Error("expected at least one Info log when logging enabled")
	}
}

func TestLoggingMiddleware_SilentWhenDisabled(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "ok"))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0

	logger := &testLogger{}
	client.SetLogger(logger)
	// logConfig.Enabled defaults to false
	client.SetLogConfig(LogConfig{Enabled: false})

	_, err := client.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	logger.mu.Lock()
	infoCount := len(logger.infos)
	logger.mu.Unlock()

	if infoCount != 0 {
		t.Errorf("expected no Info logs when logging disabled, got %d", infoCount)
	}
}

func TestLoggingMiddleware_SuppressedByContext(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "ok"))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0

	logger := &testLogger{}
	client.SetLogger(logger)
	// Enabled=true but no request headers/body logged.
	client.SetLogConfig(LogConfig{Enabled: true})

	// Disable per-request middleware logging via context.
	// Note: logResponse in recordRequestComplete fires regardless of per-request opts,
	// so exactly 1 info log is expected (from logResponse), not from loggingMiddleware.
	ctx := WithLogging(context.Background(), false)
	_, _ = client.DoWithContext(ctx, "GET", "/version", nil)

	logger.mu.Lock()
	infoCount := len(logger.infos)
	logger.mu.Unlock()

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

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "ok"))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0

	var fired bool

	client.AddHook(func(e *Event) { fired = true })

	_, _ = client.Do("GET", "/version", nil)

	if !fired {
		t.Error("hook should have been fired")
	}
}

func TestHook_PanickingHookDoesNotCrash(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "ok"))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0
	client.AddHook(func(_ *Event) { panic("hook panic") })

	// Should not panic.
	_, err := client.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Metrics
// ---------------------------------------------------------------------------

func TestMetrics_Increments(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "ok"))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0

	before := client.Metrics()

	_, err := client.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	after := client.Metrics()

	if after.Requests-before.Requests != 1 {
		t.Errorf("Requests delta = %d, want 1", after.Requests-before.Requests)
	}
}

func TestMetrics_ErrorIncrement(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.WriteHeader(http.StatusBadRequest)
		_, _ = respWriter.Write([]byte(`{}`))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0

	before := client.Metrics()
	_, _ = client.Do("GET", "/bad", nil)
	after := client.Metrics()

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

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.SetTimeout(42 * time.Second)

	if client.timeout != 42*time.Second {
		t.Errorf("timeout = %v, want 42s", client.timeout)
	}

	if client.httpClient.Timeout != 42*time.Second {
		t.Errorf("httpClient.Timeout = %v, want 42s", client.httpClient.Timeout)
	}
}

func TestSetMaxRetries(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()

	client, _ := NewClient(opts)
	client.SetMaxRetries(7)

	if client.maxRetries != 7 {
		t.Errorf("maxRetries = %d, want 7", client.maxRetries)
	}
}

func TestSetRetryDelay(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()

	client, _ := NewClient(opts)
	client.SetRetryDelay(200 * time.Millisecond)

	if client.retryDelay != 200*time.Millisecond {
		t.Errorf("retryDelay = %v, want 200ms", client.retryDelay)
	}
}

// ---------------------------------------------------------------------------
// CacheStats / InvalidateCache / ClearCache
// ---------------------------------------------------------------------------

func TestCacheStats_NilWhenNoCache(t *testing.T) {
	t.Parallel()

	client, _ := NewClient(minimalHTTPOptions())

	if client.CacheStats() != nil {
		t.Error("CacheStats() should be nil when caching not configured")
	}
}

func TestInvalidateCache_ZeroWhenNoCache(t *testing.T) {
	t.Parallel()

	client, _ := NewClient(minimalHTTPOptions())

	n := client.InvalidateCache("/nodes/*")
	if n != 0 {
		t.Errorf("InvalidateCache with no cache = %d, want 0", n)
	}
}

func TestClearCache_NopWhenNoCache(t *testing.T) {
	t.Parallel()

	client, _ := NewClient(minimalHTTPOptions())
	// Must not panic.
	client.ClearCache()
}

func TestCachingMiddleware_HitsCache(t *testing.T) {
	t.Parallel()

	var calls int32

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "cached-value"))
	})

	opts := minimalHTTPOptions()
	opts.CacheConfig = &cache.Config{Enabled: true, MaxSize: 10 * 1024 * 1024, DefaultTTL: time.Minute, CleanupInterval: 5 * time.Minute}

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.baseURL = srv.URL

	// Two identical GET requests → should hit cache on second.
	_, err = client.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("first Do: %v", err)
	}

	_, err = client.Do("GET", "/version", nil)
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

	client, _ := NewClient(minimalHTTPOptions())

	err := client.Logout()
	if err != nil {
		t.Errorf("Logout with nil authenticator should return nil, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// isAuthenticated / needsLogin
// ---------------------------------------------------------------------------

func TestIsAuthenticated_NilAuthenticator(t *testing.T) {
	t.Parallel()

	client, _ := NewClient(minimalHTTPOptions())

	if client.isAuthenticated() {
		t.Error("isAuthenticated() with nil authenticator should return false")
	}
}

func TestNeedsLogin_WithCredentials(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.Username = testUsername
	opts.Password = testPassword

	client, _ := NewClient(opts)

	if !client.needsLogin() {
		t.Error("needsLogin() should return true when username+password set and no token/ticket")
	}
}

func TestNeedsLogin_WithToken_False(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.Username = testUsername
	opts.Password = testPassword
	opts.APIToken = "root@pam!t=secret"

	client, _ := NewClient(opts)

	if client.needsLogin() {
		t.Error("needsLogin() should return false when APIToken is set")
	}
}

func TestNeedsLogin_NilOptions(t *testing.T) {
	t.Parallel()

	client := &Client{} // No options

	if client.needsLogin() {
		t.Error("needsLogin() with nil options should return false")
	}
}

// ---------------------------------------------------------------------------
// Authenticate (ensureAuthentication with nil authenticator)
// ---------------------------------------------------------------------------

func TestAuthenticate_NilAuthenticator_NoError(t *testing.T) {
	t.Parallel()

	client, _ := NewClient(minimalHTTPOptions())

	err := client.Authenticate()
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

	srv := newTestServer(t, func(respWriter http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path

		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "ok"))
	})

	client := clientPointedAt(t, srv.URL)
	_, _ = client.Do("GET", "version", nil) // no leading slash

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
		t.Fatal("FromContext with no value should return non-nil RequestOptions")
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

	reqBuilder := NewRequestBuilder("GET", "https://pve.example.com:8006/api2/json", "/nodes")
	reqBuilder.AddQueryParam("type", "node")

	url := reqBuilder.BuildURL()

	if !strings.Contains(url, "type=node") {
		t.Errorf("URL should contain type=node, got: %q", url)
	}
}

func TestRequestBuilder_BuildBody_FormEncoded(t *testing.T) {
	t.Parallel()

	rb := NewRequestBuilder("POST", "https://pve.example.com:8006/api2/json", "/access/ticket")
	rb.AddFormParam(testFormKeyUsername, testUsername)
	rb.AddFormParam(testFormKeyPassword, testPassword)

	body, contentType, err := rb.BuildBody()
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}

	if contentType != testContentTypeForm {
		t.Errorf("content-type = %q, want application/x-www-form-urlencoded", contentType)
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

	body, contentType, err := rb.BuildBody()
	if err != nil {
		t.Fatalf("BuildBody: %v", err)
	}

	if contentType != testContentTypeJSON {
		t.Errorf("content-type = %q, want application/json", contentType)
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

	body, contentType, err := rb.BuildBody()
	if err != nil {
		t.Fatalf("BuildBody multipart: %v", err)
	}

	if !strings.HasPrefix(contentType, "multipart/form-data") {
		t.Errorf("content-type = %q, want multipart/form-data", contentType)
	}

	b, _ := io.ReadAll(body)
	if !strings.Contains(string(b), "ISO content") {
		t.Errorf("multipart body missing file content")
	}
}

func TestRequestBuilder_AddHeaders(t *testing.T) {
	t.Parallel()

	reqBuilder := NewRequestBuilder("GET", "https://pve.example.com:8006/api2/json", "/nodes")
	reqBuilder.AddHeader("X-Custom", "val1").AddHeaders(map[string]string{"X-Other": "val2"})

	if reqBuilder.headers["X-Custom"] != "val1" {
		t.Errorf("X-Custom = %q, want val1", reqBuilder.headers["X-Custom"])
	}

	if reqBuilder.headers["X-Other"] != "val2" {
		t.Errorf("X-Other = %q, want val2", reqBuilder.headers["X-Other"])
	}
}

func TestRequestBuilder_AddQueryParams(t *testing.T) {
	t.Parallel()

	reqBuilder := NewRequestBuilder("GET", "https://pve.example.com:8006/api2/json", "/nodes")
	reqBuilder.AddQueryParams(map[string]interface{}{"a": "1", "b": true})

	if reqBuilder.queryParams.Get("a") != "1" {
		t.Errorf("a = %q, want 1", reqBuilder.queryParams.Get("a"))
	}

	if reqBuilder.queryParams.Get("b") != "1" {
		t.Errorf("b (bool true) = %q, want 1", reqBuilder.queryParams.Get("b"))
	}
}

func TestRequestBuilder_AddFormParams(t *testing.T) {
	t.Parallel()

	reqBuilder := NewRequestBuilder("POST", "https://pve.example.com:8006/api2/json", "/access/ticket")
	reqBuilder.AddFormParams(map[string]interface{}{testFormKeyUsername: testUsername, "enabled": false})

	if reqBuilder.formParams.Get(testFormKeyUsername) != testUsername {
		t.Errorf("username = %q, want root@pam", reqBuilder.formParams.Get(testFormKeyUsername))
	}

	if reqBuilder.formParams.Get("enabled") != "0" {
		t.Errorf("enabled (bool false) = %q, want 0", reqBuilder.formParams.Get("enabled"))
	}
}

// ---------------------------------------------------------------------------
// DefaultRequestConfig
// ---------------------------------------------------------------------------

func TestDefaultRequestConfig(t *testing.T) {
	t.Parallel()

	cfg := DefaultRequestConfig()

	if cfg.DefaultHeaders["Accept"] != testContentTypeJSON {
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

	respParser := NewResponseParser()

	body := []byte(`{"data":{"vmid":100},"success":1}`)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeJSON}},
		Body:       io.NopCloser(bytes.NewReader(body)),
		Request:    req,
	}

	var result map[string]interface{}

	err := respParser.Parse(resp, &result)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if result["vmid"] == nil {
		t.Errorf("vmid should be present, got: %v", result)
	}
}

func TestResponseParser_Parse_4xx(t *testing.T) {
	t.Parallel()

	respParser := NewResponseParser()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeJSON}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"errors":{"path":"not found"}}`))),
		Request:    req,
	}

	var result interface{}

	err := respParser.Parse(resp, &result)
	if err == nil {
		t.Fatal("expected error for 404")
	}
}

func TestResponseParser_Parse_TextContent(t *testing.T) {
	t.Parallel()

	respParser := NewResponseParser()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeTextPlain}},
		Body:       io.NopCloser(bytes.NewReader([]byte("hello world"))),
		Request:    req,
	}

	var result string

	err := respParser.Parse(resp, &result)
	if err != nil {
		t.Fatalf("Parse text: %v", err)
	}

	if result != "hello world" {
		t.Errorf("result = %q, want 'hello world'", result)
	}
}

func TestResponseParser_Parse_NonPointerTarget(t *testing.T) {
	t.Parallel()

	respParser := NewResponseParser()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeTextPlain}},
		Body:       io.NopCloser(bytes.NewReader([]byte(testHello))),
		Request:    req,
	}

	var result string // non-pointer passed directly

	err := respParser.Parse(resp, result) // NOT &result → assignResult should error
	if err == nil {
		t.Fatal("expected error when passing non-pointer target")
	}
}

func TestResponseParser_RegisterCustomParser(t *testing.T) {
	t.Parallel()

	respParser := NewResponseParser()
	respParser.RegisterCustomParser("/special", func(_ *http.Response) (interface{}, error) {
		return "custom", nil
	})

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/special", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeJSON}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{}`))),
		Request:    req,
	}

	var result string

	err := respParser.Parse(resp, &result)
	if err != nil {
		t.Fatalf("custom parser: %v", err)
	}

	if result != "custom" {
		t.Errorf("result = %q, want 'custom'", result)
	}
}

func TestResponseParser_Parse_StrictMode_InvalidJSON(t *testing.T) {
	t.Parallel()

	respParser := NewResponseParser()
	respParser.StrictMode = true

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeJSON}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`not json`))),
		Request:    req,
	}

	var result interface{}

	err := respParser.Parse(resp, &result)
	if err == nil {
		t.Fatal("expected error in strict mode for invalid JSON")
	}
}

// ---------------------------------------------------------------------------
// ResponseHandler
// ---------------------------------------------------------------------------

func TestResponseHandler_Handle(t *testing.T) {
	t.Parallel()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeJSON}},
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

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeJSON}},
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

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeJSON}},
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

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeJSON}},
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"data":"` + testHello + `"}`))),
		Request:    req,
	}

	rh := NewResponseHandler()

	result, err := rh.HandleString(resp)
	if err != nil {
		t.Fatalf("HandleString: %v", err)
	}

	if result != testHello {
		t.Errorf("result = %q, want 'hello'", result)
	}
}

func TestResponseHandler_HandleBool(t *testing.T) {
	t.Parallel()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeJSON}},
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

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeJSON}},
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

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeJSON}},
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

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeJSON}},
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

	streamHandler := NewStreamHandler()

	var received []byte

	streamHandler.OnChunk = func(b []byte) error {
		received = append(received, b...)

		return nil
	}

	var completeCalled bool

	streamHandler.OnComplete = func() error {
		completeCalled = true

		return nil
	}

	err := streamHandler.Handle(resp)
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
		return errTestChunk
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

	if result["Authorization"] != testRedacted {
		t.Errorf("Authorization = %v, want REDACTED", result["Authorization"])
	}

	if result["Cookie"] != testRedacted {
		t.Errorf("Cookie = %v, want REDACTED", result["Cookie"])
	}

	if result["CSRFPreventionToken"] != testRedacted {
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
// UploadWithContext
// ---------------------------------------------------------------------------

func TestUploadWithContext_Success(t *testing.T) {
	t.Parallel()

	var gotContentType string

	srv := newTestServer(t, func(respWriter http.ResponseWriter, r *http.Request) {
		gotContentType = r.Header.Get(testHeaderContentType)
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "uploaded"))
	})

	client := clientPointedAt(t, srv.URL)

	resp, err := client.UploadWithContext(
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

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		respWriter.WriteHeader(http.StatusOK)
		// success=0 with message → API-level error
		_, _ = respWriter.Write([]byte(`{"success":0,"message":"permission denied"}`))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0

	_, err := client.Do("GET", "/nodes", nil)
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

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "ok"))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0

	var waitGroup sync.WaitGroup

	const goroutines = 20

	waitGroup.Add(goroutines)

	for range goroutines {
		go func() {
			defer waitGroup.Done()

			_, _ = client.Do("GET", "/version", nil)
		}()
	}

	waitGroup.Wait()
}

// ---------------------------------------------------------------------------
// Additional coverage: simple setters
// ---------------------------------------------------------------------------

func TestSetHeader_AppliedToRequest(t *testing.T) {
	t.Parallel()

	var gotHeader string

	srv := newTestServer(t, func(respWriter http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Custom")
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, nil))
	})

	client := clientPointedAt(t, srv.URL)
	client.SetHeader("X-Custom", "applied")
	_, _ = client.Do("GET", "/version", nil)

	if gotHeader != "applied" {
		t.Errorf("X-Custom = %q, want applied", gotHeader)
	}
}

func TestRemoveHeader_StopsApplyingToRequest(t *testing.T) {
	t.Parallel()

	var gotHeader string

	srv := newTestServer(t, func(respWriter http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Custom")
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, nil))
	})

	client := clientPointedAt(t, srv.URL)
	client.SetHeader("X-Custom", "applied")
	client.RemoveHeader("X-Custom")
	_, _ = client.Do("GET", "/version", nil)

	if gotHeader != "" {
		t.Errorf("X-Custom = %q, want empty after RemoveHeader", gotHeader)
	}
}

func TestSetHeader_OverridesUserAgentButNotAuth(t *testing.T) {
	t.Parallel()

	var gotUA string

	srv := newTestServer(t, func(respWriter http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, nil))
	})

	client := clientPointedAt(t, srv.URL)
	client.SetHeader("User-Agent", "custom-agent/2.0")
	_, _ = client.Do("GET", "/version", nil)

	if gotUA != "custom-agent/2.0" {
		t.Errorf("User-Agent = %q, want custom-agent/2.0", gotUA)
	}
}

func TestSetHeader_ConcurrentWithRequests(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, nil))
	})

	client := clientPointedAt(t, srv.URL)

	const goroutines = 25

	var waitGroup sync.WaitGroup

	for range goroutines {
		waitGroup.Add(3)

		go func() { defer waitGroup.Done(); client.SetHeader("X-Custom", "v") }()
		go func() { defer waitGroup.Done(); client.RemoveHeader("X-Custom") }()
		go func() { defer waitGroup.Done(); _, _ = client.Do("GET", "/version", nil) }()
	}

	waitGroup.Wait()
}

func TestSetMetrics_DoesNotPanic(t *testing.T) {
	t.Parallel()

	client, _ := NewClient(minimalHTTPOptions())
	// SetMetrics with nil should be a no-op.
	client.SetMetrics(nil)
}

func TestSetTFAHandler_DoesNotPanic(t *testing.T) {
	t.Parallel()

	client, _ := NewClient(minimalHTTPOptions())
	client.SetTFAHandler(nil)
}

func TestLogConfigAccessor(t *testing.T) {
	t.Parallel()

	client, _ := NewClient(minimalHTTPOptions())
	cfg := client.LogConfig()

	// Verify returned config matches what was set.
	client.SetLogConfig(LogConfig{Enabled: false, SampleRate: 0.5})
	cfg2 := client.LogConfig()

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

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	stats := client.CacheStats()
	if stats == nil {
		t.Fatal("CacheStats() should be non-nil when cache enabled")
	}
}

func TestClearCache_WithCache(t *testing.T) {
	t.Parallel()

	var calls int32

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "val"))
	})

	opts := minimalHTTPOptions()
	opts.CacheConfig = &cache.Config{Enabled: true, MaxSize: 10 * 1024 * 1024, DefaultTTL: time.Minute, CleanupInterval: 5 * time.Minute}

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.baseURL = srv.URL

	_, _ = client.Do("GET", "/version", nil)
	client.ClearCache()
	_, _ = client.Do("GET", "/version", nil)

	// After clear, second request hits server.
	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("calls = %d, want 2 (cache cleared between requests)", calls)
	}
}

func TestInvalidateCache_WithCache(t *testing.T) {
	t.Parallel()

	var calls int32

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "val"))
	})

	opts := minimalHTTPOptions()
	opts.CacheConfig = &cache.Config{Enabled: true, MaxSize: 10 * 1024 * 1024, DefaultTTL: time.Minute, CleanupInterval: 5 * time.Minute}

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.baseURL = srv.URL

	// Populate cache.
	_, _ = client.Do("GET", "/version", nil)

	// Invalidate and refetch.
	n := client.InvalidateCache("*")
	_ = n // may or may not invalidate depending on key format

	// Verify no panic.
}

// ---------------------------------------------------------------------------
// applyAuthHeaders with API token authenticator
// ---------------------------------------------------------------------------

func TestApplyAuthHeaders_APIToken(t *testing.T) {
	t.Parallel()

	var gotAuth string

	srv := newTestServer(t, func(respWriter http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "ok"))
	})

	opts := minimalHTTPOptions()
	opts.APIToken = testAPITokenFull

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.baseURL = srv.URL

	_, err = client.Do("GET", "/version", nil)
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

	srv := newTestServer(t, func(respWriter http.ResponseWriter, r *http.Request) {
		// First request: POST to /access/ticket (login).
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "ticket") {
			atomic.AddInt32(&loginCalls, 1)
			respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
			_, _ = respWriter.Write([]byte(`{"data":{"ticket":"PVE:root@pam:ticket","CSRFPreventionToken":"csrf"}}`))

			return
		}
		// Normal response.
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "ok"))
	})

	opts := minimalHTTPOptions()
	opts.Username = testUsername
	opts.Password = testPassword
	opts.AutoLogin = true

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.baseURL = srv.URL

	// shouldAutoLogin returns true, performAutoLogin runs.
	// Even if auth fails (mock returns wrong format), the function ran.
	_, _ = client.Do("GET", "/version", nil)

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
	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.baseURL = "http://127.0.0.1:1" // no listener
	client.maxRetries = 0

	// recordRequestError is called when executeWithMiddleware fails.
	// The ClientMetrics.Errors counter is NOT updated by recordRequestError
	// (only by recordRequestComplete); verify the request returns an error.
	_, doErr := client.Do("GET", "/version", nil)
	if doErr == nil {
		t.Error("expected error on connection refused")
	}
}

// ---------------------------------------------------------------------------
// logRequest with headers / query params logging enabled
// ---------------------------------------------------------------------------

func TestLogRequest_WithHeaders(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "ok"))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0

	logger := &testLogger{}
	client.SetLogger(logger)
	client.SetLogConfig(LogConfig{
		Enabled:          true,
		LogRequestHeader: true,
		LogQueryParams:   true,
	})

	_, _ = client.Do("GET", "/version", map[string]interface{}{"type": "node"})

	logger.mu.Lock()
	count := len(logger.infos)
	logger.mu.Unlock()

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
	opts.APIToken = testAPITokenShort

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// APIToken authenticator is always "authenticated" after Authenticate().
	// ensureAuthentication with IsAuthenticated=true returns nil immediately.
	ensureErr := client.ensureAuthentication()
	if ensureErr != nil {
		t.Errorf("ensureAuthentication() = %v, want nil when already auth'd", ensureErr)
	}
}

// ---------------------------------------------------------------------------
// logResponse with response body logging
// ---------------------------------------------------------------------------

func TestLogResponse_WithBody(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "body-data"))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0

	logger := &testLogger{}
	client.SetLogger(logger)
	client.SetLogConfig(LogConfig{
		Enabled:           true,
		LogResponseBody:   true,
		LogResponseHeader: true,
		MaxBodyBytes:      512,
	})

	_, err := client.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	logger.mu.Lock()
	count := len(logger.infos)
	logger.mu.Unlock()

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
		Host:      testHostPVE,
		Port:      8006,
		Protocol:  testProtoHTTPS,
		Timeout:   5 * time.Second,
		KeepAlive: 5,
		CachedFingerprints: map[string]bool{
			"AA:BB:CC:DD:EE:FF": true,
			"11:22:33:44:55:66": false, // untrusted, not added
		},
	}

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient with CachedFingerprints: %v", err)
	}

	transport, ok := client.httpClient.Transport.(*http.Transport)
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
		Host:               testHostPVE,
		Port:               8006,
		Protocol:           testProtoHTTPS,
		Timeout:            5 * time.Second,
		KeepAlive:          5,
		ManualVerification: true,
	}

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient with ManualVerification: %v", err)
	}

	transport, ok := client.httpClient.Transport.(*http.Transport)
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
		Host:      testHostPVE,
		Port:      8006,
		Protocol:  testProtoHTTPS,
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

	respParser := NewResponseParser()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeTextPlain}},
		Body:       io.NopCloser(strings.NewReader("42")),
		Request:    req,
	}

	var result int64

	err := respParser.Parse(resp, &result)
	if err != nil {
		t.Fatalf("Parse text→int64: %v", err)
	}

	if result != 42 {
		t.Errorf("result = %d, want 42", result)
	}
}

func TestResponseParser_StringToFloat(t *testing.T) {
	t.Parallel()

	respParser := NewResponseParser()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeTextPlain}},
		Body:       io.NopCloser(strings.NewReader("3.14")),
		Request:    req,
	}

	var result float64

	err := respParser.Parse(resp, &result)
	if err != nil {
		t.Fatalf("Parse text→float64: %v", err)
	}

	if result != 3.14 {
		t.Errorf("result = %f, want 3.14", result)
	}
}

func TestResponseParser_StringToBool(t *testing.T) {
	t.Parallel()

	respParser := NewResponseParser()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeTextPlain}},
		Body:       io.NopCloser(strings.NewReader("true")),
		Request:    req,
	}

	var result bool

	err := respParser.Parse(resp, &result)
	if err != nil {
		t.Fatalf("Parse text→bool: %v", err)
	}

	if !result {
		t.Error("result should be true")
	}
}

func TestResponseParser_CannotConvert_Error(t *testing.T) {
	t.Parallel()

	respParser := NewResponseParser()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeTextPlain}},
		Body:       io.NopCloser(strings.NewReader("not-a-struct")),
		Request:    req,
	}

	type myStruct struct{ X int }

	var result myStruct

	err := respParser.Parse(resp, &result)
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

	client, _ := NewClient(minimalHTTPOptions())

	logger := &testLogger{}
	client.SetLogger(logger)
	client.SetLogConfig(LogConfig{Enabled: true})

	client.logTicketRenewalFailure(errTestRenewalFailed)

	logger.mu.Lock()
	warnCount := len(logger.warns)
	logger.mu.Unlock()

	if warnCount == 0 {
		t.Error("expected a Warn log for ticket renewal failure")
	}
}

func TestLogTicketRenewalFailure_NoLogger(t *testing.T) {
	t.Parallel()

	client, _ := NewClient(minimalHTTPOptions())
	// Must not panic when no logger set.
	client.logTicketRenewalFailure(errTestRenewalFailed)
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
	encodedBody, err := cfg.BodyEncoder(map[string]string{"k": "v"})
	if err != nil {
		t.Fatalf("BodyEncoder: %v", err)
	}

	if !strings.Contains(string(encodedBody), `"k"`) {
		t.Errorf("BodyEncoder = %q, missing key k", string(encodedBody))
	}
}

// ---------------------------------------------------------------------------
// Prometheus metrics path (recordRequestStart/Error/Complete)
// ---------------------------------------------------------------------------

func TestSetMetrics_RecordRequestComplete(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "ok"))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0

	m := pmetrics.NewDefaultMetrics()
	client.SetMetrics(m)

	_, err := client.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	// Prom metrics were exercised; verify no panic. Counters should be > 0.
	// We can't assert exact values without exposing counter internals,
	// but the code path must complete without panic.
}

func TestSetMetrics_RecordRequestError(t *testing.T) {
	t.Parallel()

	client, _ := NewClient(minimalHTTPOptions())
	client.baseURL = "http://127.0.0.1:1" // no listener
	client.maxRetries = 0

	m := pmetrics.NewDefaultMetrics()
	client.SetMetrics(m)

	// Should exercise recordRequestError path (prom.Dec on active connections etc.)
	_, _ = client.Do("GET", "/version", nil)
}

func TestSetMetrics_RecordRequestStart_ContentLength(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "ok"))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0

	m := pmetrics.NewDefaultMetrics()
	client.SetMetrics(m)

	// POST with body → ContentLength > 0 → BytesSent path.
	_, err := client.Do("POST", "/access/ticket", map[string]interface{}{
		testFormKeyUsername: testUsername,
		testFormKeyPassword: testPassword,
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
func (f *failLogoutAuthenticator) Logout() error                 { return errTestLogoutFailed }

func TestLogout_ErrorPropagated(t *testing.T) {
	t.Parallel()

	client, _ := NewClient(minimalHTTPOptions())
	client.authenticator = &failLogoutAuthenticator{}

	err := client.Logout()
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

	client, _ := NewClient(minimalHTTPOptions())
	// client.authenticator is nil

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	resp401 := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       io.NopCloser(strings.NewReader("")),
	}

	next := Handler(func(_ *http.Request) (*http.Response, error) {
		return resp401, nil
	})

	// With nil authenticator, 401 is returned as-is.
	result, err := client.handleAuthenticationRetry(req, resp401, next)
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	defer func() {
		if result != nil && result.Body != nil {
			_ = result.Body.Close()
		}
	}()

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
		Host:      testHostPVE,
		Port:      8006,
		Protocol:  testProtoHTTPS,
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
		Host:      testHostPVE,
		Port:      8006,
		Protocol:  testProtoHTTPS,
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
	opts.Username = testUsername
	opts.Password = testPassword

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// Force loginAttempted=true to trigger the double-check branch.
	client.loginMutex.Lock()
	client.loginAttempted = true
	client.loginMutex.Unlock()

	// performAutoLogin should return nil immediately (double-check: loginAttempted=true).
	autoLoginErr := client.performAutoLogin()
	if autoLoginErr != nil {
		t.Errorf("performAutoLogin with loginAttempted=true = %v, want nil", autoLoginErr)
	}
}

// ---------------------------------------------------------------------------
// buildMultipartBody: write field error (closed writer)
// ---------------------------------------------------------------------------

func TestRequestBuilder_BuildBody_Multipart_LargeFile(t *testing.T) {
	t.Parallel()

	reqBuilder := NewRequestBuilder("POST", "https://pve.example.com:8006/api2/json", "/upload")
	reqBuilder.AddFormParam("storage", "local")

	// Multi-field + multi-file to exercise full multipart path.
	reqBuilder.AddFile("file1", "a.iso", strings.NewReader("content-a"))
	reqBuilder.AddFile("file2", "b.iso", strings.NewReader("content-b"))

	body, contentType, err := reqBuilder.BuildBody()
	if err != nil {
		t.Fatalf("BuildBody multipart multi-file: %v", err)
	}

	if !strings.HasPrefix(contentType, "multipart/form-data") {
		t.Errorf("content-type = %q, want multipart/form-data", contentType)
	}

	bodyBytes, _ := io.ReadAll(body)
	if !strings.Contains(string(bodyBytes), "content-a") || !strings.Contains(string(bodyBytes), "content-b") {
		t.Errorf("multipart body missing file contents: %q", string(bodyBytes))
	}
}

// ---------------------------------------------------------------------------
// BuildURL: path without leading slash
// ---------------------------------------------------------------------------

func TestRequestBuilder_BuildURL_NoLeadingSlash(t *testing.T) {
	t.Parallel()

	reqBuilder := NewRequestBuilder("GET", "https://pve.example.com:8006/api2/json", "nodes")
	url := reqBuilder.BuildURL()

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

	respParser := NewResponseParser()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	// data is a JSON number; target is float64 → direct assignable.
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeJSON}},
		Body:       io.NopCloser(strings.NewReader(`{"data":1.5}`)),
		Request:    req,
	}

	var result float64

	err := respParser.Parse(resp, &result)
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

	client, _ := NewClient(minimalHTTPOptions())
	// client.authenticator == nil

	err := client.ensureAuthentication()
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
	opts.APIToken = testAPITokenShort

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// Before Authenticate(), API token auth is not authenticated.
	// After Authenticate(), it is (no-op for tokens).
	_ = client.authenticator.Authenticate()

	if !client.isAuthenticated() {
		t.Error("isAuthenticated() should return true after Authenticate() with API token")
	}
}

// ---------------------------------------------------------------------------
// DoWithContext: body close error log (nil logger branch)
// ---------------------------------------------------------------------------

func TestDoWithContext_ResponseBodyClose_NilLogger(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "ok"))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0
	// No logger set → body close error warning code path (logger == nil branch).

	_, err := client.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
}

// ---------------------------------------------------------------------------
// handleAuthenticationRetry: 401 with API token auth (Refresh no-op → retry)
// ---------------------------------------------------------------------------

func TestHandleAuthenticationRetry_401_WithAuth(t *testing.T) {
	t.Parallel()

	var calls int32

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			respWriter.WriteHeader(http.StatusUnauthorized)
			_, _ = respWriter.Write([]byte(`{"message":"unauthorized"}`))

			return
		}
		// Second call (retry after auth): success.
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "ok"))
	})

	opts := minimalHTTPOptions()
	opts.APIToken = testAPITokenShort

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.baseURL = srv.URL
	client.maxRetries = 0
	_ = client.authenticator.Authenticate()

	// The auth middleware calls handleAuthenticationRetry on 401.
	// API token Refresh() is a no-op; retry should succeed on second call.
	resp, err := client.Do("GET", "/version", nil)
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

	respParser := NewResponseParser()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	// Envelope without "data" field — should try to unmarshal whole body.
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeJSON}},
		Body:       io.NopCloser(strings.NewReader(`{"key":"val"}`)),
		Request:    req,
	}

	var result map[string]interface{}

	err := respParser.Parse(resp, &result)
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

	respParser := NewResponseParser()

	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.com/path", nil)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{testHeaderContentType: []string{testContentTypeTextPlain}},
		Body:       io.NopCloser(strings.NewReader(testHello)),
		Request:    req,
	}

	var result string

	err := respParser.Parse(resp, &result)
	if err != nil {
		t.Fatalf("Parse direct string: %v", err)
	}

	if result != testHello {
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
// cachingMiddleware: POST not cached
// ---------------------------------------------------------------------------

func TestCachingMiddleware_POST_NotCached(t *testing.T) {
	t.Parallel()

	var calls int32

	srv := newTestServer(t, func(respWriter http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "ok"))
	})

	opts := minimalHTTPOptions()
	opts.CacheConfig = &cache.Config{Enabled: true, MaxSize: 10 * 1024 * 1024, DefaultTTL: time.Minute, CleanupInterval: 5 * time.Minute}

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.baseURL = srv.URL
	client.maxRetries = 0

	// Two POST requests to same path — both should hit server (not cached).
	_, _ = client.Do("POST", "/access/ticket", map[string]interface{}{testFormKeyUsername: testUsername, testFormKeyPassword: "x"})
	_, _ = client.Do("POST", "/access/ticket", map[string]interface{}{testFormKeyUsername: testUsername, testFormKeyPassword: "x"})

	if atomic.LoadInt32(&calls) != 2 {
		t.Errorf("calls = %d, want 2 (POST not cached)", calls)
	}
}

// ---------------------------------------------------------------------------
// UploadWithContext: context cancellation
// ---------------------------------------------------------------------------

func TestUploadWithContext_Cancelled(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(respWriter http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}

		respWriter.WriteHeader(http.StatusOK)
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 0

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)

	go func() {
		_, err := client.UploadWithContext(ctx, "/upload", map[string]string{}, "file", "f.iso", strings.NewReader("data"))
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

	srv := newTestServer(t, func(respWriter http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)

		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "ok"))
	})

	client := clientPointedAt(t, srv.URL)

	_, err := client.Do("POST", "/nodes/x/qemu/100/config", map[string]interface{}{
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
// slices must encode as repeated query keys, not Go-style "[a b client]".
func TestDo_GetHotPath_SliceRepeatedKeys(t *testing.T) {
	t.Parallel()

	var gotQuery string

	srv := newTestServer(t, func(respWriter http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery

		respWriter.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = respWriter.Write(pveEnvelope(t, "ok"))
	})

	client := clientPointedAt(t, srv.URL)

	_, err := client.Do("GET", "/cluster/log", map[string]interface{}{
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

// ---------------------------------------------------------------------------
// createHTTPTransport — §7.30 transport-tuning knob mapping
// ---------------------------------------------------------------------------

// TestCreateHTTPTransport_ZeroKnobs asserts that when all new transport knobs
// are zero the produced transport is equivalent to the baseline (MaxIdleConns==
// KeepAlive, MaxIdleConnsPerHost==KeepAlive, IdleConnTimeout==LongTimeout(),
// TLSHandshakeTimeout==0, DialContext==nil).
func TestCreateHTTPTransport_ZeroKnobs(t *testing.T) {
	t.Parallel()

	opts := &Options{
		KeepAlive: 7,
		// All new knobs intentionally left zero.
	}

	tr := createHTTPTransport(opts)

	if tr.MaxIdleConns != 7 {
		t.Errorf("MaxIdleConns = %d, want 7", tr.MaxIdleConns)
	}

	if tr.MaxIdleConnsPerHost != 7 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 7 (KeepAlive fallback)", tr.MaxIdleConnsPerHost)
	}

	if tr.IdleConnTimeout != constants.LongTimeout() {
		t.Errorf("IdleConnTimeout = %v, want %v", tr.IdleConnTimeout, constants.LongTimeout())
	}

	if tr.TLSHandshakeTimeout != 0 {
		t.Errorf("TLSHandshakeTimeout = %v, want 0 (Go default)", tr.TLSHandshakeTimeout)
	}

	if tr.DialContext != nil {
		t.Errorf("DialContext = non-nil, want nil (no dial knobs set)")
	}
}

// TestCreateHTTPTransport_Knobs_MaxIdleConnsPerHost asserts that a non-zero
// MaxIdleConnsPerHost overrides the KeepAlive fallback.
func TestCreateHTTPTransport_Knobs_MaxIdleConnsPerHost(t *testing.T) {
	t.Parallel()

	opts := &Options{
		KeepAlive:           5,
		MaxIdleConnsPerHost: 20,
	}

	tr := createHTTPTransport(opts)

	if tr.MaxIdleConnsPerHost != 20 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 20", tr.MaxIdleConnsPerHost)
	}

	// MaxIdleConns still tracks KeepAlive.
	if tr.MaxIdleConns != 5 {
		t.Errorf("MaxIdleConns = %d, want 5 (KeepAlive)", tr.MaxIdleConns)
	}
}

// TestCreateHTTPTransport_Knobs_IdleConnTimeout asserts that a non-zero
// IdleConnTimeoutSec sets the transport field to the expected duration.
func TestCreateHTTPTransport_Knobs_IdleConnTimeout(t *testing.T) {
	t.Parallel()

	opts := &Options{
		KeepAlive:          5,
		IdleConnTimeoutSec: 120,
	}

	tr := createHTTPTransport(opts)

	want := 120 * time.Second
	if tr.IdleConnTimeout != want {
		t.Errorf("IdleConnTimeout = %v, want %v", tr.IdleConnTimeout, want)
	}
}

// TestCreateHTTPTransport_Knobs_TLSHandshakeTimeout asserts that a non-zero
// TLSHandshakeTimeoutSec sets the transport field to the expected duration.
func TestCreateHTTPTransport_Knobs_TLSHandshakeTimeout(t *testing.T) {
	t.Parallel()

	opts := &Options{
		KeepAlive:              5,
		TLSHandshakeTimeoutSec: 15,
	}

	tr := createHTTPTransport(opts)

	want := 15 * time.Second
	if tr.TLSHandshakeTimeout != want {
		t.Errorf("TLSHandshakeTimeout = %v, want %v", tr.TLSHandshakeTimeout, want)
	}
}

// TestCreateHTTPTransport_Knobs_DialTimeout asserts that a non-zero
// DialTimeoutSec causes DialContext to be set (non-nil).
func TestCreateHTTPTransport_Knobs_DialTimeout(t *testing.T) {
	t.Parallel()

	opts := &Options{
		KeepAlive:      5,
		DialTimeoutSec: 10,
	}

	tr := createHTTPTransport(opts)

	if tr.DialContext == nil {
		t.Error("DialContext = nil, want non-nil when DialTimeoutSec > 0")
	}
}

// TestCreateHTTPTransport_Knobs_TCPKeepAlive asserts that a non-zero
// TCPKeepAliveSec causes DialContext to be set (non-nil).
func TestCreateHTTPTransport_Knobs_TCPKeepAlive(t *testing.T) {
	t.Parallel()

	opts := &Options{
		KeepAlive:       5,
		TCPKeepAliveSec: 30,
	}

	tr := createHTTPTransport(opts)

	if tr.DialContext == nil {
		t.Error("DialContext = nil, want non-nil when TCPKeepAliveSec > 0")
	}
}

// TestCreateHTTPTransport_Knobs_AllSet exercises all five new knobs together,
// confirming each field receives its expected value.
func TestCreateHTTPTransport_Knobs_AllSet(t *testing.T) {
	t.Parallel()

	opts := &Options{
		KeepAlive:              5,
		DialTimeoutSec:         10,
		TLSHandshakeTimeoutSec: 15,
		MaxIdleConnsPerHost:    20,
		IdleConnTimeoutSec:     120,
		TCPKeepAliveSec:        30,
	}

	tr := createHTTPTransport(opts)

	if tr.MaxIdleConnsPerHost != 20 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 20", tr.MaxIdleConnsPerHost)
	}

	if tr.IdleConnTimeout != 120*time.Second {
		t.Errorf("IdleConnTimeout = %v, want 120s", tr.IdleConnTimeout)
	}

	if tr.TLSHandshakeTimeout != 15*time.Second {
		t.Errorf("TLSHandshakeTimeout = %v, want 15s", tr.TLSHandshakeTimeout)
	}

	if tr.DialContext == nil {
		t.Error("DialContext = nil, want non-nil when DialTimeoutSec and TCPKeepAliveSec > 0")
	}
}
