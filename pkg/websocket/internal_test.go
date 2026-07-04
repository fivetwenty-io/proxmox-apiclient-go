package websocket

// Whitebox tests: package websocket (not websocket_test), so these reach
// into unexported fields (dialer, conn, handlers, reconnect) to reproduce
// and verify fixes for bugs that only manifest at that level:
//   - Connect() publishing c.conn before deadlines are configured.
//   - readLoop busy-spinning on repeated SetReadDeadline failures.
//   - Subscription.Cancel not removing its handler from the handlers map.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

var errForcedDeadlineFailure = errors.New("forced deadline failure for test")

// failingDeadlineConn wraps a real net.Conn so tests can force
// SetReadDeadline to fail after a fixed number of leading successful calls.
// gorilla's handshake uses SetDeadline (not SetReadDeadline/SetWriteDeadline),
// so it is unaffected by this override and the WebSocket upgrade always
// completes normally.
type failingDeadlineConn struct {
	net.Conn

	succeedCalls int32 // number of leading calls that succeed; the rest fail
	readCalls    atomic.Int32
}

func (f *failingDeadlineConn) SetReadDeadline(deadline time.Time) error {
	n := f.readCalls.Add(1)
	if n <= f.succeedCalls {
		err := f.Conn.SetReadDeadline(deadline)
		if err != nil {
			return fmt.Errorf("underlying SetReadDeadline: %w", err)
		}

		return nil
	}

	return errForcedDeadlineFailure
}

// newFailingReadDeadlineDialer returns a websocket.Dialer.NetDialContext
// function that dials a real connection and wraps it in a
// failingDeadlineConn configured to let the first succeedCalls
// SetReadDeadline calls through before failing every call after that. If out
// is non-nil, the wrapped conn is stored there for later inspection.
func newFailingReadDeadlineDialer(
	succeedCalls int32, out **failingDeadlineConn,
) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		raw, dialErr := (&net.Dialer{}).DialContext(ctx, network, addr)
		if dialErr != nil {
			return nil, fmt.Errorf("dial: %w", dialErr)
		}

		wrapped := &failingDeadlineConn{Conn: raw, succeedCalls: succeedCalls}
		if out != nil {
			*out = wrapped
		}

		return wrapped, nil
	}
}

// newInternalWSServer starts a plain WebSocket echo-less server: it upgrades
// and reads until the client disconnects, without ever writing a frame
// (so a client's ReadMessage call blocks until deadline/close, giving tests
// full control over timing via the configured ReadTimeout).
func newInternalWSServer(t *testing.T) *httptest.Server {
	t.Helper()

	upgrader := websocket.Upgrader{CheckOrigin: func(_ *http.Request) bool { return true }}

	return httptest.NewServer(http.HandlerFunc(func(respWriter http.ResponseWriter, req *http.Request) {
		conn, err := upgrader.Upgrade(respWriter, req, nil)
		if err != nil {
			return
		}

		defer func() { _ = conn.Close() }()

		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))

		for {
			_, _, readErr := conn.ReadMessage()
			if readErr != nil {
				return
			}
		}
	}))
}

// testConfigFor builds a Config pointed at srv with sane, fast test timeouts.
func testConfigFor(t *testing.T, srv *httptest.Server) *Config {
	t.Helper()

	host, portStr, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("Atoi(%q): %v", portStr, err)
	}

	cfg := DefaultConfig()
	cfg.Host = host
	cfg.Port = port
	cfg.Secure = false
	cfg.Path = "/ws"
	cfg.PingInterval = 0
	cfg.ReadTimeout = 5 * time.Second
	cfg.WriteTimeout = 2 * time.Second
	cfg.HandshakeTimeout = 2 * time.Second
	cfg.MaxReconnectAttempts = 1
	cfg.ReconnectInterval = 10 * time.Millisecond

	return cfg
}

// newTestClient builds a Client (via New) pointed at srv, failing the test on error.
func newTestClient(t *testing.T, srv *httptest.Server) *Client {
	t.Helper()

	client, err := New(testConfigFor(t, srv))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return client
}

// TestConnect_DeadlineFailureLeavesClientReconnectable is a regression test
// for a bug where Connect() assigned c.conn before configuring read/write
// deadlines: when SetReadDeadline failed, Connect returned an error but left
// c.conn non-nil, so every later Connect() call failed forever with
// errAlreadyConnected even though nothing was actually connected.
func TestConnect_DeadlineFailureLeavesClientReconnectable(t *testing.T) {
	t.Parallel()

	srv := newInternalWSServer(t)
	defer srv.Close()

	client := newTestClient(t, srv)

	assertFirstConnectFailsWithoutLeakingState(t, client)
	assertRetryConnectSucceeds(t, client)
}

// assertFirstConnectFailsWithoutLeakingState dials with a conn that fails
// every SetReadDeadline call, so Connect() must fail without ever
// publishing c.conn.
func assertFirstConnectFailsWithoutLeakingState(t *testing.T, client *Client) {
	t.Helper()

	client.dialer.NetDialContext = newFailingReadDeadlineDialer(0, nil)

	firstErr := client.Connect(context.Background())
	if firstErr == nil {
		t.Fatal("expected Connect to fail when SetReadDeadline fails")
	}

	if client.IsConnected() {
		t.Fatal("client must not report connected after a deadline-configuration failure")
	}
}

// assertRetryConnectSucceeds dials with a conn that lets every
// SetReadDeadline call through. If the bug were present, c.conn would
// already be non-nil and this would fail with errAlreadyConnected instead
// of actually reconnecting.
func assertRetryConnectSucceeds(t *testing.T, client *Client) {
	t.Helper()

	client.dialer.NetDialContext = newFailingReadDeadlineDialer(1<<30, nil)

	secondErr := client.Connect(context.Background())
	if secondErr != nil {
		t.Fatalf("retry Connect should succeed once the client is reconnectable, got: %v", secondErr)
	}

	defer func() { _ = client.Disconnect() }()

	if !client.IsConnected() {
		t.Error("client should report connected after the successful retry")
	}
}

