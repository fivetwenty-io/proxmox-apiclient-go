package pool_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/pool"
)

// concurrentRequests is how many requests the Close test holds open at once,
// so the pool's transport ends up with that many idle connections.
const concurrentRequests = 3

// roundTripOnly is a caller's wrapper around the pool's transport that
// implements only RoundTrip, so nothing can reach the transport through it.
type roundTripOnly struct {
	base http.RoundTripper
}

func (w *roundTripOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	return w.base.RoundTrip(req) //nolint:wrapcheck // a transport hands back the inner error untouched
}

// closingWrapper is a caller's wrapper that also closes idle connections,
// counting the calls.
type closingWrapper struct {
	roundTripOnly

	closed atomic.Int32
}

func (w *closingWrapper) CloseIdleConnections() {
	w.closed.Add(1)
}

// connTracker is a DialContext that records every connection it dials and
// whether each one has been closed.
type connTracker struct {
	mu    sync.Mutex
	conns []*trackedConn
}

type trackedConn struct {
	net.Conn

	closed atomic.Bool
}

func (c *trackedConn) Close() error {
	c.closed.Store(true)

	return c.Conn.Close() //nolint:wrapcheck // a net.Conn hands back the inner error untouched
}

func (d *connTracker) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err //nolint:wrapcheck // a dial function hands back the dialer's error untouched
	}

	tracked := &trackedConn{Conn: conn}

	d.mu.Lock()
	d.conns = append(d.conns, tracked)
	d.mu.Unlock()

	return tracked, nil
}

// counts reports how many connections were dialed and how many are open.
func (d *connTracker) counts() (int, int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	open := 0

	for _, conn := range d.conns {
		if !conn.closed.Load() {
			open++
		}
	}

	return len(d.conns), open
}

// newBarrierServer starts a server whose handler answers only once
// concurrentRequests requests have arrived, so each one needs its own
// connection.
func newBarrierServer(t *testing.T) *httptest.Server {
	t.Helper()

	var arrived sync.WaitGroup

	arrived.Add(concurrentRequests)

	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		arrived.Done()

		waited := make(chan struct{})

		go func() {
			arrived.Wait()
			close(waited)
		}()

		select {
		case <-waited:
		case <-time.After(5 * time.Second):
		}

		_, _ = io.WriteString(writer, "ok")
	}))
	t.Cleanup(srv.Close)

	return srv
}

// sendConcurrently sends concurrentRequests GETs through the pool at once and
// drains and closes each body, so their connections go back to the transport
// idle.
func sendConcurrently(t *testing.T, poolInstance *pool.Pool, url string) {
	t.Helper()

	var group sync.WaitGroup

	for range concurrentRequests {
		group.Go(func() {
			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
			if err != nil {
				t.Errorf("NewRequest: %v", err)

				return
			}

			resp, err := poolInstance.Do(req)
			if err != nil {
				t.Errorf("Do: %v", err)

				return
			}

			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		})
	}

	group.Wait()
}

// TestPoolGet_SameClientAcrossSetters verifies Get returns the identical
// client and transport before and after each setter, that the setter changes
// only its own limit on that transport, and that what a caller configured on
// the client stays in place.
func TestPoolGet_SameClientAcrossSetters(t *testing.T) {
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

			poolInstance := pool.New(pool.DefaultConfig())

			t.Cleanup(func() { _ = poolInstance.Close() })

			before, beforeTransport := sharedClient(t, poolInstance)
			before.Timeout = 1234 * time.Millisecond

			want, other := setter.limit(beforeTransport)+5, setter.other(beforeTransport)

			setter.set(poolInstance, want)

			after, afterTransport := sharedClient(t, poolInstance)
			if after != before || afterTransport != beforeTransport {
				t.Fatal("Get returned a different client or transport after the setter")
			}

			if setter.limit(afterTransport) != want || setter.other(afterTransport) != other {
				t.Errorf("limits after %s = %d/%d, want %d with the other limit still %d",
					name, setter.limit(afterTransport), setter.other(afterTransport), want, other)
			}

			if after.Timeout != 1234*time.Millisecond {
				t.Errorf("client Timeout = %v, want the caller's 1.234s", after.Timeout)
			}
		})
	}
}

