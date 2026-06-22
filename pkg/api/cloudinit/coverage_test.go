package cloudinit_test

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/api/cloudinit"
	pveclient "github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/client"
)

// Constants for strings that appear ≥3 times across the cloudinit_test package.
const (
	covCIInterfaces  = "interfaces"
	covCIDHCP        = "dhcp"
	covCINameservers = "nameservers"
	covCIIPDHCP      = "ip=dhcp"
	covCIGW          = "10.0.0.1"
)

// covCIPVEResp is a typed PVE envelope (avoids errchkjson on map[string]any).
type covCIPVEResp struct {
	Data    json.RawMessage `json:"data"`
	Success int             `json:"success"`
}

// covCIWriteJSON serialises v, wraps in PVE envelope, and writes to w.
// json.Marshal(any) is intentional in this test helper.
//

func covCIWriteJSON(w http.ResponseWriter, v any) {
	raw, _ := json.Marshal(v) //nolint:errchkjson
	resp := covCIPVEResp{Data: json.RawMessage(raw), Success: 1}

	err := json.NewEncoder(w).Encode(resp)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// covCIClientFromURL builds a pveclient.Client pointed at the test server.
//
//nolint:ireturn // test helper returns interface required by cloudinit.New
func covCIClientFromURL(t *testing.T, rawURL string) pveclient.Client {
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
		Protocol: "http",
		APIToken: "u@pam!tok=sec",
	})
	if err != nil {
		t.Fatalf("covCIClientFromURL: %v", err)
	}

	return cli
}

// ---- BuildIPConfig (pure, table-driven) ------------------------------------

func TestCovBuildIPConfig_Nil(t *testing.T) {
	t.Parallel()

	svc := cloudinit.New(nil)

	got, err := svc.BuildIPConfig(nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if len(got) != 0 {
		t.Fatalf("expected empty map, got %v", got)
	}
}

func TestCovBuildIPConfig_IPOnly(t *testing.T) {
	t.Parallel()

	svc := cloudinit.New(nil)

	got, err := svc.BuildIPConfig(map[string]any{"ip": "10.0.0.5/24"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if got["ipconfig0"] != "ip=10.0.0.5/24" {
		t.Fatalf("ipconfig0: %q", got["ipconfig0"])
	}

	if _, hasGW := got["gw"]; hasGW {
		t.Fatalf("unexpected gw key in result")
	}
}

func TestCovBuildIPConfig_IPAndGW(t *testing.T) {
	t.Parallel()

	svc := cloudinit.New(nil)

	got, err := svc.BuildIPConfig(map[string]any{
		"ip": "10.0.0.5/24",
		"gw": covCIGW,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	want := "ip=10.0.0.5/24,gw=" + covCIGW
	if got["ipconfig0"] != want {
		t.Fatalf("ipconfig0: got %q want %q", got["ipconfig0"], want)
	}
}

func TestCovBuildIPConfig_Nameserver(t *testing.T) {
	t.Parallel()

	svc := cloudinit.New(nil)

	got, err := svc.BuildIPConfig(map[string]any{
		"ip":         "192.168.1.10/24",
		"nameserver": "9.9.9.9",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if got["nameserver"] != "9.9.9.9" {
		t.Fatalf("nameserver: %q", got["nameserver"])
	}
}

func TestCovBuildIPConfig_NoIP(t *testing.T) {
	t.Parallel()

	// When ip absent, ipconfig0 must not be set.
	svc := cloudinit.New(nil)

	got, err := svc.BuildIPConfig(map[string]any{"gw": covCIGW})
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if _, ok := got["ipconfig0"]; ok {
		t.Fatalf("ipconfig0 should not be set when ip is absent")
	}
}

// ---- AttachWithNetwork -----------------------------------------------------

// covCIServerState holds counters set by covSetupCIServer handlers.
type covCIServerState struct {
	putCount    int
	uploadParts []string
}

func covSetupCIServer(t *testing.T, state *covCIServerState) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("/api2/json/nodes/n1/qemu/100/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "method", http.StatusMethodNotAllowed)

			return
		}

		state.putCount++

		covCIWriteJSON(w, map[string]bool{"ok": true})
	})

	mux.HandleFunc("/api2/json/nodes/n1/storage/local/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)

			return
		}

		ct := r.Header.Get("Content-Type")
		_, params, _ := mime.ParseMediaType(ct)
		boundary := params["boundary"]

		if boundary != "" {
			mr := multipart.NewReader(r.Body, boundary)
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}

				name := p.FormName()
				if name == "" {
					name = p.FileName()
				}

				state.uploadParts = append(state.uploadParts, name)
				_, _ = io.ReadAll(p)
			}
		}

		covCIWriteJSON(w, map[string]bool{"ok": true})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return srv
}

func TestCovAttachWithNetwork_WithNetworkData(t *testing.T) {
	t.Parallel()

	state := &covCIServerState{}
	srv := covSetupCIServer(t, state)

	svc := cloudinit.New(covCIClientFromURL(t, srv.URL))

	err := svc.AttachWithNetwork(
		context.Background(),
		"n1", 100, "local",
		[]byte("#cloud-config\nhostname: vm100"),
		[]byte("version: 2\nethernets: {}"),
	)
	if err != nil {
		t.Fatalf("AttachWithNetwork: %v", err)
	}

	// PUT config for: ide2, cicustom(user), cicustom(user+network) = 3 calls.
	if state.putCount < 3 {
		t.Fatalf("expected >=3 PUT config calls, got %d", state.putCount)
	}
}

func TestCovAttachWithNetwork_EmptyNetworkData(t *testing.T) {
	t.Parallel()

	state := &covCIServerState{}
	srv := covSetupCIServer(t, state)

	svc := cloudinit.New(covCIClientFromURL(t, srv.URL))

	err := svc.AttachWithNetwork(
		context.Background(),
		"n1", 100, "local",
		[]byte("#cloud-config\nhostname: vm100"),
		nil, // no network data → early return after Attach
	)
	if err != nil {
		t.Fatalf("AttachWithNetwork (no network): %v", err)
	}

	// PUT for ide2 + cicustom(user) only = 2.
	if state.putCount < 2 {
		t.Fatalf("expected >=2 PUT config calls, got %d", state.putCount)
	}
}

func TestCovAttach_NoUserData(t *testing.T) {
	t.Parallel()

	putCount := 0
	uploadCalled := false

	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/nodes/n1/qemu/200/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(w, "method", http.StatusMethodNotAllowed)

			return
		}

		putCount++

		covCIWriteJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api2/json/nodes/n1/storage/local/upload", func(w http.ResponseWriter, r *http.Request) {
		uploadCalled = true

		covCIWriteJSON(w, map[string]bool{"ok": true})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	svc := cloudinit.New(covCIClientFromURL(t, srv.URL))

	err := svc.Attach(context.Background(), "n1", 200, "local", nil)
	if err != nil {
		t.Fatalf("Attach (no userData): %v", err)
	}

	if putCount != 1 {
		t.Fatalf("expected 1 PUT config call, got %d", putCount)
	}

	if uploadCalled {
		t.Fatal("upload should not be called when userData is nil")
	}
}

func TestCovAttachWithNetwork_UploadError(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/nodes/n1/qemu/300/config", func(w http.ResponseWriter, r *http.Request) {
		covCIWriteJSON(w, map[string]bool{"ok": true})
	})
	mux.HandleFunc("/api2/json/nodes/n1/storage/local/upload", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "storage full", http.StatusInternalServerError)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	svc := cloudinit.New(covCIClientFromURL(t, srv.URL))
	// First upload (user-data) hits error; must propagate via Attach.
	err := svc.AttachWithNetwork(
		context.Background(),
		"n1", 300, "local",
		[]byte("#cloud-config"),
		[]byte("version: 2"),
	)
	if err == nil {
		t.Fatal("expected error when upload fails")
	}
}

