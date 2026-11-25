package websocket_test

// Behavior tests for the WebSocket client side of this package. Every server
// here is built with github.com/coder/websocket, and every assertion reads
// only what this package exposes or what the server observed on the wire, so
// the tests hold whichever WebSocket library the client is built on.

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	issl "github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/ssl"
	pkgws "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/websocket"
)

// Message type values, which are the WebSocket opcodes for text and binary
// data frames.
const (
	behaviorText   = int(websocket.MessageText)
	behaviorBinary = int(websocket.MessageBinary)
)

// largeMessageBytes is well above the 32 KiB read limit coder/websocket
// applies by default, so a round trip of this size shows that no limit cuts
// a console stream short.
const largeMessageBytes = 1 << 20

// behaviorPath is the path the event client dials. The test servers answer
// on every path.
const behaviorPath = "/behavior"

// serverReadWindow bounds how long a test server reads before it gives up.
const serverReadWindow = 5 * time.Second

// acceptWS upgrades a request on a test server and lifts the server's read
// limit, so the server reads messages of any size. It returns nil when the
// upgrade fails.
func acceptWS(respWriter http.ResponseWriter, req *http.Request, opts *websocket.AcceptOptions) *websocket.Conn {
	conn, err := websocket.Accept(respWriter, req, opts)
	if err != nil {
		return nil
	}

	conn.SetReadLimit(-1)

	return conn
}

// newEchoServer returns a server that sends every message it reads back with
// the same type.
func newEchoServer(t *testing.T, tlsServer bool) *httptest.Server {
	t.Helper()

	handler := http.HandlerFunc(func(respWriter http.ResponseWriter, req *http.Request) {
		conn := acceptWS(respWriter, req, nil)
		if conn == nil {
			return
		}

		defer func() { _ = conn.CloseNow() }()

		ctx, cancel := context.WithTimeout(req.Context(), serverReadWindow)
		defer cancel()

		for {
			msgType, data, err := conn.Read(ctx)
			if err != nil {
				return
			}

			err = conn.Write(ctx, msgType, data)
			if err != nil {
				return
			}
		}
	})

	var srv *httptest.Server
	if tlsServer {
		srv = httptest.NewTLSServer(handler)
	} else {
		srv = httptest.NewServer(handler)
	}

	t.Cleanup(srv.Close)

	return srv
}

// receivedMessage is one message a recording server read.
type receivedMessage struct {
	msgType websocket.MessageType
	data    []byte
}

// recordingServer reads every message a client sends and reports each one,
// then the close status the read loop ended with.
type recordingServer struct {
	*httptest.Server

	messages chan receivedMessage
	closes   chan websocket.StatusCode
	headers  chan http.Header
}

// newRecordingServer returns a server that records what the client sends.
// When greeting is not empty, the server sends it as a text message first.
func newRecordingServer(t *testing.T, greeting string, opts *websocket.AcceptOptions) *recordingServer {
	t.Helper()

	rec := &recordingServer{
		messages: make(chan receivedMessage, 1024),
		closes:   make(chan websocket.StatusCode, 16),
		headers:  make(chan http.Header, 16),
	}

	rec.Server = httptest.NewServer(http.HandlerFunc(func(respWriter http.ResponseWriter, req *http.Request) {
		rec.headers <- req.Header.Clone()

		conn := acceptWS(respWriter, req, opts)
		if conn == nil {
			return
		}

		defer func() { _ = conn.CloseNow() }()

		ctx, cancel := context.WithTimeout(req.Context(), serverReadWindow)
		defer cancel()

		if greeting != "" {
			_ = conn.Write(ctx, websocket.MessageText, []byte(greeting))
		}

		for {
			msgType, data, err := conn.Read(ctx)
			if err != nil {
				rec.closes <- websocket.CloseStatus(err)

				return
			}

			rec.messages <- receivedMessage{msgType: msgType, data: data}
		}
	}))
	t.Cleanup(rec.Close)

	return rec
}

// closingServer returns a server that upgrades and then ends the connection
// with the given close code, or, when drop is true, closes the TCP connection
// without a close frame.
func closingServer(t *testing.T, code websocket.StatusCode, drop bool) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(respWriter http.ResponseWriter, req *http.Request) {
		conn := acceptWS(respWriter, req, nil)
		if conn == nil {
			return
		}

		if drop {
			_ = conn.CloseNow()

			return
		}

		_ = conn.Close(code, "bye")
	}))
	t.Cleanup(srv.Close)

	return srv
}

