package http //nolint:testpackage // white-box test: inspects the client's unexported transport fields

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/swaptransport"
)

const (
	wrapTestPathTicket  = "/api2/json/access/ticket"
	wrapTestPathVersion = "/api2/json/version"
	wrapTestLoopback    = "127.0.0.1"
	wrapTestPVEVersion  = "8.0"
	wrapTestNoWrapper   = "no wrapper"
)

var errWrapTestDial = errors.New("dial stub")

// recordedRequest is what a recordingWrapper saw of one request.
type recordedRequest struct {
	method        string
	path          string
	host          string
	contentLength int64
	hasGetBody    bool
}

// recordingWrapper is a RoundTripper that notes each request and then hands it
// to the base transport it wraps.
type recordingWrapper struct {
	base *http.Transport

	// onEnter, when set, runs as each request enters the wrapper, before the
	// request is recorded or handed on.
	onEnter func()

	mu       sync.Mutex
	requests []recordedRequest
}

func (w *recordingWrapper) RoundTrip(req *http.Request) (*http.Response, error) {
	if w.onEnter != nil {
		w.onEnter()
	}

	w.mu.Lock()
	w.requests = append(w.requests, recordedRequest{
		method:        req.Method,
		path:          req.URL.Path,
		host:          req.URL.Host,
		contentLength: req.ContentLength,
		hasGetBody:    req.GetBody != nil,
	})
	w.mu.Unlock()

	return w.base.RoundTrip(req) //nolint:wrapcheck // a transport must hand back the base's error untouched
}

func (w *recordingWrapper) seen() []recordedRequest {
	w.mu.Lock()
	defer w.mu.Unlock()

	return append([]recordedRequest(nil), w.requests...)
}

func (w *recordingWrapper) countPath(path string) int {
	count := 0

	for _, r := range w.seen() {
		if r.path == path {
			count++
		}
	}

	return count
}

// installRecorder sets opts.WrapTransport to a recordingWrapper and returns a
// pointer that is filled in when NewClient calls the option.
func installRecorder(opts *Options, calls *int32) **recordingWrapper {
	var wrapper *recordingWrapper

	opts.WrapTransport = func(base *http.Transport) http.RoundTripper {
		atomic.AddInt32(calls, 1)

		wrapper = &recordingWrapper{base: base}

		return wrapper
	}

	return &wrapper
}

// versionEnvelope is the PVE envelope a test server answers GET /version with.
func versionEnvelope(t *testing.T) []byte {
	t.Helper()

	return pveEnvelope(t, map[string]string{"version": wrapTestPVEVersion})
}

// liveTransport returns the *http.Transport that later requests use on a
// client built without WrapTransport.
func liveTransport(t *testing.T, client *Client) *http.Transport {
	t.Helper()

	swap, ok := redirect.Unwrap(client.httpClient.Load().Transport).(*swaptransport.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *swaptransport.Transport", redirect.Unwrap(client.httpClient.Load().Transport))
	}

	if swap != client.swap {
		t.Fatal("the http.Client's swaptransport.Transport is not the one the client retunes")
	}

	return swap.Load()
}

// TestNewClient_NilWrapTransportSendsOnBuiltTransport verifies the default:
// with no wrapper the http.Client sends on the *http.Transport NewClient
// built, through the swappable holder SetKeepAlive retunes.
func TestNewClient_NilWrapTransportSendsOnBuiltTransport(t *testing.T) {
	t.Parallel()

	client, err := NewClient(minimalHTTPOptions())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if liveTransport(t, client) != client.baseTransport {
		t.Error("baseTransport is not the transport the http.Client uses")
	}
}

