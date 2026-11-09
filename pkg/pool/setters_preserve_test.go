package pool_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/pool"
)

const (
	preserveCookie  = "pool-session"
	preserveTimeout = 1234 * time.Millisecond
)

// errStopRedirect is what the test's redirect policy returns, so a request
// that reaches the policy fails with it instead of following the redirect.
var errStopRedirect = errors.New("redirect stopped by the caller's policy")

// newPreserveServer starts a TLS server that sets a cookie on /set, answers
// 200 on /check only when that cookie comes back, and redirects /redirect to
// /check.
func newPreserveServer(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/set":
			http.SetCookie(writer, &http.Cookie{Name: preserveCookie, Value: "1", Path: "/"})
		case "/check":
			_, err := request.Cookie(preserveCookie)
			if err != nil {
				http.Error(writer, "cookie missing", http.StatusBadRequest)

				return
			}
		case "/redirect":
			http.Redirect(writer, request, "/check", http.StatusFound)

			return
		}

		_, _ = io.WriteString(writer, "ok")
	}))
	t.Cleanup(srv.Close)

	return srv
}

// callerSettings records what the caller configured on the pool's client and
// counts the calls its proxy and dial functions receive.
type callerSettings struct {
	roots    *x509.CertPool
	jar      http.CookieJar
	proxied  atomic.Int32
	dialed   atomic.Int32
	redirect atomic.Int32
}

// configure sets custom TLS roots, a proxy function, a dial function, a
// cookie jar, a redirect policy, and a timeout on the client Get returns.
func (s *callerSettings) configure(t *testing.T, poolInstance *pool.Pool, srv *httptest.Server) {
	t.Helper()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}

	s.jar = jar
	s.roots = x509.NewCertPool()
	s.roots.AddCert(srv.Certificate())

	client, transport := sharedClient(t, poolInstance)

	transport.TLSClientConfig = &tls.Config{RootCAs: s.roots, MinVersion: tls.VersionTLS12}
	transport.Proxy = func(*http.Request) (*url.URL, error) {
		s.proxied.Add(1)

		return nil, nil //nolint:nilnil // nil URL and nil error mean "no proxy" to net/http
	}
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		s.dialed.Add(1)

		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	client.Jar = jar
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		s.redirect.Add(1)

		return errStopRedirect
	}
	client.Timeout = preserveTimeout
}

// get sends one GET for path through the pool, returning the status code.
func (s *callerSettings) get(t *testing.T, poolInstance *pool.Pool, srv *httptest.Server, path string) (int, error) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+path, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := poolInstance.Do(req)
	if err != nil {
		return 0, err //nolint:wrapcheck // the caller matches the redirect policy's error with errors.Is
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	return resp.StatusCode, nil
}

// verify checks that the pool's current client and transport still carry
// every caller setting, by inspecting them and by sending requests that each
// setting decides.
func (s *callerSettings) verify(t *testing.T, poolInstance *pool.Pool, srv *httptest.Server) {
	t.Helper()

	client, transport := sharedClient(t, poolInstance)

	if transport.TLSClientConfig == nil || transport.TLSClientConfig.RootCAs != s.roots {
		t.Error("the custom TLS roots did not carry over")
	}

	if transport.Proxy == nil || transport.DialContext == nil {
		t.Error("the proxy or dial function did not carry over")
	}

	if client.Jar != s.jar || client.CheckRedirect == nil || client.Timeout != preserveTimeout {
		t.Errorf("client Jar/CheckRedirect/Timeout = %v/%v/%v, want the caller's jar, policy, and %v",
			client.Jar, client.CheckRedirect != nil, client.Timeout, preserveTimeout)
	}

	proxied, dialed, redirected := s.proxied.Load(), s.dialed.Load(), s.redirect.Load()

	// /check succeeds only over TLS with the custom roots, with the cookie
	// from the jar, and through the proxy and dial functions; the new
	// transport has no pooled connection, so it must dial.
	status, err := s.get(t, poolInstance, srv, "/check")
	if err != nil || status != http.StatusOK {
		t.Fatalf("GET /check = %d, %v; want 200 with the custom roots and the jar's cookie", status, err)
	}

	if s.proxied.Load() == proxied || s.dialed.Load() == dialed {
		t.Error("the request did not go through the caller's proxy and dial functions")
	}

	_, err = s.get(t, poolInstance, srv, "/redirect")
	if !errors.Is(err, errStopRedirect) || s.redirect.Load() == redirected {
		t.Errorf("GET /redirect err = %v, want the caller's redirect policy to stop it", err)
	}
}

