package lxc_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/api/lxc"
	pveclient "github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/client"
)

// ---- test helpers ----

func optsFromURL(u string) pveclient.Options {
	parsed, _ := url.Parse(u)
	host := strings.Split(parsed.Host, ":")[0]
	port := 0

	if parts := strings.Split(parsed.Host, ":"); len(parts) == 2 {
		p, _ := strconv.Atoi(parts[1])
		port = p
	}

	return pveclient.Options{Host: host, Port: port, Protocol: "http", APIToken: "user@pam!tok=sec"}
}

func newTestClient(t *testing.T, srv *httptest.Server) pveclient.Client {
	t.Helper()

	cli, err := pveclient.NewClient(optsFromURL(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return cli
}

// pveResponse wraps data in the PVE JSON envelope.
func pveResponse(data interface{}) []byte {
	b, _ := json.Marshal(map[string]interface{}{"data": data, "success": 1})

	return b
}

// ---- NewClient ----

func TestNewClient(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
}

// ---- List ----

func TestClient_List_Empty(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodGet {
			http.Error(w, "want GET", http.StatusMethodNotAllowed)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse([]interface{}{}))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	containers, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(containers) != 0 {
		t.Errorf("List empty: want 0 containers, got %d", len(containers))
	}
}

func TestClient_List_WithContainers(t *testing.T) {
	t.Parallel()

	data := []interface{}{
		map[string]interface{}{
			"vmid":   float64(100),
			"status": "running",
			"name":   "web-01",
			"uptime": float64(3600),
		},
		map[string]interface{}{
			"vmid":   float64(101),
			"status": "stopped",
			"name":   "db-01",
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse(data))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	containers, err := c.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(containers) != 2 {
		t.Fatalf("List: want 2 containers, got %d", len(containers))
	}

	if containers[0].VMID != 100 {
		t.Errorf("containers[0].VMID: want 100, got %d", containers[0].VMID)
	}

	if containers[0].Status != "running" {
		t.Errorf("containers[0].Status: want running, got %q", containers[0].Status)
	}

	if containers[0].Uptime != 3600 {
		t.Errorf("containers[0].Uptime: want 3600, got %d", containers[0].Uptime)
	}

	if containers[1].VMID != 101 {
		t.Errorf("containers[1].VMID: want 101, got %d", containers[1].VMID)
	}
}

func TestClient_List_ServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "server error", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	_, err := c.List(context.Background())
	if err == nil {
		t.Fatal("List: want error on server 500, got nil")
	}
}

// ---- Create ----

func TestClient_Create_Success(t *testing.T) {
	t.Parallel()

	const expectedUPID = "UPID:pve1:00001234:00000001:AABBCCDD:vzcreate:100:root@pam:"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "want POST", http.StatusMethodNotAllowed)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse(expectedUPID))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")
	cfg := lxc.ContainerConfig{
		VMID:         100,
		OSTemplate:   "local:vztmpl/ubuntu-22.04-standard_22.04-1_amd64.tar.zst",
		Hostname:     "test-ct",
		Memory:       512,
		Cores:        2,
		Unprivileged: true,
		Start:        true,
	}

	upid, err := c.Create(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if upid != expectedUPID {
		t.Errorf("Create UPID: want %q, got %q", expectedUPID, upid)
	}
}

func TestClient_Create_AllParams(t *testing.T) {
	t.Parallel()

	var captured map[string]interface{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_ = req.ParseForm()
		captured = make(map[string]interface{})

		for k, v := range req.Form {
			if len(v) == 1 {
				captured[k] = v[0]
			}
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse("UPID:pve1:x"))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")
	cfg := lxc.ContainerConfig{
		VMID:         200,
		OSTemplate:   "local:vztmpl/debian-12.tar.zst",
		Hostname:     "myct",
		Description:  "desc",
		Memory:       1024,
		Swap:         512,
		Cores:        4,
		CPULimit:     2,
		CPUUnits:     1024,
		RootFS:       "local:8",
		Net0:         "name=eth0,bridge=vmbr0",
		Unprivileged: true,
		Password:     "secret",
		SSHKeys:      "ssh-rsa AAAA...",
		Nameserver:   "8.8.8.8",
		Searchdomain: "example.com",
		Start:        true,
		Storage:      "local",
		Pool:         "prod",
		Features:     map[string]string{"nesting": "1"},
	}

	_, err := c.Create(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Create all params: %v", err)
	}
}

func TestClient_Create_ServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "conflict", http.StatusConflict)
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	_, err := c.Create(context.Background(), lxc.ContainerConfig{VMID: 100, OSTemplate: "x"})
	if err == nil {
		t.Fatal("Create: want error on 409, got nil")
	}
}

func TestClient_Create_NonStringUPID(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return a number instead of string UPID → ErrUnexpectedResponseType
		_, _ = w.Write(pveResponse(12345))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	_, err := c.Create(context.Background(), lxc.ContainerConfig{VMID: 100, OSTemplate: "x"})
	if !errors.Is(err, lxc.ErrUnexpectedResponseType) {
		t.Errorf("Create non-string UPID: want ErrUnexpectedResponseType, got %v", err)
	}
}

