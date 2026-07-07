package websocket_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	pkgws "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/websocket"
)

// newWSServer returns an httptest server that upgrades to WebSocket, sends
// a JSON event text frame, then stays open until the client closes.
func newWSServer(t *testing.T, frameJSON string) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(respWriter http.ResponseWriter, req *http.Request) {
		conn, err := wsUpgrader.Upgrade(respWriter, req, nil)
		if err != nil {
			return
		}

		defer func() { _ = conn.Close() }()

		if frameJSON != "" {
			_ = conn.WriteMessage(websocket.TextMessage, []byte(frameJSON))
		}

		// Keep the connection open for up to 2 s or until client disconnects.
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

		for {
			_, _, readErr := conn.ReadMessage()
			if readErr != nil {
				break
			}
		}
	}))

	return srv
}

// newPingCountingWSServer upgrades to WebSocket and increments *pings for every
// ping frame received from the client, replying with the matching pong.
func newPingCountingWSServer(t *testing.T, pings *int32) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(respWriter http.ResponseWriter, req *http.Request) {
		conn, upErr := wsUpgrader.Upgrade(respWriter, req, nil)
		if upErr != nil {
			return
		}

		defer func() { _ = conn.Close() }()

		conn.SetPingHandler(func(appData string) error {
			atomic.AddInt32(pings, 1)

			return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(time.Second))
		})

		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))

		for {
			_, _, readErr := conn.ReadMessage()
			if readErr != nil {
				return
			}
		}
	}))
}

// wsClientURL builds a ws:// URL from an httptest.Server using the fixed /ws path.
func wsClientURL(srv *httptest.Server) string {
	return "ws://" + srv.Listener.Addr().String() + "/ws"
}

// newClient builds a pkgws.Client connected to addr (ws://...).
func newClient(t *testing.T, addr string) *pkgws.Client {
	t.Helper()

	cfg := pkgws.DefaultConfig()
	cfg.Host = testLocalhost

	// Parse port from addr "ws://host:port/path"
	noScheme := strings.TrimPrefix(addr, "ws://")

	colonIdx := strings.LastIndex(noScheme, ":")
	if colonIdx >= 0 {
		portPath := noScheme[colonIdx+1:]
		slashIdx := strings.Index(portPath, "/")

		var portStr string
		if slashIdx >= 0 {
			portStr = portPath[:slashIdx]
			cfg.Path = portPath[slashIdx:]
		} else {
			portStr = portPath
		}

		var port int

		_, scanErr := fmt.Sscanf(portStr, "%d", &port)
		if scanErr == nil {
			cfg.Port = port
		}
	}

	cfg.Secure = false
	cfg.PingInterval = 0 // disable ping for tests
	cfg.HandshakeTimeout = 2 * time.Second
	cfg.ReadTimeout = 2 * time.Second
	cfg.WriteTimeout = 2 * time.Second
	cfg.MaxReconnectAttempts = 1
	cfg.ReconnectInterval = 10 * time.Millisecond

	wsClient, err := pkgws.New(cfg)
	if err != nil {
		t.Fatalf("pkgws.New: %v", err)
	}

	return wsClient
}

// --- DefaultConfig / New ---

func TestDefaultConfig_NonNil(t *testing.T) {
	t.Parallel()

	cfg := pkgws.DefaultConfig()
	if cfg == nil {
		t.Fatal("DefaultConfig returned nil")
	}

	if cfg.Port <= 0 {
		t.Errorf("port should be > 0, got %d", cfg.Port)
	}
}

func TestNew_RequiresHost(t *testing.T) {
	t.Parallel()

	cfg := pkgws.DefaultConfig()
	cfg.Host = ""

	_, err := pkgws.New(cfg)
	if err == nil {
		t.Fatal("expected error when host is empty")
	}
}

func TestNew_NilConfigUsesDefault(t *testing.T) {
	t.Parallel()

	// nil config should get defaulted, but host is empty → error.
	_, err := pkgws.New(nil)
	if err == nil {
		t.Fatal("expected error for nil config (host empty)")
	}
}

func TestNew_ValidConfig(t *testing.T) {
	t.Parallel()

	cfg := pkgws.DefaultConfig()
	cfg.Host = testHost

	wsClient, err := pkgws.New(cfg)
	if err != nil {
		t.Fatalf("pkgws.New: %v", err)
	}

	if wsClient == nil {
		t.Fatal("expected non-nil Client")
	}
}

