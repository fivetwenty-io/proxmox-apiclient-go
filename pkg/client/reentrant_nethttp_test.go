package client_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	pve "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

// publicReentry dials connections whose Close, once armed, calls the public
// client's SetTimeout and SetKeepAlive before closing, the way a connection
// wrapper that retunes the client when a connection closes would.
type publicReentry struct {
	cli      atomic.Pointer[pve.Client]
	armed    atomic.Bool
	finished chan struct{}
}

func (r *publicReentry) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err //nolint:wrapcheck // a dial function hands back the dialer's error untouched
	}

	return &publicReentrantConn{Conn: conn, reentry: r}, nil
}

func (r *publicReentry) reenter() {
	if !r.armed.CompareAndSwap(true, false) {
		return
	}

	defer close(r.finished)

	cli := *r.cli.Load()
	cli.SetTimeout(7 * time.Second)
	cli.SetKeepAlive(3)
}

type publicReentrantConn struct {
	net.Conn

	reentry *publicReentry
}

func (c *publicReentrantConn) Close() error {
	c.reentry.reenter()

	return c.Conn.Close() //nolint:wrapcheck // a net.Conn hands back the inner error untouched
}

// newBarrierServer answers /version with a PVE envelope once release closes,
// reporting each request on entered and each closed connection on closed.
func newBarrierServer(t *testing.T, entered chan<- struct{}, release <-chan struct{}) (*httptest.Server, <-chan struct{}) {
	t.Helper()

	closed := make(chan struct{}, 16)

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}

		<-release

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"version":"test"},"success":1}`)
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			closed <- struct{}{}
		}
	}
	srv.Start()
	t.Cleanup(srv.Close)

	return srv, closed
}

// awaitDone fails the test when done is not closed within 5 seconds.
func awaitDone(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not return: a lock was held while a connection's Close called back into the client", what)
	}
}

// startBlockedGets starts count GET /version requests on cli and waits for
// all of them to reach the barrier server. It returns the channel each
// request's result arrives on once release closes.
func startBlockedGets(
	t *testing.T, cli pve.Client, count int, entered <-chan struct{}, release chan struct{},
) <-chan error {
	t.Helper()

	results := make(chan error, count)

	for range count {
		go func() {
			_, err := cli.Get("/version", nil)
			results <- err
		}()
	}

	for range count {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("the requests never all reached the server")
		}
	}

	return results
}

// awaitGets receives count results, failing the test when they do not all
// arrive within 5 seconds or when any is an error.
func awaitGets(t *testing.T, results <-chan error, count int) {
	t.Helper()

	received := make(chan struct{})
	errs := make([]error, 0, count)

	go func() {
		defer close(received)

		for range count {
			errs = append(errs, <-results)
		}
	}()

	awaitDone(t, received, "the requests whose connections went idle")

	for _, err := range errs {
		if err != nil {
			t.Errorf("Get: %v", err)
		}
	}
}

// closeAndAwaitConns closes cli within 5 seconds and then waits for the
// server to see count connections closed.
func closeAndAwaitConns(t *testing.T, cli pve.Client, closed <-chan struct{}, count int) {
	t.Helper()

	closeDone := make(chan struct{})

	go func() {
		defer close(closeDone)

		_ = cli.Close()
	}()

	awaitDone(t, closeDone, "Close")

	for range count {
		select {
		case <-closed:
		case <-time.After(5 * time.Second):
			t.Fatal("a connection stayed open after the background close and Close")
		}
	}
}

// TestSetKeepAlive_ReentrantCloseFromNetHTTPDoesNotDeadlock lets net/http close
// an idle connection from a reentrant DialContext, by the idle timer and by
// MaxIdleConns eviction, while it holds the transport's idle lock. The
// connection's Close calls the public SetTimeout and SetKeepAlive, and
// SetKeepAlive retires that same transport, so it deadlocks if it waits to
// close the retired transport's idle connections. Every connection the
// server saw must then be closed by the background close or by Close.
func TestSetKeepAlive_ReentrantCloseFromNetHTTPDoesNotDeadlock(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		requests int
		mutate   func(*pve.Options)
	}{
		"idle timer expires the connection": {
			requests: 1,
			mutate:   func(o *pve.Options) { o.IdleConnTimeoutSec = 1 },
		},
		"MaxIdleConns evicts the oldest connection": {
			requests: 2,
			mutate: func(o *pve.Options) {
				o.KeepAlive = 1
				o.MaxIdleConnsPerHost = 2
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			entered := make(chan struct{}, tc.requests)
			release := make(chan struct{})
			srv, closed := newBarrierServer(t, entered, release)
			host, port := parseHostPort(srv.URL)
			reentry := &publicReentry{finished: make(chan struct{})}

			opts := pve.Options{
				Host:        host,
				Port:        port,
				Protocol:    testProtoHTTP,
				APIToken:    testAPIToken,
				DialContext: reentry.dial,
			}
			tc.mutate(&opts)

			cli, err := pve.NewClient(opts)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			reentry.cli.Store(&cli)

			results := startBlockedGets(t, cli, tc.requests, entered, release)

			reentry.armed.Store(true)
			close(release)

			awaitDone(t, reentry.finished, "the reentrant SetTimeout and SetKeepAlive")
			awaitGets(t, results, tc.requests)
			closeAndAwaitConns(t, cli, closed, tc.requests)
		})
	}
}
