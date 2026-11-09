package http //nolint:testpackage // white-box test: retunes the client's swap transport and waits on its cleanup

import (
	"net/http"
	"testing"
	"time"
)

// shortIdleConnTimeout is the idle timeout the expiration case gives the
// client's transport, so net/http's idle timer fires within the test.
const shortIdleConnTimeout = 50 * time.Millisecond

// retune replaces the client's current transport with a clone that apply
// changes, before any request has run, and closes the replaced transport's
// idle connections, of which there are none yet.
func retune(client *Client, apply func(next *http.Transport)) {
	client.swap.Update(func(next *http.Transport) bool {
		apply(next)

		return true
	}).CloseIdleConnections()
}

// awaitResults receives count request results from results, failing the test
// when they do not all arrive within 5 seconds or when any is an error.
func awaitResults(t *testing.T, results <-chan error, count int) {
	t.Helper()

	received := make(chan struct{})
	errs := make([]error, 0, count)

	go func() {
		defer close(received)

		for range count {
			errs = append(errs, <-results)
		}
	}()

	awaitReturn(t, received, "the requests whose connections went idle")

	for _, err := range errs {
		if err != nil {
			t.Errorf("request: %v", err)
		}
	}
}

// The two tests below let net/http itself close an idle connection from a
// reentrant dial function, once by the idle timer and once by MaxIdleConns
// eviction. net/http calls the connection's Close while holding the
// transport's idle lock in both cases. The Close calls SetTimeout,
// SetKeepAlive, and both pool setters, and SetKeepAlive retires that same
// transport, so it deadlocks if SetKeepAlive waits to close the retired
// transport's idle connections. Each test then waits for the background
// close and checks that it closed every connection.

// TestNetHTTPIdleTimer_ReentrantSetKeepAliveDoesNotDeadlock covers the idle
// timer expiring the connection.
func TestNetHTTPIdleTimer_ReentrantSetKeepAliveDoesNotDeadlock(t *testing.T) {
	t.Parallel()

	srv, states := connStateServer(t, nil, nil)
	reenter := newReentryFor(t, srv.URL)

	retune(reenter.client, func(next *http.Transport) { next.IdleConnTimeout = shortIdleConnTimeout })

	reenter.armed.Store(true)

	_, err := reenter.client.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("client Do: %v", err)
	}

	awaitReturn(t, reenter.finished, "the reentrant SDK calls from the expired connection's Close")
	awaitRetiring(t, reenter.client)
	waitForConnState(t, states, http.StateClosed)
	reenter.checkApplied(t, true)
}

// TestNetHTTPEviction_ReentrantSetKeepAliveDoesNotDeadlock covers a
// connection returning to the pool evicting the oldest one past MaxIdleConns.
func TestNetHTTPEviction_ReentrantSetKeepAliveDoesNotDeadlock(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{}, 2)
	release := make(chan struct{})

	srv, states := connStateServer(t, entered, release)
	reenter := newReentryFor(t, srv.URL)

	// Two connections fit per host but only one fits in the whole pool,
	// so the second connection to go idle evicts the first.
	retune(reenter.client, func(next *http.Transport) {
		next.MaxIdleConns = 1
		next.MaxIdleConnsPerHost = 2
	})

	done := make(chan error, 2)

	for range 2 {
		go func() {
			_, err := reenter.client.Do("GET", "/slow", nil)
			done <- err
		}()
	}

	for range 2 {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("the two slow requests never both reached the server")
		}
	}

	reenter.armed.Store(true)
	close(release)

	awaitReturn(t, reenter.finished, "the reentrant SDK calls from the evicted connection's Close")

	awaitResults(t, done, 2)
	awaitRetiring(t, reenter.client)

	// The evicted connection closes first, and the background close of the
	// retired transport then closes the one left idle on it.
	waitForConnState(t, states, http.StateClosed)
	waitForConnState(t, states, http.StateClosed)
	reenter.checkApplied(t, true)
}
