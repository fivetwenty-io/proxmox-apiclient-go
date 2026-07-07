package websocket_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	pkgws "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/websocket"
)

// --- test constants ---

const (
	testSchemeHTTP = "http"
	testHost       = "pve1"
	testLocalhost  = "127.0.0.1"
	testTicket     = "PVECLUSTERID:testticket"
	testCSRF       = "testcsrf"
	testVNCTicket  = "VNC:tk"
	testRootAtPAM  = "root@pam"
	testKeyData    = "data"
	testKeyPort    = "port"
	testKeyTicket  = "ticket"
	testKeyUPID    = "upid"
	testKeyUser    = "user"
	testSpiceHost  = "192.0.2.1"
)

// --- helpers ---

//nolint:gochecknoglobals // shared test-only WebSocket upgrader; not mutated after init
var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(_ *http.Request) bool { return true },
}

// newTestServer returns an httptest.Server that:
//   - POST /api2/json/* → replies with the given JSON body and status code
//   - GET /api2/json/*  → upgrades to WebSocket, sends one text frame, then closes
func newTestServer(t *testing.T, postBody string, postStatus int) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("/api2/json/", func(respWriter http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodPost:
			respWriter.Header().Set("Content-Type", "application/json")
			respWriter.WriteHeader(postStatus)
			_, _ = fmt.Fprint(respWriter, postBody)

		case http.MethodGet:
			conn, err := wsUpgrader.Upgrade(respWriter, req, nil)
			if err != nil {
				http.Error(respWriter, "upgrade failed", http.StatusInternalServerError)

				return
			}

			defer func() { _ = conn.Close() }()

			// Send a single text frame then close normally.
			_ = conn.WriteMessage(websocket.TextMessage, []byte("hello"))
			_ = conn.WriteMessage(
				websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
			)

		default:
			http.Error(respWriter, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	return httptest.NewServer(mux)
}

// hostPort splits "host:port" from an httptest.Server URL.
func hostPort(srv *httptest.Server) (string, int) {
	addr := srv.Listener.Addr().String()
	// addr = "127.0.0.1:PORT"
	lastColon := strings.LastIndex(addr, ":")
	if lastColon < 0 {
		return addr, 80
	}

	portStr := addr[lastColon+1:]

	var port int

	_, _ = fmt.Sscanf(portStr, "%d", &port)

	return addr[:lastColon], port
}

// validVNCResponse wraps a VNCSession in the PVE data envelope.
func validVNCResponse(port int, ticket string) string {
	body, err := json.Marshal(map[string]interface{}{
		testKeyData: map[string]interface{}{
			testKeyPort:   port,
			testKeyTicket: ticket,
			testKeyUPID:   "UPID:pve1:00001234:00000000:6789ABCD:vncproxy:100:root@pam:",
			testKeyUser:   testRootAtPAM,
			"cert":        "fakecert",
		},
	})
	if err != nil {
		panic(fmt.Sprintf("validVNCResponse: json.Marshal: %v", err))
	}

	return string(body)
}

func validTermResponse(port int, ticket string) string {
	body, err := json.Marshal(map[string]interface{}{
		testKeyData: map[string]interface{}{
			testKeyPort:   port,
			testKeyTicket: ticket,
			testKeyUPID:   "UPID:pve1:00001234:00000000:6789ABCD:termproxy:100:root@pam:",
			testKeyUser:   testRootAtPAM,
		},
	})
	if err != nil {
		panic(fmt.Sprintf("validTermResponse: json.Marshal: %v", err))
	}

	return string(body)
}

func validSpiceResponse() string {
	body, err := json.Marshal(map[string]interface{}{
		testKeyData: map[string]interface{}{
			"host":     testSpiceHost,
			"password": "spicepass",
			"proxy":    testSpiceHost,
			"tls-port": 61000,
			"type":     "spice",
		},
	})
	if err != nil {
		panic(fmt.Sprintf("validSpiceResponse: json.Marshal: %v", err))
	}

	return string(body)
}

// proxyClientFor builds a ProxyClient pointing at an httptest.Server (plain HTTP/WS).
func proxyClientFor(t *testing.T, srv *httptest.Server) *pkgws.ProxyClient {
	t.Helper()

	host, port := hostPort(srv)

	client, err := pkgws.NewProxyClient(&pkgws.ProxyConfig{
		Host:      host,
		Port:      port,
		Scheme:    testSchemeHTTP, // plain http test server; ws (not wss) for WebSocket
		Insecure:  true,
		Ticket:    testTicket,
		CSRFToken: testCSRF,
	})
	if err != nil {
		t.Fatalf("NewProxyClient: %v", err)
	}

	return client
}

// --- NewProxyClient validation tests ---

func TestNewProxyClient_NilConfig(t *testing.T) {
	t.Parallel()

	_, err := pkgws.NewProxyClient(nil)
	if !errors.Is(err, pkgws.ErrNodeRequired) {
		t.Fatalf("expected ErrNodeRequired, got %v", err)
	}
}

func TestNewProxyClient_EmptyHost(t *testing.T) {
	t.Parallel()

	_, err := pkgws.NewProxyClient(&pkgws.ProxyConfig{Host: ""})
	if !errors.Is(err, pkgws.ErrNodeRequired) {
		t.Fatalf("expected ErrNodeRequired, got %v", err)
	}
}

func TestNewProxyClient_DefaultPort(t *testing.T) {
	t.Parallel()

	// Port 0 should be defaulted to 8006; construction should succeed.
	proxyClient, err := pkgws.NewProxyClient(&pkgws.ProxyConfig{Host: testHost, Port: 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if proxyClient == nil {
		t.Fatal("expected non-nil ProxyClient")
	}
}

// --- VMVNCProxy happy path ---

func TestVMVNCProxy_HappyPath(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, validVNCResponse(5900, "VNC:testticket"), http.StatusOK)
	defer srv.Close()

	client := proxyClientFor(t, srv)
	ctx := context.Background()

	session, err := client.VMVNCProxy(ctx, testHost, 100)
	if err != nil {
		t.Fatalf("VMVNCProxy: %v", err)
	}

	if session.Port != 5900 {
		t.Errorf("port: want 5900, got %d", session.Port)
	}

	if session.Ticket != "VNC:testticket" {
		t.Errorf("ticket: want VNC:testticket, got %q", session.Ticket)
	}

	if session.User != testRootAtPAM {
		t.Errorf("user: want root@pam, got %q", session.User)
	}
}

// --- VMVNCProxy input validation ---

func TestVMVNCProxy_EmptyNode(t *testing.T) {
	t.Parallel()

	proxyClient, _ := pkgws.NewProxyClient(&pkgws.ProxyConfig{Host: testHost})
	_, err := proxyClient.VMVNCProxy(context.Background(), "", 100)

	if !errors.Is(err, pkgws.ErrNodeRequired) {
		t.Fatalf("expected ErrNodeRequired, got %v", err)
	}
}

func TestVMVNCProxy_InvalidVMID(t *testing.T) {
	t.Parallel()

	proxyClient, _ := pkgws.NewProxyClient(&pkgws.ProxyConfig{Host: testHost})
	_, err := proxyClient.VMVNCProxy(context.Background(), testHost, 50)

	if !errors.Is(err, pkgws.ErrVMIDInvalid) {
		t.Fatalf("expected ErrVMIDInvalid, got %v", err)
	}
}

// --- VMVNCProxy 4xx error ---

func TestVMVNCProxy_HTTPError(t *testing.T) {
	t.Parallel()

	errBody := `{"errors":{"permission":"VM.Console required"}}`

	srv := newTestServer(t, errBody, http.StatusForbidden)
	defer srv.Close()

	client := proxyClientFor(t, srv)

	_, err := client.VMVNCProxy(context.Background(), testHost, 100)
	if err == nil {
		t.Fatal("expected error on 403 response")
	}

	if !errors.Is(err, pkgws.ErrHTTPError) {
		t.Errorf("expected ErrHTTPError, got: %v", err)
	}
}

// --- VMVNCConnect happy path ---

func TestVMVNCConnect_HappyPath(t *testing.T) {
	t.Parallel()

	// POST returns VNC session; GET upgrades WebSocket.
	srv := newTestServer(t, validVNCResponse(5900, "VNC:testticket"), http.StatusOK)
	defer srv.Close()

	client := proxyClientFor(t, srv)
	ctx := context.Background()

	session := &pkgws.VNCSession{Port: 5900, Ticket: "VNC:testticket", User: testRootAtPAM}

	conn, err := client.VMVNCConnect(ctx, testHost, 100, session)
	if err != nil {
		t.Fatalf("VMVNCConnect: %v", err)
	}

	defer func() { _ = conn.Close() }()

	// Read the "hello" frame the test server sends.
	msgType, data, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}

	if msgType != websocket.TextMessage {
		t.Errorf("message type: want %d, got %d", websocket.TextMessage, msgType)
	}

	if string(data) != "hello" {
		t.Errorf("data: want hello, got %q", string(data))
	}
}

// --- VMVNCConnect input validation ---

func TestVMVNCConnect_NilSession(t *testing.T) {
	t.Parallel()

	proxyClient, _ := pkgws.NewProxyClient(&pkgws.ProxyConfig{Host: testHost})
	_, err := proxyClient.VMVNCConnect(context.Background(), testHost, 100, nil)

	if !errors.Is(err, pkgws.ErrNilSession) {
		t.Fatalf("expected ErrNilSession, got %v", err)
	}
}

func TestVMVNCConnect_NoTicket(t *testing.T) {
	t.Parallel()

	proxyClient, _ := pkgws.NewProxyClient(&pkgws.ProxyConfig{Host: testHost})
	session := &pkgws.VNCSession{Port: 5900}
	_, err := proxyClient.VMVNCConnect(context.Background(), testHost, 100, session)

	if !errors.Is(err, pkgws.ErrSessionNoTicket) {
		t.Fatalf("expected ErrSessionNoTicket, got %v", err)
	}
}

func TestVMVNCConnect_NoPort(t *testing.T) {
	t.Parallel()

	proxyClient, _ := pkgws.NewProxyClient(&pkgws.ProxyConfig{Host: testHost})
	session := &pkgws.VNCSession{Ticket: testVNCTicket}
	_, err := proxyClient.VMVNCConnect(context.Background(), testHost, 100, session)

	if !errors.Is(err, pkgws.ErrSessionNoPort) {
		t.Fatalf("expected ErrSessionNoPort, got %v", err)
	}
}

func TestVMVNCConnect_PortOutOfRange(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, validVNCResponse(5900, testVNCTicket), http.StatusOK)
	defer srv.Close()

	client := proxyClientFor(t, srv)
	// Port outside 5900-5999 should be rejected before dialing.
	session := &pkgws.VNCSession{Port: 9999, Ticket: testVNCTicket}
	_, err := client.VMVNCConnect(context.Background(), testHost, 100, session)

	if !errors.Is(err, pkgws.ErrPortOutOfRange) {
		t.Fatalf("expected ErrPortOutOfRange, got %v", err)
	}
}

// --- VMTermProxy ---

func TestVMTermProxy_HappyPath(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, validTermResponse(5901, "TERM:tk"), http.StatusOK)
	defer srv.Close()

	client := proxyClientFor(t, srv)

	session, err := client.VMTermProxy(context.Background(), testHost, 100, "")
	if err != nil {
		t.Fatalf("VMTermProxy: %v", err)
	}

	if session.Port != 5901 {
		t.Errorf("port: want 5901, got %d", session.Port)
	}
}

