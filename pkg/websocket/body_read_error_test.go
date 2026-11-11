package websocket_test

import (
	"context"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect/redirecttest"
	pkgws "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/websocket"
)

// TestProxyClient_BodyReadErrorLeavesNoServerText answers the ticket POST with
// a reply whose trailer line the transport cannot read. The error must hold
// none of the trailer's bytes.
func TestProxyClient_BodyReadErrorLeavesNoServerText(t *testing.T) {
	t.Parallel()

	server := redirecttest.ServeRaw(t, redirecttest.BadTrailer(`{"data":{}}`))

	client, err := pkgws.NewProxyClient(&pkgws.ProxyConfig{
		Host: "127.0.0.1", Port: portOf(t, server.Addr), Scheme: testSchemeHTTP, Insecure: true,
		Ticket: testTicket, CSRFToken: testCSRF,
	})
	if err != nil {
		t.Fatalf("NewProxyClient: %v", err)
	}

	_, err = client.NodeTermProxy(context.Background(), "pve1")
	if err == nil {
		t.Fatal("got no error, want the body read to fail")
	}

	redirecttest.AssertNoTrailerText(t, err.Error())
}
