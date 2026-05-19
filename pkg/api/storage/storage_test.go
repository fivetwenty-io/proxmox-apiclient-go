package storage_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/api/storage"
	pveclient "github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/client"
)

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

func TestExistsNotFound(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cli, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := storage.New(cli)

	ok, err := svc.Exists(context.Background(), "node1", "local", "vol/doesnotexist")
	if err != nil {
		t.Fatalf("exists err: %v", err)
	}

	if ok {
		t.Fatalf("expected false")
	}
}

func TestDeleteVolumeIgnoresNotFound(t *testing.T) {
	t.Parallel()

	// DELETE returns 404 Not Found
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cli, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := storage.New(cli)

	err = svc.DeleteVolume(context.Background(), "node1", "local", "does/not/exist")
	if err != nil {
		t.Fatalf("delete should ignore 404, got: %v", err)
	}
}

func TestDeleteVolumeIfExistsHappyPath(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":null}`))
	}))
	defer srv.Close()

	cli, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := storage.New(cli)

	existed, err := svc.DeleteVolumeIfExists(context.Background(), "node1", "local", "local:vm-100-disk-0")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !existed {
		t.Fatalf("expected existed=true, got false")
	}
}

func TestDeleteVolumeIfExistsNotFound(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	cli, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := storage.New(cli)

	existed, err := svc.DeleteVolumeIfExists(context.Background(), "node1", "local", "does/not/exist")
	if err != nil {
		t.Fatalf("expected nil error on 404, got: %v", err)
	}

	if existed {
		t.Fatalf("expected existed=false, got true")
	}
}

func TestDeleteVolumeIfExistsServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	cli, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := storage.New(cli)

	_, err = svc.DeleteVolumeIfExists(context.Background(), "node1", "local", "local:vm-100-disk-0")
	if err == nil {
		t.Fatalf("expected non-nil error on 500, got nil")
	}
}

func TestUploadHappyPath(t *testing.T) {
	t.Parallel()

	const wantUPID = "UPID:node1:00001234:DEADBEEF:67890ABC:upload::root@pam:"

	var (
		capturedPath    string
		capturedContent string
		capturedFile    string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path

		if err := r.ParseMultipartForm(10 << 20); err != nil {
			http.Error(w, "bad multipart", http.StatusBadRequest)
			return
		}

		capturedContent = r.FormValue("content")
		// PVE rejects with 400 when "filename" appears both as a form field
		// AND as the multipart file part name. Assert that "filename" is
		// transmitted ONLY as the file part filename attribute (not as a
		// duplicate form field) by reading r.MultipartForm.File directly.
		if files, ok := r.MultipartForm.File["filename"]; ok && len(files) > 0 {
			capturedFile = files[0].Filename
		}
		if r.MultipartForm.Value["filename"] != nil {
			http.Error(w, "duplicate filename field rejected by PVE", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		resp := map[string]interface{}{"data": wantUPID}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cli, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := storage.New(cli)

	upid, err := svc.Upload(context.Background(), "node1", "local", "iso", "debian-12.iso", strings.NewReader("fake-iso-data"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if upid != wantUPID {
		t.Fatalf("upid: want %q, got %q", wantUPID, upid)
	}

	wantPath := "/api2/json/nodes/node1/storage/local/upload"
	if capturedPath != wantPath {
		t.Fatalf("path: want %q, got %q", wantPath, capturedPath)
	}

	if capturedContent != "iso" {
		t.Fatalf("content field: want %q, got %q", "iso", capturedContent)
	}

	if capturedFile != "debian-12.iso" {
		t.Fatalf("filename field: want %q, got %q", "debian-12.iso", capturedFile)
	}
}

func TestUploadUPIDFromMap(t *testing.T) {
	t.Parallel()

	const wantUPID = "UPID:node1:00001234:DEADBEEF:67890ABC:upload::root@pam:"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			http.Error(w, "bad multipart", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		resp := map[string]interface{}{"data": map[string]interface{}{"upid": wantUPID}}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	cli, err := pveclient.NewClient(optsFromServerURL(srv.URL))
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := storage.New(cli)

	upid, err := svc.Upload(context.Background(), "node1", "local", "iso", "debian-12.iso", strings.NewReader("fake-iso-data"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if upid != wantUPID {
		t.Fatalf("upid: want %q, got %q", wantUPID, upid)
	}
}

// Ensure io import is used — referenced by Upload signature tests above.
var _ io.Reader = (*strings.Reader)(nil)
