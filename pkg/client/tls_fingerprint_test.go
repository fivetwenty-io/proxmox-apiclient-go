package client_test

import (
	"crypto/x509"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	ihssl "github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/ssl"
	pve "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

// newTLSServer returns a TLS server that mimics minimal PVE API behavior.
func newTLSServer() *httptest.Server {
	return httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.URL.Path, "/api2/json") {
			http.NotFound(writer, request)

			return
		}

		writer.Header().Set("Content-Type", "application/json")

		switch request.URL.Path {
		case testVersionEndpoint:
			_, _ = io.WriteString(writer, `{"data":{"version":"test"},"success":1}`)
		default:
			http.NotFound(writer, request)
		}
	}))
}

// parseHostPort extracts host and port from an httptest server URL.
func parseHostPort(raw string) (string, int) {
	u, _ := url.Parse(raw)
	host := strings.Split(u.Host, ":")[0]
	port := 443

	if parts := strings.Split(u.Host, ":"); len(parts) == 2 {
		p, err := strconv.Atoi(parts[1])
		if err == nil {
			port = p
		}
	}

	return host, port
}

func TestTLS_CachedFingerprint_AllowsConnection(t *testing.T) {
	t.Parallel()

	srv := newTLSServer()
	defer srv.Close()

	// Extract leaf certificate and calculate SHA256 fingerprint
	certDER := srv.TLS.Certificates[0].Certificate[0]

	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		t.Fatalf("failed to parse server certificate: %v", err)
	}

	fingerprint := ihssl.CalculateFingerprint(cert)

	host, port := parseHostPort(srv.URL)

	opts := pve.Options{
		Host:               host,
		Port:               port,
		Protocol:           testProtoHTTPS,
		APIToken:           testAPIToken,
		CachedFingerprints: map[string]bool{fingerprint: true},
	}

	cli, err := pve.NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = cli.Get("/version", nil)
	if err != nil {
		t.Fatalf("Get(/version) with cached fingerprint failed: %v", err)
	}
}

func TestTLS_ManualVerifyCallback_AcceptsAndRegisters(t *testing.T) {
	t.Parallel()

	srv := newTLSServer()
	defer srv.Close()

	var registered atomic.Value
	registered.Store("")

	verifyCalled := atomic.Bool{}

	host, port := parseHostPort(srv.URL)

	opts := pve.Options{
		Host:     host,
		Port:     port,
		Protocol: testProtoHTTPS,
		APIToken: testAPIToken,
		// No cached fingerprints; use verify callback to accept
		VerifyFingerprintCallback: func(cert *x509.Certificate) bool {
			verifyCalled.Store(true)

			return true
		},
		RegisterFingerprintCallback: func(fingerprint string) {
			registered.Store(fingerprint)
		},
	}

	cli, err := pve.NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = cli.Get("/version", nil)
	if err != nil {
		t.Fatalf("Get(/version) with verify callback failed: %v", err)
	}

	if !verifyCalled.Load() {
		t.Fatalf("expected VerifyFingerprintCallback to be called")
	}

	if fingerprint, _ := registered.Load().(string); fingerprint == "" {
		t.Fatalf("expected RegisterFingerprintCallback to be invoked")
	}
}

func TestTLS_ManualVerification_NoCallback_RejectsUnknown(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow TLS test in short mode")
	}

	t.Parallel()

	srv := newTLSServer()
	defer srv.Close()

	host, port := parseHostPort(srv.URL)

	opts := pve.Options{
		Host:               host,
		Port:               port,
		Protocol:           testProtoHTTPS,
		APIToken:           testAPIToken,
		ManualVerification: true,
		Timeout:            1 * time.Second, // Set 1 second timeout to speed up test
		// No cached fingerprints and no verify callback => should reject
	}

	cli, err := pve.NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = cli.Get("/version", nil)
	if err == nil {
		t.Fatalf("expected TLS connection to be rejected for unknown fingerprint with manual verification")
	}
}