// --- SetHeaders / SetAuth ---

func TestSetHeaders(t *testing.T) {
	t.Parallel()

	cfg := pkgws.DefaultConfig()
	cfg.Host = testHost

	wsClient, _ := pkgws.New(cfg)
	headers := http.Header{}
	headers.Set("X-Custom", "value")
	wsClient.SetHeaders(headers) // must not panic
}

func TestSetAuth(t *testing.T) {
	t.Parallel()

	cfg := pkgws.DefaultConfig()
	cfg.Host = testHost

	wsClient, _ := pkgws.New(cfg)
	wsClient.SetAuth("ticket-value", "csrf-value") // must not panic
}

func TestSetAuth_EmptyValues(t *testing.T) {
	t.Parallel()

	cfg := pkgws.DefaultConfig()
	cfg.Host = testHost

	wsClient, _ := pkgws.New(cfg)
	wsClient.SetAuth("", "") // must not panic
}

// --- Connect / Disconnect / IsConnected ---

func TestConnect_HappyPath(t *testing.T) {
	t.Parallel()

	srv := newWSServer(t, "")
	defer srv.Close()

	wsClient := newClient(t, wsClientURL(srv))

	err := wsClient.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if !wsClient.IsConnected() {
		t.Error("IsConnected should be true after Connect")
	}

	err = wsClient.Disconnect()
	if err != nil {
		t.Errorf("Disconnect: %v", err)
	}
}

func TestConnect_AlreadyConnected(t *testing.T) {
	t.Parallel()

	srv := newWSServer(t, "")
	defer srv.Close()

	wsClient := newClient(t, wsClientURL(srv))

	err := wsClient.Connect(context.Background())
	if err != nil {
		t.Fatalf("first Connect: %v", err)
	}

	defer wsClient.Disconnect() //nolint:errcheck

	err = wsClient.Connect(context.Background())
	if err == nil {
		t.Error("second Connect should return errAlreadyConnected")
	}
}

func TestDisconnect_NotConnected(t *testing.T) {
	t.Parallel()

	cfg := pkgws.DefaultConfig()
	cfg.Host = testHost

	wsClient, _ := pkgws.New(cfg)

	// Disconnect without connecting is a no-op.
	err := wsClient.Disconnect()
	if err != nil {
		t.Errorf("Disconnect on unconnected client: %v", err)
	}
}

func TestIsConnected_False_BeforeConnect(t *testing.T) {
	t.Parallel()

	cfg := pkgws.DefaultConfig()
	cfg.Host = testHost

	wsClient, _ := pkgws.New(cfg)

	if wsClient.IsConnected() {
		t.Error("IsConnected should be false before Connect")
	}
}

// --- ConnectWithRetry ---

func TestConnectWithRetry_SuccessOnFirstAttempt(t *testing.T) {
	t.Parallel()

	srv := newWSServer(t, "")
	defer srv.Close()

	wsClient := newClient(t, wsClientURL(srv))

	err := wsClient.ConnectWithRetry(context.Background())
	if err != nil {
		t.Fatalf("ConnectWithRetry: %v", err)
	}

	defer wsClient.Disconnect() //nolint:errcheck
}

func TestConnectWithRetry_FailsWhenRefused(t *testing.T) {
	t.Parallel()

	cfg := pkgws.DefaultConfig()
	cfg.Host = testLocalhost
	cfg.Port = 1 // always refused
	cfg.Secure = false
	cfg.MaxReconnectAttempts = 1
	cfg.ReconnectInterval = 1 * time.Millisecond
	cfg.HandshakeTimeout = 50 * time.Millisecond

	wsClient, _ := pkgws.New(cfg)

	err := wsClient.ConnectWithRetry(context.Background())
	if err == nil {
		t.Error("expected error when connection refused")
	}
}

// --- Event handlers: On / OnAll / Off ---

