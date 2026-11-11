package redirect_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/permanent"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect/redirecttest"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/safetext"
)

// rawControls are characters that must never reach an error as themselves.
func rawControls() []string {
	return []string{"\u001b", "\u202e", "\u009b", "\n"}
}

// assertNoRawControls fails when text holds a raw control or direction
// character.
func assertNoRawControls(t *testing.T, text string) {
	t.Helper()

	for _, raw := range rawControls() {
		if strings.Contains(text, raw) {
			t.Errorf("%.200q holds the raw character %q", text, raw)
		}
	}
}

// TestFramingKindStopsAtTheEarliestMarker builds protocol errors whose quoted
// bytes hold a second marker phrase. The kind must end at the marker that
// comes first in the text, not at the one listed first, so none of the
// server's bytes stay.
func TestFramingKindStopsAtTheEarliestMarker(t *testing.T) {
	t.Parallel()

	cases := map[string]framingCase{
		"content length quoting a status marker": {
			brokenConnection + `bad Content-Length "EVILBYTES malformed HTTP response"`,
			brokenConnection + "bad Content-Length",
		},
		"goaway debug quoting a length marker": {
			"http2: server sent GOAWAY and closed the connection; LastStreamID=3, ErrCode=PROTOCOL_ERROR, " +
				`debug="EVILBYTES bad Content-Length"`,
			"http2: server sent GOAWAY and closed the connection; LastStreamID=3, ErrCode=PROTOCOL_ERROR",
		},
		"version quoting a code marker": {
			brokenConnection + `malformed HTTP version "EVILBYTES malformed HTTP status code"`,
			brokenConnection + "malformed HTTP version",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := redirect.Scrub(&textError{tc.text})

			var framing *redirect.FramingError
			if !errors.As(got, &framing) {
				t.Fatalf("Scrub returned %v, want a framing error", got)
			}

			if framing.Kind != tc.want {
				t.Errorf("kind = %q, want %q", framing.Kind, tc.want)
			}

			if strings.Contains(got.Error(), "EVILBYTES") {
				t.Errorf("%q keeps the server's bytes", got.Error())
			}
		})
	}
}

// TestFramingKindIsCapped feeds Scrub a protocol error whose text before the
// marker is 100,000 bytes long. The kind must be cut.
func TestFramingKindIsCapped(t *testing.T) {
	t.Parallel()

	text := strings.Repeat("A", 100_000) + ` bad Content-Length "x"`

	got := redirect.Scrub(&textError{text})

	var framing *redirect.FramingError
	if !errors.As(got, &framing) {
		t.Fatalf("Scrub returned a %T, want a framing error", got)
	}

	if limit := safetext.MaxServerTextBytes + len("...(truncated)"); len(framing.Kind) > limit {
		t.Errorf("kind is %d bytes, want at most %d", len(framing.Kind), limit)
	}
}

// longPath is a redirect target path that percent-encoding triples.
func longPath() string { return "/" + strings.Repeat("\u202e", 300_000) }

// maxURLText is the longest URL text a refusal may carry, with its marker.
const maxURLText = safetext.MaxServerTextBytes + len("...(truncated)")

// TestRefusalOfALongLocationIsBounded sends a 302 whose Location is close to a
// megabyte. The refusal and its To field must stay short.
func TestRefusalOfALongLocationIsBounded(t *testing.T) {
	t.Parallel()

	server := redirecttest.Serve(t, http.StatusFound, []byte(longPath()))
	client := &http.Client{Transport: redirect.Guard(nil, nil), CheckRedirect: redirect.Wrap(nil)}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL()+"/version", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()

		t.Fatal("got no error, want the redirect refused")
	}

	err = redirect.Scrub(err)

	var refusal *redirect.Error
	if !errors.As(err, &refusal) {
		t.Fatalf("error = %v, want a refusal", err)
	}

	if len(refusal.To) > maxURLText || refusal.To == "" {
		t.Errorf("To is %d bytes, want between 1 and %d", len(refusal.To), maxURLText)
	}

	if len(err.Error()) > 4*safetext.MaxServerTextBytes {
		t.Errorf("error is %d bytes, want it bounded", len(err.Error()))
	}
}

// TestRefusePolicyBoundsBothURLs calls the policy net/http runs for a followed
// redirect with a long target and a long previous hop.
func TestRefusePolicyBoundsBothURLs(t *testing.T) {
	t.Parallel()

	long := &url.URL{Scheme: "http", Host: "h", Path: longPath()}

	err := redirect.Refuse(&http.Request{URL: long}, []*http.Request{{URL: long}})

	var refusal *redirect.Error
	if !errors.As(err, &refusal) {
		t.Fatalf("Refuse returned %v, want a refusal", err)
	}

	if len(refusal.From) > maxURLText || len(refusal.To) > maxURLText {
		t.Errorf("From is %d bytes and To is %d bytes, want at most %d", len(refusal.From), len(refusal.To), maxURLText)
	}
}

// TestScrubBoundsTheURLOfAURLError checks that a URL error naming a long URL
// comes back with the URL cut.
func TestScrubBoundsTheURLOfAURLError(t *testing.T) {
	t.Parallel()

	got := redirect.Scrub(&url.Error{Op: opGet, URL: "http://h" + longPath(), Err: errors.New("connection refused")})

	var urlErr *url.Error
	if !errors.As(got, &urlErr) || len(urlErr.URL) > maxURLText {
		t.Errorf("Scrub returned %v, want a URL error with the URL cut", got)
	}
}

