package http //nolint:testpackage // white-box test: inspects the client's unexported transport holder

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	issl "github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/ssl"
)

const (
	swapTestClientCN   = "swap-test-client"
	swapTestPEMFileDir = 0o600
	swapTestKeyType    = "EC PRIVATE KEY"
	swapTestCertType   = "CERTIFICATE"
	swapTestNewLimit   = 42
)

// swapTestPKI is a throwaway certificate authority with a server certificate
// for 127.0.0.1 and a client certificate, all written to disk the way the
// client's SSLOptions expect them.
type swapTestPKI struct {
	caPath         string
	clientCertPath string
	clientKeyPath  string
	caPool         *x509.CertPool
	serverCert     tls.Certificate
}

// newSwapTestPKI builds a swapTestPKI under t.TempDir.
func newSwapTestPKI(t *testing.T) *swapTestPKI {
	t.Helper()

	dir := t.TempDir()

	caKey, caCert, caDER := newSwapTestCA(t)

	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	serverCert := newSwapTestServerCert(t, caCert, caKey)

	clientTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: swapTestClientCN},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	clientDER, clientKey := signSwapTestCert(t, clientTemplate, caCert, caKey)

	clientKeyDER, err := x509.MarshalECPrivateKey(clientKey)
	if err != nil {
		t.Fatalf("marshal client key: %v", err)
	}

	pki := &swapTestPKI{
		caPath:         filepath.Join(dir, "ca.pem"),
		clientCertPath: filepath.Join(dir, "client.pem"),
		clientKeyPath:  filepath.Join(dir, "client-key.pem"),
		caPool:         pool,
		serverCert:     serverCert,
	}

	writeSwapTestPEM(t, pki.caPath, swapTestCertType, caDER)
	writeSwapTestPEM(t, pki.clientCertPath, swapTestCertType, clientDER)
	writeSwapTestPEM(t, pki.clientKeyPath, swapTestKeyType, clientKeyDER)

	return pki
}

// newSwapTestServerCert issues the server certificate for 127.0.0.1.
func newSwapTestServerCert(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey) tls.Certificate {
	t.Helper()

	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "swap-test-server"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP(wrapTestLoopback)},
	}

	serverDER, serverKey := signSwapTestCert(t, serverTemplate, caCert, caKey)

	serverKeyDER, err := x509.MarshalECPrivateKey(serverKey)
	if err != nil {
		t.Fatalf("marshal server key: %v", err)
	}

	serverCert, err := tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: swapTestCertType, Bytes: serverDER}),
		pem.EncodeToMemory(&pem.Block{Type: swapTestKeyType, Bytes: serverKeyDER}),
	)
	if err != nil {
		t.Fatalf("load server certificate: %v", err)
	}

	return serverCert
}

// newSwapTestCA generates the certificate authority's key and certificate.
func newSwapTestCA(t *testing.T) (*ecdsa.PrivateKey, *x509.Certificate, []byte) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "swap-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA certificate: %v", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA certificate: %v", err)
	}

	return key, cert, der
}

// signSwapTestCert signs template with the authority and returns the DER
// certificate with the new key.
func signSwapTestCert(
	t *testing.T,
	template, caCert *x509.Certificate,
	caKey *ecdsa.PrivateKey,
) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("sign certificate: %v", err)
	}

	return der, key
}

// writeSwapTestPEM writes one PEM block to path.
func writeSwapTestPEM(t *testing.T, path, blockType string, der []byte) {
	t.Helper()

	err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), swapTestPEMFileDir)
	if err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// swapTLSServer is a TLS server answering every request with the version
// envelope, counting its connections and noting the client certificate of the
// latest request.
type swapTLSServer struct {
	*httptest.Server

	conns    atomic.Int32
	requests atomic.Int32
	clientCN atomic.Value
}

