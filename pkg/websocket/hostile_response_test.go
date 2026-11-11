package websocket_test

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect/redirecttest"
	pkgws "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/websocket"
)

// TestProxyClient_HostileResponsesLeaveNoSecret sends every hostile Location
// value to the ticket POST the proxy client makes. The client refuses every
// redirect, so the error must hold none of the secrets in the header and the
// request must be sent once.
func TestProxyClient_HostileResponsesLeaveNoSecret(t *testing.T) {
	t.Parallel()

	for _, hostile := range redirecttest.Cases(redirecttest.DeadAddr(t)) {
		t.Run(hostile.Name, func(t *testing.T) {
			t.Parallel()

			server := redirecttest.Serve(t, http.StatusTemporaryRedirect, hostile.Location)

			client, err := pkgws.NewProxyClient(&pkgws.ProxyConfig{
				Host:      "127.0.0.1",
				Port:      portOf(t, server.Addr),
				Scheme:    testSchemeHTTP,
				Insecure:  true,
				Ticket:    testTicket,
				CSRFToken: testCSRF,
			})
			if err != nil {
				t.Fatalf("NewProxyClient: %v", err)
			}

			_, err = client.NodeTermProxy(context.Background(), "pve1")
			redirecttest.AssertClean(t, err)

			if got := server.Hits(); got != 1 {
				t.Errorf("server read %d requests, want 1", got)
			}
		})
	}
}

// portOf returns the port of a host:port address.
func portOf(t *testing.T, addr string) int {
	t.Helper()

	_, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", addr, err)
	}

	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("port of %q: %v", addr, err)
	}

	return port
}

// TestProxyClient_TransportErrorsLeaveNoUserInformation configures a host with
// user information and points it at a port nothing listens on. The error from
// the ticket POST must show the URL as scheme, host, and path only.
func TestProxyClient_TransportErrorsLeaveNoUserInformation(t *testing.T) {
	t.Parallel()

	host, portText, err := net.SplitHostPort(redirecttest.DeadAddr(t))
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}

	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("Atoi: %v", err)
	}

	client, err := pkgws.NewProxyClient(&pkgws.ProxyConfig{
		Host:      "user:" + redirecttest.SecretUser + "@" + host,
		Port:      port,
		Scheme:    testSchemeHTTP,
		Insecure:  true,
		Ticket:    testTicket,
		CSRFToken: testCSRF,
	})
	if err != nil {
		t.Fatalf("NewProxyClient: %v", err)
	}

	_, err = client.NodeTermProxy(context.Background(), "pve1")
	redirecttest.AssertClean(t, err)

	if !strings.Contains(err.Error(), "dial tcp") {
		t.Errorf("error = %v, want the dial failure kept", err)
	}
}

// TestProxyClient_RefusesARedirectWithoutALocation checks a 3xx answer with no
// Location header is refused, which net/http alone hands back as a response.
func TestProxyClient_RefusesARedirectWithoutALocation(t *testing.T) {
	t.Parallel()

	server := redirecttest.Serve(t, http.StatusFound, nil)

	client, err := pkgws.NewProxyClient(&pkgws.ProxyConfig{
		Host: "127.0.0.1", Port: portOf(t, server.Addr), Scheme: testSchemeHTTP, Ticket: testTicket, CSRFToken: testCSRF,
	})
	if err != nil {
		t.Fatalf("NewProxyClient: %v", err)
	}

	_, err = client.NodeTermProxy(context.Background(), "pve1")
	if err == nil || !strings.Contains(err.Error(), "redirect refused") {
		t.Errorf("error = %v, want a redirect refusal", err)
	}
}
