package network_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/api/network"
)

// ---------- BridgeExists: non-list data → false ----------

func TestCovBridgeExistsNonListData(t *testing.T) {
	t.Parallel()

	// Server returns scalar "data" instead of array.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			keyData:    "unexpected-scalar",
			keySuccess: 1,
		})
	}))
	defer srv.Close()

	cli := createTestClient(t, srv.URL)
	svc := network.New(cli)

	exists, err := svc.BridgeExists(context.Background(), "n1", "vmbr0")
	if err != nil {
		t.Fatalf("BridgeExists: %v", err)
	}

	if exists {
		t.Error("expected false for non-list data, got true")
	}
}

// ---------- BridgeExists: GET error ----------

func TestCovBridgeExistsGETError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cli := createTestClient(t, srv.URL)
	svc := network.New(cli)

	_, err := svc.BridgeExists(context.Background(), "n1", "vmbr0")
	if err == nil {
		t.Fatal("expected error on GET 500, got nil")
	}
}

// ---------- DeleteBridge: BridgeExists returns error ----------

func TestCovDeleteBridgeBridgeExistsError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cli := createTestClient(t, srv.URL)
	svc := network.New(cli)

	err := svc.DeleteBridge(context.Background(), "n1", "vmbr0")
	if err == nil {
		t.Fatal("expected error when BridgeExists fails, got nil")
	}
}

// ---------- DeleteBridge: not exists → no-op ----------

func TestCovDeleteBridgeNotExistsNoOp(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Return empty list — bridge does not exist.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			keyData:    []interface{}{},
			keySuccess: 1,
		})
	}))
	defer srv.Close()

	cli := createTestClient(t, srv.URL)
	svc := network.New(cli)

	err := svc.DeleteBridge(context.Background(), "n1", "vmbr99")
	if err != nil {
		t.Fatalf("DeleteBridge on non-existent bridge: %v", err)
	}
}

// ---------- DeleteBridge: delete error ----------

func TestCovDeleteBridgeDeleteError(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()

	// GET returns the bridge so exists=true.
	mux.HandleFunc("/api2/json/nodes/n1/network", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			keyData:    []interface{}{map[string]interface{}{keyIface: "vmbr5"}},
			keySuccess: 1,
		})
	})

	// DELETE returns 500.
	mux.HandleFunc("/api2/json/nodes/n1/network/vmbr5", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.Error(w, "method", http.StatusMethodNotAllowed)

			return
		}

		http.Error(w, "internal error", http.StatusInternalServerError)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli := createTestClient(t, srv.URL)
	svc := network.New(cli)

	err := svc.DeleteBridge(context.Background(), "n1", "vmbr5")
	if err == nil {
		t.Fatal("expected error on DELETE 500, got nil")
	}
}

// ---------- EnsureBridge: nil params uses defaults ----------

func TestCovEnsureBridgeNilParamsDefaults(t *testing.T) {
	t.Parallel()

	var gotType, gotIface string

	mux := http.NewServeMux()

	// GET returns empty list so bridge does not exist.
	mux.HandleFunc("/api2/json/nodes/n1/network", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				keyData:    []interface{}{},
				keySuccess: 1,
			})
		case http.MethodPost:
			// PVE params arrive as application/x-www-form-urlencoded.
			err := r.ParseForm()
			if err != nil {
				http.Error(w, "bad form", http.StatusBadRequest)

				return
			}

			gotType = r.FormValue("type")
			gotIface = r.FormValue("iface")

			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				keyData:    map[string]interface{}{"ok": true},
				keySuccess: 1,
			})
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli := createTestClient(t, srv.URL)
	svc := network.New(cli)

	err := svc.EnsureBridge(context.Background(), "n1", "vmbr10", nil)
	if err != nil {
		t.Fatalf("EnsureBridge: %v", err)
	}

	if gotType != "bridge" {
		t.Errorf("default type = %q, want bridge", gotType)
	}

	if gotIface != "vmbr10" {
		t.Errorf("default iface = %q, want vmbr10", gotIface)
	}
}

// ---------- EnsureBridge: already exists → no POST ----------

func TestCovEnsureBridgeAlreadyExists(t *testing.T) {
	t.Parallel()

	postCalled := false

	mux := http.NewServeMux()

	mux.HandleFunc("/api2/json/nodes/n1/network", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				keyData: []interface{}{
					map[string]interface{}{keyIface: "vmbr0"},
				},
				keySuccess: 1,
			})
		case http.MethodPost:
			postCalled = true

			http.Error(w, "should not be called", http.StatusInternalServerError)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli := createTestClient(t, srv.URL)
	svc := network.New(cli)

	err := svc.EnsureBridge(context.Background(), "n1", "vmbr0", nil)
	if err != nil {
		t.Fatalf("EnsureBridge: %v", err)
	}

	if postCalled {
		t.Error("EnsureBridge POSTed even though bridge already existed")
	}
}

// ---------- EnsureBridge: POST error ----------

func TestCovEnsureBridgePostError(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()

	mux.HandleFunc("/api2/json/nodes/n1/network", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				keyData:    []interface{}{},
				keySuccess: 1,
			})
		case http.MethodPost:
			http.Error(w, "internal error", http.StatusInternalServerError)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	cli := createTestClient(t, srv.URL)
	svc := network.New(cli)

	err := svc.EnsureBridge(context.Background(), "n1", "vmbr11", nil)
	if err == nil {
		t.Fatal("expected error on POST 500, got nil")
	}
}

// ---------- Reload: 500 error ----------

func TestCovReload500Error(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cli := createTestClient(t, srv.URL)
	svc := network.New(cli)

	err := svc.Reload(context.Background(), "n1")
	if err == nil {
		t.Fatal("expected error on Reload 500, got nil")
	}
}