// TestNewClient_WrapTransportInstalledOnceAndSharedWithAuthenticator verifies
// the option is called exactly once and its result is the http.Client's
// transport, which is the same http.Client the authenticator holds.
func TestNewClient_WrapTransportInstalledOnceAndSharedWithAuthenticator(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()

	var calls int32

	wrapper := installRecorder(opts, &calls)

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("WrapTransport calls = %d, want 1", got)
	}

	if redirect.Unwrap(client.httpClient.Load().Transport) != http.RoundTripper(*wrapper) {
		t.Errorf("Transport is %T, want the wrapper's result", redirect.Unwrap(client.httpClient.Load().Transport))
	}

	if client.swap != nil {
		t.Error("a wrapped client must not hold a swaptransport.Transport")
	}

	if (*wrapper).base != client.baseTransport {
		t.Error("the wrapper was not handed the transport NewClient built")
	}
}

// roundTripFunc adapts a function to http.RoundTripper, so a test can return a
// typed nil of a func kind.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// TestNewClient_WrapTransportReturningNilFails verifies a wrapper that returns
// nil, or a RoundTripper holding a nil value, is an error rather than a client
// that panics on its first request or in Close.
func TestNewClient_WrapTransportReturningNilFails(t *testing.T) {
	t.Parallel()

	cases := map[string]http.RoundTripper{
		"literal nil":           nil,
		"nil *http.Transport":   (*http.Transport)(nil),
		"nil *recordingWrapper": (*recordingWrapper)(nil),
		"nil RoundTripper func": roundTripFunc(nil),
		"nil swaptransport":     (*swaptransport.Transport)(nil),
	}

	for name, result := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts := minimalHTTPOptions()
			opts.WrapTransport = func(*http.Transport) http.RoundTripper { return result }

			client, err := NewClient(opts)
			if !errors.Is(err, ErrWrapTransportNil) {
				t.Fatalf("NewClient error = %v, want ErrWrapTransportNil", err)
			}

			if client != nil {
				t.Error("NewClient returned a client alongside the error")
			}
		})
	}
}

// TestNewClient_WrapTransportAcceptsNonNilValues verifies the nil check does
// not reject a usable RoundTripper, whether a pointer, a struct value, or a
// func.
func TestNewClient_WrapTransportAcceptsNonNilValues(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*http.Transport) http.RoundTripper{
		"base itself":  func(b *http.Transport) http.RoundTripper { return b },
		"struct value": func(b *http.Transport) http.RoundTripper { return roundTripOnly{inner: b} },
		"func":         func(b *http.Transport) http.RoundTripper { return roundTripFunc(b.RoundTrip) },
	}

	for name, wrap := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts := minimalHTTPOptions()
			opts.WrapTransport = wrap

			_, err := NewClient(opts)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
		})
	}
}

// captureBase builds a client with opts and returns the base transport its
// WrapTransport option was handed.
func captureBase(t *testing.T, opts *Options) *http.Transport {
	t.Helper()

	var sawBase *http.Transport

	opts.WrapTransport = func(base *http.Transport) http.RoundTripper {
		sawBase = base

		return base
	}

	_, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if sawBase == nil {
		t.Fatal("WrapTransport was not called")
	}

	return sawBase
}

// TestNewClient_WrapTransportBaseCarriesTLSAndTimeouts verifies the base
// transport handed to the wrapper already has the TLS config and timeouts the
// options asked for.
func TestNewClient_WrapTransportBaseCarriesTLSAndTimeouts(t *testing.T) {
	t.Parallel()

	base := captureBase(t, &Options{
		Host:                   wrapTestLoopback,
		Port:                   8006,
		Protocol:               protoHTTPS,
		Timeout:                5 * time.Second,
		KeepAlive:              7,
		TLSHandshakeTimeoutSec: 4,
		IdleConnTimeoutSec:     9,
		MaxIdleConnsPerHost:    3,
		SSLOptions:             &SSLOptions{VerifyMode: SSLVerifyNone},
	})

	if base.TLSClientConfig == nil || !base.TLSClientConfig.InsecureSkipVerify {
		t.Error("the base transport has no TLS config when the wrapper runs")
	}

	if base.TLSHandshakeTimeout != 4*time.Second {
		t.Errorf("TLSHandshakeTimeout = %v, want 4s", base.TLSHandshakeTimeout)
	}

	if base.IdleConnTimeout != 9*time.Second {
		t.Errorf("IdleConnTimeout = %v, want 9s", base.IdleConnTimeout)
	}

	if base.MaxIdleConns != 7 || base.MaxIdleConnsPerHost != 3 {
		t.Errorf("idle conns = %d/%d, want 7/3", base.MaxIdleConns, base.MaxIdleConnsPerHost)
	}
}

