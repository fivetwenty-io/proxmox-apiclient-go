package http //nolint:testpackage // white-box test: reaches the client's retry setting through the shared helpers

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// errTransport is a RoundTripper that fails every request with err and counts
// the requests.
type errTransport struct {
	err   error
	calls atomic.Int32
}

func (e *errTransport) RoundTrip(*http.Request) (*http.Response, error) {
	e.calls.Add(1)

	return nil, e.err
}

// TestCertificateErrorWithFramingTextIsAttemptedOnce sends a request whose
// transport fails with a hostname error. The certificate name in the error
// holds a phrase the framing cleaner searches for, as a server could choose
// it. The cleaner must leave the error alone, so the retry loop still sees a
// certificate the client refuses, stops at once, and keeps the types a caller
// matches.
func TestCertificateErrorWithFramingTextIsAttemptedOnce(t *testing.T) {
	t.Parallel()

	cause := &tls.CertificateVerificationError{
		Err: x509.HostnameError{
			Certificate: &x509.Certificate{DNSNames: []string{"bad Content-Length"}},
			Host:        "h",
		},
	}
	transport := &errTransport{err: cause}

	opts := minimalHTTPOptions()
	opts.APIToken = testAPITokenFull
	opts.WrapTransport = func(*http.Transport) http.RoundTripper { return transport }

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	client.SetMaxRetries(redirectTestRetry)
	client.SetRetryDelay(time.Millisecond)

	_, err = client.DoWithContext(context.Background(), http.MethodGet, "/version", nil)
	if err == nil {
		t.Fatal("request succeeded, want the certificate error")
	}

	if got := transport.calls.Load(); got != 1 {
		t.Errorf("transport saw %d requests, want 1", got)
	}

	var verifyErr *tls.CertificateVerificationError
	if !errors.As(err, &verifyErr) {
		t.Errorf("error = %v, want a *tls.CertificateVerificationError in the chain", err)
	}

	var hostnameErr x509.HostnameError
	if !errors.As(err, &hostnameErr) {
		t.Errorf("error = %v, want an x509.HostnameError in the chain", err)
	}
}