func TestVMTermProxy_WithSerial(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, validTermResponse(5902, "TERM:serial"), http.StatusOK)
	defer srv.Close()

	client := proxyClientFor(t, srv)

	session, err := client.VMTermProxy(context.Background(), testHost, 100, "serial0")
	if err != nil {
		t.Fatalf("VMTermProxy with serial: %v", err)
	}

	if session.Ticket != "TERM:serial" {
		t.Errorf("ticket: want TERM:serial, got %q", session.Ticket)
	}
}

func TestVMTermProxy_InputValidation(t *testing.T) {
	t.Parallel()

	proxyClient, _ := pkgws.NewProxyClient(&pkgws.ProxyConfig{Host: testHost})

	_, err := proxyClient.VMTermProxy(context.Background(), "", 100, "")
	if !errors.Is(err, pkgws.ErrNodeRequired) {
		t.Errorf("empty node: expected ErrNodeRequired, got %v", err)
	}

	_, err = proxyClient.VMTermProxy(context.Background(), testHost, 99, "")
	if !errors.Is(err, pkgws.ErrVMIDInvalid) {
		t.Errorf("vmid 99: expected ErrVMIDInvalid, got %v", err)
	}
}

// --- VMSpiceProxy ---

func TestVMSpiceProxy_HappyPath(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, validSpiceResponse(), http.StatusOK)
	defer srv.Close()

	client := proxyClientFor(t, srv)

	session, err := client.VMSpiceProxy(context.Background(), testHost, 100, "")
	if err != nil {
		t.Fatalf("VMSpiceProxy: %v", err)
	}

	if session.Host != testSpiceHost {
		t.Errorf("host: want 192.0.2.1, got %q", session.Host)
	}

	if session.TLSPort != 61000 {
		t.Errorf("tls-port: want 61000, got %d", session.TLSPort)
	}
}

