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

const keyName = "name"

func TestSnapshots_List_Create_Delete_Rollback(t *testing.T) {
	t.Parallel()

	srv := setupSnapshotTestServer()
	defer srv.Close()

	client := createSnapshotTestClient(t, srv.URL)
	svc := qemu.New(client)

	testSnapshotList(t, svc)
	testSnapshotCreate(t, svc)
	testSnapshotDelete(t, svc)
	testSnapshotRollback(t, svc)
}

func setupSnapshotTestServer() *httptest.Server {
	mux := http.NewServeMux()
	setupSnapshotHandlers(mux)

	return httptest.NewServer(mux)
}

func setupSnapshotHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/api2/json/nodes/n1/qemu/321/snapshot", handleSnapshotOperations)
	mux.HandleFunc("/api2/json/nodes/n1/qemu/321/snapshot/pre-upgrade", handleSnapshotDelete)
	mux.HandleFunc("/api2/json/nodes/n1/qemu/321/snapshot/base/rollback", handleSnapshotRollback)
}

func handleSnapshotOperations(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		err := json.NewEncoder(writer).Encode(map[string]any{keyData: []any{
			map[string]any{keyName: "base", "vmstate": 0},
			map[string]any{keyName: "pre-upgrade", "vmstate": 0},
		}, keySuccess: 1})
		if err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
		}
	case http.MethodPost:
		err := json.NewEncoder(writer).Encode(map[string]any{keyData: "UPID:create-snap", keySuccess: 1})
		if err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
		}
	default:
		http.Error(writer, "method", http.StatusMethodNotAllowed)
	}
}

func handleSnapshotDelete(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodDelete {
		err := json.NewEncoder(writer).Encode(map[string]any{keyData: map[string]any{"ok": true}, keySuccess: 1})
		if err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
		}

		return
	}

	http.Error(writer, "method", http.StatusMethodNotAllowed)
}

func handleSnapshotRollback(writer http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodPost {
		err := json.NewEncoder(writer).Encode(map[string]any{keyData: "UPID:rollback", keySuccess: 1})
		if err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
		}

		return
	}

	http.Error(writer, "method", http.StatusMethodNotAllowed)
}

//nolint:ireturn // test helper returns interface required by qemu.New
func createSnapshotTestClient(t *testing.T, serverURL string) pveclient.Client {
	t.Helper()

	parsed, _ := url.Parse(serverURL)
	host := strings.Split(parsed.Host, ":")[0]
	port := 0

	if parts := strings.Split(parsed.Host, ":"); len(parts) == 2 {
		p, _ := strconv.Atoi(parts[1])
		port = p
	}

	cli, err := pveclient.NewClient(pveclient.Options{Host: host, Port: port, Protocol: "http", APIToken: "u@pam!tok=sec"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	return cli
}

func testSnapshotList(t *testing.T, svc qemu.Service) {
	t.Helper()

	snaps, err := svc.ListSnapshots(context.Background(), "n1", 321)
	if err != nil {
		t.Fatalf("list: %v", err)
	}

	if len(snaps) != 2 {
		t.Fatalf("expected 2, got %d", len(snaps))
	}
}

func testSnapshotCreate(t *testing.T, svc qemu.Service) {
	t.Helper()

	upid, err := svc.Snapshot(context.Background(), "n1", 321, "pre-migrate", nil)
	if err != nil || upid == "" {
		t.Fatalf("snapshot upid: %v %s", err, upid)
	}
}

func testSnapshotDelete(t *testing.T, svc qemu.Service) {
	t.Helper()

	err := svc.DeleteSnapshot(context.Background(), "n1", 321, "pre-upgrade")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
}

func testSnapshotRollback(t *testing.T, svc qemu.Service) {
	t.Helper()

	upid, err := svc.RollbackSnapshot(context.Background(), "n1", 321, "base")
	if err != nil || upid == "" {
		t.Fatalf("rollback upid: %v %s", err, upid)
	}
}