// behaviorConfig builds an event client Config pointed at srv, with fast
// timeouts and pings turned off.
func behaviorConfig(srv *httptest.Server) *pkgws.Config {
	host, port := hostPort(srv)

	cfg := pkgws.DefaultConfig()
	cfg.Host = host
	cfg.Port = port
	cfg.Secure = false
	cfg.Path = behaviorPath
	cfg.PingInterval = 0
	cfg.HandshakeTimeout = 2 * time.Second
	cfg.ReadTimeout = 3 * time.Second
	cfg.WriteTimeout = 2 * time.Second
	cfg.MaxReconnectAttempts = 1
	cfg.ReconnectInterval = 10 * time.Millisecond

	return cfg
}

// newBehaviorClient builds an event client from cfg and disconnects it when
// the test ends.
func newBehaviorClient(t *testing.T, cfg *pkgws.Config) *pkgws.Client {
	t.Helper()

	client, err := pkgws.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	t.Cleanup(func() { _ = client.Disconnect() })

	return client
}

// collectErrors gathers what the client reports on its error channel for the
// given window.
func collectErrors(client *pkgws.Client, window time.Duration) []error {
	var errs []error

	timer := time.NewTimer(window)
	defer timer.Stop()

	for {
		select {
		case err := <-client.Errors():
			errs = append(errs, err)
		case <-timer.C:
			return errs
		}
	}
}

// waitForError returns every error the client reports until one whose text
// starts with prefix arrives, and that error, which is nil when none arrives
// within window.
func waitForError(client *pkgws.Client, prefix string, window time.Duration) ([]error, error) {
	var seen []error

	timer := time.NewTimer(window)
	defer timer.Stop()

	for {
		select {
		case err := <-client.Errors():
			seen = append(seen, err)

			if strings.HasPrefix(err.Error(), prefix) {
				return seen, err
			}
		case <-timer.C:
			return seen, nil
		}
	}
}

// escapeHTML writes each of <, >, and & as the \u escape encoding/json uses.
func escapeHTML(text string) string {
	return strings.NewReplacer("<", `\u003c`, ">", `\u003e`, "&", `\u0026`).Replace(text)
}

// nextMessage returns the next message a recording server read.
func nextMessage(t *testing.T, rec *recordingServer) receivedMessage {
	t.Helper()

	select {
	case msg := <-rec.messages:
		return msg
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for the server to read a message")
	}

	return receivedMessage{}
}

// openProxyConn opens a proxy session to srv through the tunnel endpoint,
// which needs no VNC port, and closes it when the test ends.
func openProxyConn(t *testing.T, srv *httptest.Server) *pkgws.Conn {
	t.Helper()

	conn, err := proxyClientFor(t, srv).VMTunnelWebSocket(
		context.Background(), testHost, 100, "/run/test.sock", "ticket",
	)
	if err != nil {
		t.Fatalf("VMTunnelWebSocket: %v", err)
	}

	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

// --- round trips ---

// TestBehavior_ProxyConnRoundTripsTextAndBinary writes a text and a binary
// message through a proxy session and reads each back with its type.
func TestBehavior_ProxyConnRoundTripsTextAndBinary(t *testing.T) {
	t.Parallel()

	conn := openProxyConn(t, newEchoServer(t, false))

	cases := []struct {
		msgType int
		data    []byte
	}{
		{msgType: behaviorText, data: []byte("hello, console")},
		{msgType: behaviorBinary, data: []byte{0x00, 0x01, 0x7f, 0x80, 0xfe, 0xff}},
		{msgType: behaviorBinary, data: []byte{}},
	}

	for _, tc := range cases {
		err := conn.WriteMessage(tc.msgType, tc.data)
		if err != nil {
			t.Fatalf("WriteMessage(%d): %v", tc.msgType, err)
		}

		gotType, gotData, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("ReadMessage: %v", err)
		}

		if gotType != tc.msgType {
			t.Errorf("message type: want %d, got %d", tc.msgType, gotType)
		}

		if !bytes.Equal(gotData, tc.data) {
			t.Errorf("payload: want %x, got %x", tc.data, gotData)
		}
	}
}

