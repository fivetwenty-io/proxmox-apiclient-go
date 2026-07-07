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

// Constants for strings that appear ≥3 times across the qemu_test package.
const (
	covProto   = "http"
	covKeyUPID = "upid"
	covVirtio0 = "virtio0"
	covScsi0   = "scsi0"
	covScsi2   = "scsi2"
	covDevSDA  = "/dev/sda"
)

// covPVEResp is a typed PVE envelope that satisfies errchkjson (no any fields).
type covPVEResp struct {
	Data    json.RawMessage `json:"data"`
	Success int             `json:"success"`
}

// covWriteRaw serialises v to JSON, wraps it in {"data":…,"success":1}, and writes it.
// json.Marshal(any) is intentional in this test helper.
//

func covWriteRaw(w http.ResponseWriter, v any) {
	raw, _ := json.Marshal(v) //nolint:errchkjson
	resp := covPVEResp{Data: json.RawMessage(raw), Success: 1}

	err := json.NewEncoder(w).Encode(resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// covClientFromURL constructs a pveclient.Client pointing at the given test server URL.
//
//nolint:ireturn // test helper returns interface required by qemu.New
func covClientFromURL(t *testing.T, rawURL string) pveclient.Client {
	t.Helper()

	parsed, _ := url.Parse(rawURL)
	host := strings.Split(parsed.Host, ":")[0]
	port := 0

	if parts := strings.Split(parsed.Host, ":"); len(parts) == 2 {
		p, _ := strconv.Atoi(parts[1])
		port = p
	}

	cli, err := pveclient.NewClient(pveclient.Options{
		Host:     host,
		Port:     port,
		Protocol: covProto,
		APIToken: "u@pam!tok=sec",
	})
	if err != nil {
		t.Fatalf("covClientFromURL: %v", err)
	}

	return cli
}

// ---- Create ----------------------------------------------------------------

func TestCovCreate_StringUPID(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)

			return
		}

		covWriteRaw(w, "UPID:create:string")
	}))
	defer srv.Close()

	svc := qemu.New(covClientFromURL(t, srv.URL))

	upid, err := svc.Create(context.Background(), "n1", map[string]interface{}{"vmid": 200})
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}

	if upid != "UPID:create:string" {
		t.Fatalf("expected UPID string, got %q", upid)
	}
}

func TestCovCreate_MapUPID(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		covWriteRaw(w, map[string]string{covKeyUPID: "UPID:create:map"})
	}))
	defer srv.Close()

	svc := qemu.New(covClientFromURL(t, srv.URL))

	upid, err := svc.Create(context.Background(), "n1", nil)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}

	if upid != "UPID:create:map" {
		t.Fatalf("expected UPID from map, got %q", upid)
	}
}

func TestCovCreate_NeitherStringNorMap(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		covWriteRaw(w, nil)
	}))
	defer srv.Close()

	svc := qemu.New(covClientFromURL(t, srv.URL))

	upid, err := svc.Create(context.Background(), "n1", nil)
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}

	if upid != "" {
		t.Fatalf("expected empty upid, got %q", upid)
	}
}

// ---- Status ----------------------------------------------------------------

func TestCovStatus_Map(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		covWriteRaw(w, map[string]any{"status": "running", "uptime": 42})
	}))
	defer srv.Close()

	svc := qemu.New(covClientFromURL(t, srv.URL))

	st, err := svc.Status(context.Background(), "n1", 100)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	if st["status"] != "running" {
		t.Fatalf("status field: %v", st["status"])
	}
}

func TestCovStatus_NonMap(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		covWriteRaw(w, "not-a-map")
	}))
	defer srv.Close()

	svc := qemu.New(covClientFromURL(t, srv.URL))

	st, err := svc.Status(context.Background(), "n1", 100)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	if len(st) != 0 {
		t.Fatalf("expected empty map, got %v", st)
	}
}

// ---- postUPID wrappers (Start/Stop/Reset/Clone/Template) -------------------

func covUPIDServer(t *testing.T, pathSuffix, upid string) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, pathSuffix) {
			http.Error(w, "unexpected: "+r.Method+" "+r.URL.Path, http.StatusBadRequest)

			return
		}

		covWriteRaw(w, upid)
	}))
}

