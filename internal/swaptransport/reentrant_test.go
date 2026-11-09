package swaptransport_test

import (
	"context"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/swaptransport"
)

// reentrantDialer dials connections whose Close, once armed, calls Update,
// CloseIdleConnections, and RetiredCount on swap before closing, the way a
// connection wrapper that retunes the SDK when a connection closes would.
type reentrantDialer struct {
	swap     *swaptransport.Transport
	armed    atomic.Bool
	finished chan struct{}
}

func (d *reentrantDialer) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err //nolint:wrapcheck // a dial function hands back the dialer's error untouched
	}

	return &reentrantConn{Conn: conn, dialer: d}, nil
}

func (d *reentrantDialer) reenter() {
	if !d.armed.CompareAndSwap(true, false) {
		return
	}

	defer close(d.finished)

	d.swap.Update(func(next *http.Transport) bool {
		next.MaxIdleConns = 99

		return true
	}).CloseIdleConnections()
	d.swap.CloseIdleConnections()
	_ = d.swap.RetiredCount()
}

type reentrantConn struct {
	net.Conn

	dialer *reentrantDialer
}

func (c *reentrantConn) Close() error {
	c.dialer.reenter()

	return c.Conn.Close() //nolint:wrapcheck // a net.Conn hands back the inner error untouched
}

// awaitReentry fails the test when the reentrant calls or the triggering call
// do not return, which means a lock was held while closing the connection.
func awaitReentry(t *testing.T, dialer *reentrantDialer, triggered <-chan struct{}) {
	t.Helper()

	for _, done := range []<-chan struct{}{dialer.finished, triggered} {
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("closing a connection deadlocked: a lock was held while its Close called back into the Transport")
		}
	}

	if got := dialer.swap.Load().MaxIdleConns; got != 99 {
		t.Errorf("MaxIdleConns = %d, want 99 from the reentrant Update", got)
	}
}

// TestUpdate_ReentrantConnectionCloseDoesNotDeadlock closes an idle
// connection of the replaced transport through the Idle that Update returns.
func TestUpdate_ReentrantConnectionCloseDoesNotDeadlock(t *testing.T) {
	t.Parallel()

	srv, states := newStateServer(t)
	dialer := &reentrantDialer{finished: make(chan struct{})}
	dialer.swap = swaptransport.New(&http.Transport{DialContext: dialer.dial})
	client := &http.Client{Transport: dialer.swap}

	mustGet(t, client, srv.URL)
	waitForState(t, states, http.StateIdle)

	dialer.armed.Store(true)

	triggered := make(chan struct{})

	go func() {
		defer close(triggered)

		dialer.swap.Update(func(next *http.Transport) bool {
			next.MaxIdleConns = 1

			return true
		}).CloseIdleConnections()
	}()

	awaitReentry(t, dialer, triggered)
	waitForState(t, states, http.StateClosed)
}

// TestCloseIdleConnections_ReentrantRetiredConnectionDoesNotDeadlock leaves
// an idle connection on a retired transport, by pausing a request in GetConn
// across an Update, and closes it through CloseIdleConnections, which walks
// the retired list.
func TestCloseIdleConnections_ReentrantRetiredConnectionDoesNotDeadlock(t *testing.T) {
	t.Parallel()

	srv, states := newStateServer(t)
	dialer := &reentrantDialer{finished: make(chan struct{})}
	dialer.swap = swaptransport.New(&http.Transport{DialContext: dialer.dial})
	client := &http.Client{Transport: dialer.swap}
	original := dialer.swap.Load()

	reached := make(chan struct{})
	release := make(chan struct{})
	done := pausedGet(client, srv.URL, reached, release)

	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("the request never reached GetConn")
	}

	dialer.swap.Update(func(next *http.Transport) bool {
		next.MaxIdleConns = 1

		return true
	}).CloseIdleConnections()

	close(release)

	err := <-done
	if err != nil {
		t.Fatalf("paused request: %v", err)
	}

	waitForState(t, states, http.StateIdle)

	if dialer.swap.Load() == original || dialer.swap.RetiredCount() != 1 {
		t.Fatal("the original transport was not retired")
	}

	dialer.armed.Store(true)

	triggered := make(chan struct{})

	go func() {
		defer close(triggered)

		dialer.swap.CloseIdleConnections()
	}()

	awaitReentry(t, dialer, triggered)
	waitForState(t, states, http.StateClosed)
}