// startSwapTLSServer starts a TLS server with config as its TLS settings;
// a nil config serves httptest's own self-signed certificate.
func startSwapTLSServer(t *testing.T, config *tls.Config) *swapTLSServer {
	t.Helper()

	server := &swapTLSServer{}

	server.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		server.requests.Add(1)

		if request.TLS != nil && len(request.TLS.PeerCertificates) > 0 {
			server.clientCN.Store(request.TLS.PeerCertificates[0].Subject.CommonName)
		}

		writer.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = writer.Write(versionEnvelope(t))
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			server.conns.Add(1)
		}
	}

	if config != nil {
		server.TLS = config
	}

	server.StartTLS()
	t.Cleanup(server.Close)

	return server
}

// swapClient builds a client for the server with API-token auth and no retries.
func swapClient(t *testing.T, rawURL string, mutate func(*Options)) *Client {
	t.Helper()

	client := newClientForServerURL(t, rawURL, func(o *Options) {
		o.Protocol = testProtoHTTPS
		o.APIToken = testAPITokenShort

		mutate(o)
	})
	client.SetMaxRetries(0)

	t.Cleanup(func() { _ = client.Close() })

	return client
}

// swapTransport swaps the client's transport through SetKeepAlive and fails unless
// the swap happened.
func swapTransport(t *testing.T, client *Client) {
	t.Helper()

	before := liveTransport(t, client)

	client.SetKeepAlive(swapTestNewLimit)

	if liveTransport(t, client) == before {
		t.Fatal("SetKeepAlive did not install a new transport")
	}
}

// requireVersion sends GET /version and fails on any error.
func requireVersion(t *testing.T, client *Client) {
	t.Helper()

	_, err := client.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("GET /version: %v", err)
	}
}

// TestSetKeepAlive_SwappedTransportStillVerifiesAgainstConfiguredRoots verifies
// the swapped transport still checks the server's certificate against the
// configured CA, on a fresh handshake and without InsecureSkipVerify, and
// still refuses a certificate that CA did not sign.
func TestSetKeepAlive_SwappedTransportStillVerifiesAgainstConfiguredRoots(t *testing.T) {
	t.Parallel()

	pki := newSwapTestPKI(t)
	server := startSwapTLSServer(t, &tls.Config{Certificates: []tls.Certificate{pki.serverCert}, MinVersion: tls.VersionTLS12})

	trusting := swapClient(t, server.URL, func(o *Options) {
		o.SSLOptions = &SSLOptions{VerifyMode: SSLVerifyFull, CACert: pki.caPath}
	})

	requireVersion(t, trusting)

	if got := server.conns.Load(); got != 1 {
		t.Fatalf("connections before the swap = %d, want 1", got)
	}

	swapTransport(t, trusting)

	if config := liveTransport(t, trusting).TLSClientConfig; config == nil || config.InsecureSkipVerify {
		t.Fatal("the swapped transport does not verify the server certificate")
	}

	requireVersion(t, trusting)

	if got := server.conns.Load(); got != 2 {
		t.Errorf("connections after the swap = %d, want the swapped transport to open its own", got)
	}

	// The same server, with no CA configured, must be refused on the swapped
	// transport: its certificate chains to a CA the client was never given.
	strict := swapClient(t, server.URL, func(o *Options) {
		o.SSLOptions = &SSLOptions{VerifyMode: SSLVerifyFull}
	})

	swapTransport(t, strict)

	_, err := strict.Do("GET", "/version", nil)
	if err == nil {
		t.Fatal("the swapped transport accepted a certificate no configured root signed")
	}

	if !isCertificateVerificationError(err) {
		t.Errorf("GET /version error = %v, want a certificate verification failure", err)
	}
}

