package pve_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	pveclient "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/pdm/pve"
)

// TestListRemotesNodesStatusDecodesIdleAndUptime covers the idle and uptime fields
// that this node status response gained alongside its existing ones.
func TestListRemotesNodesStatusDecodesIdleAndUptime(t *testing.T) {
	t.Parallel()

	var path string

	payload := `{"data":{"boot-info":{"mode":"efi"},"cpu":0.05,"cpuinfo":{"cpus":8},` +
		`"current-kernel":{"release":"6.17"},"idle":123456,"loadavg":["0.1","0.2","0.3"],` +
		`"memory":{"total":1},"pveversion":"pve-manager/9.2.21","rootfs":{"total":1},"uptime":86400},"success":1}`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(payload))
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

	resp, err := pve.New(client).ListRemotesNodesStatus(context.Background(), "cluster1", "node1")
	if err != nil {
		t.Fatalf("ListRemotesNodesStatus: %v", err)
	}

	if path != "/api2/json/pve/remotes/cluster1/nodes/node1/status" {
		t.Errorf("path = %q", path)
	}

	if resp.Idle.Int() != 123456 || resp.Uptime.Int() != 86400 {
		t.Errorf("idle/uptime = %d/%d", resp.Idle.Int(), resp.Uptime.Int())
	}

	if resp.Pveversion != "pve-manager/9.2.21" || float64(resp.Cpu) != 0.05 || len(resp.Loadavg) != 3 {
		t.Errorf("existing fields not decoded: %+v", resp)
	}
}