func TestCovStart(t *testing.T) {
	t.Parallel()

	srv := covUPIDServer(t, "/status/start", "UPID:start")
	defer srv.Close()

	svc := qemu.New(covClientFromURL(t, srv.URL))

	upid, err := svc.Start(context.Background(), "n1", 100)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if upid != "UPID:start" {
		t.Fatalf("upid: %q", upid)
	}
}

func TestCovStop(t *testing.T) {
	t.Parallel()

	srv := covUPIDServer(t, "/status/stop", "UPID:stop")
	defer srv.Close()

	svc := qemu.New(covClientFromURL(t, srv.URL))

	upid, err := svc.Stop(context.Background(), "n1", 100)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if upid != "UPID:stop" {
		t.Fatalf("upid: %q", upid)
	}
}

func TestCovReset(t *testing.T) {
	t.Parallel()

	srv := covUPIDServer(t, "/status/reset", "UPID:reset")
	defer srv.Close()

	svc := qemu.New(covClientFromURL(t, srv.URL))

	upid, err := svc.Reset(context.Background(), "n1", 100)
	if err != nil {
		t.Fatalf("Reset: %v", err)
	}

	if upid != "UPID:reset" {
		t.Fatalf("upid: %q", upid)
	}
}

func TestCovClone(t *testing.T) {
	t.Parallel()

	srv := covUPIDServer(t, "/clone", "UPID:clone")
	defer srv.Close()

	svc := qemu.New(covClientFromURL(t, srv.URL))

	upid, err := svc.Clone(context.Background(), "n1", 100, map[string]interface{}{"newid": 200})
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}

	if upid != "UPID:clone" {
		t.Fatalf("upid: %q", upid)
	}
}

func TestCovTemplate(t *testing.T) {
	t.Parallel()

	srv := covUPIDServer(t, "/template", "UPID:template")
	defer srv.Close()

	svc := qemu.New(covClientFromURL(t, srv.URL))

	upid, err := svc.Template(context.Background(), "n1", 100)
	if err != nil {
		t.Fatalf("Template: %v", err)
	}

	if upid != "UPID:template" {
		t.Fatalf("upid: %q", upid)
	}
}

// ---- postUPID internals: map-with-upid and map-without-upid ----------------

func TestCovStart_MapUPID(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		covWriteRaw(w, map[string]string{covKeyUPID: "UPID:map-start"})
	}))
	defer srv.Close()

	svc := qemu.New(covClientFromURL(t, srv.URL))

	upid, err := svc.Start(context.Background(), "n1", 100)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if upid != "UPID:map-start" {
		t.Fatalf("upid: %q", upid)
	}
}

func TestCovStart_MapWithoutUPID(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		covWriteRaw(w, map[string]string{"other": "value"})
	}))
	defer srv.Close()

	svc := qemu.New(covClientFromURL(t, srv.URL))

	upid, err := svc.Start(context.Background(), "n1", 100)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if upid != "" {
		t.Fatalf("expected empty upid, got %q", upid)
	}
}

func TestCovStart_Error(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	svc := qemu.New(covClientFromURL(t, srv.URL))

	_, err := svc.Start(context.Background(), "n1", 100)
	if err == nil {
		t.Fatal("expected error from HTTP 500")
	}
}

// ---- GuessDevicePath (pure, table-driven) ----------------------------------

func TestCovGuessDevicePath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		diskID string
		want   string
	}{
		{covVirtio0, "/dev/vda"},
		{"virtio1", "/dev/vdb"},
		{"virtio25", "/dev/vdz"},
		{covScsi0, covDevSDA},
		{"scsi1", "/dev/sdb"},
		{covScsi2, "/dev/sdc"},
		{"sata0", covDevSDA},
		{"sata3", "/dev/sdd"},
		{"ide0", covDevSDA},
		{"ide1", "/dev/sdb"},
		{"unknown0", ""},
		{"notadisk", ""},
		{"", ""},
		{"scsi", ""},
		{"0", ""},
	}

	for _, tc := range cases {
		t.Run(tc.diskID, func(t *testing.T) {
			t.Parallel()

			got := qemu.GuessDevicePath(tc.diskID)
			if got != tc.want {
				t.Errorf("GuessDevicePath(%q) = %q, want %q", tc.diskID, got, tc.want)
			}
		})
	}
}

// ---- ResizeDisk zero-size guard --------------------------------------------