// TestNewClient_WrapTransportBaseCarriesDialAndProxy verifies the base
// transport handed to the wrapper already dials and proxies as the options
// asked.
func TestNewClient_WrapTransportBaseCarriesDialAndProxy(t *testing.T) {
	t.Parallel()

	var dialed, proxied int32

	proxyURL := &url.URL{Scheme: "http", Host: "proxy.invalid:3128"}

	opts := minimalHTTPOptions()
	opts.DialContext = func(context.Context, string, string) (net.Conn, error) {
		atomic.AddInt32(&dialed, 1)

		return nil, errWrapTestDial
	}
	opts.Proxy = func(*http.Request) (*url.URL, error) {
		atomic.AddInt32(&proxied, 1)

		return proxyURL, nil
	}

	base := captureBase(t, opts)

	if base.DialContext == nil {
		t.Fatal("the base transport has no dial function")
	}

	_, err := base.DialContext(context.Background(), "tcp", wrapTestLoopback+":8006")
	if !errors.Is(err, errWrapTestDial) || atomic.LoadInt32(&dialed) != 1 {
		t.Errorf("the base transport does not dial through Options.DialContext (err=%v)", err)
	}

	if base.Proxy == nil {
		t.Fatal("the base transport has no proxy function")
	}

	got, _ := base.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "pve.invalid"}})
	if got != proxyURL || atomic.LoadInt32(&proxied) != 1 {
		t.Error("the base transport does not use Options.Proxy")
	}
}

// TestNewClient_WrapTransportRunsForPlainHTTP verifies the wrapper is called
// for an http client too, where no TLS config is set.
func TestNewClient_WrapTransportRunsForPlainHTTP(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()

	var calls int32

	wrapper := installRecorder(opts, &calls)

	_, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("WrapTransport calls = %d, want 1", calls)
	}

	if (*wrapper).base.TLSClientConfig != nil {
		t.Error("an http client's base transport should carry no TLS config")
	}
}

// TestWrapTransport_SeesEveryRequestIncludingLogin verifies a ticket login and
// a forced re-login after a 401 both pass through the wrapper, alongside the
// API request itself.
func TestWrapTransport_SeesEveryRequestIncludingLogin(t *testing.T) {
	t.Parallel()

	var versionCalls int32

	srv := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case wrapTestPathTicket:
			writer.Header().Set(testHeaderContentType, testContentTypeJSON)
			_, _ = writer.Write([]byte(`{"data":{"ticket":"fresh","CSRFPreventionToken":"csrf"},"success":1}`))
		case wrapTestPathVersion:
			if atomic.AddInt32(&versionCalls, 1) == 1 {
				writer.WriteHeader(http.StatusUnauthorized)
				_, _ = writer.Write([]byte(`{"message":"ticket rejected"}`))

				return
			}

			writer.Header().Set(testHeaderContentType, testContentTypeJSON)
			_, _ = writer.Write(versionEnvelope(t))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	})

	var (
		calls   int32
		wrapper **recordingWrapper
	)

	client := newClientForServerURL(t, srv.URL, func(o *Options) {
		o.Username = testUsername
		o.Password = testPassword
		wrapper = installRecorder(o, &calls)
	})

	// Expire the cached ticket mid-flight: the first /version answers 401, so
	// the client must log in again before the retry.
	_, err := client.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	rec := *wrapper

	if got := rec.countPath(wrapTestPathVersion); got != 2 {
		t.Errorf("wrapper saw %d /version requests, want 2", got)
	}

	// One login for the first request and a forced re-login after the 401.
	if got := rec.countPath(wrapTestPathTicket); got != 2 {
		t.Errorf("wrapper saw %d POST /access/ticket requests, want 2 (login and forced re-login)", got)
	}

	for _, r := range rec.seen() {
		if r.path == wrapTestPathTicket && r.method != http.MethodPost {
			t.Errorf("ticket request method = %s, want POST", r.method)
		}
	}
}