// TestBehavior_ProxyConnCarriesMessagesLargerThan32KiB sends a 1 MiB binary
// message each way, as a console stream can.
func TestBehavior_ProxyConnCarriesMessagesLargerThan32KiB(t *testing.T) {
	t.Parallel()

	conn := openProxyConn(t, newEchoServer(t, false))

	payload := make([]byte, largeMessageBytes)
	for i := range payload {
		payload[i] = byte(i % 251)
	}

	err := conn.WriteMessage(behaviorBinary, payload)
	if err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	gotType, gotData, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}

	if gotType != behaviorBinary {
		t.Errorf("message type: want %d, got %d", behaviorBinary, gotType)
	}

	if !bytes.Equal(gotData, payload) {
		t.Errorf("payload of %d bytes came back as %d bytes or changed", len(payload), len(gotData))
	}
}

// TestBehavior_ClientReceivesEventsLargerThan32KiB has the server send one
// event whose JSON is larger than 32 KiB.
func TestBehavior_ClientReceivesEventsLargerThan32KiB(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("x", 2*largeMessageBytes/8)

	event, err := json.Marshal(map[string]interface{}{
		"type": "task.log",
		"id":   "big",
		"data": map[string]string{"line": big},
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	rec := newRecordingServer(t, string(event), nil)
	client := newBehaviorClient(t, behaviorConfig(rec.Server))

	received := make(chan *pkgws.Event, 1)

	client.On("task.log", func(ev *pkgws.Event) { received <- ev })

	err = client.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	select {
	case ev := <-received:
		if got, _ := ev.Data["line"].(string); got != big {
			t.Errorf("event data line holds %d bytes, want %d", len(got), len(big))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for the large event")
	}
}

// TestBehavior_ClientSendFraming checks what reaches the server for Send and
// SendText: Send writes one text message of JSON followed by a newline, as
// encoding/json's Encoder writes it, and SendText writes the text as given.
func TestBehavior_ClientSendFraming(t *testing.T) {
	t.Parallel()

	rec := newRecordingServer(t, "", nil)
	client := newBehaviorClient(t, behaviorConfig(rec.Server))

	err := client.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	err = client.Send(map[string]string{"action": "subscribe", "html": "<b>&"})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	msg := nextMessage(t, rec)
	if msg.msgType != websocket.MessageText {
		t.Errorf("Send message type: want text, got %v", msg.msgType)
	}

	// encoding/json escapes <, >, and & by default, so the payload carries
	// each as a \u escape.
	wantJSON := `{"action":"subscribe","html":"` + escapeHTML("<b>&") + `"}` + "\n"
	if string(msg.data) != wantJSON {
		t.Errorf("Send payload: want %q, got %q", wantJSON, msg.data)
	}

	err = client.SendText("plain text")
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}

	msg = nextMessage(t, rec)
	if msg.msgType != websocket.MessageText || string(msg.data) != "plain text" {
		t.Errorf("SendText: want text %q, got %v %q", "plain text", msg.msgType, msg.data)
	}
}

// TestBehavior_ClientHandshakeHeaders checks the headers the event client
// sends on the upgrade request: the ticket cookie and the CSRF token from
// SetAuth, an Authorization header set through SetHeaders, and no subprotocol
// or compression offer.
func TestBehavior_ClientHandshakeHeaders(t *testing.T) {
	t.Parallel()

	rec := newRecordingServer(t, "", nil)
	client := newBehaviorClient(t, behaviorConfig(rec.Server))

	headers := http.Header{}
	headers.Set("Authorization", "PVEAPIToken=root@pam!ci=SECRETVALUE")
	headers.Set("X-Custom", "custom-value")
	client.SetHeaders(headers)
	client.SetAuth("PVE:root@pam:TICKET", "CSRF-TOKEN")

	err := client.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	var got http.Header
	select {
	case got = <-rec.headers:
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for the upgrade request")
	}

	want := map[string]string{
		"Cookie":              "PVEAuthCookie=PVE:root@pam:TICKET",
		"Csrfpreventiontoken": "CSRF-TOKEN",
		"Authorization":       "PVEAPIToken=root@pam!ci=SECRETVALUE",
		"X-Custom":            "custom-value",
		"Upgrade":             "websocket",
	}
	for name, value := range want {
		if got.Get(name) != value {
			t.Errorf("header %s: want %q, got %q", name, value, got.Get(name))
		}
	}

	for _, name := range []string{"Sec-Websocket-Protocol", "Sec-Websocket-Extensions"} {
		if values := got.Values(name); len(values) != 0 {
			t.Errorf("header %s: want none, got %q", name, values)
		}
	}
}

// TestBehavior_ConnectContextBoundsOnlyTheHandshake cancels the context
// passed to Connect right after Connect returns, and the connection keeps
// working.
func TestBehavior_ConnectContextBoundsOnlyTheHandshake(t *testing.T) {
	t.Parallel()

	rec := newRecordingServer(t, "", nil)
	client := newBehaviorClient(t, behaviorConfig(rec.Server))

	ctx, cancel := context.WithCancel(context.Background())

	err := client.Connect(ctx)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	cancel()
	time.Sleep(50 * time.Millisecond)

	err = client.SendText("after cancel")
	if err != nil {
		t.Fatalf("SendText after the Connect context ended: %v", err)
	}

	if msg := nextMessage(t, rec); string(msg.data) != "after cancel" {
		t.Errorf("server read %q, want %q", msg.data, "after cancel")
	}

	if !client.IsConnected() {
		t.Error("client disconnected when the Connect context ended")
	}
}

// --- ping, pong, and keepalive ---

// TestBehavior_PongsKeepTheConnectionAlive uses a read timeout far shorter
// than the test and a server that never sends a data message. The client's
// pings draw pongs, each pong pushes the read deadline out, and the client
// stays connected without an error.
func TestBehavior_PongsKeepTheConnectionAlive(t *testing.T) {
	t.Parallel()

	var pings atomic.Int32

	rec := newRecordingServer(t, "", &websocket.AcceptOptions{
		OnPingReceived: func(context.Context, []byte) bool {
			pings.Add(1)

			return true
		},
	})

	cfg := behaviorConfig(rec.Server)
	cfg.PingInterval = 50 * time.Millisecond
	cfg.ReadTimeout = 300 * time.Millisecond
	cfg.WriteTimeout = 300 * time.Millisecond

	client := newBehaviorClient(t, cfg)

	err := client.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	errs := collectErrors(client, time.Second)
	if len(errs) != 0 {
		t.Errorf("client reported %v while pongs were arriving", errs)
	}

	if !client.IsConnected() {
		t.Error("client disconnected although every ping drew a pong")
	}

	if got := pings.Load(); got < 5 {
		t.Errorf("server read %d pings in a second at a 50ms interval, want at least 5", got)
	}
}

// TestBehavior_SilentPeerHitsTheReadTimeout turns pings off, so nothing
// pushes the read deadline out. The read times out, which the client treats
// as a disconnect and not as a read error.
func TestBehavior_SilentPeerHitsTheReadTimeout(t *testing.T) {
	t.Parallel()

	rec := newRecordingServer(t, "", nil)

	cfg := behaviorConfig(rec.Server)
	cfg.ReadTimeout = 200 * time.Millisecond

	client := newBehaviorClient(t, cfg)

	err := client.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	seen, found := waitForError(client, "disconnected, attempting to reconnect", 3*time.Second)
	if found == nil {
		t.Fatalf("no disconnect reported after the read timeout; saw %v", seen)
	}

	for _, err := range seen {
		if strings.HasPrefix(err.Error(), "read error:") {
			t.Errorf("read timeout reported as %q, want no read error", err)
		}
	}
}

// TestBehavior_ServerPingsAreAnswered has the server ping the client, and the
// client's read loop answers with a pong.
func TestBehavior_ServerPingsAreAnswered(t *testing.T) {
	t.Parallel()

	pingResult := make(chan error, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(respWriter http.ResponseWriter, req *http.Request) {
		conn := acceptWS(respWriter, req, nil)
		if conn == nil {
			return
		}

		defer func() { _ = conn.CloseNow() }()

		ctx := conn.CloseRead(req.Context())

		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()

		pingResult <- conn.Ping(pingCtx)

		<-ctx.Done()
	}))
	t.Cleanup(srv.Close)

	client := newBehaviorClient(t, behaviorConfig(srv))

	err := client.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	select {
	case err := <-pingResult:
		if err != nil {
			t.Errorf("server ping: %v, want a pong", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for the server ping")
	}
}

// TestBehavior_UnansweredPingsAreNotErrors uses a server that reads pings and
// never answers them. The client keeps pinging and reports nothing, because a
// peer that stops answering is caught by the read timeout.
func TestBehavior_UnansweredPingsAreNotErrors(t *testing.T) {
	t.Parallel()

	var pings atomic.Int32

	rec := newRecordingServer(t, "", &websocket.AcceptOptions{
		OnPingReceived: func(context.Context, []byte) bool {
			pings.Add(1)

			return false
		},
	})

	cfg := behaviorConfig(rec.Server)
	cfg.PingInterval = 50 * time.Millisecond
	cfg.WriteTimeout = 100 * time.Millisecond
	cfg.ReadTimeout = 4 * time.Second

	client := newBehaviorClient(t, cfg)

	err := client.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	errs := collectErrors(client, 800*time.Millisecond)
	if len(errs) != 0 {
		t.Errorf("client reported %v for pings the server did not answer", errs)
	}

	if got := pings.Load(); got < 3 {
		t.Errorf("server read %d pings, want the client to keep pinging", got)
	}

	if !client.IsConnected() {
		t.Error("client disconnected before the read timeout")
	}
}

// --- closing ---

// TestBehavior_DisconnectSendsNormalClosure checks that Disconnect returns nil
// and that the server reads a close frame with status 1000.
func TestBehavior_DisconnectSendsNormalClosure(t *testing.T) {
	t.Parallel()

	rec := newRecordingServer(t, "", nil)

	client, err := pkgws.New(behaviorConfig(rec.Server))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = client.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	err = client.Disconnect()
	if err != nil {
		t.Errorf("Disconnect: %v", err)
	}

	select {
	case code := <-rec.closes:
		if code != websocket.StatusNormalClosure {
			t.Errorf("server read close status %v, want %v", code, websocket.StatusNormalClosure)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for the server to read the close")
	}

	if client.IsConnected() {
		t.Error("IsConnected is true after Disconnect")
	}
}

// TestBehavior_ProxyConnCloseSendsNormalClosure does the same for a proxy
// session.
func TestBehavior_ProxyConnCloseSendsNormalClosure(t *testing.T) {
	t.Parallel()

	rec := newRecordingServer(t, "", nil)

	conn, err := proxyClientFor(t, rec.Server).VMTunnelWebSocket(
		context.Background(), testHost, 100, "/run/test.sock", "ticket",
	)
	if err != nil {
		t.Fatalf("VMTunnelWebSocket: %v", err)
	}

	err = conn.Close()
	if err != nil {
		t.Errorf("Close: %v", err)
	}

	select {
	case code := <-rec.closes:
		if code != websocket.StatusNormalClosure {
			t.Errorf("server read close status %v, want %v", code, websocket.StatusNormalClosure)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for the server to read the close")
	}
}

// TestBehavior_ExpectedClosesAreNotReported closes from the server with 1000
// and with 1001. The client notices the disconnect and reports no read error.
func TestBehavior_ExpectedClosesAreNotReported(t *testing.T) {
	t.Parallel()

	for _, code := range []websocket.StatusCode{websocket.StatusNormalClosure, websocket.StatusGoingAway} {
		t.Run(code.String(), func(t *testing.T) {
			t.Parallel()

			client := newBehaviorClient(t, behaviorConfig(closingServer(t, code, false)))

			err := client.Connect(context.Background())
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}

			seen, found := waitForError(client, "disconnected, attempting to reconnect", 3*time.Second)
			if found == nil {
				t.Fatalf("no disconnect reported after a %v close; saw %v", code, seen)
			}

			for _, err := range seen {
				if strings.HasPrefix(err.Error(), "read error:") {
					t.Errorf("close %v reported as %q, want no read error", code, err)
				}
			}
		})
	}
}

// TestBehavior_UnexpectedClosesAreReported closes from the server with 1011,
// and drops the TCP connection without a close frame. The client reports a
// read error for each, before it reports the disconnect.
func TestBehavior_UnexpectedClosesAreReported(t *testing.T) {
	t.Parallel()

	cases := map[string]bool{"internal error close": false, "dropped connection": true}

	for name, drop := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client := newBehaviorClient(t, behaviorConfig(closingServer(t, websocket.StatusInternalError, drop)))

			err := client.Connect(context.Background())
			if err != nil {
				t.Fatalf("Connect: %v", err)
			}

			seen, found := waitForError(client, "read error:", 3*time.Second)
			if found == nil {
				t.Fatalf("no read error reported; saw %v", seen)
			}

			if len(seen) != 1 {
				t.Errorf("errors before the read error: %v, want the read error first", seen[:len(seen)-1])
			}
		})
	}
}

// TestBehavior_ProxyConnReadReportsUnexpectedCloses reads from a proxy
// session whose server closes with 1011 or drops the connection.
func TestBehavior_ProxyConnReadReportsUnexpectedCloses(t *testing.T) {
	t.Parallel()

	for name, drop := range map[string]bool{"internal error close": false, "dropped connection": true} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			conn := openProxyConn(t, closingServer(t, websocket.StatusInternalError, drop))

			_, _, err := conn.ReadMessage()
			if err == nil {
				t.Fatal("ReadMessage returned no error after the server ended the connection")
			}

			if !strings.HasPrefix(err.Error(), "read message: ") {
				t.Errorf("error %q lost its prefix", err)
			}
		})
	}
}

