package storage_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/storage"
	pveclient "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

const (
	covKeyData    = "data"
	covKeySuccess = "success"
)

// ---------- CreateVolume ----------

func TestCovCreateVolumeSizeZeroError(t *testing.T) {
	t.Parallel()

	// Server never hit for invalid input.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cli, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := storage.New(cli)

	_, err = svc.CreateVolume(context.Background(), "node1", "local", 0, "", 100, "vm-100-disk-0")
	if err == nil {
		t.Fatal("expected error for sizeGiB=0, got nil")
	}
}

func TestCovCreateVolumeSizeNegativeError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cli, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := storage.New(cli)

	_, err = svc.CreateVolume(context.Background(), "node1", "local", -5, "", 100, "vm-100-disk-0")
	if err == nil {
		t.Fatal("expected error for sizeGiB<0, got nil")
	}
}

func TestCovCreateVolumeStringResponse(t *testing.T) {
	t.Parallel()

	const wantVolID = "local:vm-100-disk-0"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "want POST", http.StatusMethodNotAllowed)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{covKeyData: wantVolID, covKeySuccess: 1})
	}))
	defer srv.Close()

	cli, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := storage.New(cli)

	volid, err := svc.CreateVolume(context.Background(), "node1", "local", 10, "raw", 100, "vm-100-disk-0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if volid != wantVolID {
		t.Errorf("volid = %q, want %q", volid, wantVolID)
	}
}

func TestCovCreateVolumeMapResponse(t *testing.T) {
	t.Parallel()

	const wantVolID = "local:vm-200-disk-0"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "want POST", http.StatusMethodNotAllowed)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			covKeyData:    map[string]interface{}{"volid": wantVolID},
			covKeySuccess: 1,
		})
	}))
	defer srv.Close()

	cli, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := storage.New(cli)

	volid, err := svc.CreateVolume(context.Background(), "node1", "local", 20, "", 200, "vm-200-disk-0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if volid != wantVolID {
		t.Errorf("volid = %q, want %q", volid, wantVolID)
	}
}

func TestCovCreateVolumeCorrectPath(t *testing.T) {
	t.Parallel()

	var gotPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{covKeyData: "local:vm-300-disk-0", covKeySuccess: 1})
	}))
	defer srv.Close()

	cli, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := storage.New(cli)

	_, err = svc.CreateVolume(context.Background(), "node1", "local", 5, "", 300, "vm-300-disk-0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantPath := "/api2/json/nodes/node1/storage/local/content"
	if gotPath != wantPath {
		t.Errorf("path = %q, want %q", gotPath, wantPath)
	}
}

// ---------- Exists 500 wraps error ----------

func TestCovExists500WrapsErr(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cli, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := storage.New(cli)

	ok, err := svc.Exists(context.Background(), "node1", "local", "local:vm-100-disk-0")
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}

	if ok {
		t.Error("expected ok=false on 500")
	}
}

// ---------- DeleteVolumeAsync non-404 error ----------

func TestCovDeleteVolumeAsyncServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cli, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := storage.New(cli)

	_, err = svc.DeleteVolumeAsync(context.Background(), "node1", "local", "local:vm-100-disk-0")
	if err == nil {
		t.Fatal("expected error on 500, got nil")
	}
}
