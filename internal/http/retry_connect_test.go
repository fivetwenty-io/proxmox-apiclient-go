package http //nolint:testpackage // white-box: exercises retryMiddleware directly

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	issl "github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/ssl"
	apierrors "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/errors"
)

// countingClient points a client at addr and rebuilds its middleware chain as
// retryMiddleware wrapping a counter, so tests can assert the exact number of
// transport attempts a request made.
//
// Order matters: executeWithMiddleware wraps in reverse, so middleware[0] is the
// OUTERMOST layer. The counter must sit AFTER retryMiddleware to be re-entered
// on every attempt; placing it first counts the whole retry loop as one call.
func countingClient(t *testing.T, addr string, calls *int32) *Client {
	t.Helper()

	client := clientPointedAt(t, addr)
	client.maxRetries = 3
	client.retryDelay = time.Millisecond
	client.middleware = []Middleware{
		client.retryMiddleware,
		func(r *http.Request, next Handler) (*http.Response, error) {
			atomic.AddInt32(calls, 1)

			return next(r)
		},
	}

	return client
}

// TestRetryMiddleware_DialRefusedNotRetried verifies that a refused TCP dial is
// terminal even for an idempotent method. Retrying cannot help when the
// connection was never established, and each attempt costs a full dial timeout.
func TestRetryMiddleware_DialRefusedNotRetried(t *testing.T) {
	t.Parallel()

	var calls int32

	client := countingClient(t, "http://127.0.0.1:1", &calls) // nothing listens on port 1

	_, err := client.Do("GET", "/version", nil)
	if err == nil {
		t.Fatal("expected an error dialing a closed port")
	}

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (a refused dial must not be retried)", got)
	}

	var connErr *apierrors.ConnectionError
	if !errorsAs(err, &connErr) {
		t.Errorf("error = %v, want a *apierrors.ConnectionError", err)
	}
}

// TestRetryMiddleware_DNSFailureNotRetried verifies that an unresolvable host is
// terminal. Repeating the lookup three more times cannot make the name resolve.
//
// The lookup failure is stubbed at the dialer rather than using a real
// unresolvable name: interposing resolvers (captive portals, wildcard DNS,
// blackholing filters) can turn NXDOMAIN into a slow timeout, which is a
// different error class entirely.
func TestRetryMiddleware_DNSFailureNotRetried(t *testing.T) {
	t.Parallel()

	var calls int32

	client := countingClient(t, "http://pmx-nonexistent.invalid:8006", &calls)
	client.httpClient.Transport = &http.Transport{
		DialContext: func(_ context.Context, _, _ string) (net.Conn, error) {
			return nil, &net.DNSError{Err: "no such host", Name: "pmx-nonexistent.invalid", IsNotFound: true}
		},
	}

	_, err := client.Do("GET", "/version", nil)
	if err == nil {
		t.Fatal("expected an error resolving an invalid host")
	}

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (a DNS failure must not be retried)", got)
	}

	var connErr *apierrors.ConnectionError
	if !errorsAs(err, &connErr) {
		t.Errorf("error = %v, want a *apierrors.ConnectionError", err)
	}
}

// TestRetryMiddleware_PostConnectErrorStillRetried is the regression guard for
// over-broad classification: a transport error that happens AFTER the connection
// was established (here the server hangs up mid-response) is genuinely transient
// and must still be retried.
func TestRetryMiddleware_PostConnectErrorStillRetried(t *testing.T) {
	t.Parallel()

	var calls int32

	srv := newTestServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		hijacker, ok := writer.(http.Hijacker)
		if !ok {
			t.Error("test server does not support hijacking")

			return
		}

		conn, _, hijackErr := hijacker.Hijack()
		if hijackErr != nil {
			t.Errorf("hijack: %v", hijackErr)

			return
		}

		_ = conn.Close() // hang up without writing a response
	})

	client := countingClient(t, srv.URL, &calls)

	_, err := client.Do("GET", "/version", nil)
	if err == nil {
		t.Fatal("expected an error when the server hangs up")
	}

	if got := atomic.LoadInt32(&calls); got != 4 {
		t.Errorf("calls = %d, want 4 (a post-connect failure must still be retried)", got)
	}
}