// ---- parseInterfaces edge cases via BuildIPConfigsFromCPISpec --------------

func TestCovBuildIPConfigsFromCPISpec_Nil(t *testing.T) {
	t.Parallel()

	svc := cloudinit.New(nil)

	got, err := svc.BuildIPConfigsFromCPISpec(nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if len(got) != 0 {
		t.Fatalf("expected empty map, got %v", got)
	}
}

func TestCovBuildIPConfigsFromCPISpec_NonStringNameserver(t *testing.T) {
	t.Parallel()

	// Non-string entries in nameservers slice must be silently skipped.
	svc := cloudinit.New(nil)
	spec := map[string]any{
		covCIInterfaces:  []any{map[string]any{covCIDHCP: true}},
		covCINameservers: []any{42, nil, "4.4.4.4"},
	}

	got, err := svc.BuildIPConfigsFromCPISpec(spec)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	// Only "4.4.4.4" is a string.
	if got["nameserver"] != "4.4.4.4" {
		t.Fatalf("nameserver: %q", got["nameserver"])
	}
}

func TestCovParseInterfaces_NoInterfacesKey(t *testing.T) {
	t.Parallel()

	// Spec missing "interfaces" → no NICs → empty result.
	svc := cloudinit.New(nil)
	spec := map[string]any{covCINameservers: []any{"2.2.2.2"}}

	got, err := svc.BuildIPConfigsFromCPISpec(spec)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if _, ok := got["ipconfig0"]; ok {
		t.Fatal("ipconfig0 should not be set when interfaces absent")
	}

	if got["nameserver"] != "2.2.2.2" {
		t.Fatalf("nameserver: %q", got["nameserver"])
	}
}

func TestCovParseInterfaces_NonMapEntry(t *testing.T) {
	t.Parallel()

	// Non-map interface entries must be skipped without error.
	svc := cloudinit.New(nil)
	spec := map[string]any{
		covCIInterfaces: []any{
			"not-a-map",
			map[string]any{covCIDHCP: true},
		},
	}

	got, err := svc.BuildIPConfigsFromCPISpec(spec)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	// "not-a-map" skipped; dhcp NIC becomes index 0.
	if got["ipconfig0"] != covCIIPDHCP {
		t.Fatalf("ipconfig0: %q", got["ipconfig0"])
	}
}

func TestCovParseInterfaces_StaticNoGateway(t *testing.T) {
	t.Parallel()

	svc := cloudinit.New(nil)
	spec := map[string]any{
		covCIInterfaces: []any{
			map[string]any{"address": "10.1.2.3/24"},
		},
	}

	got, err := svc.BuildIPConfigsFromCPISpec(spec)
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if got["ipconfig0"] != "ip=10.1.2.3/24" {
		t.Fatalf("ipconfig0: %q", got["ipconfig0"])
	}
}