func TestOn_ReceivesEvent(t *testing.T) {
	t.Parallel()

	eventJSON := `{"type":"node.update","id":"ev1","resource":"node/pve1","action":"update","status":"ok"}`

	srv := newWSServer(t, eventJSON)
	defer srv.Close()

	wsClient := newClient(t, wsClientURL(srv))

	received := make(chan *pkgws.Event, 1)

	wsClient.On("node.update", func(wsEvent *pkgws.Event) {
		received <- wsEvent
	})

	err := wsClient.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	defer wsClient.Disconnect() //nolint:errcheck

	select {
	case wsEvent := <-received:
		if wsEvent.Type != "node.update" {
			t.Errorf("event type: want node.update, got %q", wsEvent.Type)
		}
	case <-time.After(2 * time.Second):
		t.Error("timeout waiting for event")
	}
}

func TestOnAll_ReceivesAllEvents(t *testing.T) {
	t.Parallel()

	eventJSON := `{"type":"storage.update","id":"ev2"}`

	srv := newWSServer(t, eventJSON)
	defer srv.Close()

	wsClient := newClient(t, wsClientURL(srv))

	var count int32

	wsClient.OnAll(func(_ *pkgws.Event) {
		atomic.AddInt32(&count, 1)
	})

	err := wsClient.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	defer wsClient.Disconnect() //nolint:errcheck

	// Wait for at least one event.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&count) > 0 {
			break
		}

		time.Sleep(10 * time.Millisecond)
	}

	if atomic.LoadInt32(&count) == 0 {
		t.Error("OnAll handler never called")
	}
}

func TestOff_RemovesHandler(t *testing.T) {
	t.Parallel()

	cfg := pkgws.DefaultConfig()
	cfg.Host = testHost

	wsClient, _ := pkgws.New(cfg)
	wsClient.On("test.event", func(_ *pkgws.Event) {})
	wsClient.Off("test.event") // must not panic
}

// --- Send / SendText ---

func TestSend_NotConnected(t *testing.T) {
	t.Parallel()

	cfg := pkgws.DefaultConfig()
	cfg.Host = testHost

	wsClient, _ := pkgws.New(cfg)

	err := wsClient.Send(map[string]string{"key": "value"})
	if err == nil {
		t.Error("Send without connection should error")
	}
}

func TestSendText_NotConnected(t *testing.T) {
	t.Parallel()

	cfg := pkgws.DefaultConfig()
	cfg.Host = testHost

	wsClient, _ := pkgws.New(cfg)

	err := wsClient.SendText("hello")
	if err == nil {
		t.Error("SendText without connection should error")
	}
}

func TestSend_HappyPath(t *testing.T) {
	t.Parallel()

	srv := newWSServer(t, "")
	defer srv.Close()

	wsClient := newClient(t, wsClientURL(srv))

	err := wsClient.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	defer wsClient.Disconnect() //nolint:errcheck

	sendErr := wsClient.Send(map[string]string{"action": "ping"})
	if sendErr != nil {
		t.Errorf("Send: %v", sendErr)
	}
}

func TestSendText_HappyPath(t *testing.T) {
	t.Parallel()

	srv := newWSServer(t, "")
	defer srv.Close()

	wsClient := newClient(t, wsClientURL(srv))

	err := wsClient.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	defer wsClient.Disconnect() //nolint:errcheck

	err = wsClient.SendText("hello")
	if err != nil {
		t.Errorf("SendText: %v", err)
	}
}

// --- Errors channel ---

func TestErrors_ChannelReturnedNonNil(t *testing.T) {
	t.Parallel()

	cfg := pkgws.DefaultConfig()
	cfg.Host = testHost

	wsClient, _ := pkgws.New(cfg)

	ch := wsClient.Errors()
	if ch == nil {
		t.Error("Errors() should return non-nil channel")
	}
}

// --- Subscription ---

func TestNewSubscription_Cancel(t *testing.T) {
	t.Parallel()

	cfg := pkgws.DefaultConfig()
	cfg.Host = testHost

	wsClient, _ := pkgws.New(cfg)

	sub := wsClient.NewSubscription("test.event", func(_ *pkgws.Event) {})
	sub.Cancel() // must not panic
}

// --- Stream ---

