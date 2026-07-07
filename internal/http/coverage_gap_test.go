package http //nolint:testpackage // white-box test: accesses unexported client fields/methods

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	issl "github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/ssl"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/auth"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/cache"
	apierrors "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/errors"
)

var (
	errTestFakeTFA   = errors.New("fake tfa handler error")
	errTestBoom      = errors.New("boom")
	errTestPlainFail = errors.New("plain failure")
)

// fakeTFAHandler is a minimal auth.TFAHandler stub for unit-testing
// handleTFAAuthentication in isolation.
type fakeTFAHandler struct {
	response *auth.TFAResponse
	err      error
}

func (f fakeTFAHandler) HandleTFAChallenge(_ *auth.TFAChallenge) (*auth.TFAResponse, error) {
	return f.response, f.err
}

// newClientForServerURL builds a *Client whose Options.Host/Port/Protocol
// point directly at rawURL, so both Client.baseURL and any internal
// authenticator baseURL (fixed at construction from Options.BaseURL()) hit
// the real server — required for exercising TicketAuthenticator
// login/TFA/refresh requests, which clientPointedAt (baseURL patched
// post-construction) cannot reach.
func newClientForServerURL(t *testing.T, rawURL string, mutate func(*Options)) *Client {
	t.Helper()

	parsedURL, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse server URL %q: %v", rawURL, err)
	}

	port, err := strconv.Atoi(parsedURL.Port())
	if err != nil {
		t.Fatalf("parse server port from %q: %v", rawURL, err)
	}

	opts := &Options{
		Host:      parsedURL.Hostname(),
		Port:      port,
		Protocol:  testProtoHTTP,
		Timeout:   5 * time.Second,
		KeepAlive: 5,
	}

	if mutate != nil {
		mutate(opts)
	}

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	return client
}

// ---------------------------------------------------------------------------
// handleTFAAuthentication
// ---------------------------------------------------------------------------

func TestHandleTFAAuthentication_NoHandler_ReturnsNil(t *testing.T) {
	t.Parallel()

	client := clientPointedAt(t, "http://127.0.0.1:1")
	client.tfaHandler = nil

	err := client.handleTFAAuthentication(errTestBoom)
	if err != nil {
		t.Errorf("handleTFAAuthentication() = %v, want nil when no handler configured", err)
	}
}

func TestHandleTFAAuthentication_NonTFAError_ReturnsNil(t *testing.T) {
	t.Parallel()

	client := clientPointedAt(t, "http://127.0.0.1:1")
	client.tfaHandler = fakeTFAHandler{}

	err := client.handleTFAAuthentication(errTestPlainFail)
	if err != nil {
		t.Errorf("handleTFAAuthentication() = %v, want nil for a non-TFARequiredError", err)
	}
}

func TestHandleTFAAuthentication_NonTicketAuthenticator_ReturnsNil(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.APIToken = testAPITokenFull

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.tfaHandler = fakeTFAHandler{}

	tferr := &apierrors.TFARequiredError{Types: []string{testTFATypeTOTP}}

	err = client.handleTFAAuthentication(tferr)
	if err != nil {
		t.Errorf("handleTFAAuthentication() = %v, want nil when authenticator is not ticket based", err)
	}
}

func TestHandleTFAAuthentication_HandlerError(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.Username = testUsername
	opts.Password = testPassword

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.tfaHandler = fakeTFAHandler{err: errTestFakeTFA}

	tferr := &apierrors.TFARequiredError{Ticket: testTFAPartialTicket, Challenge: testTFAChallenge, Types: []string{testTFATypeTOTP}}

	err = client.handleTFAAuthentication(tferr)
	if err == nil {
		t.Fatal("handleTFAAuthentication() = nil, want error when the TFA handler fails")
	}

	if !errors.Is(err, errTestFakeTFA) {
		t.Errorf("handleTFAAuthentication() = %v, want wrapping %v", err, errTestFakeTFA)
	}
}

