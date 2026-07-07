package cloudinit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/cloudinit"
	pveclient "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

func TestAttachWithUpload(t *testing.T) {
	t.Parallel()

	putConfigCount := 0
	uploadHit := false

	mux := http.NewServeMux()
	mux.HandleFunc("/api2/json/nodes/n1/qemu/100/config", func(writer http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			http.Error(writer, "method", http.StatusMethodNotAllowed)

			return
		}

		putConfigCount++
		_, _ = writer.Write([]byte(`{"data": {"ok": true}, "success": 1}`))
	})
	mux.HandleFunc("/api2/json/nodes/n1/storage/local/upload", func(writer http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(writer, "method", http.StatusMethodNotAllowed)

			return
		}

		uploadHit = true
		_, _ = writer.Write([]byte(`{"data": {"ok": true}, "success": 1}`))
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	parsed, _ := url.Parse(srv.URL)
	host := strings.Split(parsed.Host, ":")[0]

	p := 0
	if parts := strings.Split(parsed.Host, ":"); len(parts) == 2 {
		p, _ = strconv.Atoi(parts[1])
	}

	cli, err := pveclient.NewClient(pveclient.Options{Host: host, Port: p, Protocol: "http", APIToken: "u@pam!tok=sec"})
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	svc := cloudinit.New(cli)

	err = svc.Attach(context.Background(), "n1", 100, "local", []byte("#cloud-config\nhostname: test"))
	if err != nil {
		t.Fatalf("attach: %v", err)
	}

	if !uploadHit {
		t.Fatalf("expected upload to be called")
	}

	if putConfigCount < 2 {
		t.Fatalf("expected two config PUTs, got %d", putConfigCount)
	}
}

func TestBuildIPConfigs(t *testing.T) {
	t.Parallel()

	svc := cloudinit.New(nil)

	cfg, err := svc.BuildIPConfigs([]cloudinit.NICSpec{{DHCP: true}, {AddressCIDR: "10.0.0.10/24", Gateway: "10.0.0.1"}}, []string{"8.8.8.8", "1.1.1.1"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}

	if cfg["ipconfig0"] != "ip=dhcp" {
		t.Fatalf("ipconfig0: %s", cfg["ipconfig0"])
	}

	if cfg["ipconfig1"] != "ip=10.0.0.10/24,gw=10.0.0.1" {
		t.Fatalf("ipconfig1: %s", cfg["ipconfig1"])
	}

	if cfg["nameserver"] != "8.8.8.8 1.1.1.1" {
		t.Fatalf("nameserver: %s", cfg["nameserver"])
	}
}