// TestWrapTransport_WithHostRequestCarriesOverrideHost verifies a request
// routed with WithHost reaches the wrapper addressed to the override host.
func TestWrapTransport_WithHostRequestCarriesOverrideHost(t *testing.T) {
	t.Parallel()

	var baseCalls, nodeCalls int32

	baseSrv := newEnvelopeCountingServer(t, testDataBase, &baseCalls)
	nodeSrv := newEnvelopeCountingServer(t, testDataNode, &nodeCalls)

	var calls int32

	client := newClientForServerURL(t, baseSrv.URL, func(o *Options) {
		o.APIToken = testAPITokenShort
		installRecorder(o, &calls)
	})

	nodeURL, err := url.Parse(nodeSrv.URL)
	if err != nil {
		t.Fatalf("parse node URL: %v", err)
	}

	ctx := WithHost(context.Background(), nodeURL.Host)

	_, err = client.DoWithContext(ctx, "GET", "/version", nil)
	if err != nil {
		t.Fatalf("DoWithContext: %v", err)
	}

	rec, ok := redirect.Unwrap(client.httpClient.Load().Transport).(*recordingWrapper)
	if !ok {
		t.Fatalf("Transport is %T, want *recordingWrapper", redirect.Unwrap(client.httpClient.Load().Transport))
	}

	seen := rec.seen()
	if len(seen) != 1 || seen[0].host != nodeURL.Host {
		t.Errorf("wrapper saw %+v, want one request to %s", seen, nodeURL.Host)
	}

	if atomic.LoadInt32(&nodeCalls) != 1 || atomic.LoadInt32(&baseCalls) != 0 {
		t.Errorf("node/base calls = %d/%d, want 1/0", nodeCalls, baseCalls)
	}
}

// gatedReader yields the first openBytes of its source and then blocks until
// the gate closes, so a test can tell whether a consumer asked for more data
// before the gate opened.
type gatedReader struct {
	src       io.Reader
	openBytes int64
	gate      <-chan struct{}
	timeout   time.Duration

	read     int64
	timedOut atomic.Bool
}

func (g *gatedReader) Read(b []byte) (int, error) {
	if g.read >= g.openBytes {
		select {
		case <-g.gate:
		case <-time.After(g.timeout):
			g.timedOut.Store(true)
		}
	} else if remaining := g.openBytes - g.read; int64(len(b)) > remaining {
		b = b[:remaining]
	}

	n, err := g.src.Read(b)
	g.read += int64(n)

	return n, err //nolint:wrapcheck // io.Reader contract: pass io.EOF and source errors through verbatim
}

// TestWrapTransport_UploadStreamReachesWrapperUnbuffered verifies an upload
// stream reaches the wrapper before the client has read it all. The source
// withholds everything past the first block until the wrapper has been
// entered, so a client that buffered the body first would starve it and time
// out.
func TestWrapTransport_UploadStreamReachesWrapperUnbuffered(t *testing.T) {
	t.Parallel()

	const (
		fileSize  = 1 << 20
		openBytes = 1 << 10
	)

	var received int64

	srv := newTestServer(t, func(writer http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		atomic.StoreInt64(&received, n)

		writer.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = writer.Write(pveEnvelope(t, "UPID:node:upload"))
	})

	var calls int32

	client := newClientForServerURL(t, srv.URL, func(o *Options) {
		o.APIToken = testAPITokenShort
		installRecorder(o, &calls)
	})

	rec, ok := redirect.Unwrap(client.httpClient.Load().Transport).(*recordingWrapper)
	if !ok {
		t.Fatalf("Transport is %T, want *recordingWrapper", redirect.Unwrap(client.httpClient.Load().Transport))
	}

	gate := make(chan struct{})

	var once sync.Once

	rec.onEnter = func() { once.Do(func() { close(gate) }) }

	src := &gatedReader{
		src:       &patternReader{remaining: fileSize},
		openBytes: openBytes,
		gate:      gate,
		timeout:   10 * time.Second,
	}

	_, err := client.UploadWithContext(context.Background(), "/nodes/node/storage/local/upload",
		map[string]string{streamTestFieldContent: streamTestContentISO}, streamTestFileField, streamTestFileName,
		NewSizedReader(src, fileSize))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	if src.timedOut.Load() {
		t.Fatal("the upload was read past the first block before it reached the wrapper, so it was buffered")
	}

	assertSingleUpload(t, rec, fileSize, atomic.LoadInt64(&received))
}