func TestNewStream_Events(t *testing.T) {
	t.Parallel()

	eventJSON := `{"type":"vm.update","id":"s1"}`

	srv := newWSServer(t, eventJSON)
	defer srv.Close()

	wsClient := newClient(t, wsClientURL(srv))

	stream := wsClient.NewStream(10)
	defer stream.Stop()

	err := wsClient.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	defer wsClient.Disconnect() //nolint:errcheck

	select {
	case wsEvent, ok := <-stream.Events():
		if !ok {
			t.Error("stream closed prematurely")
		}

		if wsEvent.Type != "vm.update" {
			t.Errorf("event type: want vm.update, got %q", wsEvent.Type)
		}
	case <-time.After(2 * time.Second):
		t.Error("timeout waiting for stream event")
	}
}

func TestNewStream_DefaultBuffer(t *testing.T) {
	t.Parallel()

	cfg := pkgws.DefaultConfig()
	cfg.Host = testHost

	wsClient, _ := pkgws.New(cfg)

	stream := wsClient.NewStream(0) // 0 → defaults to 100
	defer stream.Stop()

	if stream.Events() == nil {
		t.Error("Events() channel is nil")
	}
}

// --- raw / malformed message fallback ---

func TestHandleMessage_RawFallback(t *testing.T) {
	t.Parallel()

	// Send a non-JSON payload; client should fall back to raw event type.
	rawSrv := httptest.NewServer(http.HandlerFunc(func(respWriter http.ResponseWriter, req *http.Request) {
		conn, err := wsUpgrader.Upgrade(respWriter, req, nil)
		if err != nil {
			return
		}

		defer func() { _ = conn.Close() }()

		_ = conn.WriteMessage(websocket.TextMessage, []byte("not-json"))

		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		for {
			_, _, readErr := conn.ReadMessage()
			if readErr != nil {
				break
			}
		}
	}))
	defer rawSrv.Close()

	wsClient := newClient(t, "ws://"+rawSrv.Listener.Addr().String()+"/ws")

	rawReceived := make(chan *pkgws.Event, 1)

	wsClient.On("raw", func(wsEvent *pkgws.Event) {
		rawReceived <- wsEvent
	})

	err := wsClient.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	defer wsClient.Disconnect() //nolint:errcheck

	select {
	case wsEvent := <-rawReceived:
		if wsEvent.Type != "raw" {
			t.Errorf("expected raw event, got %q", wsEvent.Type)
		}
	case <-time.After(2 * time.Second):
		t.Error("timeout waiting for raw event")
	}
}

// --- Ping/pong keepalive ---

func TestConnect_WithPingEnabled(t *testing.T) {
	t.Parallel()

	pings := new(int32)

	// Server that counts client ping frames so the test can assert pings
	// actually fired rather than just sleeping.
	srv := newPingCountingWSServer(t, pings)
	defer srv.Close()

	cfg := pkgws.DefaultConfig()
	cfg.Host = testLocalhost

	host, port := hostPort(srv)
	cfg.Host = host
	cfg.Port = port
	cfg.Secure = false
	cfg.Path = "/ws"
	cfg.PingInterval = 50 * time.Millisecond
	cfg.HandshakeTimeout = 2 * time.Second
	cfg.ReadTimeout = 500 * time.Millisecond
	cfg.WriteTimeout = 500 * time.Millisecond
	cfg.MaxReconnectAttempts = 1
	cfg.ReconnectInterval = 10 * time.Millisecond

	wsClient, err := pkgws.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	connectErr := wsClient.Connect(context.Background())
	if connectErr != nil {
		t.Fatalf("Connect: %v", connectErr)
	}

	// Poll until at least one ping is observed, up to a generous deadline, so
	// the test is not tied to a fixed sleep duration.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(pings) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if got := atomic.LoadInt32(pings); got == 0 {
		t.Error("expected at least one ping frame to be sent, got none")
	}

	disconnectErr := wsClient.Disconnect()
	if disconnectErr != nil {
		t.Errorf("Disconnect: %v", disconnectErr)
	}
}

// --- Reconnect on server close ---

func TestConnectWithRetry_ContextCancelled(t *testing.T) {
	t.Parallel()

	cfg := pkgws.DefaultConfig()
	cfg.Host = testLocalhost
	cfg.Port = 1 // always refused
	cfg.Secure = false
	cfg.MaxReconnectAttempts = 10
	cfg.ReconnectInterval = 200 * time.Millisecond
	cfg.HandshakeTimeout = 50 * time.Millisecond

	wsClient, _ := pkgws.New(cfg)

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	err := wsClient.ConnectWithRetry(ctx)
	if err == nil {
		t.Error("expected error when context cancelled during retry")
	}
}