// --- dial errors, redirects, and TLS ---

// statusServer answers every request with status and a body that holds a
// secret, without upgrading.
func statusServer(t *testing.T, status int) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(respWriter http.ResponseWriter, _ *http.Request) {
		respWriter.WriteHeader(status)
		_, _ = fmt.Fprint(respWriter, "BODYSECRET "+dialVNCTicket)
	}))
	t.Cleanup(srv.Close)

	return srv
}

// TestBehavior_HandshakeStatusErrorsLeaveNoTicket answers each WebSocket dial
// with a status other than 101. The error holds no ticket, cookie, socket,
// CSRF token, or body text, and names the host and path.
func TestBehavior_HandshakeStatusErrorsLeaveNoTicket(t *testing.T) {
	t.Parallel()

	for _, status := range []int{http.StatusUnauthorized, http.StatusNotFound, http.StatusOK} {
		for name, dial := range dials() {
			t.Run(fmt.Sprintf("%s %d", name, status), func(t *testing.T) {
				t.Parallel()

				srv := statusServer(t, status)
				host, _ := hostPort(srv)

				err := dial(context.Background(), proxyClientFor(t, srv))
				assertNoDialSecret(t, err)

				if strings.Contains(err.Error(), "BODYSECRET") {
					t.Errorf("error %q holds the response body", err)
				}

				if !strings.Contains(err.Error(), host) || !strings.Contains(err.Error(), "/api2/json/nodes/pve1/") {
					t.Errorf("error %q lost the host or the path", err)
				}
			})
		}

		t.Run(fmt.Sprintf("event client %d", status), func(t *testing.T) {
			t.Parallel()

			client := newBehaviorClient(t, behaviorConfig(statusServer(t, status)))
			client.SetAuth(testTicket, testCSRF)

			err := client.Connect(context.Background())
			assertNoDialSecret(t, err)

			if strings.Contains(err.Error(), "BODYSECRET") {
				t.Errorf("error %q holds the response body", err)
			}
		})
	}
}

