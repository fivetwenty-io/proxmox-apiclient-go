package http //nolint:testpackage // white-box test: drives the client's swap transport and reads its timeout

import (
	"context"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/pool"
)

// Values the reentrant Close sets, distinct from every value the triggers use.
const (
	reentrantTimeout   = 7 * time.Second
	reentrantKeepAlive = 3
	reentrantPoolMax   = 5
	reentrantPoolHost  = 4
)

// reentry is shared by the connections its dial method returns. Once armed,
// the first of them to close calls SetTimeout and SetKeepAlive on the client
// and both setters on the pool, the way a connection wrapper that retunes the
// SDK when a connection closes would.
type reentry struct {
	client   *Client
	pool     *pool.Pool
	armed    atomic.Bool
	finished chan struct{}
}

func newReentry() *reentry {
	return &reentry{finished: make(chan struct{})}
}

// dial is a DialContext that wraps every connection in a reentrantConn.
func (r *reentry) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err //nolint:wrapcheck // a dial function hands back the dialer's error untouched
	}

	return &reentrantConn{Conn: conn, reentry: r}, nil
}

// reenter makes the reentrant calls once after the test arms it.
func (r *reentry) reenter() {
	if !r.armed.CompareAndSwap(true, false) {
		return
	}

	defer close(r.finished)

	r.client.SetTimeout(reentrantTimeout)
	r.client.SetKeepAlive(reentrantKeepAlive)
	r.pool.SetMaxConnections(reentrantPoolMax)
	r.pool.SetMaxConnectionsPerHost(reentrantPoolHost)
}

// reentrantConn is a net.Conn whose Close calls back into the SDK first.
type reentrantConn struct {
	net.Conn

	reentry *reentry
}

func (c *reentrantConn) Close() error {
	c.reentry.reenter()

	return c.Conn.Close() //nolint:wrapcheck // a net.Conn hands back the inner error untouched
}

// poolTransport returns the *http.Transport of a client the pool handed out.
func poolTransport(t *testing.T, client *http.Client) *http.Transport {
	t.Helper()

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("pool client Transport is %T, want *http.Transport", client.Transport)
	}

	return transport
}

// sendThroughPool sends one GET for url through the pool and drains and
// closes the body, so the connection goes back to the pool's transport.
func sendThroughPool(t *testing.T, poolInstance *pool.Pool, url string) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := poolInstance.Do(req)
	if err != nil {
		t.Fatalf("pool Do: %v", err)
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

// reentrantCase is one SDK path that closes idle connections.
type reentrantCase struct {
	viaPool     bool
	trigger     func(client *Client, poolInstance *pool.Pool)
	poolStaysUp bool
}

// TestIdleClose_ReentrantConnectionDoesNotDeadlock leaves a connection from a
// reentrant dial function idle on the client or the pool, then closes it
// through each SDK path that closes idle connections: the client's
// SetKeepAlive and Close, and the pool's Close. The pool setters change the
// pool's transport in place and close no connection, so they are not closing
// paths. The connection's Close calls SetTimeout, SetKeepAlive, and both pool
// setters, which deadlocks if the closing path still holds tuneMu, the swap
// transport's lock, the retired list's lock, or the pool's lock.
func TestIdleClose_ReentrantConnectionDoesNotDeadlock(t *testing.T) {
	t.Parallel()

	cases := map[string]reentrantCase{
		"client SetKeepAlive": {
			trigger:     func(client *Client, _ *pool.Pool) { client.SetKeepAlive(9) },
			poolStaysUp: true,
		},
		"client Close": {
			trigger:     func(client *Client, _ *pool.Pool) { _ = client.Close() },
			poolStaysUp: true,
		},
		"pool Close": {
			viaPool: true,
			trigger: func(_ *Client, poolInstance *pool.Pool) { _ = poolInstance.Close() },
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			srv, states := connStateServer(t, nil, nil)
			reenter := newReentryFor(t, srv.URL)

			if tc.viaPool {
				sendThroughPool(t, reenter.pool, srv.URL+"/api2/json/version")
			} else {
				_, err := reenter.client.Do("GET", "/version", nil)
				if err != nil {
					t.Fatalf("client Do: %v", err)
				}
			}

			waitForConnState(t, states, http.StateIdle)

			reenter.armed.Store(true)

			triggered := make(chan struct{})

			go func() {
				defer close(triggered)

				tc.trigger(reenter.client, reenter.pool)
			}()

			awaitReturn(t, reenter.finished, "the reentrant SDK calls from the connection's Close")
			awaitReturn(t, triggered, "the call that closed the connection")
			reenter.checkApplied(t, tc.poolStaysUp)
		})
	}
}

// newReentryFor builds a client and a pool for serverURL that both dial
// through the returned reentry, setting the pool's dial function through Get
// as a caller would.
func newReentryFor(t *testing.T, serverURL string) *reentry {
	t.Helper()

	reenter := newReentry()

	reenter.client = newClientForServerURL(t, serverURL, func(o *Options) {
		o.APIToken = testAPITokenShort
		o.DialContext = reenter.dial
	})
	t.Cleanup(func() { _ = reenter.client.Close() })

	reenter.pool = pool.New(pool.DefaultConfig())

	t.Cleanup(func() { _ = reenter.pool.Close() })

	shared, err := reenter.pool.Get()
	if err != nil {
		t.Fatalf("pool Get: %v", err)
	}

	reenter.pool.Put(shared)
	poolTransport(t, shared).DialContext = reenter.dial

	return reenter
}

// awaitReturn fails the test when done is not closed within 5 seconds.
func awaitReturn(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not return: the closing path held an SDK lock while closing the connection", what)
	}
}

// checkApplied verifies the reentrant calls took effect on the client and,
// when the pool is still open, on the pool.
func (r *reentry) checkApplied(t *testing.T, poolOpen bool) {
	t.Helper()

	if got := r.client.httpClient.Load().Timeout; got != reentrantTimeout {
		t.Errorf("client Timeout = %v, want %v from the reentrant SetTimeout", got, reentrantTimeout)
	}

	if got := r.client.swap.Load().MaxIdleConns; got != reentrantKeepAlive {
		t.Errorf("client MaxIdleConns = %d, want %d from the reentrant SetKeepAlive", got, reentrantKeepAlive)
	}

	if !poolOpen {
		return
	}

	current, err := r.pool.Get()
	if err != nil {
		t.Fatalf("pool Get after the reentrant calls: %v", err)
	}

	r.pool.Put(current)

	transport := poolTransport(t, current)
	if transport.MaxIdleConns != reentrantPoolMax || transport.MaxIdleConnsPerHost != reentrantPoolHost {
		t.Errorf("pool idle limits = %d/%d, want %d/%d from the reentrant setters",
			transport.MaxIdleConns, transport.MaxIdleConnsPerHost, reentrantPoolMax, reentrantPoolHost)
	}
}