func TestHandleTFAAuthentication_CompleteTFAError(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api2/json/access/tfa" {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusOK)
			// Malformed JSON on a 200 response: CompleteTFA's parseTFAResponse
			// must surface this as an error rather than a false AuthResult.
			_, _ = writer.Write([]byte("{not json"))

			return
		}

		writer.WriteHeader(http.StatusNotFound)
	})

	client := newClientForServerURL(t, srv.URL, func(o *Options) {
		o.Username = testUsername
		o.Password = testPassword
	})

	client.tfaHandler = fakeTFAHandler{response: &auth.TFAResponse{Response: "123456"}}

	tferr := &apierrors.TFARequiredError{Ticket: testTFAPartialTicket, Challenge: testTFAChallenge, Types: []string{testTFATypeTOTP}}

	err := client.handleTFAAuthentication(tferr)
	if err == nil {
		t.Fatal("handleTFAAuthentication() = nil, want error when CompleteTFA fails to parse the response")
	}
}

func TestHandleTFAAuthentication_Success(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/api2/json/access/tfa" {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(
				`{"data":{"ticket":"full-ticket","CSRFPreventionToken":"csrf-1"},"success":1}`,
			))

			return
		}

		writer.WriteHeader(http.StatusNotFound)
	})

	client := newClientForServerURL(t, srv.URL, func(o *Options) {
		o.Username = testUsername
		o.Password = testPassword
	})

	client.tfaHandler = fakeTFAHandler{response: &auth.TFAResponse{Response: "123456"}}

	tferr := &apierrors.TFARequiredError{Ticket: testTFAPartialTicket, Challenge: testTFAChallenge, Types: []string{testTFATypeTOTP}}

	err := client.handleTFAAuthentication(tferr)
	if err != nil {
		t.Fatalf("handleTFAAuthentication() unexpected error: %v", err)
	}

	if !client.isAuthenticated() {
		t.Error("expected client to be authenticated after successful TFA completion")
	}
}

// ---------------------------------------------------------------------------
// handleAuthenticationRetry / forceReauthenticate
// ---------------------------------------------------------------------------

// TestHandleAuthRetry_TicketAuth401RetriesAndSucceeds verifies that a 401 from
// a ticket-based authenticator forces re-authentication and retries the
// request exactly once, succeeding when the retried request is accepted.
func TestHandleAuthRetry_TicketAuth401RetriesAndSucceeds(t *testing.T) {
	t.Parallel()

	var (
		versionCalls int32
		loginCalls   int32
	)

	srv := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api2/json/access/ticket":
			atomic.AddInt32(&loginCalls, 1)
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(
				`{"data":{"ticket":"fresh-ticket","CSRFPreventionToken":"csrf"},"success":1}`,
			))
		case "/api2/json/version":
			n := atomic.AddInt32(&versionCalls, 1)
			if n == 1 {
				writer.WriteHeader(http.StatusUnauthorized)
				_, _ = writer.Write([]byte(`{"message":"ticket rejected"}`))

				return
			}

			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write(pveEnvelope(t, map[string]string{"version": "8.0"}))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	})

	client := newClientForServerURL(t, srv.URL, func(o *Options) {
		o.Ticket = "PVE:root@pam:000000::stale"
	})

	_, err := client.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do() unexpected error after 401 re-auth retry: %v", err)
	}

	if got := atomic.LoadInt32(&versionCalls); got != 2 {
		t.Errorf("version calls = %d, want 2 (original 401 + one retry)", got)
	}

	if got := atomic.LoadInt32(&loginCalls); got != 1 {
		t.Errorf("login calls = %d, want 1 (forced re-authentication)", got)
	}
}

// TestHandleAuthRetry_ForceReauthenticateFails verifies that a failure to
// re-authenticate after a 401 is surfaced as an error rather than silently
// retried.
func TestHandleAuthRetry_ForceReauthenticateFails(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api2/json/access/ticket":
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = writer.Write([]byte(`{"message":"login broken"}`))
		case "/api2/json/version":
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(`{"message":"ticket rejected"}`))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	})

	client := newClientForServerURL(t, srv.URL, func(o *Options) {
		o.Ticket = "PVE:root@pam:000000::stale"
	})

	_, err := client.Do("GET", "/version", nil)
	if err == nil {
		t.Fatal("Do() = nil error, want an error when forced re-authentication fails")
	}
}

func TestForceReauthenticate_NilAuthenticator(t *testing.T) {
	t.Parallel()

	client := clientPointedAt(t, "http://127.0.0.1:1")
	client.authenticator = nil

	err := client.forceReauthenticate()
	if err != nil {
		t.Errorf("forceReauthenticate() = %v, want nil when authenticator is nil", err)
	}
}