// --- Bounded handler concurrency ---

// concurrencyTracker records, via a handler built by trackingHandler, how
// many invocations are running at once (current/peak) and how many have
// finished (done).
type concurrencyTracker struct {
	current int32
	peak    int32
	done    int32
}

// trackingHandler returns an EventHandler that updates ct's counters and
// sleeps briefly to widen the window in which concurrent invocations overlap.
func (ct *concurrencyTracker) trackingHandler() pkgws.EventHandler {
	return func(_ *pkgws.Event) {
		n := atomic.AddInt32(&ct.current, 1)

		for {
			p := atomic.LoadInt32(&ct.peak)
			if n <= p || atomic.CompareAndSwapInt32(&ct.peak, p, n) {
				break
			}
		}

		time.Sleep(20 * time.Millisecond)

		atomic.AddInt32(&ct.current, -1)
		atomic.AddInt32(&ct.done, 1)
	}
}

// burstClientConfig builds a Config pointed at srv with the given
// MaxConcurrentHandlers, suitable for TestHandleMessage_BoundsConcurrentHandlers.
func burstClientConfig(srv *httptest.Server, maxConcurrent int) *pkgws.Config {
	cfg := pkgws.DefaultConfig()
	cfg.PingInterval = 0
	cfg.MaxConcurrentHandlers = maxConcurrent

	host, port := hostPort(srv)
	cfg.Host = host
	cfg.Port = port
	cfg.Secure = false
	cfg.Path = "/ws"
	cfg.HandshakeTimeout = 2 * time.Second
	cfg.ReadTimeout = 2 * time.Second
	cfg.WriteTimeout = 2 * time.Second
	cfg.MaxReconnectAttempts = 1
	cfg.ReconnectInterval = 10 * time.Millisecond

	return cfg
}

// TestHandleMessage_BoundsConcurrentHandlers verifies MaxConcurrentHandlers
// caps how many handler goroutines run at once: previously handleMessage
// spawned `go handler(&event)` per handler per message with no bound at all.
func TestHandleMessage_BoundsConcurrentHandlers(t *testing.T) {
	t.Parallel()

	const (
		maxConcurrent = 2
		numEvents     = 20
	)

	srv := newWSServer(t, `{"type":"burst","id":"ev"}`)
	defer srv.Close()

	wsClient, err := pkgws.New(burstClientConfig(srv, maxConcurrent))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var tracker concurrencyTracker

	// Register numEvents handlers for the same event type so the single
	// incoming "burst" frame fans out to numEvents concurrent dispatches,
	// which is exactly what MaxConcurrentHandlers must cap.
	for range numEvents {
		wsClient.On("burst", tracker.trackingHandler())
	}

	connectErr := wsClient.Connect(context.Background())
	if connectErr != nil {
		t.Fatalf("Connect: %v", connectErr)
	}

	defer wsClient.Disconnect() //nolint:errcheck

	deadline := time.Now().Add(3 * time.Second)
	for atomic.LoadInt32(&tracker.done) < numEvents && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}

	if got := atomic.LoadInt32(&tracker.done); got != numEvents {
		t.Fatalf("expected all %d handlers to run, got %d", numEvents, got)
	}

	if got := atomic.LoadInt32(&tracker.peak); got > maxConcurrent {
		t.Errorf("expected at most %d concurrently-running handlers, observed peak of %d", maxConcurrent, got)
	}
}

// TestStream_StopIdempotent verifies that Stop can be called repeatedly and
// concurrently without panicking. Previously Stop closed stopChan (and
// eventChan) unconditionally, so a second call panicked on a double close.
func TestStream_StopIdempotent(t *testing.T) {
	t.Parallel()

	cfg := pkgws.DefaultConfig()
	cfg.Host = testHost

	wsClient, err := pkgws.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	stream := wsClient.NewStream(10)

	const callers = 20

	var waitGroup sync.WaitGroup

	waitGroup.Add(callers)

	for range callers {
		go func() {
			defer waitGroup.Done()

			stream.Stop()
		}()
	}

	waitGroup.Wait()

	// A further direct call must also be a no-op.
	stream.Stop()
}
