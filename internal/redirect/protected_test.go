package redirect_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/permanent"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect/redirecttest"
)

// markerCertificateName is a certificate name that holds a phrase the framing
// cleaner searches for, as a server could choose it.
const markerCertificateName = "bad Content-Length"

// hostnameFailure is the error a TLS client returns when the certificate a
// server presents does not name the host, with a marker phrase in the name.
func hostnameFailure() error {
	return &tls.CertificateVerificationError{
		Err: x509.HostnameError{
			Certificate: &x509.Certificate{DNSNames: []string{markerCertificateName}},
			Host:        "h",
		},
	}
}

// TestScrubKeepsTheChainOfACertificateErrorWithMarkerText builds the error the
// verification found: a hostname error whose certificate name holds a framing
// marker. Scrub must leave it as the same value, with its types and its full
// text.
func TestScrubKeepsTheChainOfACertificateErrorWithMarkerText(t *testing.T) {
	t.Parallel()

	cause := hostnameFailure()
	original := &url.Error{Op: opGet, URL: "https://h/api2/json/version", Err: cause}
	want := original.Error()

	got := redirect.Scrub(original)

	var verifyErr *tls.CertificateVerificationError
	if !errors.As(got, &verifyErr) {
		t.Errorf("Scrub dropped the *tls.CertificateVerificationError from %v", got)
	}

	var hostnameErr x509.HostnameError
	if !errors.As(got, &hostnameErr) {
		t.Errorf("Scrub dropped the x509.HostnameError from %v", got)
	}

	if got.Error() != want {
		t.Errorf("text = %q, want %q", got.Error(), want)
	}

	var framing *redirect.FramingError
	if errors.As(got, &framing) {
		t.Errorf("Scrub reclassified %v as a framing error", got)
	}

	if permanent.Is(got) {
		t.Errorf("Scrub marked %v permanent", got)
	}
}

// TestScrubNeverReclassifiesContextCertificateOrRefusalErrors wraps each kind
// of error the retry loop decides by type in text that holds a marker for
// every cleaner. None may change.
func TestScrubNeverReclassifiesContextCertificateOrRefusalErrors(t *testing.T) {
	t.Parallel()

	causes := map[string]error{
		"canceled": fmt.Errorf("%s %s: %w", markerCertificateName, "failed to parse Location header", context.Canceled),
		"deadline": fmt.Errorf("malformed HTTP status code: %w", context.DeadlineExceeded),
		"refused": fmt.Errorf("%s: %w", markerCertificateName,
			&redirect.Error{Status: 302, From: "http://h/x"}),
		"unknown authority": fmt.Errorf("malformed MIME header: %w",
			x509.UnknownAuthorityError{Cert: &x509.Certificate{}}),
		"invalid certificate": fmt.Errorf("unsupported transfer encoding: %w",
			x509.CertificateInvalidError{Cert: &x509.Certificate{}, Reason: x509.Expired}),
		"hostname": hostnameFailure(),
	}

	for name, cause := range causes {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for shape, input := range map[string]error{
				"bare":      cause,
				"url error": &url.Error{Op: opGet, URL: "https://h/x?ticket=QSECRET", Err: cause},
			} {
				got := redirect.Scrub(input)
				if !errors.Is(got, cause) {
					t.Errorf("%s: Scrub lost %v from %v", shape, cause, got)
				}

				var framing *redirect.FramingError
				if errors.As(got, &framing) {
					t.Errorf("%s: Scrub turned %v into a framing error", shape, cause)
				}

				var header *redirect.HeaderError
				if errors.As(got, &header) {
					t.Errorf("%s: Scrub turned %v into a header error", shape, cause)
				}

				if permanent.Is(got) != permanent.Is(cause) {
					t.Errorf("%s: permanence of %v changed", shape, cause)
				}
			}
		})
	}
}

// TestFramingErrorUnwrapsToTheOriginalCause checks that the cleaned error is
// still the error it replaced for errors.Is and errors.As, and that neither
// its text nor its %#v form holds the quoted bytes.
func TestFramingErrorUnwrapsToTheOriginalCause(t *testing.T) {
	t.Parallel()

	cause := &textError{brokenConnection + "bad Content-Length " + fmt.Sprintf("%q", serverText)}
	got := redirect.Scrub(&url.Error{Op: opGet, URL: "http://h/x", Err: cause})

	if !errors.Is(got, error(cause)) {
		t.Errorf("errors.Is lost the original cause from %v", got)
	}

	var original *textError
	if !errors.As(got, &original) || original != cause {
		t.Errorf("errors.As did not reach the original cause from %v", got)
	}

	var framing *redirect.FramingError
	if !errors.As(got, &framing) {
		t.Fatalf("Scrub returned %v, want a framing error", got)
	}

	shown := []string{
		got.Error(), fmt.Sprintf("%v", framing), fmt.Sprintf("%+v", got), fmt.Sprintf("%#v", framing),
		fmt.Sprintf("%#v", got),
	}

	for _, text := range shown {
		for _, secret := range append(redirecttest.Secrets(), "FRAGSECRET") {
			if strings.Contains(text, secret) {
				t.Errorf("%q holds %q", text, secret)
			}
		}
	}
}