// TestRetryMiddleware_BackoffHonorsCancellation verifies that canceling the
// request context interrupts the retry backoff instead of sleeping it out. The
// backoff used to be a bare time.Sleep, so Ctrl-C could not shorten a retry
// storm against an unhealthy server.
func TestRetryMiddleware_BackoffHonorsCancellation(t *testing.T) {
	t.Parallel()

	srv := newTestServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte(`{"message":"unavailable"}`))
	})

	client := clientPointedAt(t, srv.URL)
	client.maxRetries = 3
	client.retryDelay = time.Hour // would hang effectively forever if not cancellable

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	done := make(chan error, 1)

	go func() {
		_, err := client.DoWithContext(ctx, "GET", "/version", nil)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected an error after cancellation")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("retry backoff ignored context cancellation")
	}
}

// TestIsTerminalTransportError covers the classifier directly, including the
// negative cases that keep transient failures retryable.
func TestIsTerminalTransportError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"dial refused", &net.OpError{Op: "dial", Err: connRefusedError{}}, true},
		{"dns failure", &net.DNSError{Err: "no such host", Name: "nope.invalid"}, true},
		{"read reset", &net.OpError{Op: "read", Err: connRefusedError{}}, false},
		{"write reset", &net.OpError{Op: "write", Err: connRefusedError{}}, false},
		{"nil", nil, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := isTerminalTransportError(tc.err); got != tc.want {
				t.Errorf("isTerminalTransportError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// connRefusedError is a stand-in for a syscall-level connection error.
type connRefusedError struct{}

func (connRefusedError) Error() string { return "connection refused" }

// tlsCountingClient is countingClient pointed at a TLS test server, with the
// transport replaced by one built from cfg, so a test decides how the
// client verifies the server's certificate.
func tlsCountingClient(t *testing.T, cfg *tls.Config, calls *int32) *Client {
	t.Helper()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"data":{}}`))
	}))
	t.Cleanup(srv.Close)

	client := countingClient(t, srv.URL, calls)
	client.httpClient.Transport = &http.Transport{TLSClientConfig: cfg}

	return client
}

// TestRetryMiddleware_FingerprintMismatchNotRetried verifies that a server
// whose certificate fails the fingerprint pin is refused after one attempt.
// The same certificate comes back on every retry, so retrying only adds the
// handshakes and the backoff.
func TestRetryMiddleware_FingerprintMismatchNotRetried(t *testing.T) {
	t.Parallel()

	var calls int32

	client := tlsCountingClient(t, &tls.Config{
		InsecureSkipVerify: true,
		VerifyPeerCertificate: func(_ [][]byte, _ [][]*x509.Certificate) error {
			return fmt.Errorf("%w: AA:BB", issl.ErrCannotVerifyFingerprint)
		},
	}, &calls)

	_, err := client.Do("GET", "/version", nil)
	if err == nil {
		t.Fatal("expected an error for a certificate that fails the pin")
	}

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (a pin mismatch must not be retried)", got)
	}

	if !errors.Is(err, issl.ErrCannotVerifyFingerprint) {
		t.Errorf("error = %v, want it to wrap ErrCannotVerifyFingerprint", err)
	}

	if !strings.Contains(err.Error(), "request failed after 1 attempt(s)") {
		t.Errorf("error = %v, want the retry loop's attempt count", err)
	}
}

// TestRetryMiddleware_UntrustedChainNotRetried verifies that a certificate
// standard chain verification rejects is refused after one attempt.
func TestRetryMiddleware_UntrustedChainNotRetried(t *testing.T) {
	t.Parallel()

	var calls int32

	client := tlsCountingClient(t, &tls.Config{RootCAs: x509.NewCertPool(), MinVersion: tls.VersionTLS12}, &calls)

	_, err := client.Do("GET", "/version", nil)
	if err == nil {
		t.Fatal("expected an error for a certificate from an unknown authority")
	}

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("calls = %d, want 1 (an untrusted chain must not be retried)", got)
	}
}

// TestIsCertificateVerificationError covers the classifier directly,
// including handshake failures that are not a refusal of the certificate
// and so stay retryable.
func TestIsCertificateVerificationError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"pin mismatch", fmt.Errorf("%w: AA", issl.ErrCannotVerifyFingerprint), true},
		{"untrusted fingerprint", fmt.Errorf("%w: AA", issl.ErrCertificateFingerprintNotTrusted), true},
		{"callback refused", fmt.Errorf("%w for fingerprint AA", issl.ErrCertificateVerificationFailed), true},
		{"unknown fingerprint", fmt.Errorf("%w: AA", issl.ErrUnknownCertificateFingerprint), true},
		{"no certificate", issl.ErrNoCertificatesProvided, true},
		{"chain rejected", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, true},
		{"unknown authority", &url.Error{Op: http.MethodGet, URL: "https://pve", Err: x509.UnknownAuthorityError{}}, true},
		{"wrong hostname", x509.HostnameError{Host: "pve"}, true},
		{"expired", x509.CertificateInvalidError{Reason: x509.Expired}, true},
		{"not tls", tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"}, false},
		{"handshake timeout", &url.Error{Op: http.MethodGet, URL: "https://pve", Err: context.DeadlineExceeded}, false},
		{"read reset", &net.OpError{Op: "read", Err: connRefusedError{}}, false},
		{"nil", nil, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := isCertificateVerificationError(tc.err); got != tc.want {
				t.Errorf("isCertificateVerificationError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// pinnedCountingClient builds a client through Options against a TLS test
// server, so the certificate check runs through configureFingerprintVerification
// exactly as it does in production, and counts transport attempts the way
// countingClient does.
func pinnedCountingClient(t *testing.T, configure func(*Options), calls *int32) *Client {
	t.Helper()

	srv := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"data":{}}`))
	}))
	t.Cleanup(srv.Close)

	addr, ok := srv.Listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address %v is not TCP", srv.Listener.Addr())
	}

	opts := minimalHTTPOptions()
	opts.Protocol = testProtoHTTPS
	opts.Port = addr.Port
	configure(opts)

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.maxRetries = 3
	client.retryDelay = time.Millisecond
	client.middleware = []Middleware{
		client.retryMiddleware,
		func(r *http.Request, next Handler) (*http.Response, error) {
			atomic.AddInt32(calls, 1)

			return next(r)
		},
	}

	return client
}