// ---- Status ----

func TestClient_Status_Success(t *testing.T) {
	t.Parallel()

	data := map[string]interface{}{
		"status":  "running",
		"name":    "web-01",
		"uptime":  float64(7200),
		"cpus":    float64(2),
		"maxmem":  float64(536870912),
		"mem":     float64(268435456),
		"maxdisk": float64(10737418240),
		"disk":    float64(1073741824),
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse(data))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	status, err := c.Status(context.Background(), 100)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}

	if status == nil {
		t.Fatal("Status: got nil")
	}

	if status.VMID != 100 {
		t.Errorf("Status.VMID: want 100, got %d", status.VMID)
	}

	if status.Status != "running" {
		t.Errorf("Status.Status: want running, got %q", status.Status)
	}

	if status.Uptime != 7200 {
		t.Errorf("Status.Uptime: want 7200, got %d", status.Uptime)
	}

	if status.CPUs != 2 {
		t.Errorf("Status.CPUs: want 2, got %d", status.CPUs)
	}

	if status.MaxMem != 536870912 {
		t.Errorf("Status.MaxMem: want 536870912, got %d", status.MaxMem)
	}
}

func TestClient_Status_NonMapResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return non-map → parseContainerStatus fallback path
		_, _ = w.Write(pveResponse("not-a-map"))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	status, err := c.Status(context.Background(), 200)
	if err != nil {
		t.Fatalf("Status non-map: %v", err)
	}

	if status.VMID != 200 {
		t.Errorf("Status non-map fallback VMID: want 200, got %d", status.VMID)
	}
}

func TestClient_Status_ServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	_, err := c.Status(context.Background(), 999)
	if err == nil {
		t.Fatal("Status: want error on 404, got nil")
	}
}

// ---- Start ----

func TestClient_Start_Success(t *testing.T) {
	t.Parallel()

	const upid = "UPID:pve1:00001234:00000001:AABB:vzstart:100:root@pam:"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !strings.HasSuffix(req.URL.Path, "/status/start") {
			http.Error(w, "bad path", http.StatusNotFound)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse(upid))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	got, err := c.Start(context.Background(), 100)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if got != upid {
		t.Errorf("Start UPID: want %q, got %q", upid, got)
	}
}

func TestClient_Start_NonStringResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse(42))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	_, err := c.Start(context.Background(), 100)
	if !errors.Is(err, lxc.ErrUnexpectedResponseType) {
		t.Errorf("Start non-string: want ErrUnexpectedResponseType, got %v", err)
	}
}

// ---- Stop ----

func TestClient_Stop_Success(t *testing.T) {
	t.Parallel()

	const upid = "UPID:pve1:x:vzstop:100:"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse(upid))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	got, err := c.Stop(context.Background(), 100)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if got != upid {
		t.Errorf("Stop UPID: want %q, got %q", upid, got)
	}
}

func TestClient_Stop_NonStringResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse(false))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	_, err := c.Stop(context.Background(), 100)
	if !errors.Is(err, lxc.ErrUnexpectedResponseType) {
		t.Errorf("Stop non-string: want ErrUnexpectedResponseType, got %v", err)
	}
}

// ---- Shutdown ----

func TestClient_Shutdown_WithTimeout(t *testing.T) {
	t.Parallel()

	var capturedTimeout string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_ = req.ParseForm()
		capturedTimeout = req.FormValue("timeout")

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse("UPID:pve1:x"))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	_, err := c.Shutdown(context.Background(), 100, 60)
	if err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if capturedTimeout != "60" {
		t.Errorf("Shutdown timeout param: want '60', got %q", capturedTimeout)
	}
}

func TestClient_Shutdown_ZeroTimeout(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse("UPID:pve1:x"))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	_, err := c.Shutdown(context.Background(), 100, 0)
	if err != nil {
		t.Fatalf("Shutdown zero timeout: %v", err)
	}
}

func TestClient_Shutdown_NonStringResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse(nil))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	_, err := c.Shutdown(context.Background(), 100, 30)
	if !errors.Is(err, lxc.ErrUnexpectedResponseType) {
		t.Errorf("Shutdown non-string: want ErrUnexpectedResponseType, got %v", err)
	}
}

// ---- Reboot ----

func TestClient_Reboot_Success(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse("UPID:pve1:x:vzreboot"))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	upid, err := c.Reboot(context.Background(), 100)
	if err != nil {
		t.Fatalf("Reboot: %v", err)
	}

	if !strings.HasPrefix(upid, "UPID") {
		t.Errorf("Reboot: want UPID string, got %q", upid)
	}
}

func TestClient_Reboot_NonStringResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse(map[string]interface{}{"task": "x"}))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	_, err := c.Reboot(context.Background(), 100)
	if !errors.Is(err, lxc.ErrUnexpectedResponseType) {
		t.Errorf("Reboot non-string: want ErrUnexpectedResponseType, got %v", err)
	}
}

// ---- Delete ----