// redirectPair is a front server that answers every request with a redirect
// to a target server, and counts what each one reads.
type redirectPair struct {
	front, target       *httptest.Server
	frontHits, tgtHits  atomic.Int32
	targetLocationQuery string
}

// newRedirectPair starts the target, a WebSocket echo server, and the front,
// which redirects with status to the target's address with a secret in the
// query.
func newRedirectPair(t *testing.T, status int) *redirectPair {
	t.Helper()

	pair := &redirectPair{targetLocationQuery: "?vncticket=" + dialVNCTicket}

	pair.target = httptest.NewServer(http.HandlerFunc(func(respWriter http.ResponseWriter, req *http.Request) {
		pair.tgtHits.Add(1)

		conn := acceptWS(respWriter, req, nil)
		if conn == nil {
			return
		}

		_ = conn.Close(websocket.StatusNormalClosure, "")
	}))
	t.Cleanup(pair.target.Close)

	pair.front = httptest.NewServer(http.HandlerFunc(func(respWriter http.ResponseWriter, req *http.Request) {
		pair.frontHits.Add(1)

		http.Redirect(respWriter, req, pair.target.URL+req.URL.Path+pair.targetLocationQuery, status)
	}))
	t.Cleanup(pair.front.Close)

	return pair
}

// assertRefused checks that a dial through pair failed, reached the front
// once, never reached the target, and kept secrets out of its error.
func (pair *redirectPair) assertRefused(t *testing.T, err error) {
	t.Helper()

	assertNoDialSecret(t, err)

	if got := pair.frontHits.Load(); got != 1 {
		t.Errorf("front read %d requests, want 1", got)
	}

	if got := pair.tgtHits.Load(); got != 0 {
		t.Errorf("redirect target read %d requests, want 0", got)
	}
}