func TestVMSpiceProxy_InputValidation(t *testing.T) {
	t.Parallel()

	proxyClient, _ := pkgws.NewProxyClient(&pkgws.ProxyConfig{Host: testHost})

	_, err := proxyClient.VMSpiceProxy(context.Background(), testHost, 50, "")
	if !errors.Is(err, pkgws.ErrVMIDInvalid) {
		t.Errorf("expected ErrVMIDInvalid, got %v", err)
	}
}

// --- VMTunnelWebSocket ---

func TestVMTunnelWebSocket_HappyPath(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, "", http.StatusOK)
	defer srv.Close()

	client := proxyClientFor(t, srv)

	conn, err := client.VMTunnelWebSocket(
		context.Background(), testHost, 100, "/var/run/qemu-100.sock", "tunnel-ticket",
	)
	if err != nil {
		t.Fatalf("VMTunnelWebSocket: %v", err)
	}

	defer func() { _ = conn.Close() }()
}

func TestVMTunnelWebSocket_MissingSocket(t *testing.T) {
	t.Parallel()

	proxyClient, _ := pkgws.NewProxyClient(&pkgws.ProxyConfig{Host: testHost})
	_, err := proxyClient.VMTunnelWebSocket(context.Background(), testHost, 100, "", "ticket")

	if !errors.Is(err, pkgws.ErrSocketRequired) {
		t.Fatalf("expected ErrSocketRequired, got %v", err)
	}
}