// assertSingleUpload checks the wrapper saw exactly one upload of at least
// minSize bytes and that the server received as many bytes as it declared.
func assertSingleUpload(t *testing.T, rec *recordingWrapper, minSize, received int64) {
	t.Helper()

	seen := rec.seen()
	if len(seen) != 1 {
		t.Fatalf("wrapper saw %d requests, want 1", len(seen))
	}

	if seen[0].contentLength < minSize {
		t.Errorf("ContentLength = %d, want at least the %d byte file", seen[0].contentLength, minSize)
	}

	if received != seen[0].contentLength {
		t.Errorf("server received %d bytes, wrapper saw ContentLength %d", received, seen[0].contentLength)
	}
}

// TestSetKeepAlive_LeavesWrapperTransportsUnchanged verifies that with a
// wrapper installed SetKeepAlive records the value and writes no transport,
// neither the base the wrapper was handed nor a clone it installed.
func TestSetKeepAlive_LeavesWrapperTransportsUnchanged(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*http.Transport) http.RoundTripper{
		"wrapper around base":   func(b *http.Transport) http.RoundTripper { return &recordingWrapper{base: b} },
		"wrapper returns clone": func(b *http.Transport) http.RoundTripper { return b.Clone() },
	}

	for name, wrap := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts := minimalHTTPOptions()

			var base *http.Transport

			opts.WrapTransport = func(b *http.Transport) http.RoundTripper {
				base = b

				return wrap(b)
			}

			client, err := NewClient(opts)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			logger := &testLogger{}
			client.SetLogger(logger)
			client.SetLogConfig(LogConfig{Enabled: true})

			installed := redirect.Unwrap(client.httpClient.Load().Transport)

			client.SetKeepAlive(11)

			if redirect.Unwrap(client.httpClient.Load().Transport) != installed {
				t.Error("SetKeepAlive replaced the wrapper's RoundTripper")
			}

			assertIdleLimitsUnchanged(t, base, installed)

			if opts.KeepAlive != 11 {
				t.Errorf("Options.KeepAlive = %d, want 11 recorded", opts.KeepAlive)
			}

			logger.mu.Lock()
			defer logger.mu.Unlock()

			if len(logger.warns) != 1 || logger.warns[0] != "SetKeepAlive left the transport unchanged" {
				t.Errorf("warnings = %q, want one saying the transport was left unchanged", logger.warns)
			}
		})
	}
}

// assertIdleLimitsUnchanged checks that base, and installed when it is a
// *http.Transport, still carry the 5/5 idle limits minimalHTTPOptions sets.
func assertIdleLimitsUnchanged(t *testing.T, base *http.Transport, installed http.RoundTripper) {
	t.Helper()

	transports := map[string]*http.Transport{"base": base}
	if clone, ok := installed.(*http.Transport); ok {
		transports["installed clone"] = clone
	}

	for label, tr := range transports {
		if tr.MaxIdleConns != 5 || tr.MaxIdleConnsPerHost != 5 {
			t.Errorf("%s: MaxIdleConns = %d, MaxIdleConnsPerHost = %d, want 5 and 5 (unchanged)",
				label, tr.MaxIdleConns, tr.MaxIdleConnsPerHost)
		}
	}
}

