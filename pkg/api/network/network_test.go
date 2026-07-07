package network_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/network"
	pveclient "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

const (
	keyData    = "data"
	keySuccess = "success"
	keyIface   = "iface"
)

func TestBridgeExistsAndEnsure(t *testing.T) {
	t.Parallel()

	ensured := false
	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/nodes/n1/network", func(writer http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(writer).Encode(map[string]any{keyData: []any{
				map[string]any{keyIface: "lo"},
				map[string]any{keyIface: "vmbr0"},
			}, keySuccess: 1})
		case http.MethodPost:
			ensured = true
			_ = json.NewEncoder(writer).Encode(map[string]any{keyData: map[string]any{"ok": true}, keySuccess: 1})
		default:
			http.Error(writer, "method", http.StatusMethodNotAllowed)
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	parsed, _ := url.Parse(srv.URL)
	host := strings.Split(parsed.Host, ":")[0]

	port := 0
	if parts := strings.Split(parsed.Host, ":"); len(parts) == 2 {
		port, _ = strconv.Atoi(parts[1])
	}

	cli, err := pveclient.NewClient(pveclient.Options{Host: host, Port: port, Protocol: "http", APIToken: "u@pam!tok=sec"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := network.New(cli)

	exists, err := svc.BridgeExists(context.Background(), "n1", "vmbr0")
	if err != nil {
		t.Fatalf("exists: %v", err)
	}

	if !exists {
		t.Fatalf("expected vmbr0 to exist")
	}

	err = svc.EnsureBridge(context.Background(), "n1", "vmbr1", map[string]interface{}{"autostart": 1})
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}

	if !ensured {
		t.Fatalf("expected ensure POST")
	}
}

func TestReloadAndDeleteBridge(t *testing.T) {
	t.Parallel()

	state := &testState{}

	srv := setupTestServer(state)
	defer srv.Close()

	client := createTestClient(t, srv.URL)
	svc := network.New(client)

	testBridgeDeletion(t, svc, state)
	testNetworkReload(t, svc, state)
}

type testState struct {
	deleted  bool
	reloaded bool
}

func setupTestServer(state *testState) *httptest.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/api2/json/nodes/n1/network", func(writer http.ResponseWriter, request *http.Request) {
		handleNetworkRequest(writer, request, state)
	})

	mux.HandleFunc("/api2/json/nodes/n1/network/vmbr9", func(writer http.ResponseWriter, request *http.Request) {
		handleBridgeRequest(writer, request, state)
	})

	return httptest.NewServer(mux)
}

func handleNetworkRequest(writer http.ResponseWriter, request *http.Request, state *testState) {
	switch request.Method {
	case http.MethodGet:
		err := json.NewEncoder(writer).Encode(map[string]any{keyData: []any{
			map[string]any{keyIface: "vmbr9"},
		}, keySuccess: 1})
		if err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
		}
	case http.MethodPost:
		state.reloaded = true

		err := json.NewEncoder(writer).Encode(map[string]any{keyData: map[string]any{"ok": true}, keySuccess: 1})
		if err != nil {
			http.Error(writer, err.Error(), http.StatusInternalServerError)
		}
	default:
		http.Error(writer, "method", http.StatusMethodNotAllowed)
	}
}

func handleBridgeRequest(writer http.ResponseWriter, request *http.Request, state *testState) {
	if request.Method != http.MethodDelete {
		http.Error(writer, "method", http.StatusMethodNotAllowed)

		return
	}

	state.deleted = true

	err := json.NewEncoder(writer).Encode(map[string]any{keyData: map[string]any{"ok": true}, keySuccess: 1})
	if err != nil {
		http.Error(writer, err.Error(), http.StatusInternalServerError)
	}
}

//nolint:ireturn // Test helper - returns interface for test setup
func createTestClient(t *testing.T, serverURL string) pveclient.Client {
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

func testBridgeDeletion(t *testing.T, svc network.Service, state *testState) {
	t.Helper()

	err := svc.DeleteBridge(context.Background(), "n1", "vmbr9")
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	if !state.deleted {
		t.Fatalf("expected delete to be called")
	}
}

func testNetworkReload(t *testing.T, svc network.Service, state *testState) {
	t.Helper()

	err := svc.Reload(context.Background(), "n1")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	if !state.reloaded {
		t.Fatalf("expected reload to be called")
	}
}