func TestVMTunnelWebSocket_MissingTicket(t *testing.T) {
	t.Parallel()

	proxyClient, _ := pkgws.NewProxyClient(&pkgws.ProxyConfig{Host: testHost})
	_, err := proxyClient.VMTunnelWebSocket(context.Background(), testHost, 100, "/run/sock", "")

	if !errors.Is(err, pkgws.ErrTicketRequired) {
		t.Fatalf("expected ErrTicketRequired, got %v", err)
	}
}

// --- LXC endpoints ---

func TestLXCVNCProxy_HappyPath(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, validVNCResponse(5910, "VNC:lxc"), http.StatusOK)
	defer srv.Close()

	client := proxyClientFor(t, srv)

	session, err := client.LXCVNCProxy(context.Background(), testHost, 200)
	if err != nil {
		t.Fatalf("LXCVNCProxy: %v", err)
	}

	if session.Port != 5910 {
		t.Errorf("port: want 5910, got %d", session.Port)
	}
}

func TestLXCVNCConnect_HappyPath(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, "", http.StatusOK)
	defer srv.Close()

	client := proxyClientFor(t, srv)
	session := &pkgws.VNCSession{Port: 5910, Ticket: "VNC:lxc", User: testRootAtPAM}

	conn, err := client.LXCVNCConnect(context.Background(), testHost, 200, session)
	if err != nil {
		t.Fatalf("LXCVNCConnect: %v", err)
	}

	defer func() { _ = conn.Close() }()
}

func TestLXCTermProxy_HappyPath(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, validTermResponse(5911, "TERM:lxc"), http.StatusOK)
	defer srv.Close()

	client := proxyClientFor(t, srv)

	session, err := client.LXCTermProxy(context.Background(), testHost, 200)
	if err != nil {
		t.Fatalf("LXCTermProxy: %v", err)
	}

	if session.Ticket != "TERM:lxc" {
		t.Errorf("ticket: want TERM:lxc, got %q", session.Ticket)
	}
}

func TestLXCSpiceProxy_HappyPath(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, validSpiceResponse(), http.StatusOK)
	defer srv.Close()

	client := proxyClientFor(t, srv)

	session, err := client.LXCSpiceProxy(context.Background(), testHost, 200, "")
	if err != nil {
		t.Fatalf("LXCSpiceProxy: %v", err)
	}

	if session.Type != "spice" {
		t.Errorf("type: want spice, got %q", session.Type)
	}
}