// TestRetryMiddleware_PinWiringNotRetried drives the production pinning
// wiring end to end: a cached pin that does not match the server, and a
// manual-verification callback that rejects it. Each must end after one
// transport attempt, which fails if the wiring stops carrying the
// verifier's sentinel to the retry loop.
func TestRetryMiddleware_PinWiringNotRetried(t *testing.T) {
	t.Parallel()

	const wrongPin = "00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF"

	t.Run("cached pin mismatch", func(t *testing.T) {
		t.Parallel()

		var calls int32

		client := pinnedCountingClient(t, func(opts *Options) {
			opts.CachedFingerprints = map[string]bool{wrongPin: true}
		}, &calls)

		_, err := client.Do("GET", "/version", nil)
		if !errors.Is(err, issl.ErrCannotVerifyFingerprint) {
			t.Fatalf("error = %v, want it to wrap ErrCannotVerifyFingerprint", err)
		}

		if got := atomic.LoadInt32(&calls); got != 1 {
			t.Errorf("calls = %d, want 1 (a pin mismatch must not be retried)", got)
		}
	})

	t.Run("manual verification rejected", func(t *testing.T) {
		t.Parallel()

		var calls, asked int32

		client := pinnedCountingClient(t, func(opts *Options) {
			opts.ManualVerification = true
			opts.ManualVerifyCallback = func(issl.ManualVerificationRequest) bool {
				atomic.AddInt32(&asked, 1)

				return false
			}
		}, &calls)

		_, err := client.Do("GET", "/version", nil)
		if !errors.Is(err, issl.ErrUnknownCertificateFingerprint) {
			t.Fatalf("error = %v, want it to wrap ErrUnknownCertificateFingerprint", err)
		}

		if got := atomic.LoadInt32(&calls); got != 1 {
			t.Errorf("calls = %d, want 1 (a rejected certificate must not be retried)", got)
		}

		if got := atomic.LoadInt32(&asked); got != 1 {
			t.Errorf("callback asked %d times, want 1", got)
		}
	})
}