func TestClient_Delete_NoPurge(t *testing.T) {
	t.Parallel()

	var method string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		method = req.Method

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse("UPID:pve1:x:vzdestroy"))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	upid, err := c.Delete(context.Background(), 100, false)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if method != http.MethodDelete {
		t.Errorf("Delete: want DELETE method, got %q", method)
	}

	if upid == "" {
		t.Error("Delete: want non-empty UPID")
	}
}

func TestClient_Delete_WithPurge(t *testing.T) {
	t.Parallel()

	var capturedPurge string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		capturedPurge = req.URL.Query().Get("purge")

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse("UPID:pve1:x"))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	_, err := c.Delete(context.Background(), 100, true)
	if err != nil {
		t.Fatalf("Delete purge: %v", err)
	}

	if capturedPurge != "1" {
		t.Errorf("Delete purge param: want '1', got %q", capturedPurge)
	}
}

func TestClient_Delete_NonStringResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse(0))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	_, err := c.Delete(context.Background(), 100, false)
	if !errors.Is(err, lxc.ErrUnexpectedResponseType) {
		t.Errorf("Delete non-string: want ErrUnexpectedResponseType, got %v", err)
	}
}

// ---- GetConfig ----

func TestClient_GetConfig_Success(t *testing.T) {
	t.Parallel()

	cfgData := map[string]interface{}{
		"hostname": "myct",
		"memory":   float64(512),
		"cores":    float64(2),
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse(cfgData))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	config, err := c.GetConfig(context.Background(), 100)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}

	if config["hostname"] != "myct" {
		t.Errorf("GetConfig hostname: want myct, got %v", config["hostname"])
	}
}

func TestClient_GetConfig_NonMapResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse("not-a-map"))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	_, err := c.GetConfig(context.Background(), 100)
	if !errors.Is(err, lxc.ErrUnexpectedResponseType) {
		t.Errorf("GetConfig non-map: want ErrUnexpectedResponseType, got %v", err)
	}
}

// ---- UpdateConfig ----

func TestClient_UpdateConfig_Success(t *testing.T) {
	t.Parallel()

	var capturedMethod string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		capturedMethod = req.Method

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse(nil))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	err := c.UpdateConfig(context.Background(), 100, map[string]interface{}{
		"memory": 1024,
		"cores":  4,
	})
	if err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}

	if capturedMethod != http.MethodPut {
		t.Errorf("UpdateConfig: want PUT, got %q", capturedMethod)
	}
}

func TestClient_UpdateConfig_ServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	err := c.UpdateConfig(context.Background(), 100, nil)
	if err == nil {
		t.Fatal("UpdateConfig: want error on 403, got nil")
	}
}

// ---- Clone ----

func TestClient_Clone_MinimalOpts(t *testing.T) {
	t.Parallel()

	const newUPID = "UPID:pve1:x:vzclone:100:"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse(newUPID))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	upid, err := c.Clone(context.Background(), 100, 200, lxc.CloneOptions{})
	if err != nil {
		t.Fatalf("Clone: %v", err)
	}

	if upid != newUPID {
		t.Errorf("Clone UPID: want %q, got %q", newUPID, upid)
	}
}

func TestClient_Clone_AllOpts(t *testing.T) {
	t.Parallel()

	var capturedParams url.Values

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_ = req.ParseForm()
		capturedParams = req.Form

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse("UPID:pve1:x"))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")
	opts := lxc.CloneOptions{
		Hostname:    "clone-01",
		Description: "cloned ct",
		Target:      "pve2",
		Pool:        "dev",
		Storage:     "local",
		Full:        true,
	}

	_, err := c.Clone(context.Background(), 100, 201, opts)
	if err != nil {
		t.Fatalf("Clone all opts: %v", err)
	}

	if capturedParams.Get("hostname") != "clone-01" {
		t.Errorf("Clone hostname param: want 'clone-01', got %q", capturedParams.Get("hostname"))
	}

	if capturedParams.Get("full") != "1" {
		t.Errorf("Clone full param: want '1', got %q", capturedParams.Get("full"))
	}
}

func TestClient_Clone_NonStringResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse(map[string]interface{}{"upid": "x"}))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	_, err := c.Clone(context.Background(), 100, 200, lxc.CloneOptions{})
	if !errors.Is(err, lxc.ErrUnexpectedResponseType) {
		t.Errorf("Clone non-string: want ErrUnexpectedResponseType, got %v", err)
	}
}

// ---- Resize ----

func TestClient_Resize_Success(t *testing.T) {
	t.Parallel()

	var capturedMethod string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		capturedMethod = req.Method

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(pveResponse(nil))
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	err := c.Resize(context.Background(), 100, "rootfs", "+10G")
	if err != nil {
		t.Fatalf("Resize: %v", err)
	}

	if capturedMethod != http.MethodPut {
		t.Errorf("Resize: want PUT, got %q", capturedMethod)
	}
}

func TestClient_Resize_ServerError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer srv.Close()

	c := lxc.NewClient(newTestClient(t, srv), "pve1")

	err := c.Resize(context.Background(), 100, "rootfs", "+10G")
	if err == nil {
		t.Fatal("Resize: want error on 400, got nil")
	}
}