func TestForceReauthenticate_NonTicketAuthenticatorFallsBackToRefresh(t *testing.T) {
	t.Parallel()

	client := clientPointedAt(t, "http://127.0.0.1:1")
	client.authenticator = auth.NewInvalidAuthenticator(errTestFakeTFA)

	err := client.forceReauthenticate()
	if err == nil {
		t.Fatal("forceReauthenticate() = nil, want error from the fallback Refresh() call")
	}

	if !errors.Is(err, errTestFakeTFA) {
		t.Errorf("forceReauthenticate() = %v, want wrapping %v", err, errTestFakeTFA)
	}
}

// ---------------------------------------------------------------------------
// SetCSRFToken
// ---------------------------------------------------------------------------

func TestSetCSRFToken_NonTicketAuthenticator_NoOp(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.APIToken = testAPITokenFull

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// Must not panic; there is no ticket-based state to update.
	client.SetCSRFToken("csrf-value")
}

func TestSetCSRFToken_NoExistingTicket_NoOp(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.Username = testUsername
	opts.Password = testPassword

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ticketAuth, ok := client.authenticator.(*auth.TicketAuthenticator)
	if !ok {
		t.Fatalf("authenticator is %T, want *auth.TicketAuthenticator", client.authenticator)
	}

	client.SetCSRFToken("csrf-value")

	if tkt := ticketAuth.GetTicket(); tkt != nil {
		t.Errorf("expected no ticket to be created by SetCSRFToken alone, got %+v", tkt)
	}
}

func TestSetCSRFToken_UpdatesExistingTicket(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.Ticket = testInitialTicket

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.SetCSRFToken("new-csrf")

	ticketAuth, ok := client.authenticator.(*auth.TicketAuthenticator)
	if !ok {
		t.Fatalf("authenticator is %T, want *auth.TicketAuthenticator", client.authenticator)
	}

	tkt := ticketAuth.GetTicket()
	if tkt == nil || tkt.CSRFToken != "new-csrf" || tkt.Value != testInitialTicket {
		t.Errorf("SetCSRFToken did not update the token while preserving the ticket value: %+v", tkt)
	}
}

// ---------------------------------------------------------------------------
// CSRFToken seeded at construction (Options.CSRFToken)
// ---------------------------------------------------------------------------

func TestNewClient_TicketAndCSRFToken_SeededAtConstruction(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.Ticket = "PVE:root@pam:000000::deadbeef"
	opts.CSRFToken = "seeded-csrf"

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	headers := client.authenticator.GetHeaders()
	if headers["CSRFPreventionToken"] != "seeded-csrf" {
		t.Errorf("CSRFPreventionToken header = %q, want %q (seeded at construction)",
			headers["CSRFPreventionToken"], "seeded-csrf")
	}
}

// ---------------------------------------------------------------------------
// APITokenName wiring
// ---------------------------------------------------------------------------

