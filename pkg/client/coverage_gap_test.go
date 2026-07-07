package client_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
	pmetrics "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/metrics"
)

// ---------------------------------------------------------------------------
// Login
// ---------------------------------------------------------------------------

func TestLogin_Success(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != testAccessTicketEndpoint {
			http.NotFound(w, r)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"ticket":"TICKET","CSRFPreventionToken":"CSRF"},"success":1}`))
	}))
	defer srv.Close()

	host, port := parseServerURL(srv.URL)

	cli, err := client.NewClient(client.Options{
		Host:     host,
		Port:     port,
		Protocol: testProtoHTTP,
		Username: testUsername,
		Password: testPassword,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	err = cli.Login()
	if err != nil {
		t.Fatalf("Login() unexpected error: %v", err)
	}
}

func TestLogin_Failure(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"message":"login broken"}`))
	}))
	defer srv.Close()

	host, port := parseServerURL(srv.URL)

	cli, err := client.NewClient(client.Options{
		Host:     host,
		Port:     port,
		Protocol: testProtoHTTP,
		Username: testUsername,
		Password: testPassword,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	err = cli.Login()
	if err == nil {
		t.Fatal("Login() = nil error, want an error when the login endpoint fails")
	}
}

// ---------------------------------------------------------------------------
// UploadCtx
// ---------------------------------------------------------------------------

// uploadCapture records what an upload test server observed for a single
// multipart upload request, for assertion by the calling test.
type uploadCapture struct {
	fileField string
	filename  string
	body      string
	formValue string
}

// uploadTestHandler returns an http.HandlerFunc that parses a multipart
// upload against the fixed path used by the UploadCtx tests, recording the
// received field/file/body into capture and replying with a PVE-shaped
// success envelope.
func uploadTestHandler(capture *uploadCapture) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api2/json/nodes/n1/storage/local/upload" {
			http.NotFound(w, r)

			return
		}

		parseErr := r.ParseMultipartForm(1 << 20)
		if parseErr != nil {
			http.Error(w, parseErr.Error(), http.StatusBadRequest)

			return
		}

		capture.formValue = r.FormValue("content")

		file, header, fileErr := r.FormFile("filename")
		if fileErr != nil {
			http.Error(w, fileErr.Error(), http.StatusBadRequest)

			return
		}
		defer func() { _ = file.Close() }()

		capture.fileField = "filename"
		capture.filename = header.Filename

		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(file)
		capture.body = buf.String()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"upid:...","success":1}`))
	}
}

func TestUploadCtx_Success(t *testing.T) {
	t.Parallel()

	capture := &uploadCapture{}

	srv := httptest.NewServer(uploadTestHandler(capture))
	defer srv.Close()

	cli, err := client.NewClient(optsFromServer(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	resp, err := cli.UploadCtx(
		context.Background(),
		"/nodes/n1/storage/local/upload",
		map[string]string{"content": "iso"},
		"filename",
		"test.iso",
		strings.NewReader("iso-bytes"),
	)
	if err != nil {
		t.Fatalf("UploadCtx() unexpected error: %v", err)
	}

	if resp == nil || resp.Data == nil {
		t.Fatal("UploadCtx() returned nil/empty response")
	}

	if capture.formValue != "iso" {
		t.Errorf("form field 'content' = %q, want %q", capture.formValue, "iso")
	}

	if capture.fileField != "filename" || capture.filename != "test.iso" || capture.body != "iso-bytes" {
		t.Errorf("upload not received intact: field=%q filename=%q body=%q",
			capture.fileField, capture.filename, capture.body)
	}
}

func TestUploadCtx_ServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cli, err := client.NewClient(optsFromServer(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = cli.UploadCtx(
		context.Background(),
		"/nodes/n1/storage/local/upload",
		nil,
		"filename",
		"test.iso",
		strings.NewReader("data"),
	)
	if err == nil {
		t.Fatal("UploadCtx() = nil error, want error on 500 response")
	}
}

// ---------------------------------------------------------------------------
// Request options: WithRetries / WithRetryDelay / WithLogging / WithLogFields
// / WithTimeout — assert observable effect on request behavior.
// ---------------------------------------------------------------------------

// TestWithRetries_ZeroDisablesRetry verifies that overriding retries to 0 via
// WithRetries causes a GET against an always-failing endpoint to give up
// after exactly one attempt, instead of the client's default retry count.
func TestWithRetries_ZeroDisablesRetry(t *testing.T) {
	t.Parallel()

	var calls int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	cli, err := client.NewClient(optsFromServer(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx := client.WithRetries(context.Background(), 0)

	_, err = cli.GetCtx(ctx, "/test", nil)
	if err == nil {
		t.Fatal("GetCtx() = nil error, want error from the always-503 endpoint")
	}

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 with WithRetries(ctx, 0)", got)
	}
}

// TestWithRetries_PositiveEnablesRetry verifies that a positive override lets
// a GET succeed after transient 503s that exceed the client's zero-retry
// baseline.
func TestWithRetries_PositiveEnablesRetry(t *testing.T) {
	t.Parallel()

	var calls int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"ok","success":1}`))
	}))
	defer srv.Close()

	cli, err := client.NewClient(optsFromServer(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx := client.WithRetries(context.Background(), 3)
	ctx = client.WithRetryDelay(ctx, time.Millisecond)

	_, err = cli.GetCtx(ctx, "/test", nil)
	if err != nil {
		t.Fatalf("GetCtx() unexpected error with retries enabled: %v", err)
	}

	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("calls = %d, want 3 (2 failures + 1 success)", got)
	}
}