func TestLXCTunnelWebSocket_HappyPath(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, "", http.StatusOK)
	defer srv.Close()

	client := proxyClientFor(t, srv)

	conn, err := client.LXCTunnelWebSocket(
		context.Background(), testHost, 200, "/var/run/lxc-200.sock", "lxc-tunnel-ticket",
	)
	if err != nil {
		t.Fatalf("LXCTunnelWebSocket: %v", err)
	}

	defer func() { _ = conn.Close() }()
}

func TestLXCTunnelWebSocket_InputValidation(t *testing.T) {
	t.Parallel()

	proxyClient, _ := pkgws.NewProxyClient(&pkgws.ProxyConfig{Host: testHost})

	_, err := proxyClient.LXCTunnelWebSocket(context.Background(), testHost, 200, "", "ticket")
	if !errors.Is(err, pkgws.ErrSocketRequired) {
		t.Errorf("expected ErrSocketRequired, got %v", err)
	}

	_, err = proxyClient.LXCTunnelWebSocket(context.Background(), testHost, 200, "/run/s", "")
	if !errors.Is(err, pkgws.ErrTicketRequired) {
		t.Errorf("expected ErrTicketRequired, got %v", err)
	}
}

func TestLXCEndpoints_InputValidation(t *testing.T) {
	t.Parallel()

	proxyClient, _ := pkgws.NewProxyClient(&pkgws.ProxyConfig{Host: testHost})
	ctx := context.Background()

	_, errVNCProxy := proxyClient.LXCVNCProxy(ctx, "", 200)
	if !errors.Is(errVNCProxy, pkgws.ErrNodeRequired) {
		t.Errorf("LXCVNCProxy empty node: got %v", errVNCProxy)
	}

	_, errTermProxy := proxyClient.LXCTermProxy(ctx, testHost, 50)
	if !errors.Is(errTermProxy, pkgws.ErrVMIDInvalid) {
		t.Errorf("LXCTermProxy low vmid: got %v", errTermProxy)
	}

	_, errSpiceProxy := proxyClient.LXCSpiceProxy(ctx, testHost, 50, "")
	if !errors.Is(errSpiceProxy, pkgws.ErrVMIDInvalid) {
		t.Errorf("LXCSpiceProxy low vmid: got %v", errSpiceProxy)
	}

	_, errVNCConnect := proxyClient.LXCVNCConnect(ctx, testHost, 200, nil)
	if !errors.Is(errVNCConnect, pkgws.ErrNilSession) {
		t.Errorf("LXCVNCConnect nil session: got %v", errVNCConnect)
	}
}

// --- Node-level endpoints ---

func TestNodeVNCShell_HappyPath(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, validVNCResponse(5920, "VNC:node"), http.StatusOK)
	defer srv.Close()

	client := proxyClientFor(t, srv)

	session, err := client.NodeVNCShell(context.Background(), testHost, "login")
	if err != nil {
		t.Fatalf("NodeVNCShell: %v", err)
	}

	if session.Port != 5920 {
		t.Errorf("port: want 5920, got %d", session.Port)
	}
}

func TestNodeVNCShell_EmptyCmd(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, validVNCResponse(5920, "VNC:node2"), http.StatusOK)
	defer srv.Close()

	client := proxyClientFor(t, srv)
	// Empty cmd is valid (server uses its default).
	session, err := client.NodeVNCShell(context.Background(), testHost, "")
	if err != nil {
		t.Fatalf("NodeVNCShell empty cmd: %v", err)
	}

	if session.Ticket != "VNC:node2" {
		t.Errorf("ticket mismatch: got %q", session.Ticket)
	}
}

func TestNodeVNCShell_InputValidation(t *testing.T) {
	t.Parallel()

	proxyClient, _ := pkgws.NewProxyClient(&pkgws.ProxyConfig{Host: testHost})
	_, err := proxyClient.NodeVNCShell(context.Background(), "", "login")

	if !errors.Is(err, pkgws.ErrNodeRequired) {
		t.Fatalf("expected ErrNodeRequired, got %v", err)
	}
}

func TestNodeVNCConnect_HappyPath(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, "", http.StatusOK)
	defer srv.Close()

	client := proxyClientFor(t, srv)
	session := &pkgws.VNCSession{Port: 5920, Ticket: "VNC:node", User: testRootAtPAM}

	conn, err := client.NodeVNCConnect(context.Background(), testHost, session)
	if err != nil {
		t.Fatalf("NodeVNCConnect: %v", err)
	}

	defer func() { _ = conn.Close() }()
}