// TestSetKeepAlive_WithoutNewClientRecordsOnly verifies a Client built without
// NewClient, which has no swappable transport, takes the call without panicking.
func TestSetKeepAlive_WithoutNewClientRecordsOnly(t *testing.T) {
	t.Parallel()

	(&Client{}).SetKeepAlive(3)

	opts := &Options{}
	client := &Client{options: opts}
	client.SetKeepAlive(3)

	if opts.KeepAlive != 3 {
		t.Errorf("Options.KeepAlive = %d, want 3", opts.KeepAlive)
	}
}

// TestSetKeepAlive_InstallsRetunedClone verifies SetKeepAlive installs a new
// transport carrying the new limits and the original configuration, and
// leaves the replaced transport's fields untouched.
func TestSetKeepAlive_InstallsRetunedClone(t *testing.T) {
	t.Parallel()

	opts := &Options{
		Host:                   wrapTestLoopback,
		Port:                   8006,
		Protocol:               protoHTTPS,
		Timeout:                5 * time.Second,
		KeepAlive:              5,
		TLSHandshakeTimeoutSec: 4,
		IdleConnTimeoutSec:     9,
		DialContext: func(context.Context, string, string) (net.Conn, error) {
			return nil, errWrapTestDial
		},
		SSLOptions: &SSLOptions{VerifyMode: SSLVerifyNone},
	}

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	before := liveTransport(t, client)

	client.SetKeepAlive(42)

	after := liveTransport(t, client)

	if after == before {
		t.Fatal("SetKeepAlive wrote the transport in use instead of installing a clone")
	}

	if before.MaxIdleConns != 5 || before.MaxIdleConnsPerHost != 5 {
		t.Errorf("replaced transport changed to %d/%d, want 5/5", before.MaxIdleConns, before.MaxIdleConnsPerHost)
	}

	if after.MaxIdleConns != 42 || after.MaxIdleConnsPerHost != 42 {
		t.Errorf("new transport idle limits = %d/%d, want 42/42", after.MaxIdleConns, after.MaxIdleConnsPerHost)
	}

	assertCloneKeepsConfiguration(t, after)

	if got := client.swap.RetiredCount(); got != 1 {
		t.Errorf("retired transports = %d, want the one replaced transport", got)
	}

	if opts.KeepAlive != 42 {
		t.Errorf("Options.KeepAlive = %d, want 42", opts.KeepAlive)
	}
}

// assertCloneKeepsConfiguration checks that a retuned transport still carries
// the TLS configuration, timeouts, and dial function TestSetKeepAlive_InstallsRetunedClone
// configured.
func assertCloneKeepsConfiguration(t *testing.T, after *http.Transport) {
	t.Helper()

	if after.TLSClientConfig == nil || !after.TLSClientConfig.InsecureSkipVerify {
		t.Error("the new transport lost the TLS configuration")
	}

	if after.TLSHandshakeTimeout != 4*time.Second || after.IdleConnTimeout != 9*time.Second {
		t.Errorf("new transport timeouts = %v/%v, want 4s/9s", after.TLSHandshakeTimeout, after.IdleConnTimeout)
	}

	if after.DialContext == nil {
		t.Fatal("the new transport lost the dial function")
	}

	_, dialErr := after.DialContext(context.Background(), "tcp", wrapTestLoopback+":8006")
	if !errors.Is(dialErr, errWrapTestDial) {
		t.Errorf("the new transport does not dial through Options.DialContext (err=%v)", dialErr)
	}
}

// TestSetKeepAlive_RepeatedLimitsKeepTransport verifies a call that changes no
// limit keeps the current transport and retires nothing.
func TestSetKeepAlive_RepeatedLimitsKeepTransport(t *testing.T) {
	t.Parallel()

	client, err := NewClient(minimalHTTPOptions())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	before := liveTransport(t, client)

	client.SetKeepAlive(5)

	if liveTransport(t, client) != before {
		t.Error("SetKeepAlive with the current limits replaced the transport")
	}

	if got := client.swap.RetiredCount(); got != 0 {
		t.Errorf("retired transports = %d, want 0", got)
	}
}