// TestWithRetryDelay_ShortensBackoff verifies WithRetryDelay actually changes
// the sleep between attempts by comparing elapsed time for a small vs. a
// larger configured delay across the same number of forced retries.
func TestWithRetryDelay_ShortensBackoff(t *testing.T) {
	t.Parallel()

	const attempts = 3

	run := func(delay time.Duration) time.Duration {
		var calls int32

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n := atomic.AddInt32(&calls, 1)
			if int(n) < attempts {
				w.WriteHeader(http.StatusServiceUnavailable)

				return
			}

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":"ok","success":1}`))
		}))
		defer srv.Close()

		cli, err := client.NewClient(optsFromServer(srv.URL))
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}

		ctx := client.WithRetries(context.Background(), attempts)
		ctx = client.WithRetryDelay(ctx, delay)

		start := time.Now()

		_, getErr := cli.GetCtx(ctx, "/test", nil)
		if getErr != nil {
			t.Fatalf("GetCtx() unexpected error: %v", getErr)
		}

		return time.Since(start)
	}

	fast := run(time.Millisecond)
	slow := run(80 * time.Millisecond)

	if slow <= fast {
		t.Errorf("elapsed with large RetryDelay (%v) not greater than with small RetryDelay (%v)", slow, fast)
	}
}

// ---------------------------------------------------------------------------
// WithLogging / WithLogFields
// ---------------------------------------------------------------------------

// capturingLogger records every log call for assertions.
type capturingLogger struct {
	mu    sync.Mutex
	infos []map[string]interface{}
}

func (l *capturingLogger) Debug(string, map[string]interface{}) {}
func (l *capturingLogger) Warn(string, map[string]interface{})  {}
func (l *capturingLogger) Error(string, map[string]interface{}) {}

func (l *capturingLogger) Info(_ string, fields map[string]interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.infos = append(l.infos, fields)
}

func (l *capturingLogger) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()

	return len(l.infos)
}

// anyFieldsWithValue reports whether any captured log entry has fields[key]
// == want, returning that entry's fields.
func (l *capturingLogger) anyFieldsWithValue(key string, want interface{}) (map[string]interface{}, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for _, fields := range l.infos {
		if fields[key] == want {
			return fields, true
		}
	}

	return nil, false
}