// TestSetKeepAlive_SwappedTransportStillPresentsClientCertificate verifies the
// swapped transport still sends the configured client certificate to a server
// that requires one.
func TestSetKeepAlive_SwappedTransportStillPresentsClientCertificate(t *testing.T) {
	t.Parallel()

	pki := newSwapTestPKI(t)
	server := startSwapTLSServer(t, &tls.Config{
		Certificates: []tls.Certificate{pki.serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pki.caPool,
		MinVersion:   tls.VersionTLS12,
	})

	client := swapClient(t, server.URL, func(o *Options) {
		o.SSLOptions = &SSLOptions{
			VerifyMode: SSLVerifyFull,
			CACert:     pki.caPath,
			ClientCert: pki.clientCertPath,
			ClientKey:  pki.clientKeyPath,
		}
	})

	requireVersion(t, client)
	swapTransport(t, client)

	server.clientCN.Store("")

	requireVersion(t, client)

	if got := server.conns.Load(); got != 2 {
		t.Errorf("connections = %d, want the swapped transport to open its own", got)
	}

	if got, _ := server.clientCN.Load().(string); got != swapTestClientCN {
		t.Errorf("client certificate seen after the swap = %q, want %q", got, swapTestClientCN)
	}
}

// TestSetKeepAlive_SwappedTransportStillConsultsVerifyCallback verifies the
// swapped transport still asks the configured certificate callback to judge
// the server, and honours its answer, when the server's certificate chains to
// no root the client holds.
func TestSetKeepAlive_SwappedTransportStillConsultsVerifyCallback(t *testing.T) {
	t.Parallel()

	server := startSwapTLSServer(t, nil)

	cases := map[string]bool{"accepting": true, "rejecting": false}

	for name, accept := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var (
				asked atomic.Int32
				seen  atomic.Pointer[x509.Certificate]
			)

			client := swapClient(t, server.URL, func(o *Options) {
				o.SSLOptions = &SSLOptions{VerifyMode: SSLVerifyFull}
				o.VerifyFingerprintCallback = func(cert *x509.Certificate) bool {
					asked.Add(1)
					seen.Store(cert)

					return accept
				}
			})

			swapTransport(t, client)

			_, err := client.Do("GET", "/version", nil)

			if got := asked.Load(); got != 1 {
				t.Fatalf("verification callback calls after the swap = %d, want 1", got)
			}

			if cert := seen.Load(); cert == nil || issl.CalculateFingerprint(cert) != issl.CalculateFingerprint(server.Certificate()) {
				t.Error("the callback was not handed the server's certificate")
			}

			if accept && err != nil {
				t.Errorf("GET /version = %v, want success once the callback accepts", err)
			}

			if !accept && !isCertificateVerificationError(err) {
				t.Errorf("GET /version = %v, want the callback's refusal", err)
			}
		})
	}
}

// TestSetKeepAlive_SwappedTransportStillRoutesThroughProxy verifies requests
// sent after SetKeepAlive still go through the explicit proxy, by naming a
// host that resolves nowhere and counting what reaches the proxy.
func TestSetKeepAlive_SwappedTransportStillRoutesThroughProxy(t *testing.T) {
	t.Parallel()

	var (
		viaProxy atomic.Int32
		lastHost atomic.Value
	)

	proxy := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		viaProxy.Add(1)
		lastHost.Store(request.Host)

		writer.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = writer.Write(versionEnvelope(t))
	})

	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatalf("parse proxy URL: %v", err)
	}

	const originHost = "pve.swap-test.invalid"

	var chosen atomic.Int32

	client := newClientForServerURL(t, proxy.URL, func(o *Options) {
		o.Host = originHost
		o.Port = 8006
		o.APIToken = testAPITokenShort
		o.Proxy = func(*http.Request) (*url.URL, error) {
			chosen.Add(1)

			return proxyURL, nil
		}
	})
	client.SetMaxRetries(0)

	t.Cleanup(func() { _ = client.Close() })

	requireVersion(t, client)
	swapTransport(t, client)

	before := viaProxy.Load()

	requireVersion(t, client)

	if got := viaProxy.Load(); got != before+1 {
		t.Errorf("requests through the proxy after the swap = %d, want %d", got-before, 1)
	}

	if got, _ := lastHost.Load().(string); got != originHost+":8006" {
		t.Errorf("proxy saw Host %q, want the origin %q", got, originHost+":8006")
	}

	if got := chosen.Load(); got != 2 {
		t.Errorf("proxy selector calls = %d, want one per request", got)
	}
}