// connStateServer starts a server answering /version with a PVE envelope and
// reporting every connection state change on the returned channel. A request
// to /slow waits until release closes.
func connStateServer(t *testing.T, entered chan<- struct{}, release <-chan struct{}) (*httptest.Server, <-chan http.ConnState) {
	t.Helper()

	states := make(chan http.ConnState, 64)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api2/json/slow" {
			entered <- struct{}{}

			<-release
		}

		writer.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = writer.Write(versionEnvelope(t))
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) { states <- state }
	srv.Start()
	t.Cleanup(srv.Close)

	return srv, states
}

// TestSetKeepAlive_ClosesReplacedTransportIdleConnections verifies the idle
// connection pooled by the replaced transport is closed by the swap.
func TestSetKeepAlive_ClosesReplacedTransportIdleConnections(t *testing.T) {
	t.Parallel()

	srv, states := connStateServer(t, nil, nil)

	client := newClientForServerURL(t, srv.URL, func(o *Options) { o.APIToken = testAPITokenShort })

	_, err := client.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	waitForConnState(t, states, http.StateIdle)

	client.SetKeepAlive(9)
	awaitRetiring(t, client)

	waitForConnState(t, states, http.StateClosed)

	_, err = client.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do after SetKeepAlive: %v", err)
	}

	waitForConnState(t, states, http.StateNew)
}

// TestSetKeepAlive_InFlightRequestFinishesOnReplacedTransport verifies a
// request in flight across the swap completes on the replaced transport,
// which then closes its connection instead of pooling it.
func TestSetKeepAlive_InFlightRequestFinishesOnReplacedTransport(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{}, 1)
	release := make(chan struct{})

	srv, states := connStateServer(t, entered, release)

	client := newClientForServerURL(t, srv.URL, func(o *Options) { o.APIToken = testAPITokenShort })

	done := make(chan error, 1)

	go func() {
		_, err := client.Do("GET", "/slow", nil)
		done <- err
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the slow request never reached the server")
	}

	client.SetKeepAlive(9)
	awaitRetiring(t, client)
	close(release)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("in-flight Do: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the in-flight request never finished")
	}

	waitForConnState(t, states, http.StateClosed)

	if liveTransport(t, client).MaxIdleConns != 9 {
		t.Error("later requests do not use the retuned transport")
	}

	closeErr := client.Close()
	if closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
}

// TestSetKeepAlive_RaceWithRequests calls SetKeepAlive while requests run and
// their response bodies close, so go test -race catches any write to a
// transport net/http is reading.
func TestSetKeepAlive_RaceWithRequests(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*http.Transport) http.RoundTripper{
		wrapTestNoWrapper:       nil,
		"wrapper around base":   func(b *http.Transport) http.RoundTripper { return &recordingWrapper{base: b} },
		"wrapper returns clone": func(b *http.Transport) http.RoundTripper { return b.Clone() },
	}

	for name, wrap := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var calls int32

			srv := newEnvelopeCountingServer(t, testDataBase, &calls)

			client := newClientForServerURL(t, srv.URL, func(o *Options) {
				o.APIToken = testAPITokenShort
				o.WrapTransport = wrap
			})

			const (
				workers  = 4
				requests = 25
			)

			err := hammerSetter(func(i int) { client.SetKeepAlive(1 + i%4) }, workers, requests, func() error {
				_, doErr := client.Do("GET", "/version", nil)

				return doErr
			})
			if err != nil {
				t.Fatalf("Do during SetKeepAlive: %v", err)
			}

			if got := atomic.LoadInt32(&calls); got != workers*requests {
				t.Errorf("server saw %d requests, want %d", got, workers*requests)
			}

			closeErr := client.Close()
			if closeErr != nil {
				t.Fatalf("Close: %v", closeErr)
			}
		})
	}
}

