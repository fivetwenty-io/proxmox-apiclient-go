package client_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	pve "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

const (
	wrapTestPathTicket  = "/api2/json/access/ticket"
	wrapTestPathVersion = "/api2/json/version"
)

// pathRecorder is a RoundTripper that notes the method and path of each
// request before passing it to the transport it wraps.
type pathRecorder struct {
	base http.RoundTripper

	mu    sync.Mutex
	paths []string
}

func (p *pathRecorder) RoundTrip(req *http.Request) (*http.Response, error) {
	p.mu.Lock()
	p.paths = append(p.paths, req.Method+" "+req.URL.Path)
	p.mu.Unlock()

	return p.base.RoundTrip(req) //nolint:wrapcheck // a transport must hand back the base's error untouched
}

func (p *pathRecorder) seen() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.paths...)
}

func newWrapTestServer(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case wrapTestPathTicket:
			_, _ = io.WriteString(w, `{"data":{"ticket":"PVE:root@pam:0::t","CSRFPreventionToken":"c"},"success":1}`)
		case wrapTestPathVersion:
			_, _ = io.WriteString(w, `{"data":{"version":"test"},"success":1}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

// TestWrapTransport_UnsetLeavesClientWorking guards the default. The field is
// opt-in, so a client that leaves it nil must behave as it did before it
// existed.
func TestWrapTransport_UnsetLeavesClientWorking(t *testing.T) {
	t.Parallel()

	srv := newWrapTestServer(t)
	host, port := parseHostPort(srv.URL)

	cli, err := pve.NewClient(pve.Options{
		Host:     host,
		Port:     port,
		Protocol: testProtoHTTP,
		APIToken: testAPIToken,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = cli.Get("/version", nil)
	if err != nil {
		t.Fatalf("Get(/version) with no WrapTransport failed: %v", err)
	}
}

// TestWrapTransport_SeesLoginAndRequest verifies the wrapper sits under the
// ticket login as well as the API request, and is built exactly once.
func TestWrapTransport_SeesLoginAndRequest(t *testing.T) {
	t.Parallel()

	srv := newWrapTestServer(t)
	host, port := parseHostPort(srv.URL)

	var (
		built int32
		rec   *pathRecorder
	)

	cli, err := pve.NewClient(pve.Options{
		Host:     host,
		Port:     port,
		Protocol: testProtoHTTP,
		Username: "root@pam",
		Password: "secret",
		WrapTransport: func(base *http.Transport) http.RoundTripper {
			atomic.AddInt32(&built, 1)

			rec = &pathRecorder{base: base}

			return rec
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = cli.Get("/version", nil)
	if err != nil {
		t.Fatalf("Get(/version) through the wrapper failed: %v", err)
	}

	if got := atomic.LoadInt32(&built); got != 1 {
		t.Errorf("WrapTransport called %d times, want 1", got)
	}

	var login, version int

	for _, entry := range rec.seen() {
		switch entry {
		case "POST " + wrapTestPathTicket:
			login++
		case "GET " + wrapTestPathVersion:
			version++
		}
	}

	if login != 1 || version != 1 {
		t.Errorf("wrapper saw %v, want one POST /access/ticket and one GET /version", rec.seen())
	}
}

// TestWrapTransport_BaseKeepsDialContext verifies the transport handed to the
// wrapper already dials through Options.DialContext, so a wrapper that clones
// it keeps the caller's dial function.
func TestWrapTransport_BaseKeepsDialContext(t *testing.T) {
	t.Parallel()

	srv := newWrapTestServer(t)
	srvAddr := strings.TrimPrefix(srv.URL, "http://")

	var dialed atomic.Int64

	cli, err := pve.NewClient(pve.Options{
		Host:     "unroutable.invalid",
		Port:     8006,
		Protocol: testProtoHTTP,
		APIToken: testAPIToken,
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			dialed.Add(1)

			var d net.Dialer

			return d.DialContext(ctx, network, srvAddr)
		},
		WrapTransport: func(base *http.Transport) http.RoundTripper {
			return base.Clone()
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = cli.Get("/version", nil)
	if err != nil {
		t.Fatalf("Get(/version) through a cloned base transport failed: %v", err)
	}

	if dialed.Load() == 0 {
		t.Error("the cloned base transport did not dial through Options.DialContext")
	}
}

// TestWrapTransport_BaseCarriesTLSConfig verifies the base transport already
// holds the TLS configuration the options asked for when the wrapper runs.
func TestWrapTransport_BaseCarriesTLSConfig(t *testing.T) {
	t.Parallel()

	var sawTLS bool

	_, err := pve.NewClient(pve.Options{
		Host:       "pve.example.com",
		Port:       8006,
		Protocol:   testProtoHTTPS,
		APIToken:   testAPIToken,
		SSLOptions: &pve.SSLOptions{VerifyMode: pve.SSLVerifyNone},
		WrapTransport: func(base *http.Transport) http.RoundTripper {
			sawTLS = base.TLSClientConfig != nil && base.TLSClientConfig.InsecureSkipVerify

			return base
		},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	if !sawTLS {
		t.Error("the base transport had no insecure-skip-verify TLS config when the wrapper ran")
	}
}

// TestWrapTransport_NilResultFailsNewClient verifies a wrapper returning nil,
// or a typed nil, is reported instead of producing a client that panics later.
func TestWrapTransport_NilResultFailsNewClient(t *testing.T) {
	t.Parallel()

	cases := map[string]http.RoundTripper{
		"literal nil":         nil,
		"nil *http.Transport": (*http.Transport)(nil),
		"nil *pathRecorder":   (*pathRecorder)(nil),
	}

	for name, result := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := pve.NewClient(pve.Options{
				Host:          "pve.example.com",
				Port:          8006,
				Protocol:      testProtoHTTP,
				APIToken:      testAPIToken,
				WrapTransport: func(*http.Transport) http.RoundTripper { return result },
			})
			if err == nil {
				t.Fatal("NewClient() = nil error, want an error when WrapTransport returns a nil RoundTripper")
			}
		})
	}
}

// TestSetKeepAlive_RaceWithRequests calls SetKeepAlive through the public
// client while requests run and their response bodies close, with and
// without a wrapper, so go test -race catches a write to a transport in use.
func TestSetKeepAlive_RaceWithRequests(t *testing.T) {
	t.Parallel()

	cases := map[string]func(*http.Transport) http.RoundTripper{
		"no wrapper": nil,
		"wrapper":    func(b *http.Transport) http.RoundTripper { return &pathRecorder{base: b} },
	}

	for name, wrap := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			srv := newWrapTestServer(t)
			host, port := parseHostPort(srv.URL)

			cli, err := pve.NewClient(pve.Options{
				Host:          host,
				Port:          port,
				Protocol:      testProtoHTTP,
				APIToken:      testAPIToken,
				WrapTransport: wrap,
			})
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			failures := hammerPublicSetKeepAlive(cli, 4, 25)
			if failures != 0 {
				t.Errorf("%d requests failed while SetKeepAlive ran", failures)
			}
		})
	}
}

// hammerPublicSetKeepAlive sends workers*requests GET /version requests on cli
// while another goroutine calls SetKeepAlive with changing values, and returns
// how many requests failed.
func hammerPublicSetKeepAlive(cli pve.Client, workers, requests int) int32 {
	var (
		group    sync.WaitGroup
		failures atomic.Int32
		stop     = make(chan struct{})
		tuned    = make(chan struct{})
	)

	go func() {
		defer close(tuned)

		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				cli.SetKeepAlive(1 + i%4)
			}
		}
	}()

	for range workers {
		group.Go(func() {
			for range requests {
				_, err := cli.Get("/version", nil)
				if err != nil {
					failures.Add(1)
				}
			}
		})
	}

	group.Wait()
	close(stop)
	<-tuned

	return failures.Load()
}
