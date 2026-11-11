package websocket_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pkgws "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/websocket"
	"github.com/gorilla/websocket"
)

// Values a WebSocket dial error must never repeat. The ticket and the socket go
// in the query of the dial URL, and the cookie and the CSRF token go in the
// request headers.
const (
	dialVNCTicket    = "VNCTICKETSECRET"
	dialTunnelTicket = "TUNNELTICKETSECRET"
	dialSocket       = "/run/pve/SOCKETSECRET.sock"
	dialGarbage      = "HTTP/1.1 http://u:HUNTER2@h/x?ticket=QSECRET Found\r\n\r\n"
)

// assertNoDialSecret fails when err, in either printed form, holds a value the
// dial sent in its URL or its headers, or text the server chose.
func assertNoDialSecret(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("dial succeeded, want a failure")
	}

	forbidden := []string{
		dialVNCTicket, dialTunnelTicket, dialSocket, "SOCKETSECRET", "vncticket", "ticket=",
		testTicket, "testticket", testCSRF, "HUNTER2", "QSECRET",
	}

	for _, text := range []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
		for _, secret := range forbidden {
			if strings.Contains(text, secret) {
				t.Errorf("error %q holds %q", text, secret)
			}
		}
	}
}

// garbageServer answers every request with a status line net/http rejects and
// returns its host and port.
func garbageServer(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(respWriter http.ResponseWriter, _ *http.Request) {
		conn, _, err := respWriter.(http.Hijacker).Hijack()
		if err != nil {
			return
		}

		_, _ = conn.Write([]byte(dialGarbage))
		_ = conn.Close()
	}))
}

// refusingServer answers every request with a 302, so the WebSocket upgrade
// fails with a bad handshake.
func refusingServer(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(respWriter http.ResponseWriter, req *http.Request) {
		http.Redirect(respWriter, req, "/elsewhere?vncticket="+dialVNCTicket, http.StatusFound)
	}))
}

// dials lists each ProxyClient call that opens a WebSocket with a ticket in
// the query.
func dials() map[string]func(context.Context, *pkgws.ProxyClient) error {
	session := &pkgws.VNCSession{Port: 5900, Ticket: dialVNCTicket}

	return map[string]func(context.Context, *pkgws.ProxyClient) error{
		"vm vnc": func(ctx context.Context, client *pkgws.ProxyClient) error {
			_, err := client.VMVNCConnect(ctx, "pve1", 100, session)

			return err //nolint:wrapcheck // the test reads the error as returned
		},
		"lxc vnc": func(ctx context.Context, client *pkgws.ProxyClient) error {
			_, err := client.LXCVNCConnect(ctx, "pve1", 100, session)

			return err //nolint:wrapcheck // the test reads the error as returned
		},
		"node vnc": func(ctx context.Context, client *pkgws.ProxyClient) error {
			_, err := client.NodeVNCConnect(ctx, "pve1", session)

			return err //nolint:wrapcheck // the test reads the error as returned
		},
		"vm tunnel": func(ctx context.Context, client *pkgws.ProxyClient) error {
			_, err := client.VMTunnelWebSocket(ctx, "pve1", 100, dialSocket, dialTunnelTicket)

			return err //nolint:wrapcheck // the test reads the error as returned
		},
		"lxc tunnel": func(ctx context.Context, client *pkgws.ProxyClient) error {
			_, err := client.LXCTunnelWebSocket(ctx, "pve1", 100, dialSocket, dialTunnelTicket)

			return err //nolint:wrapcheck // the test reads the error as returned
		},
	}
}

// TestProxyClient_DialErrorsLeaveNoTicket fails each WebSocket dial three ways
// and checks that the error text never holds the ticket, the tunnel socket, the
// cookie, or the CSRF token, while it names the host and the path.
func TestProxyClient_DialErrorsLeaveNoTicket(t *testing.T) {
	t.Parallel()

	for name, dial := range dials() {
		t.Run(name+" bad handshake", func(t *testing.T) {
			t.Parallel()

			server := refusingServer(t)
			defer server.Close()

			host, port := hostPort(server)
			err := dial(context.Background(), proxyClientFor(t, server))
			assertNoDialSecret(t, err)

			if !errors.Is(err, websocket.ErrBadHandshake) {
				t.Errorf("errors.Is(%v, ErrBadHandshake) = false, want true", err)
			}

			if !strings.Contains(err.Error(), host) || !strings.Contains(err.Error(), "/api2/json/nodes/pve1/") {
				t.Errorf("error %q lost the host %s:%d or the path", err, host, port)
			}
		})

		t.Run(name+" garbage status line", func(t *testing.T) {
			t.Parallel()

			server := garbageServer(t)
			defer server.Close()

			err := dial(context.Background(), proxyClientFor(t, server))
			assertNoDialSecret(t, err)

			if !strings.Contains(err.Error(), "malformed HTTP status code") {
				t.Errorf("error %q lost the kind of fault", err)
			}
		})

		t.Run(name+" connection refused", func(t *testing.T) {
			t.Parallel()

			server := refusingServer(t)
			client := proxyClientFor(t, server)

			server.Close()

			err := dial(context.Background(), client)
			assertNoDialSecret(t, err)

			var opErr *net.OpError
			if !errors.As(err, &opErr) {
				t.Errorf("errors.As(%v, *net.OpError) = false, want true", err)
			}
		})
	}
}

// TestClient_ConnectErrorsLeaveNoServerText checks the event client does not
// repeat what the server sent in a status line net/http rejects.
func TestClient_ConnectErrorsLeaveNoServerText(t *testing.T) {
	t.Parallel()

	server := garbageServer(t)
	defer server.Close()

	host, port := hostPort(server)

	cfg := pkgws.DefaultConfig()
	cfg.Host = host
	cfg.Port = port
	cfg.Secure = false

	client, err := pkgws.New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	err = client.Connect(context.Background())
	assertNoDialSecret(t, err)

	if !strings.Contains(err.Error(), "malformed HTTP status code") {
		t.Errorf("error %q lost the kind of fault", err)
	}
}