// TestPoolSetters_ReachTransportBehindWrapper verifies the setters change the
// pool's own transport when a caller has wrapped it, and leave the caller's
// wrapper on the shared client.
func TestPoolSetters_ReachTransportBehindWrapper(t *testing.T) {
	t.Parallel()

	poolInstance := pool.New(pool.DefaultConfig())

	t.Cleanup(func() { _ = poolInstance.Close() })

	client, transport := sharedClient(t, poolInstance)
	wrapper := &roundTripOnly{base: transport}
	client.Transport = wrapper

	wantMax, wantPerHost := transport.MaxIdleConns+3, transport.MaxIdleConnsPerHost+3

	poolInstance.SetMaxConnections(wantMax)
	poolInstance.SetMaxConnectionsPerHost(wantPerHost)

	if transport.MaxIdleConns != wantMax || transport.MaxIdleConnsPerHost != wantPerHost {
		t.Errorf("pool transport limits = %d/%d, want %d/%d through the wrapper",
			transport.MaxIdleConns, transport.MaxIdleConnsPerHost, wantMax, wantPerHost)
	}

	current, err := poolInstance.Get()
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	poolInstance.Put(current)

	if current != client || current.Transport != wrapper {
		t.Error("a setter replaced the shared client or the caller's wrapper")
	}
}

// TestPoolClose_ClosesOwnTransportBehindWrapper wraps the shared client's
// Transport with a RoundTrip-only wrapper, sends concurrent requests through
// the pool so its transport holds idle connections, and verifies Close closes
// every one of them.
func TestPoolClose_ClosesOwnTransportBehindWrapper(t *testing.T) {
	t.Parallel()

	srv := newBarrierServer(t)
	poolInstance := pool.New(pool.DefaultConfig())

	t.Cleanup(func() { _ = poolInstance.Close() })

	tracker := &connTracker{}
	client, transport := sharedClient(t, poolInstance)
	transport.DialContext = tracker.dial
	client.Transport = &roundTripOnly{base: transport}

	sendConcurrently(t, poolInstance, srv.URL)

	dialed, open := tracker.counts()
	if dialed != concurrentRequests || open != dialed {
		t.Fatalf("before Close: %d dialed, %d open; want %d idle connections", dialed, open, concurrentRequests)
	}

	err := poolInstance.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	// A connection whose body was closed may reach the idle list just after
	// Close; net/http then closes it on its read loop, so wait briefly.
	deadline := time.Now().Add(2 * time.Second)
	for _, open = tracker.counts(); open != 0 && time.Now().Before(deadline); _, open = tracker.counts() {
		time.Sleep(10 * time.Millisecond)
	}

	if open != 0 {
		t.Errorf("after Close: %d of %d idle connections still open behind the wrapper", open, dialed)
	}
}

// TestPoolClose_CallsCallerTransportCloser verifies Close also calls the
// CloseIdleConnections method of a caller's replacement Transport, once, and
// that a nil Transport, which means http.DefaultTransport, does not stop
// Close.
func TestPoolClose_CallsCallerTransportCloser(t *testing.T) {
	t.Parallel()

	t.Run("closing wrapper", func(t *testing.T) {
		t.Parallel()

		poolInstance := pool.New(pool.DefaultConfig())
		client, transport := sharedClient(t, poolInstance)
		wrapper := &closingWrapper{roundTripOnly: roundTripOnly{base: transport}}
		client.Transport = wrapper

		_ = poolInstance.Close()
		_ = poolInstance.Close()

		if got := wrapper.closed.Load(); got != 1 {
			t.Errorf("wrapper CloseIdleConnections calls = %d, want 1", got)
		}
	})

	t.Run("nil Transport", func(t *testing.T) {
		t.Parallel()

		poolInstance := pool.New(pool.DefaultConfig())
		client, _ := sharedClient(t, poolInstance)
		client.Transport = nil

		err := poolInstance.Close()
		if err != nil {
			t.Fatalf("Close: %v", err)
		}

		if poolInstance.IsHealthy() {
			t.Error("the pool reports healthy after Close")
		}
	})
}