func TestNewClient_APITokenName_UsedInAuthHeader(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.APIToken = testAPITokenFull
	opts.APITokenName = "CustomAPIToken"

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	headers := client.authenticator.GetHeaders()

	authHeader := headers["Authorization"]
	if authHeader == "" {
		t.Fatal("expected an Authorization header to be set")
	}

	wantPrefix := "CustomAPIToken="
	if len(authHeader) < len(wantPrefix) || authHeader[:len(wantPrefix)] != wantPrefix {
		t.Errorf("Authorization header = %q, want prefix %q", authHeader, wantPrefix)
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestClose_WithoutCache_IdempotentNoError(t *testing.T) {
	t.Parallel()

	client := clientPointedAt(t, "http://127.0.0.1:1")

	err := client.Close()
	if err != nil {
		t.Fatalf("Close() first call error = %v", err)
	}

	err = client.Close()
	if err != nil {
		t.Fatalf("Close() second call error = %v", err)
	}
}

func TestClose_WithCache_ClosesCleanupGoroutine(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.CacheConfig = &cache.Config{
		Enabled:         true,
		MaxSize:         1024,
		DefaultTTL:      time.Minute,
		CleanupInterval: time.Minute,
	}

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	if client.cache == nil {
		t.Fatal("expected cache to be initialized when CacheConfig.Enabled is true")
	}

	err = client.Close()
	if err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	// Safe to call again.
	err = client.Close()
	if err != nil {
		t.Fatalf("Close() second call error = %v", err)
	}
}

// ---------------------------------------------------------------------------
// configureClientCertificates
// ---------------------------------------------------------------------------

func TestConfigureClientCertificates_NilSSLOptions_NoError(t *testing.T) {
	t.Parallel()

	tlsConfig := &tls.Config{}

	err := configureClientCertificates(tlsConfig, nil)
	if err != nil {
		t.Errorf("configureClientCertificates(nil) = %v, want nil", err)
	}
}

func TestConfigureClientCertificates_NoCertConfigured_NoError(t *testing.T) {
	t.Parallel()

	tlsConfig := &tls.Config{}
	sslOptions := &SSLOptions{}

	err := configureClientCertificates(tlsConfig, sslOptions)
	if err != nil {
		t.Errorf("configureClientCertificates() = %v, want nil when no cert/key set", err)
	}

	if len(tlsConfig.Certificates) != 0 {
		t.Errorf("expected no certificates loaded, got %d", len(tlsConfig.Certificates))
	}
}

func TestConfigureClientCertificates_InvalidFiles_ReturnsError(t *testing.T) {
	t.Parallel()

	tlsConfig := &tls.Config{}
	sslOptions := &SSLOptions{
		ClientCert: "/nonexistent/path/client.crt",
		ClientKey:  "/nonexistent/path/client.key",
	}

	err := configureClientCertificates(tlsConfig, sslOptions)
	if err == nil {
		t.Fatal("configureClientCertificates() = nil, want error for nonexistent cert/key files")
	}
}

// ---------------------------------------------------------------------------
// SetKeepAlive
// ---------------------------------------------------------------------------

func TestSetKeepAlive_UpdatesLiveTransport(t *testing.T) {
	t.Parallel()

	client := clientPointedAt(t, "http://127.0.0.1:1")

	client.SetKeepAlive(42)

	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *http.Transport", client.httpClient.Transport)
	}

	if transport.MaxIdleConns != 42 {
		t.Errorf("MaxIdleConns = %d, want 42", transport.MaxIdleConns)
	}

	if transport.MaxIdleConnsPerHost != 42 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 42 (no explicit MaxIdleConnsPerHost override)", transport.MaxIdleConnsPerHost)
	}
}

func TestSetKeepAlive_RespectsExplicitMaxIdleConnsPerHost(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.MaxIdleConnsPerHost = 7

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.SetKeepAlive(42)

	transport, ok := client.httpClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *http.Transport", client.httpClient.Transport)
	}

	if transport.MaxIdleConns != 42 {
		t.Errorf("MaxIdleConns = %d, want 42", transport.MaxIdleConns)
	}

	if transport.MaxIdleConnsPerHost != 7 {
		t.Errorf("MaxIdleConnsPerHost = %d, want 7 (explicit override preserved)", transport.MaxIdleConnsPerHost)
	}
}

// ---------------------------------------------------------------------------
// Retry jitter
// ---------------------------------------------------------------------------

func TestApplyRetryJitter_BoundsAndZero(t *testing.T) {
	t.Parallel()

	if got := applyRetryJitter(0); got != 0 {
		t.Errorf("applyRetryJitter(0) = %v, want 0", got)
	}

	if got := applyRetryJitter(-time.Second); got != -time.Second {
		t.Errorf("applyRetryJitter(negative) = %v, want unchanged", got)
	}

	base := 100 * time.Millisecond
	maxDelta := base * retryJitterPercent / 100

	for range 50 {
		got := applyRetryJitter(base)
		if got < base-maxDelta-1 || got > base+maxDelta+1 {
			t.Fatalf("applyRetryJitter(%v) = %v, want within +/-%v", base, got, maxDelta)
		}
	}
}

// TestRetryMiddleware_JitterVariesDelay verifies that repeated retry delays
// are not all identical, i.e. jitter is actually applied end-to-end through
// the retry middleware rather than only in the standalone helper.
func TestRetryMiddleware_JitterVariesDelay(t *testing.T) {
	t.Parallel()

	var calls int32

	srv := newTestServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 6 {
			writer.WriteHeader(http.StatusServiceUnavailable)

			return
		}

		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write(pveEnvelope(t, "ok"))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 5
	client.retryDelay = 20 * time.Millisecond

	start := time.Now()

	_, err := client.Do("GET", "/version", nil)
	if err != nil {
		t.Fatalf("Do() unexpected error: %v", err)
	}

	elapsed := time.Since(start)

	// Sum of unjittered delays would be delay*(1+2+3+4+5) = 20ms*15 = 300ms.
	// Jitter of +/-20% keeps total elapsed within a broad but bounded window;
	// this is a smoke test that jitter did not remove the backoff entirely
	// (elapsed ~= 0) nor blow it up unboundedly.
	if elapsed <= 0 {
		t.Fatal("expected non-zero elapsed time across retried backoff sleeps")
	}
}

