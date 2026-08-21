package client_test

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	pve "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

// TestDialContext_ReachesAHostThatDoesNotResolve is the reason Options gained
// a DialContext: a PVE host behind an ssh jump host or a tunnel is not
// directly routable, so no amount of timeout tuning helps. The dial function
// is the seam. Here the configured host does not resolve at all, and the
// request still succeeds, which it can only do by going through the supplied
// dialer.
func TestDialContext_ReachesAHostThatDoesNotResolve(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api2/json") {
			http.NotFound(w, r)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"version":"test"},"success":1}`)
	}))
	defer srv.Close()

	srvAddr := strings.TrimPrefix(srv.URL, "http://")

	var dialed atomic.Int64

	opts := pve.Options{
		// .invalid is reserved by RFC 2606 and never resolves, so a
		// direct dial cannot accidentally make this test pass.
		Host:     "unroutable.invalid",
		Port:     8006,
		Protocol: testProtoHTTP,
		APIToken: testAPIToken,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dialed.Add(1)

			var d net.Dialer

			return d.DialContext(ctx, network, srvAddr)
		},
	}

	cli, err := pve.NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = cli.Get("/version", nil)
	if err != nil {
		t.Fatalf("Get(/version) through the supplied dialer failed: %v", err)
	}

	if dialed.Load() == 0 {
		t.Error("the client did not dial through Options.DialContext")
	}
}

// TestDialContext_UnsetStillDialsDirectly guards the default. The field is
// opt-in, so a client that does not set it must behave exactly as it did
// before the field existed.
func TestDialContext_UnsetStillDialsDirectly(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":{"version":"test"},"success":1}`)
	}))
	defer srv.Close()

	host, port := parseHostPort(srv.URL)

	cli, err := pve.NewClient(pve.Options{
		Host:     host,
		Port:     port,
		Protocol: testProtoHTTP,
		APIToken: testAPIToken,
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = cli.Get("/version", nil)
	if err != nil {
		t.Fatalf("Get(/version) with no DialContext failed: %v", err)
	}
}