// TestWithLogging_FalseSuppressesRequestLogging verifies that WithLogging(ctx,
// false) measurably reduces the number of log entries emitted for a request
// compared to an identical request without the override. The client also
// records one response-metrics log line outside the per-request-overridable
// logging middleware (see TestLoggingMiddleware_SuppressedByContext in
// internal/http), so the assertion is a strict decrease rather than zero.
func TestWithLogging_FalseSuppressesRequestLogging(t *testing.T) {
	t.Parallel()

	srv := createTestServer()
	defer srv.Close()

	cli, err := client.NewClient(optsFromServer(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	logger := &capturingLogger{}
	cli.SetLogger(logger)
	cli.SetLogConfig(client.LogConfig{Enabled: true})

	_, err = cli.GetCtx(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("GetCtx() normal-logging call: %v", err)
	}

	normalDelta := logger.callCount()
	if normalDelta == 0 {
		t.Fatal("expected at least one logged entry with logging enabled")
	}

	// WithLogging(false) must suppress the per-request logging middleware,
	// so this request must add strictly fewer log entries than the one above.
	ctx := client.WithLogging(context.Background(), false)

	_, err = cli.GetCtx(ctx, "/test", nil)
	if err != nil {
		t.Fatalf("GetCtx() with WithLogging(false): %v", err)
	}

	suppressedDelta := logger.callCount() - normalDelta

	if suppressedDelta >= normalDelta {
		t.Errorf("log entries added by suppressed request = %d, want fewer than normal request's %d",
			suppressedDelta, normalDelta)
	}
}

func TestWithLogFields_AttachesFieldsToLogEntry(t *testing.T) {
	t.Parallel()

	srv := createTestServer()
	defer srv.Close()

	cli, err := client.NewClient(optsFromServer(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	logger := &capturingLogger{}
	cli.SetLogger(logger)
	cli.SetLogConfig(client.LogConfig{Enabled: true})

	ctx := client.WithLogFields(context.Background(), map[string]interface{}{"request_id": "abc-123"})

	_, err = cli.GetCtx(ctx, "/test", nil)
	if err != nil {
		t.Fatalf("GetCtx() with WithLogFields: %v", err)
	}

	if _, ok := logger.anyFieldsWithValue("request_id", "abc-123"); !ok {
		t.Error("expected a logged entry carrying fields[request_id] = \"abc-123\"")
	}
}

// ---------------------------------------------------------------------------
// WithTimeout
// ---------------------------------------------------------------------------

func TestWithTimeout_CancelsSlowRequest(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"ok","success":1}`))
	}))

	defer func() {
		close(release)
		srv.Close()
	}()

	cli, err := client.NewClient(optsFromServer(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx, cancel := client.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	ctx = client.WithRetries(ctx, 0)

	start := time.Now()

	_, err = cli.GetCtx(ctx, "/test", nil)
	if err == nil {
		t.Fatal("GetCtx() = nil error, want a deadline-exceeded error")
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("GetCtx() took %v, want it to fail quickly per WithTimeout", elapsed)
	}
}

// ---------------------------------------------------------------------------
// ManualVerifyCallback / FingerprintCachePath (public Options wiring)
// ---------------------------------------------------------------------------

func TestManualVerifyCallback_ReceivesFingerprintVerificationRequest(t *testing.T) {
	t.Parallel()

	srv := newTLSServer()
	defer srv.Close()

	var gotHost string

	var gotFingerprint string

	host, port := parseHostPort(srv.URL)

	opts := client.Options{
		Host:     host,
		Port:     port,
		Protocol: testProtoHTTPS,
		APIToken: testAPIToken,
		ManualVerifyCallback: func(req client.FingerprintVerificationRequest) bool {
			gotHost = req.Host
			gotFingerprint = req.Fingerprint

			return true
		},
	}

	cli, err := client.NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = cli.Get("/version", nil)
	if err != nil {
		t.Fatalf("Get(/version) with ManualVerifyCallback failed: %v", err)
	}

	if gotHost != host {
		t.Errorf("ManualVerifyCallback host = %q, want %q", gotHost, host)
	}

	if gotFingerprint == "" {
		t.Error("ManualVerifyCallback fingerprint was empty")
	}
}

func TestFingerprintCachePath_PersistsTrustAcrossClients(t *testing.T) {
	t.Parallel()

	srv := newTLSServer()
	defer srv.Close()

	host, port := parseHostPort(srv.URL)
	cacheFile := t.TempDir() + "/fingerprints.json"

	var callbackCalls int32

	opts := client.Options{
		Host:                 host,
		Port:                 port,
		Protocol:             testProtoHTTPS,
		APIToken:             testAPIToken,
		FingerprintCachePath: cacheFile,
		ManualVerifyCallback: func(_ client.FingerprintVerificationRequest) bool {
			atomic.AddInt32(&callbackCalls, 1)

			return true
		},
	}

	cli, err := client.NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = cli.Get("/version", nil)
	if err != nil {
		t.Fatalf("Get(/version) first client failed: %v", err)
	}

	if got := atomic.LoadInt32(&callbackCalls); got != 1 {
		t.Fatalf("callback calls = %d, want 1 on first (unknown fingerprint) connection", got)
	}

	// A second, independent client pointed at the same cache file must trust
	// the fingerprint without consulting the callback again.
	opts2 := opts
	opts2.ManualVerifyCallback = func(_ client.FingerprintVerificationRequest) bool {
		atomic.AddInt32(&callbackCalls, 1)

		return true
	}

	cli2, err := client.NewClient(opts2)
	if err != nil {
		t.Fatalf("NewClient() second client error = %v", err)
	}

	_, err = cli2.Get("/version", nil)
	if err != nil {
		t.Fatalf("Get(/version) second client failed: %v", err)
	}

	if got := atomic.LoadInt32(&callbackCalls); got != 1 {
		t.Errorf("callback calls = %d after second client, want still 1 (fingerprint cached to disk)", got)
	}
}

// ---------------------------------------------------------------------------
// SetMetrics / MetricsOf
// ---------------------------------------------------------------------------

// TestSetMetrics_RecordsRequestActivity verifies SetMetrics actually wires a
// live collector into the request path: after a successful GET, the
// collector must observe the request, its duration, and received bytes, and
// ActiveConnections must return to 0 once the request completes.
func TestSetMetrics_RecordsRequestActivity(t *testing.T) {
	t.Parallel()

	srv := createTestServer()
	defer srv.Close()

	cli, err := client.NewClient(optsFromServer(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	collector := pmetrics.NewDefaultMetrics()
	cli.SetMetrics(collector)

	_, err = cli.Get("/test", nil)
	if err != nil {
		t.Fatalf("Get() unexpected error: %v", err)
	}

	if got := collector.RequestsTotal.Get(); got != 1 {
		t.Errorf("RequestsTotal = %d, want 1", got)
	}

	if got := collector.RequestsFailedTotal.Get(); got != 0 {
		t.Errorf("RequestsFailedTotal = %d, want 0 for a successful request", got)
	}

	count, _, _ := collector.RequestDuration.GetStats()
	if count != 1 {
		t.Errorf("RequestDuration observation count = %d, want 1", count)
	}

	if got := collector.BytesReceived.Get(); got <= 0 {
		t.Errorf("BytesReceived = %d, want > 0", got)
	}

	if got := collector.ActiveConnections.Get(); got != 0 {
		t.Errorf("ActiveConnections = %d, want 0 after request completion", got)
	}
}

// TestSetMetrics_FailedRequest_RecordsFailure verifies a non-2xx response is
// counted as a failed request by the attached collector.
func TestSetMetrics_FailedRequest_RecordsFailure(t *testing.T) {
	t.Parallel()

	srv := errorServer(t)
	defer srv.Close()

	cli, err := client.NewClient(optsFromServer(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	collector := pmetrics.NewDefaultMetrics()
	cli.SetMetrics(collector)

	_, err = cli.Get("/test", nil)
	if err == nil {
		t.Fatal("Get() = nil error, want error from the 500 endpoint")
	}

	if got := collector.RequestsTotal.Get(); got != 1 {
		t.Errorf("RequestsTotal = %d, want 1", got)
	}

	if got := collector.RequestsFailedTotal.Get(); got != 1 {
		t.Errorf("RequestsFailedTotal = %d, want 1 for a failed request", got)
	}
}

// TestSetMetrics_NilCollector_NoPanic verifies SetMetrics(nil) is accepted
// (e.g. to detach a previously attached collector) and requests continue to
// work normally.
func TestSetMetrics_NilCollector_NoPanic(t *testing.T) {
	t.Parallel()

	srv := createTestServer()
	defer srv.Close()

	cli, err := client.NewClient(optsFromServer(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	cli.SetMetrics(nil)

	_, err = cli.Get("/test", nil)
	if err != nil {
		t.Fatalf("Get() after SetMetrics(nil) unexpected error: %v", err)
	}
}

// TestMetricsOf_ReturnsSnapshotAfterRequests verifies MetricsOf reports a
// live, growing snapshot of request counts for a client obtained through
// NewClient (the internalHTTPAdapter-wrapped path).
func TestMetricsOf_ReturnsSnapshotAfterRequests(t *testing.T) {
	t.Parallel()

	srv := createTestServer()
	defer srv.Close()

	cli, err := client.NewClient(optsFromServer(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	before, ok := client.MetricsOf(cli)
	if !ok {
		t.Fatal("MetricsOf() ok = false, want true for a client built via NewClient")
	}

	if before.Requests != 0 {
		t.Fatalf("MetricsOf() before any request: Requests = %d, want 0", before.Requests)
	}

	const numRequests = 3

	for i := range numRequests {
		_, getErr := cli.Get("/test", nil)
		if getErr != nil {
			t.Fatalf("Get() call %d: %v", i, getErr)
		}
	}

	after, ok := client.MetricsOf(cli)
	if !ok {
		t.Fatal("MetricsOf() ok = false, want true after requests")
	}

	if after.Requests != numRequests {
		t.Errorf("MetricsOf() Requests = %d, want %d", after.Requests, numRequests)
	}

	if after.Errors != 0 {
		t.Errorf("MetricsOf() Errors = %d, want 0 for successful requests", after.Errors)
	}

	if after.TotalDuration <= 0 {
		t.Error("MetricsOf() TotalDuration = 0, want > 0 after real requests")
	}
}

// TestMetricsOf_FailedRequest_CountsError verifies MetricsOf reflects request
// failures (non-2xx responses) in its Errors count.
func TestMetricsOf_FailedRequest_CountsError(t *testing.T) {
	t.Parallel()

	srv := errorServer(t)
	defer srv.Close()

	cli, err := client.NewClient(optsFromServer(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	_, err = cli.Get("/test", nil)
	if err == nil {
		t.Fatal("Get() = nil error, want error from the 500 endpoint")
	}

	snap, ok := client.MetricsOf(cli)
	if !ok {
		t.Fatal("MetricsOf() ok = false, want true")
	}

	if snap.Errors != 1 {
		t.Errorf("MetricsOf() Errors = %d, want 1", snap.Errors)
	}
}

// fakeClientForMetrics embeds the public Client interface without providing
// any method bodies of its own, so it satisfies client.Client purely via
// promoted (nil) methods. It is never called — only used as a value whose
// dynamic type is not the package-private *client implementation, to
// exercise MetricsOf's type-assertion failure path.
type fakeClientForMetrics struct {
	client.Client
}

// TestMetricsOf_NonInternalClientType_ReturnsFalse verifies MetricsOf reports
// ok=false for a Client implementation other than the one NewClient
// constructs, rather than panicking or fabricating a snapshot.
func TestMetricsOf_NonInternalClientType_ReturnsFalse(t *testing.T) {
	t.Parallel()

	var fake client.Client = fakeClientForMetrics{}

	snap, ok := client.MetricsOf(fake)
	if ok {
		t.Errorf("MetricsOf() ok = true, want false for a non-internal Client implementation (snap=%+v)", snap)
	}
}