// TestBehavior_WebSocketDialsRefuseRedirects answers each WebSocket dial with
// a redirect to a second listener on the host. The dial fails, and the second
// listener never sees the ticket, the cookie, or the CSRF token.
func TestBehavior_WebSocketDialsRefuseRedirects(t *testing.T) {
	t.Parallel()

	statuses := []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
	}

	for _, status := range statuses {
		for name, dial := range dials() {
			t.Run(fmt.Sprintf("%s %d", name, status), func(t *testing.T) {
				t.Parallel()

				pair := newRedirectPair(t, status)
				err := dial(context.Background(), proxyClientFor(t, pair.front))
				pair.assertRefused(t, err)
			})
		}

		t.Run(fmt.Sprintf("event client %d", status), func(t *testing.T) {
			t.Parallel()

			pair := newRedirectPair(t, status)

			client := newBehaviorClient(t, behaviorConfig(pair.front))
			client.SetAuth(testTicket, testCSRF)

			err := client.Connect(context.Background())
			pair.assertRefused(t, err)
		})
	}
}

// pinnedTLSConfig builds the TLS configuration the SDK uses for fingerprint
// pinning, as internal/http does: chain verification is replaced by a check
// of the leaf certificate's SHA-256 fingerprint against the trusted one, and
// session resumption is off so every handshake is checked.
func pinnedTLSConfig(trusted string) *tls.Config {
	verifier := issl.NewFingerprintVerifier()
	verifier.AddTrustedFingerprint(trusted)

	return &tls.Config{
		MinVersion:             tls.VersionTLS12,
		InsecureSkipVerify:     true,
		SessionTicketsDisabled: true,
		VerifyPeerCertificate: func(rawCerts [][]byte, _ [][]*x509.Certificate) error {
			if len(rawCerts) == 0 {
				return issl.ErrNoCertificatesProvided
			}

			cert, err := x509.ParseCertificate(rawCerts[0])
			if err != nil {
				return fmt.Errorf("parse certificate: %w", err)
			}

			return verifier.VerifyCertificate(cert)
		},
	}
}