// hammerSetter runs workers goroutines that each send requests
// through send while another goroutine calls setter with a rising counter,
// and returns the first request error.
func hammerSetter(setter func(int), workers, requests int, send func() error) error {
	var (
		group    sync.WaitGroup
		stop     = make(chan struct{})
		tuned    = make(chan struct{})
		failures = make(chan error, 1)
	)

	go func() {
		defer close(tuned)

		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				setter(i)
			}
		}
	}()

	for range workers {
		group.Go(func() {
			for range requests {
				err := send()
				if err != nil {
					select {
					case failures <- err:
					default:
					}

					return
				}
			}
		})
	}

	group.Wait()
	close(stop)
	<-tuned

	select {
	case err := <-failures:
		return err
	default:
		return nil
	}
}

// TestWrapTransport_SeesEachRetryAttempt verifies an ordinary SDK retry passes
// through the wrapper: a GET answered 503 and then 200 reaches the wrapper
// twice, while NewClient still calls the hook only once.
func TestWrapTransport_SeesEachRetryAttempt(t *testing.T) {
	t.Parallel()

	var versionCalls int32

	srv := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != wrapTestPathVersion {
			writer.WriteHeader(http.StatusNotFound)

			return
		}

		if atomic.AddInt32(&versionCalls, 1) == 1 {
			writer.WriteHeader(http.StatusServiceUnavailable)

			return
		}

		writer.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = writer.Write(versionEnvelope(t))
	})

	var (
		calls   int32
		wrapper **recordingWrapper
	)

	client := newClientForServerURL(t, srv.URL, func(o *Options) {
		o.APIToken = testAPITokenShort
		wrapper = installRecorder(o, &calls)
	})
	client.SetRetryDelay(time.Millisecond)

	_, err := client.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	if got := (*wrapper).countPath(wrapTestPathVersion); got != 2 {
		t.Errorf("wrapper saw %d GET /version attempts, want 2 (the 503 and the retry)", got)
	}

	if got := atomic.LoadInt32(&versionCalls); got != 2 {
		t.Errorf("server saw %d GET /version requests, want 2", got)
	}

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("WrapTransport calls = %d, want 1", got)
	}
}

// TestClose_ClosesBaseIdleConnectionsBehindRoundTripOnlyWrapper verifies Close
// reaches the idle sockets of the base transport when the wrapper implements
// only RoundTrip, so the http.Client cannot.
func TestClose_ClosesBaseIdleConnectionsBehindRoundTripOnlyWrapper(t *testing.T) {
	t.Parallel()

	states := make(chan http.ConnState, 16)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("ok"))
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) { states <- state }
	srv.Start()
	t.Cleanup(srv.Close)

	opts := minimalHTTPOptions()
	opts.WrapTransport = func(b *http.Transport) http.RoundTripper {
		return roundTripOnly{inner: &recordingWrapper{base: b}}
	}

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	base := redirect.Unwrap(client.httpClient.Load().Transport)

	if _, hasClose := base.(interface{ CloseIdleConnections() }); hasClose {
		t.Fatal("the wrapper must implement only RoundTrip for this test")
	}

	resp, err := client.httpClient.Load().Get(srv.URL) //nolint:noctx // exercising the transport directly
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	waitForConnState(t, states, http.StateIdle)

	closeErr := client.Close()
	if closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	waitForConnState(t, states, http.StateClosed)
}

// roundTripOnly hides every method of the RoundTripper it wraps except
// RoundTrip.
type roundTripOnly struct {
	inner http.RoundTripper
}

func (r roundTripOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	return r.inner.RoundTrip(req) //nolint:wrapcheck // a transport must hand back the inner error untouched
}

// waitForConnState drains states until want arrives, failing after a deadline.
func waitForConnState(t *testing.T, states <-chan http.ConnState, want http.ConnState) {
	t.Helper()

	deadline := time.After(5 * time.Second)

	for {
		select {
		case got := <-states:
			if got == want {
				return
			}
		case <-deadline:
			t.Fatalf("server connection never reached state %v", want)
		}
	}
}
