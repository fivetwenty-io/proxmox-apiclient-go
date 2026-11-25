package nodes_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	pveclient "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/pdm/nodes"
)

// TestListDnsDecodesAllOfReturns covers a returns schema that upstream wraps
// in allOf, so the flattened response type must expose every member's fields.
func TestListDnsDecodesAllOfReturns(t *testing.T) {
	t.Parallel()

	var path string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"digest":"abc123","dns1":"192.0.2.1","dns2":"192.0.2.2",` +
			`"options":"edns0","search":"example.com"},"success":1}`))
	}))
	t.Cleanup(srv.Close)

	parsed, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	port, _ := strconv.Atoi(parsed.Port())

	client, err := pveclient.NewClient(pveclient.Options{
		Host: parsed.Hostname(), Port: port, Protocol: "http", APIToken: "user@pam!tok=sec",
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	resp, err := nodes.New(client).ListDns(context.Background(), "pdm1")
	if err != nil {
		t.Fatalf("ListDns: %v", err)
	}

	if path != "/api2/json/nodes/pdm1/dns" {
		t.Errorf("path = %q", path)
	}

	if resp.Digest != "abc123" || resp.Dns1 == nil || *resp.Dns1 != "192.0.2.1" ||
		resp.Dns2 == nil || *resp.Dns2 != "192.0.2.2" || resp.Dns3 != nil ||
		resp.Options == nil || *resp.Options != "edns0" || resp.Search == nil || *resp.Search != "example.com" {
		t.Errorf("unexpected response: %+v", resp)
	}
}
