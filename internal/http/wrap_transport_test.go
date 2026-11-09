package http //nolint:testpackage // white-box test: inspects the client's unexported transport fields

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	wrapTestPathTicket  = "/api2/json/access/ticket"
	wrapTestPathVersion = "/api2/json/version"
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

	mu       sync.Mutex
	requests []recordedRequest
}

func (w *recordingWrapper) RoundTrip(req *http.Request) (*http.Response, error) {
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

// TestNewClient_NilWrapTransportKeepsPlainTransport verifies the default: with
// no wrapper the http.Client carries the bare *http.Transport.
func TestNewClient_NilWrapTransportKeepsPlainTransport(t *testing.T) {
	t.Parallel()

	client, err := NewClient(minimalHTTPOptions())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *http.Transport", client.httpClient.Transport)
	}

	if transport != client.baseTransport {
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

	if client.httpClient.Transport != http.RoundTripper(*wrapper) {
		t.Errorf("Transport is %T, want the wrapper's result", client.httpClient.Transport)
	}

	if (*wrapper).base != client.baseTransport {
		t.Error("the wrapper was not handed the transport NewClient built")
	}
}

// TestNewClient_WrapTransportReturningNilFails verifies a wrapper that returns
// nil is an error rather than a client with no transport.
func TestNewClient_WrapTransportReturningNilFails(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.WrapTransport = func(*http.Transport) http.RoundTripper { return nil }

	client, err := NewClient(opts)
	if !errors.Is(err, ErrWrapTransportNil) {
		t.Fatalf("NewClient error = %v, want ErrWrapTransportNil", err)
	}

	if client != nil {
		t.Error("NewClient returned a client alongside the error")
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
		Host:                   "127.0.0.1",
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

	_, err := base.DialContext(context.Background(), "tcp", "127.0.0.1:8006")
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
			_, _ = writer.Write(pveEnvelope(t, map[string]string{"version": "8.0"}))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	})

	var (
		calls   int32
		wrapper **recordingWrapper
	)

	client := newClientForServerURL(t, srv.URL, func(o *Options) {
		o.Username = "root@pam"
		o.Password = "secret"
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
		o.APIToken = "root@pam!tok=secret"
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

	rec, ok := client.httpClient.Transport.(*recordingWrapper)
	if !ok {
		t.Fatalf("Transport is %T, want *recordingWrapper", client.httpClient.Transport)
	}

	seen := rec.seen()
	if len(seen) != 1 || seen[0].host != nodeURL.Host {
		t.Errorf("wrapper saw %+v, want one request to %s", seen, nodeURL.Host)
	}

	if atomic.LoadInt32(&nodeCalls) != 1 || atomic.LoadInt32(&baseCalls) != 0 {
		t.Errorf("node/base calls = %d/%d, want 1/0", nodeCalls, baseCalls)
	}
}

// TestWrapTransport_UploadStreamReachesWrapperUnbuffered verifies a sized
// upload arrives at the wrapper as a stream: it has a declared length but no
// GetBody, which is what a buffered body would carry.
func TestWrapTransport_UploadStreamReachesWrapperUnbuffered(t *testing.T) {
	t.Parallel()

	const fileSize = 1 << 20

	var received int64

	srv := newTestServer(t, func(writer http.ResponseWriter, r *http.Request) {
		n, _ := io.Copy(io.Discard, r.Body)
		atomic.StoreInt64(&received, n)

		writer.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = writer.Write(pveEnvelope(t, "UPID:node:upload"))
	})

	var calls int32

	client := newClientForServerURL(t, srv.URL, func(o *Options) {
		o.APIToken = "root@pam!tok=secret"
		installRecorder(o, &calls)
	})

	rec, ok := client.httpClient.Transport.(*recordingWrapper)
	if !ok {
		t.Fatalf("Transport is %T, want *recordingWrapper", client.httpClient.Transport)
	}

	// The wrapper reads nothing itself: the body must still be unread, so
	// the server receives the whole stream.
	_, err := client.UploadWithContext(context.Background(), "/nodes/node/storage/local/upload",
		map[string]string{streamTestFieldContent: streamTestContentISO}, streamTestFileField, streamTestFileName,
		NewSizedReader(&patternReader{remaining: fileSize}, fileSize))
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}

	seen := rec.seen()
	if len(seen) != 1 {
		t.Fatalf("wrapper saw %d requests, want 1", len(seen))
	}

	if seen[0].hasGetBody {
		t.Error("the upload arrived with GetBody set, so it was buffered")
	}

	if seen[0].contentLength < fileSize {
		t.Errorf("ContentLength = %d, want at least the %d byte file", seen[0].contentLength, fileSize)
	}

	if got := atomic.LoadInt64(&received); got != seen[0].contentLength {
		t.Errorf("server received %d bytes, wrapper saw ContentLength %d", got, seen[0].contentLength)
	}
}

// TestSetKeepAlive_ReachesBaseTransportBehindWrapper verifies SetKeepAlive
// still tunes the real transport when a wrapper hides it.
func TestSetKeepAlive_ReachesBaseTransportBehindWrapper(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()

	var calls int32

	wrapper := installRecorder(opts, &calls)

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.SetKeepAlive(11)

	if got := (*wrapper).base.MaxIdleConns; got != 11 {
		t.Errorf("MaxIdleConns = %d, want 11", got)
	}

	if got := (*wrapper).base.MaxIdleConnsPerHost; got != 11 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 11", got)
	}
}

// TestSetKeepAlive_FallsBackToTypeAssertion verifies a Client whose
// baseTransport is unset still tunes a plain *http.Transport.
func TestSetKeepAlive_FallsBackToTypeAssertion(t *testing.T) {
	t.Parallel()

	client := clientPointedAt(t, "http://127.0.0.1:1")
	client.baseTransport = nil

	client.SetKeepAlive(13)

	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *http.Transport", client.httpClient.Transport)
	}

	if transport.MaxIdleConns != 13 {
		t.Errorf("MaxIdleConns = %d, want 13", transport.MaxIdleConns)
	}

	client.httpClient.Transport = &recordingWrapper{}
	client.SetKeepAlive(17) // an unknown transport with no base is left alone

	if transport.MaxIdleConns != 13 {
		t.Errorf("MaxIdleConns = %d after a no-op SetKeepAlive, want 13", transport.MaxIdleConns)
	}
}
