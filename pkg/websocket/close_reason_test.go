package websocket_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/gorilla/websocket"

	pkgws "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/websocket"
)

// hostileCloseReason holds an escape sequence, a carriage return, and a text
// direction override, then enough escape bytes to run past the cap once they
// are escaped, within the 123 bytes a close frame carries.
//
//nolint:gochecknoglobals // test data
var hostileCloseReason = "\x1b[2J\r\x1b]0;EVIL\a\u202eFLIP" + strings.Repeat("\x1b", 100)

// closeWithReason returns a server that upgrades, then closes with the
// hostile reason and a code the client treats as unexpected.
func closeWithReason(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(respWriter http.ResponseWriter, req *http.Request) {
		conn, err := wsUpgrader.Upgrade(respWriter, req, nil)
		if err != nil {
			return
		}

		defer func() { _ = conn.Close() }()

		_ = conn.WriteMessage(
			websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, hostileCloseReason),
		)
	}))
	t.Cleanup(srv.Close)

	return srv
}

// assertEscaped fails when the error text holds a raw control character or
// direction override, runs past the cap, or no longer reaches the close error.
func assertEscaped(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("expected a read error")
	}

	for _, r := range err.Error() {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			t.Errorf("error text %q holds the raw character %U", err.Error(), r)
		}
	}

	if !strings.HasSuffix(err.Error(), "...(truncated)") {
		t.Errorf("error text %q is not marked as cut", err.Error())
	}

	const prefixAndMarker = 64

	if len(err.Error()) > 256+prefixAndMarker {
		t.Errorf("error text is %d bytes, want it cut near 256", len(err.Error()))
	}

	var closeErr *websocket.CloseError
	if !errors.As(err, &closeErr) || closeErr.Code != websocket.ClosePolicyViolation {
		t.Errorf("error %v no longer unwraps to the close error", err)
	}
}

// TestConnReadMessageEscapesTheCloseReason reads from a connection whose server
// closes with a hostile reason.
func TestConnReadMessageEscapesTheCloseReason(t *testing.T) {
	t.Parallel()

	srv := closeWithReason(t)
	client := proxyClientFor(t, srv)

	session := &pkgws.VNCSession{Port: 5900, Ticket: testTicket, User: testRootAtPAM}

	conn, err := client.VMVNCConnect(context.Background(), testHost, 100, session)
	if err != nil {
		t.Fatalf("VMVNCConnect: %v", err)
	}

	defer func() { _ = conn.Close() }()

	_, _, err = conn.ReadMessage()
	assertEscaped(t, err)
}

// TestClientReadLoopEscapesTheCloseReason checks the error the event client
// reports on its error channel.
func TestClientReadLoopEscapesTheCloseReason(t *testing.T) {
	t.Parallel()

	srv := closeWithReason(t)
	wsClient := newClient(t, "ws://"+srv.Listener.Addr().String()+"/ws")

	err := wsClient.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	defer wsClient.Disconnect() //nolint:errcheck

	select {
	case readErr := <-wsClient.Errors():
		if !strings.HasPrefix(readErr.Error(), "read error: ") {
			t.Fatalf("first error is %q, want the read error", readErr)
		}

		assertEscaped(t, readErr)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for the read error")
	}
}
