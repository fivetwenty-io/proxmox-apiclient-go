package qemu_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/api/qemu"
	pveclient "github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/client"
)

// helper to build client.Options from test server URL.
func optsFromServerURL(u string) pveclient.Options {
	parsed, _ := url.Parse(u)
	host := strings.Split(parsed.Host, ":")[0]
	port := 0

	if parts := strings.Split(parsed.Host, ":"); len(parts) == 2 {
		p, _ := strconv.Atoi(parts[1])
		port = p
	}

	return pveclient.Options{Host: host, Port: port, Protocol: "http", APIToken: "user@pam!tok=sec"}
}

func TestQemuDiskAttachDetachResize(t *testing.T) {
	t.Parallel()
	// Fake PVE API server
	mux := http.NewServeMux()

	// GET config returns existing scsi0
	mux.HandleFunc("/api2/json/nodes/testnode/qemu/123/config", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet {
			_ = json.NewEncoder(writer).Encode(map[string]any{"data": map[string]any{"scsi0": "local-lvm:vm-123-disk-0"}, "success": 1})

			return
		}
		// PUT attach or delete
		if request.Method == http.MethodPut {
			_ = json.NewEncoder(writer).Encode(map[string]any{"data": map[string]any{"ok": true}, "success": 1})

			return
		}

		http.Error(writer, "method", http.StatusMethodNotAllowed)
	})

	// PUT resize endpoint (PVE uses PUT for /resize, not POST)
	mux.HandleFunc("/api2/json/nodes/testnode/qemu/123/resize", func(writer http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(writer, "method", http.StatusMethodNotAllowed)

			return
		}

		_ = json.NewEncoder(writer).Encode(map[string]any{"data": "UPID:test:1", "success": 1})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Build client and service
	cli, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := qemu.New(cli)

	// Attach without DiskID, should pick scsi1
	diskID, err := svc.AttachDisk(context.Background(), "testnode", 123, "local-lvm:vm-123-disk-1", "scsi", nil)
	if err != nil {
		t.Fatalf("AttachDisk error: %v", err)
	}

	if diskID != "scsi1" {
		t.Fatalf("expected scsi1, got %s", diskID)
	}

	// Detach scsi0
	err = svc.DetachDisk(context.Background(), "testnode", 123, "scsi0")
	if err != nil {
		t.Fatalf("DetachDisk: %v", err)
	}

	// Resize scsi0
	upid, err := svc.ResizeDisk(context.Background(), "testnode", 123, "scsi0", 2)
	if err != nil {
		t.Fatalf("ResizeDisk: %v", err)
	}

	if upid == "" {
		t.Fatalf("expected upid")
	}
}