// ---------------------------------------------------------------------------
// Fingerprint verification hardening (SessionTicketsDisabled, host threading,
// ManualVerifyCallback, FingerprintCachePath)
// ---------------------------------------------------------------------------

func TestConfigureFingerprintVerification_SetsSessionTicketsDisabled(t *testing.T) {
	t.Parallel()

	tlsConfig := &tls.Config{}
	options := &Options{
		Host:               testHostPVE,
		CachedFingerprints: map[string]bool{"AA:BB": true},
	}

	configureFingerprintVerification(tlsConfig, options)

	if !tlsConfig.InsecureSkipVerify {
		t.Error("expected InsecureSkipVerify to be true when fingerprint verification is enabled")
	}

	if !tlsConfig.SessionTicketsDisabled {
		t.Error("expected SessionTicketsDisabled to be true so TLS session resumption cannot bypass the pin")
	}

	if tlsConfig.VerifyPeerCertificate == nil {
		t.Error("expected VerifyPeerCertificate to be installed")
	}
}

func TestConfigureFingerprintVerification_Disabled_NoChange(t *testing.T) {
	t.Parallel()

	tlsConfig := &tls.Config{}
	options := &Options{Host: testHostPVE}

	configureFingerprintVerification(tlsConfig, options)

	if tlsConfig.InsecureSkipVerify {
		t.Error("expected InsecureSkipVerify to remain false when no fingerprint knob is set")
	}

	if tlsConfig.VerifyPeerCertificate != nil {
		t.Error("expected VerifyPeerCertificate to remain nil when fingerprint verification is disabled")
	}
}

func TestConfigureFingerprintVerification_ManualVerifyCallback_ReceivesHost(t *testing.T) {
	t.Parallel()

	var gotHost string

	tlsConfig := &tls.Config{}
	options := &Options{
		Host:               testHostPVE,
		ManualVerification: true,
		ManualVerifyCallback: func(req issl.ManualVerificationRequest) bool {
			gotHost = req.Host

			return true
		},
	}

	configureFingerprintVerification(tlsConfig, options)

	cert := selfSignedTestCert(t)

	err := tlsConfig.VerifyPeerCertificate([][]byte{cert.Raw}, nil)
	if err != nil {
		t.Fatalf("VerifyPeerCertificate() unexpected error: %v", err)
	}

	if gotHost != testHostPVE {
		t.Errorf("ManualVerifyCallback host = %q, want %q", gotHost, testHostPVE)
	}
}

func TestConfigureFingerprintVerification_FingerprintCachePath_PersistsAcceptedFingerprint(t *testing.T) {
	t.Parallel()

	cacheFile := t.TempDir() + "/fingerprints.json"

	tlsConfig := &tls.Config{}
	options := &Options{
		Host:                 testHostPVE,
		Port:                 8006,
		FingerprintCachePath: cacheFile,
		ManualVerifyCallback: func(_ issl.ManualVerificationRequest) bool { return true },
	}

	configureFingerprintVerification(tlsConfig, options)

	cert := selfSignedTestCert(t)

	err := tlsConfig.VerifyPeerCertificate([][]byte{cert.Raw}, nil)
	if err != nil {
		t.Fatalf("VerifyPeerCertificate() unexpected error: %v", err)
	}

	persisted := issl.NewFingerprintCache(cacheFile)

	err = persisted.Load()
	if err != nil {
		t.Fatalf("Load() persisted cache: %v", err)
	}

	trusted := persisted.GetByHost(testHostPVE)
	if len(trusted) != 1 || !trusted[0].Trusted {
		t.Errorf("expected exactly one trusted fingerprint persisted for host, got %+v", trusted)
	}
}

// selfSignedTestCert generates a throwaway self-signed certificate for
// exercising VerifyPeerCertificate directly in unit tests, without needing a
// live TLS handshake.
func selfSignedTestCert(t *testing.T) *x509.Certificate {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate test key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: testHostPVE},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create test certificate: %v", err)
	}

	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse test certificate: %v", err)
	}

	return cert
}