// wrongFingerprint is a well-formed SHA-256 fingerprint no test certificate has.
//
//nolint:gochecknoglobals // test data
var wrongFingerprint = strings.TrimSuffix(strings.Repeat("AB:", 32), ":")

// tlsCase is one TLS setting a dial is tried with.
type tlsCase struct {
	name     string
	tlsCfg   func(srv *httptest.Server) *tls.Config
	insecure bool
	wantErr  func(err error) bool
}

// tlsCases lists the TLS settings the event client and the proxy client are
// tried with, and whether each must connect.
func tlsCases() []tlsCase {
	isFingerprintRefusal := func(err error) bool { return errors.Is(err, issl.ErrCannotVerifyFingerprint) }
	isUnknownAuthority := func(err error) bool {
		var verifyErr *tls.CertificateVerificationError

		return errors.As(err, &verifyErr)
	}

	return []tlsCase{
		{
			name: "pinned to the right fingerprint",
			tlsCfg: func(srv *httptest.Server) *tls.Config {
				return pinnedTLSConfig(issl.CalculateFingerprint(srv.Certificate()))
			},
		},
		{
			name:    "pinned to a wrong fingerprint",
			tlsCfg:  func(*httptest.Server) *tls.Config { return pinnedTLSConfig(wrongFingerprint) },
			wantErr: isFingerprintRefusal,
		},
		{
			name: "chain verified against the server's CA",
			tlsCfg: func(srv *httptest.Server) *tls.Config {
				roots := x509.NewCertPool()
				roots.AddCert(srv.Certificate())

				return &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
			},
		},
		{
			name:    "chain verified against the system roots",
			tlsCfg:  func(*httptest.Server) *tls.Config { return nil },
			wantErr: isUnknownAuthority,
		},
		{name: "insecure", insecure: true},
	}
}

// checkTLSOutcome fails when err does not match what tc expects.
func checkTLSOutcome(t *testing.T, tc tlsCase, err error) {
	t.Helper()

	if tc.wantErr == nil {
		if err != nil {
			t.Fatalf("dial: %v, want it to connect", err)
		}

		return
	}

	if err == nil {
		t.Fatal("dial connected, want the certificate refused")
	}

	if !tc.wantErr(err) {
		t.Errorf("dial error %v is not the expected certificate refusal", err)
	}
}