func TestCovResizeDisk_ZeroSize(t *testing.T) {
	t.Parallel()

	svc := qemu.New(nil)

	_, err := svc.ResizeDisk(context.Background(), "n1", 100, covScsi0, 0)
	if err == nil {
		t.Fatal("expected error for sizeGiB=0")
	}
}

func TestCovResizeDisk_MapUPID(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "method", http.StatusMethodNotAllowed)

			return
		}

		covWriteRaw(w, map[string]string{covKeyUPID: "UPID:resize:map"})
	}))
	defer srv.Close()

	svc := qemu.New(covClientFromURL(t, srv.URL))

	upid, err := svc.ResizeDisk(context.Background(), "n1", 100, covScsi0, 10)
	if err != nil {
		t.Fatalf("ResizeDisk: %v", err)
	}

	if upid != "UPID:resize:map" {
		t.Fatalf("upid: %q", upid)
	}
}

func TestCovResizeDisk_NoUPIDInMap(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		covWriteRaw(w, map[string]string{"other": "x"})
	}))
	defer srv.Close()

	svc := qemu.New(covClientFromURL(t, srv.URL))

	upid, err := svc.ResizeDisk(context.Background(), "n1", 100, covScsi0, 5)
	if err != nil {
		t.Fatalf("ResizeDisk: %v", err)
	}

	if upid != "" {
		t.Fatalf("expected empty, got %q", upid)
	}
}

// ---- buildAttachParams: covered via AttachDisk with opts.Extra -------------

func TestCovBuildAttachParams_WithExtra(t *testing.T) {
	t.Parallel()

	putParams := map[string]string{}

	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/nodes/n1/qemu/200/config", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			covWriteRaw(w, map[string]string{})
		case http.MethodPut:
			_ = r.ParseForm()
			for k, v := range r.PostForm {
				if len(v) > 0 {
					putParams[k] = v[0]
				}
			}

			covWriteRaw(w, map[string]bool{"ok": true})
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	svc := qemu.New(covClientFromURL(t, srv.URL))

	diskID, err := svc.AttachDisk(context.Background(), "n1", 200, "local-lvm:vm-200-disk-0", "scsi", &qemu.AttachOpts{
		Extra: map[string]interface{}{"scsihw": "virtio-scsi-pci"},
	})
	if err != nil {
		t.Fatalf("AttachDisk: %v", err)
	}

	if diskID != covScsi0 {
		t.Fatalf("diskID: %q", diskID)
	}

	if putParams["scsihw"] != "virtio-scsi-pci" {
		t.Fatalf("extra param not sent: %v", putParams)
	}
}

// ---- determineDiskID: explicit DiskID path ---------------------------------

func TestCovDetermineDiskID_ExplicitOpt(t *testing.T) {
	t.Parallel()

	callCount := 0

	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/nodes/n1/qemu/300/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			callCount++
		}

		covWriteRaw(w, map[string]bool{"ok": true})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	svc := qemu.New(covClientFromURL(t, srv.URL))

	diskID, err := svc.AttachDisk(context.Background(), "n1", 300, "vol:vm-300-disk-0", "scsi", &qemu.AttachOpts{
		DiskID: "scsi5",
	})
	if err != nil {
		t.Fatalf("AttachDisk: %v", err)
	}

	if diskID != "scsi5" {
		t.Fatalf("diskID: %q", diskID)
	}

	if callCount != 0 {
		t.Fatalf("GET config should not be called when DiskID explicit, got %d calls", callCount)
	}
}

// ---- determineDiskID: volid already present in config ----------------------

func TestCovDetermineDiskID_VolIDAlreadyInConfig(t *testing.T) {
	t.Parallel()

	const existingVol = "local-lvm:vm-400-disk-0"

	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/nodes/n1/qemu/400/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			covWriteRaw(w, map[string]string{"scsi3": existingVol})

			return
		}

		covWriteRaw(w, map[string]bool{"ok": true})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	svc := qemu.New(covClientFromURL(t, srv.URL))

	diskID, err := svc.AttachDisk(context.Background(), "n1", 400, existingVol, "scsi", nil)
	if err != nil {
		t.Fatalf("AttachDisk: %v", err)
	}

	if diskID != "scsi3" {
		t.Fatalf("expected scsi3 (existing), got %q", diskID)
	}
}