func TestNodeVNCConnect_InputValidation(t *testing.T) {
	t.Parallel()

	proxyClient, _ := pkgws.NewProxyClient(&pkgws.ProxyConfig{Host: testHost})

	_, err := proxyClient.NodeVNCConnect(context.Background(), "", &pkgws.VNCSession{Port: 5920, Ticket: "t"})
	if !errors.Is(err, pkgws.ErrNodeRequired) {
		t.Errorf("empty node: got %v", err)
	}

	_, err = proxyClient.NodeVNCConnect(context.Background(), testHost, nil)
	if !errors.Is(err, pkgws.ErrNilSession) {
		t.Errorf("nil session: got %v", err)
	}
}

func TestNodeTermProxy_HappyPath(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, validTermResponse(5921, "TERM:node"), http.StatusOK)
	defer srv.Close()

	client := proxyClientFor(t, srv)

	session, err := client.NodeTermProxy(context.Background(), testHost)
	if err != nil {
		t.Fatalf("NodeTermProxy: %v", err)
	}

	if session.Port != 5921 {
		t.Errorf("port: want 5921, got %d", session.Port)
	}
}

func TestNodeTermProxy_InputValidation(t *testing.T) {
	t.Parallel()

	proxyClient, _ := pkgws.NewProxyClient(&pkgws.ProxyConfig{Host: testHost})
	_, err := proxyClient.NodeTermProxy(context.Background(), "")

	if !errors.Is(err, pkgws.ErrNodeRequired) {
		t.Fatalf("expected ErrNodeRequired, got %v", err)
	}
}

func TestNodeSpiceShell_HappyPath(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, validSpiceResponse(), http.StatusOK)
	defer srv.Close()

	client := proxyClientFor(t, srv)

	session, err := client.NodeSpiceShell(context.Background(), testHost, "login", "")
	if err != nil {
		t.Fatalf("NodeSpiceShell: %v", err)
	}

	if session.Password != "spicepass" {
		t.Errorf("password: want spicepass, got %q", session.Password)
	}
}

func TestNodeSpiceShell_InputValidation(t *testing.T) {
	t.Parallel()

	proxyClient, _ := pkgws.NewProxyClient(&pkgws.ProxyConfig{Host: testHost})
	_, err := proxyClient.NodeSpiceShell(context.Background(), "", "login", "")

	if !errors.Is(err, pkgws.ErrNodeRequired) {
		t.Fatalf("expected ErrNodeRequired, got %v", err)
	}
}

// --- Conn.WriteMessage after Close ---

func TestConn_WriteAfterClose(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, "", http.StatusOK)
	defer srv.Close()

	client := proxyClientFor(t, srv)

	// Use VMTunnelWebSocket to open a raw Conn without needing a valid VNC port.
	conn, err := client.VMTunnelWebSocket(
		context.Background(), testHost, 100, "/run/test.sock", "ticket",
	)
	if err != nil {
		t.Fatalf("open conn: %v", err)
	}

	err = conn.Close()
	if err != nil {
		t.Fatalf("first close: %v", err)
	}

	// Second close is a no-op.
	err = conn.Close()
	if err != nil {
		t.Errorf("second close should be no-op, got %v", err)
	}

	// Write after close returns ErrWriteAfterClose.
	err = conn.WriteMessage(websocket.TextMessage, []byte("after-close"))
	if !errors.Is(err, pkgws.ErrWriteAfterClose) {
		t.Errorf("expected ErrWriteAfterClose, got %v", err)
	}
}

// --- Context cancellation ---

