package cloudinit_test

import (
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/cloudinit"
)

func TestBuildIPConfigsFromCPISpec(t *testing.T) {
	t.Parallel()

	svc := cloudinit.New(nil)
	spec := map[string]any{
		"interfaces": []any{
			map[string]any{"dhcp": true},
			map[string]any{"address": "192.168.1.50/24", "gateway": "192.168.1.1"},
		},
		"nameservers": []any{"8.8.8.8", "8.8.4.4"},
	}

	cfg, err := svc.BuildIPConfigsFromCPISpec(spec)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if cfg["ipconfig0"] != "ip=dhcp" {
		t.Fatalf("ipconfig0: %s", cfg["ipconfig0"])
	}

	if cfg["ipconfig1"] != "ip=192.168.1.50/24,gw=192.168.1.1" {
		t.Fatalf("ipconfig1: %s", cfg["ipconfig1"])
	}

	if cfg["nameserver"] != "8.8.8.8 8.8.4.4" {
		t.Fatalf("nameserver: %s", cfg["nameserver"])
	}
}