// TestBehavior_EventClientTLS dials a TLS server with each TLS setting.
func TestBehavior_EventClientTLS(t *testing.T) {
	t.Parallel()

	for _, tc := range tlsCases() {
		if tc.insecure {
			continue // Config has no insecure flag; a caller sets InsecureSkipVerify in TLSConfig.
		}

		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv := newEchoServer(t, true)

			cfg := behaviorConfig(srv)
			cfg.Secure = true
			cfg.TLSConfig = tc.tlsCfg(srv)

			client := newBehaviorClient(t, cfg)
			err := client.Connect(context.Background())
			checkTLSOutcome(t, tc, err)

			if err == nil {
				sendErr := client.SendText("over tls")
				if sendErr != nil {
					t.Errorf("SendText over TLS: %v", sendErr)
				}
			}
		})
	}
}

// TestBehavior_ProxyClientTLS dials a TLS server through ProxyClient with each
// TLS setting, and round trips a message when the dial succeeds.
func TestBehavior_ProxyClientTLS(t *testing.T) {
	t.Parallel()

	for _, tc := range tlsCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			srv := newEchoServer(t, true)
			host, port := hostPort(srv)

			proxyCfg := &pkgws.ProxyConfig{
				Host: host, Port: port, Scheme: "https", Insecure: tc.insecure,
				Ticket: testTicket, CSRFToken: testCSRF,
			}
			if tc.tlsCfg != nil {
				proxyCfg.TLSConfig = tc.tlsCfg(srv)
			}

			client, err := pkgws.NewProxyClient(proxyCfg)
			if err != nil {
				t.Fatalf("NewProxyClient: %v", err)
			}

			conn, err := client.VMTunnelWebSocket(context.Background(), testHost, 100, "/run/test.sock", "ticket")
			checkTLSOutcome(t, tc, err)

			if err != nil {
				return
			}

			defer func() { _ = conn.Close() }()

			err = conn.WriteMessage(behaviorText, []byte("over tls"))
			if err != nil {
				t.Fatalf("WriteMessage over TLS: %v", err)
			}

			_, data, err := conn.ReadMessage()
			if err != nil || string(data) != "over tls" {
				t.Errorf("ReadMessage over TLS: %q, %v", data, err)
			}
		})
	}
}

// --- concurrency ---

// TestBehavior_ConcurrentSends runs Send and SendText from many goroutines
// while the ping loop runs. Every message reaches the server whole.
func TestBehavior_ConcurrentSends(t *testing.T) {
	t.Parallel()

	const (
		senders   = 8
		perSender = 25
		total     = 2 * senders * perSender
	)

	rec := newRecordingServer(t, "", nil)

	cfg := behaviorConfig(rec.Server)
	cfg.PingInterval = 5 * time.Millisecond

	client := newBehaviorClient(t, cfg)

	err := client.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	for _, err := range sendConcurrently(client, senders, perSender) {
		if err != nil {
			t.Errorf("send: %v", err)
		}
	}

	seen := make(map[string]bool, total)

	for range total {
		msg := nextMessage(t, rec)

		text := string(msg.data)
		if !strings.HasPrefix(text, "text ") {
			var decoded map[string]int

			decodeErr := json.Unmarshal(msg.data, &decoded)
			if decodeErr != nil {
				t.Fatalf("server read %q, which is neither a text message nor whole JSON: %v", text, decodeErr)
			}
		}

		if seen[text] {
			t.Errorf("server read %q twice", text)
		}

		seen[text] = true
	}
}

// sendConcurrently starts senders goroutines that each call Send perSender
// times and senders more that each call SendText perSender times, and returns
// what every call returned once all of them finish.
func sendConcurrently(client *pkgws.Client, senders, perSender int) []error {
	var waitGroup sync.WaitGroup

	sendErrs := make(chan error, 2*senders*perSender)

	for sender := range senders {
		waitGroup.Add(2)

		go func() {
			defer waitGroup.Done()

			for seq := range perSender {
				sendErrs <- client.Send(map[string]int{"sender": sender, "seq": seq})
			}
		}()

		go func() {
			defer waitGroup.Done()

			for seq := range perSender {
				sendErrs <- client.SendText(fmt.Sprintf("text %d %d", sender, seq))
			}
		}()
	}

	waitGroup.Wait()
	close(sendErrs)

	errs := make([]error, 0, cap(sendErrs))
	for err := range sendErrs {
		errs = append(errs, err)
	}

	return errs
}