// TestPoolSetters_PreserveCallerConfiguration configures the client Get
// returns with custom TLS roots, a proxy, a dial function, a cookie jar, a
// redirect policy, and a timeout, then calls each setter and checks that all
// of them survive on the new client while only the requested limit changes.
func TestPoolSetters_PreserveCallerConfiguration(t *testing.T) {
	t.Parallel()

	setters := map[string]struct {
		set   func(poolInstance *pool.Pool, value int)
		limit func(transport *http.Transport) int
		other func(transport *http.Transport) int
	}{
		"SetMaxConnections": {
			set:   (*pool.Pool).SetMaxConnections,
			limit: func(transport *http.Transport) int { return transport.MaxIdleConns },
			other: func(transport *http.Transport) int { return transport.MaxIdleConnsPerHost },
		},
		"SetMaxConnectionsPerHost": {
			set:   (*pool.Pool).SetMaxConnectionsPerHost,
			limit: func(transport *http.Transport) int { return transport.MaxIdleConnsPerHost },
			other: func(transport *http.Transport) int { return transport.MaxIdleConns },
		},
	}

	for name, setter := range setters {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			srv := newPreserveServer(t)
			poolInstance := pool.New(pool.DefaultConfig())

			t.Cleanup(func() { _ = poolInstance.Close() })

			settings := &callerSettings{}
			settings.configure(t, poolInstance, srv)

			status, err := settings.get(t, poolInstance, srv, "/set")
			if err != nil || status != http.StatusOK {
				t.Fatalf("GET /set = %d, %v; want 200", status, err)
			}

			before, beforeTransport := sharedClient(t, poolInstance)
			want, other := setter.limit(beforeTransport)+5, setter.other(beforeTransport)

			setter.set(poolInstance, want)

			after, afterTransport := sharedClient(t, poolInstance)
			if after == before || afterTransport == beforeTransport {
				t.Fatal("the setter did not install a new client and transport")
			}

			if setter.limit(afterTransport) != want || setter.other(afterTransport) != other {
				t.Errorf("limits after %s = %d/%d, want %d with the other limit still %d",
					name, setter.limit(afterTransport), setter.other(afterTransport), want, other)
			}

			if afterTransport.IdleConnTimeout != beforeTransport.IdleConnTimeout ||
				afterTransport.ResponseHeaderTimeout != beforeTransport.ResponseHeaderTimeout ||
				afterTransport.ForceAttemptHTTP2 != beforeTransport.ForceAttemptHTTP2 {
				t.Error("a transport setting other than the requested limit changed")
			}

			settings.verify(t, poolInstance, srv)
		})
	}
}

// opaqueRoundTripper is a RoundTripper that is not an *http.Transport, as a
// caller might install through Get.
type opaqueRoundTripper struct {
	base http.RoundTripper
}

func (o *opaqueRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return o.base.RoundTrip(req) //nolint:wrapcheck // a transport hands back the inner error untouched
}

// TestPoolSetters_LeaveNonTransportRoundTripperAlone verifies that when the
// caller replaced the shared client's Transport with another RoundTripper,
// the setters keep the same client and RoundTripper, and that a later setter
// still works once an *http.Transport is back.
func TestPoolSetters_LeaveNonTransportRoundTripperAlone(t *testing.T) {
	t.Parallel()

	poolInstance := pool.New(pool.DefaultConfig())

	t.Cleanup(func() { _ = poolInstance.Close() })

	client, transport := sharedClient(t, poolInstance)
	opaque := &opaqueRoundTripper{base: transport}
	client.Transport = opaque

	poolInstance.SetMaxConnections(transport.MaxIdleConns + 3)
	poolInstance.SetMaxConnectionsPerHost(transport.MaxIdleConnsPerHost + 3)

	current, err := poolInstance.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	poolInstance.Put(current)

	if current != client || current.Transport != opaque {
		t.Fatal("a setter replaced a client whose Transport is not an *http.Transport")
	}

	client.Transport = nil

	poolInstance.SetMaxConnections(transport.MaxIdleConns + 4)

	again, err := poolInstance.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	poolInstance.Put(again)

	if again != client || again.Transport != nil {
		t.Error("a setter replaced a client whose nil Transport means http.DefaultTransport")
	}
}

// TestPoolNew_CopiesConfig verifies two pools built from one *Config share
// nothing: setters on both run concurrently without a race, and neither
// writes the caller's Config.
func TestPoolNew_CopiesConfig(t *testing.T) {
	t.Parallel()

	config := pool.DefaultConfig()
	wantMax, wantPerHost := config.MaxConnections, config.MaxConnectionsPerHost

	first := pool.New(config)
	second := pool.New(config)

	t.Cleanup(func() {
		_ = first.Close()
		_ = second.Close()
	})

	var group sync.WaitGroup

	for i := range 4 {
		group.Go(func() {
			for j := range 50 {
				first.SetMaxConnections(1 + (i+j)%7)
				second.SetMaxConnectionsPerHost(1 + (i+j)%5)
				first.SetMaxConnectionsPerHost(1 + (i+j)%3)
				second.SetMaxConnections(1 + (i+j)%6)
			}
		})
	}

	group.Wait()

	if config.MaxConnections != wantMax || config.MaxConnectionsPerHost != wantPerHost {
		t.Errorf("caller Config = %d/%d after the setters, want it unchanged at %d/%d",
			config.MaxConnections, config.MaxConnectionsPerHost, wantMax, wantPerHost)
	}
}
