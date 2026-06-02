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

// TestDetachDiskClearsUnusedSlot verifies that DetachDisk issues a second
// PUT to remove the unusedN slot PVE auto-creates when a disk is removed
// from its bus slot. This prevents a subsequent DELETE /qemu/{vmid} from
// destroying the underlying volume.
func TestDetachDiskClearsUnusedSlot(t *testing.T) {
	t.Parallel()

	const volid = "data:vm-9000-disk-0"

	// Stateful mock: first GET returns scsi1 holding the disk. After the
	// initial delete:scsi1 PUT, the next GET returns the disk demoted to
	// unused0 (PVE's documented behavior). After the delete:unused0 PUT,
	// the config is empty.
	var (
		deleteCalls []string
		state       = map[string]string{"scsi1": volid}
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/nodes/testnode/qemu/123/config", func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			data := map[string]any{}
			for k, v := range state {
				data[k] = v
			}

			_ = json.NewEncoder(writer).Encode(map[string]any{"data": data, "success": 1})

		case http.MethodPut:
			_ = request.ParseForm()
			deletes := request.PostForm.Get("delete")
			deleteCalls = append(deleteCalls, deletes)
			delete(state, deletes)
			// First delete moves disk to unused0; second clears it.
			if deletes == "scsi1" {
				state["unused0"] = volid
			}

			_ = json.NewEncoder(writer).Encode(map[string]any{"data": map[string]any{"ok": true}, "success": 1})

		default:
			http.Error(writer, "method", http.StatusMethodNotAllowed)
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := qemu.New(cli)

	if err := svc.DetachDisk(context.Background(), "testnode", 123, "scsi1"); err != nil {
		t.Fatalf("DetachDisk: %v", err)
	}

	if len(deleteCalls) != 2 {
		t.Fatalf("expected 2 PUT delete calls, got %d (%v)", len(deleteCalls), deleteCalls)
	}

	if deleteCalls[0] != "scsi1" {
		t.Fatalf("first delete should target scsi1, got %q", deleteCalls[0])
	}

	if deleteCalls[1] != "unused0" {
		t.Fatalf("second delete should target unused0, got %q", deleteCalls[1])
	}

	if _, ok := state["unused0"]; ok {
		t.Fatalf("unused0 should have been cleared, state=%v", state)
	}
}

// TestDetachDiskUnusedSlotIdempotent verifies that calling DetachDisk on
// an unusedN slot directly does NOT trigger the secondary cleanup loop,
// since the disk is already in the slot we want gone.
func TestDetachDiskUnusedSlotIdempotent(t *testing.T) {
	t.Parallel()

	var (
		deleteCalls []string
		state       = map[string]string{"unused0": "data:vm-9000-disk-0"}
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/nodes/testnode/qemu/123/config", func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			data := map[string]any{}
			for k, v := range state {
				data[k] = v
			}

			_ = json.NewEncoder(writer).Encode(map[string]any{"data": data, "success": 1})

		case http.MethodPut:
			_ = request.ParseForm()
			deletes := request.PostForm.Get("delete")
			deleteCalls = append(deleteCalls, deletes)
			delete(state, deletes)

			_ = json.NewEncoder(writer).Encode(map[string]any{"data": map[string]any{"ok": true}, "success": 1})

		default:
			http.Error(writer, "method", http.StatusMethodNotAllowed)
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := qemu.New(cli)

	if err := svc.DetachDisk(context.Background(), "testnode", 123, "unused0"); err != nil {
		t.Fatalf("DetachDisk: %v", err)
	}

	if len(deleteCalls) != 1 {
		t.Fatalf("expected exactly one PUT delete, got %d (%v)", len(deleteCalls), deleteCalls)
	}

	if deleteCalls[0] != "unused0" {
		t.Fatalf("delete should target unused0, got %q", deleteCalls[0])
	}
}