// TestRetiredAdd_ReturnsReplacedAndDroppedTransports verifies Add hands back
// the transport it kept, plus the one the bound dropped once the list is
// full, and that Snapshot is a copy of the kept list.
func TestRetiredAdd_ReturnsReplacedAndDroppedTransports(t *testing.T) {
	t.Parallel()

	var retired swaptransport.Retired

	added := make([]*http.Transport, 0, 9)

	for range 8 {
		transport := &http.Transport{}
		added = append(added, transport)

		idle := retired.Add(transport)
		if len(idle) != 1 || idle[0] != transport {
			t.Fatalf("Add below the bound returned %v, want only the added transport", idle)
		}
	}

	ninth := &http.Transport{}

	idle := retired.Add(ninth)
	if len(idle) != 2 || idle[0] != ninth || idle[1] != added[0] {
		t.Fatalf("Add at the bound returned %v, want the added transport and the oldest kept one", idle)
	}

	snapshot := retired.Snapshot()
	if len(snapshot) != 8 || snapshot[0] != added[1] || snapshot[7] != ninth {
		t.Fatalf("Snapshot = %v, want the eight most recent transports, oldest first", snapshot)
	}

	snapshot[0] = nil

	if retired.Snapshot()[0] != added[1] {
		t.Error("changing a Snapshot changed the kept list")
	}

	if retired.Len() != 8 {
		t.Errorf("Len = %d, want 8", retired.Len())
	}

	// A nil Idle, as Update returns when nothing changed, closes nothing.
	swaptransport.Idle(nil).CloseIdleConnections()
}

// backgroundCloser dials connections whose Close, once armed, closes the idle
// connections of transport through CloseInBackground, the way SetKeepAlive
// does when a connection's Close calls it.
type backgroundCloser struct {
	transport *http.Transport
	retiring  sync.WaitGroup
	armed     atomic.Bool
	returned  chan struct{}
}

func (b *backgroundCloser) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, addr)
	if err != nil {
		return nil, err //nolint:wrapcheck // a dial function hands back the dialer's error untouched
	}

	return &backgroundClosingConn{Conn: conn, closer: b}, nil
}

type backgroundClosingConn struct {
	net.Conn

	closer *backgroundCloser
}

func (c *backgroundClosingConn) Close() error {
	if c.closer.armed.CompareAndSwap(true, false) {
		swaptransport.Idle{c.closer.transport}.CloseInBackground(&c.closer.retiring)
		close(c.closer.returned)
	}

	return c.Conn.Close() //nolint:wrapcheck // a net.Conn hands back the inner error untouched
}

// TestIdleCloseInBackground_DoesNotWaitOnNetHTTPIdleLock lets net/http's
// idle timer close a connection, which it does while holding the
// transport's idle lock. The connection's Close closes that same transport's
// idle connections through CloseInBackground, which must return without
// waiting for the lock, and the goroutine it started must then finish.
func TestIdleCloseInBackground_DoesNotWaitOnNetHTTPIdleLock(t *testing.T) {
	t.Parallel()

	srv, states := newStateServer(t)
	closer := &backgroundCloser{returned: make(chan struct{})}
	closer.transport = &http.Transport{DialContext: closer.dial, IdleConnTimeout: 50 * time.Millisecond}
	client := &http.Client{Transport: closer.transport}

	closer.armed.Store(true)
	mustGet(t, client, srv.URL)

	select {
	case <-closer.returned:
	case <-time.After(5 * time.Second):
		t.Fatal("CloseInBackground waited for the idle lock net/http held while closing the connection")
	}

	finished := make(chan struct{})

	go func() {
		defer close(finished)

		closer.retiring.Wait()
	}()

	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the goroutine CloseInBackground started never finished")
	}

	waitForState(t, states, http.StateClosed)
}

// TestIdleCloseInBackground_EmptySetStartsNothing verifies a nil Idle, as
// Update returns when nothing changed, adds nothing to the wait group.
func TestIdleCloseInBackground_EmptySetStartsNothing(t *testing.T) {
	t.Parallel()

	var retiring sync.WaitGroup

	swaptransport.Idle(nil).CloseInBackground(&retiring)
	swaptransport.Idle{}.CloseInBackground(&retiring)

	// Wait returns at once only when nothing was added.
	retiring.Wait()
}