// TestReadLoop_DeadlineFailureBacksOffAndStops is a regression test for a
// busy-spin bug: when SetReadDeadline failed inside readLoop, the loop
// immediately `continue`d with no backoff, pinning the CPU at 100% if the
// conn stayed open but broken. It must now back off between retries and give
// up (closing the connection) after a bounded number of consecutive
// failures rather than retrying forever.
func TestReadLoop_DeadlineFailureBacksOffAndStops(t *testing.T) {
	t.Parallel()

	srv := newInternalWSServer(t)
	defer srv.Close()

	client := newTestClient(t, srv)

	var wrapped *failingDeadlineConn

	// succeedCalls: 1 lets Connect()'s own SetReadDeadline call through so
	// the handshake and initial connect succeed; every call after that
	// (i.e. every call made from inside readLoop) fails.
	client.dialer.NetDialContext = newFailingReadDeadlineDialer(1, &wrapped)

	connectErr := client.Connect(context.Background())
	if connectErr != nil {
		t.Fatalf("Connect: %v", connectErr)
	}

	// Disable auto-reconnect so only the readLoop under test runs; a fresh
	// reconnect would dial a brand new (unwrapped-by-default) conn and
	// confuse the assertions below.
	client.mu.Lock()
	client.reconnect = false
	client.mu.Unlock()

	elapsed := waitUntilDisconnected(t, client, 2*time.Second)

	assertDeadlineFailureCounts(t, wrapped, elapsed)
}

// waitUntilDisconnected polls client.IsConnected until it goes false or
// timeout elapses, returning the wall-clock time spent waiting. It fails the
// test if the client is still connected once timeout elapses.
func waitUntilDisconnected(t *testing.T, client *Client, timeout time.Duration) time.Duration {
	t.Helper()

	start := time.Now()

	deadline := start.Add(timeout)
	for time.Now().Before(deadline) {
		if !client.IsConnected() {
			break
		}

		time.Sleep(5 * time.Millisecond)
	}

	elapsed := time.Since(start)

	if client.IsConnected() {
		t.Fatal("expected readLoop to give up and disconnect after repeated deadline failures")
	}

	return elapsed
}

// assertDeadlineFailureCounts checks that readLoop made exactly the expected
// number of SetReadDeadline calls before giving up, and that it took at
// least as long as the expected number of backoff sleeps, confirming it
// backed off instead of busy-spinning.
func assertDeadlineFailureCounts(t *testing.T, wrapped *failingDeadlineConn, elapsed time.Duration) {
	t.Helper()

	// 1 successful call from Connect() itself, plus maxConsecutiveDeadlineFailures
	// failing calls from readLoop before it gives up.
	const wantCalls = 1 + maxConsecutiveDeadlineFailures

	calls := wrapped.readCalls.Load()
	if calls != wantCalls {
		t.Errorf("expected exactly %d SetReadDeadline calls before giving up, got %d", wantCalls, calls)
	}

	// Between the maxConsecutiveDeadlineFailures failing calls there are
	// (maxConsecutiveDeadlineFailures - 1) backoff sleeps; the loop must not
	// have returned faster than that.
	minElapsed := time.Duration(maxConsecutiveDeadlineFailures-1) * readDeadlineFailureBackoff
	if elapsed < minElapsed {
		t.Errorf("expected backoff between retries (elapsed >= %v), got %v; readLoop may be busy-spinning",
			minElapsed, elapsed)
	}
}

// TestSubscriptionCancel_RemovesHandlerFromMap is a regression test for
// Subscription.Cancel not deregistering its handler: previously Cancel only
// cancelled the wrapper's context, leaving the closure in c.handlers forever
// so long-lived clients accumulated dead handlers without bound.
func TestSubscriptionCancel_RemovesHandlerFromMap(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.Host = "127.0.0.1"

	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sub := client.NewSubscription("test.event", func(_ *Event) {})

	if before := handlerCount(client, "test.event"); before != 1 {
		t.Fatalf("expected 1 registered handler before Cancel, got %d", before)
	}

	sub.Cancel()

	if after := handlerCount(client, "test.event"); after != 0 {
		t.Errorf("expected handler to be removed from map after Cancel, got %d remaining", after)
	}

	// Cancel must remain safe to call more than once.
	sub.Cancel()
}

// TestSubscriptionCancel_OnlyRemovesOwnHandler verifies removeHandler matches
// by handler ID, not just event type, so cancelling one subscription never
// removes a sibling subscription registered for the same event type.
func TestSubscriptionCancel_OnlyRemovesOwnHandler(t *testing.T) {
	t.Parallel()

	cfg := DefaultConfig()
	cfg.Host = "127.0.0.1"

	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sub1 := client.NewSubscription("multi", func(_ *Event) {})
	sub2 := client.NewSubscription("multi", func(_ *Event) {})

	sub1.Cancel()

	if remaining := handlerCount(client, "multi"); remaining != 1 {
		t.Errorf("expected 1 handler remaining after cancelling one of two subscriptions, got %d", remaining)
	}

	sub2.Cancel()

	if remaining := handlerCount(client, "multi"); remaining != 0 {
		t.Errorf("expected 0 handlers remaining after cancelling both subscriptions, got %d", remaining)
	}
}

// handlerCount returns the number of handlers registered for eventType.
func handlerCount(client *Client, eventType string) int {
	client.mu.RLock()
	defer client.mu.RUnlock()

	return len(client.handlers[eventType])
}
