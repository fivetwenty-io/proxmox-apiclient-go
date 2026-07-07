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

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/qemu"
	pveclient "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

const (
	keyData    = "data"
	keySuccess = "success"
	diskScsi1  = "scsi1"
	diskUnused = "unused0"
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
			_ = json.NewEncoder(writer).Encode(map[string]any{keyData: map[string]any{"scsi0": "local-lvm:vm-123-disk-0"}, keySuccess: 1})

			return
		}
		// PUT attach or delete
		if request.Method == http.MethodPut {
			_ = json.NewEncoder(writer).Encode(map[string]any{keyData: map[string]any{"ok": true}, keySuccess: 1})

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

		_ = json.NewEncoder(writer).Encode(map[string]any{keyData: "UPID:test:1", keySuccess: 1})
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

	if diskID != diskScsi1 {
		t.Fatalf("expected %s, got %s", diskScsi1, diskID)
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

	deleteCalls, state := setupDetachClearsUnusedSlotServer(t, volid)

	svc := qemu.New(newClientFromTestServer(t, state))

	err := svc.DetachDisk(context.Background(), "testnode", 123, diskScsi1)
	if err != nil {
		t.Fatalf("DetachDisk: %v", err)
	}

	assertDetachClearsUnusedSlot(t, deleteCalls, state.stateMap)
}

type detachTestState struct {
	srv      *httptest.Server
	stateMap map[string]string
}

func setupDetachClearsUnusedSlotServer(t *testing.T, volid string) (*[]string, *detachTestState) {
	t.Helper()

	var deleteCalls []string

	stateMap := map[string]string{diskScsi1: volid}

	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/nodes/testnode/qemu/123/config", func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			data := map[string]any{}
			for k, v := range stateMap {
				data[k] = v
			}

			_ = json.NewEncoder(writer).Encode(map[string]any{keyData: data, keySuccess: 1})

		case http.MethodPut:
			_ = request.ParseForm()
			deletes := request.PostForm.Get("delete")
			deleteCalls = append(deleteCalls, deletes)
			delete(stateMap, deletes)
			// First delete moves disk to unused0; second clears it.
			if deletes == diskScsi1 {
				stateMap[diskUnused] = volid
			}

			_ = json.NewEncoder(writer).Encode(map[string]any{keyData: map[string]any{"ok": true}, keySuccess: 1})

		default:
			http.Error(writer, "method", http.StatusMethodNotAllowed)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &deleteCalls, &detachTestState{srv: srv, stateMap: stateMap}
}

//nolint:ireturn // test helper returns interface required by qemu.New
func newClientFromTestServer(t *testing.T, state *detachTestState) pveclient.Client {
	t.Helper()

	cli, err := pveclient.NewClient(optsFromServerURL(state.srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	return cli
}

func assertDetachClearsUnusedSlot(t *testing.T, deleteCalls *[]string, stateMap map[string]string) {
	t.Helper()

	calls := *deleteCalls

	if len(calls) != 2 {
		t.Fatalf("expected 2 PUT delete calls, got %d (%v)", len(calls), calls)
	}

	if calls[0] != diskScsi1 {
		t.Fatalf("first delete should target %s, got %q", diskScsi1, calls[0])
	}

	if calls[1] != diskUnused {
		t.Fatalf("second delete should target %s, got %q", diskUnused, calls[1])
	}

	if _, ok := stateMap[diskUnused]; ok {
		t.Fatalf("%s should have been cleared, state=%v", diskUnused, stateMap)
	}
}

// TestDetachDiskUnusedSlotIdempotent verifies that calling DetachDisk on
// an unusedN slot directly does NOT trigger the secondary cleanup loop,
// since the disk is already in the slot we want gone.
func TestDetachDiskUnusedSlotIdempotent(t *testing.T) {
	t.Parallel()

	var (
		deleteCalls []string
		state       = map[string]string{diskUnused: "data:vm-9000-disk-0"}
	)

	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/nodes/testnode/qemu/123/config", func(writer http.ResponseWriter, request *http.Request) {
		switch request.Method {
		case http.MethodGet:
			data := map[string]any{}
			for k, v := range state {
				data[k] = v
			}

			_ = json.NewEncoder(writer).Encode(map[string]any{keyData: data, keySuccess: 1})

		case http.MethodPut:
			_ = request.ParseForm()
			deletes := request.PostForm.Get("delete")
			deleteCalls = append(deleteCalls, deletes)
			delete(state, deletes)

			_ = json.NewEncoder(writer).Encode(map[string]any{keyData: map[string]any{"ok": true}, keySuccess: 1})

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

	err = svc.DetachDisk(context.Background(), "testnode", 123, diskUnused)
	if err != nil {
		t.Fatalf("DetachDisk: %v", err)
	}

	if len(deleteCalls) != 1 {
		t.Fatalf("expected exactly one PUT delete, got %d (%v)", len(deleteCalls), deleteCalls)
	}

	if deleteCalls[0] != diskUnused {
		t.Fatalf("delete should target %s, got %q", diskUnused, deleteCalls[0])
	}
}

// TestSnapshotPathParamsEscaped verifies that untrusted string path segments
// (node and snapshot name) are percent-encoded before being placed in the URL,
// preventing path traversal or query injection via a crafted value.
func TestSnapshotPathParamsEscaped(t *testing.T) {
	t.Parallel()

	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":null}`))
	}))
	defer srv.Close()

	cli, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := qemu.New(cli)

	delErr := svc.DeleteSnapshot(context.Background(), "no/de", 100, "a/b")
	if delErr != nil {
		t.Fatalf("DeleteSnapshot: %v", delErr)
	}

	if strings.Contains(gotPath, "no/de") || strings.Contains(gotPath, "snapshot/a/b") {
		t.Fatalf("path params not escaped: %q", gotPath)
	}

	if !strings.Contains(gotPath, "no%2Fde") || !strings.Contains(gotPath, "a%2Fb") {
		t.Fatalf("expected escaped node and name in path, got %q", gotPath)
	}
}