// hostileCertificateName is a certificate name with an escape sequence, as an
// attacker on the network path can present before any authentication.
const hostileCertificateName = "evil\u001b[2Jname.example"

// TestScrubEscapesACertificateNameAndKeepsTheChain feeds Scrub the hostname
// error a certificate with a hostile name produces. The text must lose the raw
// escape, and errors.As must still find the certificate errors.
func TestScrubEscapesACertificateNameAndKeepsTheChain(t *testing.T) {
	t.Parallel()

	cause := &tls.CertificateVerificationError{
		Err: x509.HostnameError{
			Certificate: &x509.Certificate{DNSNames: []string{hostileCertificateName}},
			Host:        "pve.example",
		},
	}

	for shape, input := range map[string]error{
		"bare":      cause,
		"url error": &url.Error{Op: opGet, URL: "https://pve.example/x", Err: cause},
	} {
		got := redirect.Scrub(input)
		assertNoRawControls(t, got.Error())
		assertNoRawControls(t, fmt.Sprintf("%+v", got))

		if !strings.Contains(got.Error(), `evil\x1b[2Jname.example`) {
			t.Errorf("%s: %q does not show the escaped name", shape, got.Error())
		}

		var hostnameErr x509.HostnameError
		if !errors.As(got, &hostnameErr) {
			t.Errorf("%s: Scrub dropped the x509.HostnameError from %v", shape, got)
		}

		var verifyErr *tls.CertificateVerificationError
		if !errors.As(got, &verifyErr) {
			t.Errorf("%s: Scrub dropped the *tls.CertificateVerificationError from %v", shape, got)
		}

		if permanent.Is(got) {
			t.Errorf("%s: Scrub marked %v permanent", shape, got)
		}
	}
}

// TestScrubEscapesAndCapsAnyCauseText feeds Scrub a cause that matches no
// cleaner, as a proxy's CONNECT reason phrase does, and a cause that is
// 100,000 bytes long.
func TestScrubEscapesAndCapsAnyCauseText(t *testing.T) {
	t.Parallel()

	reason := errors.New("Forbidden \u202eEVIL\u009b2J")

	got := redirect.Scrub(&url.Error{Op: opGet, URL: "https://pve.example/x", Err: reason})
	assertNoRawControls(t, got.Error())

	if !errors.Is(got, reason) {
		t.Errorf("Scrub lost the cause from %v", got)
	}

	long := errors.New(strings.Repeat("B", 100_000))

	got = redirect.Scrub(&url.Error{Op: opGet, URL: "https://pve.example/x", Err: long})
	if limit := safetext.MaxErrorTextBytes + 4*safetext.MaxServerTextBytes; len(got.Error()) > limit {
		t.Errorf("error is %d bytes, want at most %d", len(got.Error()), limit)
	}

	if !errors.Is(got, long) {
		t.Errorf("Scrub lost the long cause from %v", got)
	}
}

// selfSignedName returns a certificate and key that name only name.
func selfSignedName(t *testing.T, name string) (tls.Certificate, *x509.CertPool) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "x"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{name}, IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}

	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Skipf("the standard library refused the certificate name: %v", err)
	}

	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(parsed)

	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}

// TestHostileCertificateNameOverTheWire presents a certificate whose name
// holds an escape sequence and checks the error a guarded client returns, as
// Scrub leaves it, for the raw byte.
func TestHostileCertificateNameOverTheWire(t *testing.T) {
	t.Parallel()

	cert, pool := selfSignedName(t, hostileCertificateName)

	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			go func(conn net.Conn) {
				_ = conn.(*tls.Conn).HandshakeContext(context.Background())
				_ = conn.Close()
			}(conn)
		}
	}()

	transport := &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: pool, ServerName: "pve.example", MinVersion: tls.VersionTLS12,
	}}
	t.Cleanup(transport.CloseIdleConnections)

	client := &http.Client{Transport: redirect.Guard(transport, nil), CheckRedirect: redirect.Wrap(nil)}

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://"+listener.Addr().String()+"/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()

		t.Fatal("got no error, want the certificate rejected")
	}

	got := redirect.Scrub(err)
	assertNoRawControls(t, got.Error())

	var hostnameErr x509.HostnameError
	if !errors.As(got, &hostnameErr) {
		t.Errorf("Scrub dropped the x509.HostnameError from %v", got)
	}
}

// hostileTimeout is a timeout whose text holds an escape sequence.
type hostileTimeout struct{}

func (hostileTimeout) Error() string   { return "i/o timeout reading from evil\u001b[2J" }
func (hostileTimeout) Timeout() bool   { return true }
func (hostileTimeout) Temporary() bool { return true }

// TestScrubKeepsTheTimeoutOfAnEscapedCause checks that a *url.Error whose cause
// had its text escaped still reports a timeout. A *url.Error asks its direct
// cause, so the wrapper must answer for the error it wraps.
func TestScrubKeepsTheTimeoutOfAnEscapedCause(t *testing.T) {
	t.Parallel()

	for shape, cause := range map[string]error{
		"bare":   hostileTimeout{},
		"nested": &url.Error{Op: opGet, URL: secretURL, Err: hostileTimeout{}},
	} {
		got := redirect.Scrub(&url.Error{Op: opGet, URL: "https://pve.example/x", Err: cause})
		assertNoRawControls(t, got.Error())

		var netErr net.Error
		if !errors.As(got, &netErr) || !netErr.Timeout() {
			t.Errorf("%s: %v no longer reads as a timeout", shape, got)
		}
	}
}
