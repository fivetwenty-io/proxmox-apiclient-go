package redirect_test

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/permanent"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect/redirecttest"
)

// serverText is the text a hostile server puts in the bytes a protocol error
// quotes.
const serverText = "http://user:HUNTER2@h/x?ticket=QSECRET#FRAGSECRET"

// brokenConnection is the prefix net/http gives a fault it finds while it
// reads a response.
const brokenConnection = "net/http: HTTP/1.x transport connection broken: "

// textError is an error with a fixed message, so a test can build the text
// net/http would produce.
type textError struct{ text string }

func (e *textError) Error() string { return e.text }

// framingCase is a message net/http builds and the message Scrub must leave.
type framingCase struct{ text, want string }

// framingCases returns one message for each kind of protocol error Scrub
// cleans. Each is the kind of fault, a joiner, and the quoted bytes, with the
// broken-connection prefix unless the error comes from the HTTP/2 transport.
func framingCases() map[string]framingCase {
	quoted := fmt.Sprintf("%q", serverText)

	type fault struct{ prefix, kind, joiner, suffix string }

	faults := map[string]fault{
		"status line":     {brokenConnection, "malformed HTTP response", " ", ""},
		"status code":     {brokenConnection, "malformed HTTP status code", " ", ""},
		"version":         {brokenConnection, "malformed HTTP version", " ", ""},
		"content length":  {brokenConnection, "bad Content-Length", " ", ""},
		"empty length":    {brokenConnection, "invalid empty Content-Length", " ", ""},
		"transfer coding": {brokenConnection, "unsupported transfer encoding", ": ", ""},
		"many codings":    {brokenConnection, "too many transfer encodings", ": ", ""},
		"trailer key":     {brokenConnection, "invalid Trailer key", " ", ""},
		"lower trailer":   {brokenConnection, "bad trailer key", " ", ""},
		"two lengths": {
			brokenConnection + "http: ", "message cannot contain multiple Content-Length headers", "; got [", "]",
		},
		"pseudo header": {"", "invalid pseudo-header", " ", ""},
		"two pseudo":    {"", "duplicate pseudo-header", " ", ""},
		"header name":   {"", "invalid header field name", " ", ""},
		"header value":  {"", "invalid header field value for", " ", ""},
		"goaway": {
			"http2: server sent GOAWAY and closed the connection; LastStreamID=3, ",
			"ErrCode=PROTOCOL_ERROR", ", debug=", "",
		},
	}

	cases := make(map[string]framingCase, len(faults))
	for name, f := range faults {
		cases[name] = framingCase{f.prefix + f.kind + f.joiner + quoted + f.suffix, f.prefix + f.kind}
	}

	return cases
}

// TestScrubKeepsTheKindAndDropsTheServersBytes feeds Scrub the messages
// net/http and its HTTP/2 transport build from response bytes. Each must come
// back as the kind of fault alone, and must not be marked permanent.
func TestScrubKeepsTheKindAndDropsTheServersBytes(t *testing.T) {
	t.Parallel()

	for name, tc := range framingCases() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			original := &url.Error{Op: "Get", URL: "http://h/api2/json/version", Err: &textError{tc.text}}
			got := redirect.Scrub(original)

			var urlErr *url.Error
			if !errors.As(got, &urlErr) {
				t.Fatalf("Scrub returned %T, want a *url.Error", got)
			}

			if urlErr.Err.Error() != tc.want {
				t.Errorf("cause = %q, want %q", urlErr.Err.Error(), tc.want)
			}

			for _, secret := range append(redirecttest.Secrets(), "FRAGSECRET") {
				if strings.Contains(got.Error(), secret) {
					t.Errorf("%q holds %q", got.Error(), secret)
				}
			}

			var framing *redirect.FramingError
			if !errors.As(got, &framing) {
				t.Errorf("Scrub returned %v, want a *redirect.FramingError in the chain", got)
			}

			if permanent.Is(got) {
				t.Errorf("%v is permanent, want the retry behavior left alone", got)
			}
		})
	}
}

// TestScrubLeavesFaultsThatQuoteNothingFromTheServer checks that messages
// outside the protocol markers pass through as the same value, among them the
// request-side header errors, which name the caller's own header.
func TestScrubLeavesFaultsThatQuoteNothingFromTheServer(t *testing.T) {
	t.Parallel()

	for _, text := range []string{
		"dial tcp 127.0.0.1:1: connect: connection refused",
		"net/http: invalid header field name \"X Bad\"",
		"net/http: invalid header field value for \"X-Token\"",
		"tls: failed to verify certificate: x509: certificate signed by unknown authority",
		brokenConnection + "EOF",
		"http: server gave HTTP response to HTTPS client",
	} {
		cause := &textError{text}

		got := redirect.Scrub(cause)
		if !sameError(got, cause) {
			t.Errorf("%q came back as %v, want the same error", text, got)
		}
	}
}