func TestVMVNCConnect_ContextCancelled(t *testing.T) {
	t.Parallel()

	// Server that delays the WebSocket upgrade long enough for context to cancel.
	slow := httptest.NewServer(http.HandlerFunc(func(respWriter http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			time.Sleep(500 * time.Millisecond)

			_, _ = wsUpgrader.Upgrade(respWriter, req, nil)
		}
	}))
	defer slow.Close()

	host, port := hostPort(slow)
	client, _ := pkgws.NewProxyClient(&pkgws.ProxyConfig{
		Host:     host,
		Port:     port,
		Scheme:   testSchemeHTTP,
		Insecure: true,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	session := &pkgws.VNCSession{Port: 5900, Ticket: testVNCTicket}

	_, err := client.VMVNCConnect(ctx, testHost, 100, session)
	if err == nil {
		t.Fatal("expected error from context cancellation")
	}
}

// --- Auth header injection ---

func TestAuthHeaders_InjectedOnConnect(t *testing.T) {
	t.Parallel()

	var gotCookie, gotCSRF string

	srv := httptest.NewServer(http.HandlerFunc(func(respWriter http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			gotCookie = req.Header.Get("Cookie")
			gotCSRF = req.Header.Get("Csrfpreventiontoken")

			conn, err := wsUpgrader.Upgrade(respWriter, req, nil)
			if err != nil {
				return
			}

			defer func() { _ = conn.Close() }()
		}
	}))
	defer srv.Close()

	host, port := hostPort(srv)
	client, _ := pkgws.NewProxyClient(&pkgws.ProxyConfig{
		Host:      host,
		Port:      port,
		Scheme:    testSchemeHTTP,
		Insecure:  true,
		Ticket:    "MYTICKET",
		CSRFToken: "MYCSRF",
	})

	session := &pkgws.VNCSession{Port: 5900, Ticket: testVNCTicket}

	conn, err := client.VMVNCConnect(context.Background(), testHost, 100, session)
	if err != nil {
		t.Fatalf("VMVNCConnect: %v", err)
	}

	defer func() { _ = conn.Close() }()

	if !strings.Contains(gotCookie, "MYTICKET") {
		t.Errorf("cookie header: want MYTICKET in %q", gotCookie)
	}

	if gotCSRF != "MYCSRF" {
		t.Errorf("CSRF header: want MYCSRF, got %q", gotCSRF)
	}
}

// --- HTTPDoer integration ---

type fakeHTTPDoer struct {
	response interface{}
	err      error
}

func (f *fakeHTTPDoer) PostJSON(
	_ context.Context, _ string, _ map[string]string, dst interface{},
) error {
	if f.err != nil {
		return f.err
	}
	// dst is *pveDataEnvelope; marshal/unmarshal into it via JSON round-trip.
	jsonBytes, err := json.Marshal(f.response)
	if err != nil {
		return fmt.Errorf("fakeHTTPDoer marshal: %w", err)
	}

	err = json.Unmarshal(jsonBytes, dst)
	if err != nil {
		return fmt.Errorf("fakeHTTPDoer unmarshal: %w", err)
	}

	return nil
}

func TestVMVNCProxy_WithHTTPDoer(t *testing.T) {
	t.Parallel()

	envelope := map[string]interface{}{
		testKeyData: map[string]interface{}{
			testKeyPort:   5930,
			testKeyTicket: "VNC:doer",
			testKeyUPID:   "UPID:x",
			testKeyUser:   testRootAtPAM,
			"cert":        "",
		},
	}

	doer := &fakeHTTPDoer{response: envelope}
	client, _ := pkgws.NewProxyClient(&pkgws.ProxyConfig{
		Host:       testHost,
		HTTPClient: doer,
	})

	session, err := client.VMVNCProxy(context.Background(), testHost, 100)
	if err != nil {
		t.Fatalf("VMVNCProxy with HTTPDoer: %v", err)
	}

	if session.Port != 5930 {
		t.Errorf("port: want 5930, got %d", session.Port)
	}
}

var errFakeConnRefused = errors.New("connection refused")

func TestVMVNCProxy_HTTPDoer_Error(t *testing.T) {
	t.Parallel()

	doer := &fakeHTTPDoer{err: errFakeConnRefused}
	client, _ := pkgws.NewProxyClient(&pkgws.ProxyConfig{
		Host:       testHost,
		HTTPClient: doer,
	})

	_, err := client.VMVNCProxy(context.Background(), testHost, 100)
	if !errors.Is(err, errFakeConnRefused) {
		t.Fatalf("expected errFakeConnRefused, got %v", err)
	}
}

// --- Connection refused (no server) ---

func TestVMVNCProxy_ConnectionRefused(t *testing.T) {
	t.Parallel()

	// Port 1 is reserved and will always be refused.
	client, _ := pkgws.NewProxyClient(&pkgws.ProxyConfig{
		Host:     testLocalhost,
		Port:     1,
		Insecure: true,
	})

	_, err := client.VMVNCProxy(context.Background(), testHost, 100)
	if err == nil {
		t.Fatal("expected connection refused error")
	}
}
