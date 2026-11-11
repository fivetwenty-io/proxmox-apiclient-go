package redirect_test

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/permanent"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect"
)

// belowTheTop returns the part of a scrubbed error that holds the nested
// *url.Error. When Scrub rebuilt a *url.Error at the top, errors.As would find
// that clean copy first, so the walk starts at its cause.
func belowTheTop(scrubbed error) error {
	if rebuilt, ok := scrubbed.(*url.Error); ok { //nolint:errorlint // the test reads the top link itself
		return rebuilt.Err
	}

	return scrubbed
}

// TestErrorsAsFindsTheCleanedNestedURLError checks that errors.As, for a
// *url.Error and for a net.Error, never hands back the nested *url.Error with
// its query, user information, and fragment.
func TestErrorsAsFindsTheCleanedNestedURLError(t *testing.T) {
	t.Parallel()

	for name, input := range nestedShapes(&url.Error{Op: opGet, URL: secretURL, Err: errReset}) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := belowTheTop(redirect.Scrub(input))

			var urlErr *url.Error
			if !errors.As(got, &urlErr) {
				t.Fatalf("errors.As found no *url.Error in %v", got)
			}

			if strings.Contains(urlErr.Error(), nestedSecret) {
				t.Errorf("errors.As found %q, which holds the secret", urlErr.Error())
			}

			if !errors.Is(urlErr, errReset) {
				t.Errorf("the *url.Error errors.As found no longer reaches its cause")
			}

			var netErr net.Error
			if !errors.As(got, &netErr) {
				t.Fatalf("errors.As found no net.Error in %v", got)
			}

			if strings.Contains(netErr.Error(), nestedSecret) {
				t.Errorf("errors.As for a net.Error found %q, which holds the secret", netErr.Error())
			}
		})
	}
}

// deepTimeoutError is a timeout cause with plain text.
type deepTimeoutError struct{}

func (deepTimeoutError) Error() string   { return "i/o timeout" }
func (deepTimeoutError) Timeout() bool   { return true }
func (deepTimeoutError) Temporary() bool { return true }

// TestNestedWrapperDoesNotHideADeeperTimeout wraps a *url.Error whose cause
// is a timeout in a wrapper that has no Timeout method. errors.As for a
// net.Error must still find a timeout after Scrub, as it did before.
func TestNestedWrapperDoesNotHideADeeperTimeout(t *testing.T) {
	t.Parallel()

	input := fmt.Errorf("attempt 1 of 1: %w", &url.Error{Op: opGet, URL: secretURL, Err: deepTimeoutError{}})

	var before net.Error
	if !errors.As(input, &before) || !before.Timeout() {
		t.Fatal("the raw error does not read as a timeout")
	}

	got := redirect.Scrub(input)

	var after net.Error
	if !errors.As(got, &after) || !after.Timeout() {
		t.Errorf("%v no longer reads as a timeout through errors.As", got)
	}

	if strings.Contains(got.Error(), nestedSecret) {
		t.Errorf("%q holds the secret", got.Error())
	}
}

// netTimeoutWrapperError is a wrapper that is itself a net.Error and answers for
// the timeout of what it wraps.
type netTimeoutWrapperError struct{ inner error }

func (e *netTimeoutWrapperError) Error() string   { return "retry layer: " + e.inner.Error() }
func (e *netTimeoutWrapperError) Unwrap() error   { return e.inner }
func (e *netTimeoutWrapperError) Timeout() bool   { return true }
func (e *netTimeoutWrapperError) Temporary() bool { return false }

// timeoutOnlyWrapperError has a Timeout method and no Temporary method.
type timeoutOnlyWrapperError struct{ inner error }

func (e *timeoutOnlyWrapperError) Error() string { return "timed out: " + e.inner.Error() }
func (e *timeoutOnlyWrapperError) Unwrap() error { return e.inner }
func (e *timeoutOnlyWrapperError) Timeout() bool { return true }

// TestNestedWrapperForwardsTheTimeoutOfItsCause checks that the cleaned error
// answers Timeout as the wrapper it replaced did, both through errors.As and
// through a *url.Error that asks its direct cause, and that it has a Temporary
// method only when the wrapper had one.
func TestNestedWrapperForwardsTheTimeoutOfItsCause(t *testing.T) {
	t.Parallel()

	nested := &url.Error{Op: opGet, URL: secretURL, Err: errReset}

	t.Run("net error wrapper", func(t *testing.T) {
		t.Parallel()

		got := redirect.Scrub(&netTimeoutWrapperError{inner: nested})

		var netErr net.Error
		if !errors.As(got, &netErr) || !netErr.Timeout() {
			t.Errorf("%v does not answer as the wrapper did", got)
		}

		outer := redirect.Scrub(&url.Error{Op: opGet, URL: hostileTarget, Err: &netTimeoutWrapperError{inner: nested}})

		var outerURL *url.Error
		if !errors.As(outer, &outerURL) || !outerURL.Timeout() {
			t.Errorf("%v no longer reads as a timeout through its *url.Error", outer)
		}
	})

	t.Run("timeout only wrapper", func(t *testing.T) {
		t.Parallel()

		got := redirect.Scrub(&timeoutOnlyWrapperError{inner: nested})

		timeout, ok := got.(interface{ Timeout() bool })
		if !ok || !timeout.Timeout() {
			t.Errorf("%v lost the Timeout method of the wrapper", got)
		}

		if _, isTemporary := got.(interface{ Temporary() bool }); isTemporary {
			t.Errorf("%v gained a Temporary method the wrapper did not have", got)
		}
	})
}

// goawayWithDebug is the error the HTTP/2 transport returns for a GOAWAY frame
// whose debug data the server set to debug.
func goawayWithDebug(debug string) error {
	return &textError{"http2: server sent GOAWAY and closed the connection; LastStreamID=1, ErrCode=NO_ERROR, " +
		"debug=\"" + debug + "\""}
}

// TestServerTextCannotChooseHowAnErrorIsClassified puts each phrase that turns
// an error into a refusal or a permanent failure in the debug data of a GOAWAY
// frame. The server chose those bytes, so the error must stay a framing error
// that the retry loop treats as before, through Scrub and through ScrubRead.
func TestServerTextCannotChooseHowAnErrorIsClassified(t *testing.T) {
	t.Parallel()

	requested, err := url.Parse(hostileTarget)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	resp := &http.Response{Request: &http.Request{URL: requested}}

	for _, phrase := range []string{
		"failed to parse Location header",
		"malformed MIME header",
		"unsupported protocol scheme",
	} {
		t.Run(phrase, func(t *testing.T) {
			t.Parallel()

			for path, got := range map[string]error{
				"Scrub":     redirect.Scrub(goawayWithDebug(phrase + " EVILBYTES")),
				"ScrubRead": redirect.ScrubRead(goawayWithDebug(phrase+" EVILBYTES"), resp),
			} {
				if errors.Is(got, redirect.ErrRefused) || permanent.Is(got) {
					t.Errorf("%s: %v became a refusal or a permanent failure", path, got)
				}

				var framing *redirect.FramingError
				if !errors.As(got, &framing) {
					t.Errorf("%s: got %v, want a framing error", path, got)
				}

				if strings.Contains(got.Error(), "EVILBYTES") {
					t.Errorf("%s: %q keeps the server's bytes", path, got.Error())
				}
			}
		})
	}
}

// TestMarkersBeforeTheServersBytesStillClassify checks the errors whose own
// phrase comes first and whose quoted bytes hold a framing phrase. The quoted
// bytes come after the phrase, so the error is still a refusal or a header
// failure.
func TestMarkersBeforeTheServersBytesStillClassify(t *testing.T) {
	t.Parallel()

	refusal := redirect.Scrub(&url.Error{Op: opGet, URL: hostileTarget, Err: &textError{
		`failed to parse Location header "http://h/x, debug=bad Content-Length": invalid URL escape`,
	}})
	if !errors.Is(refusal, redirect.ErrRefused) {
		t.Errorf("an unparsed Location quoting a framing phrase gave %v, want a refusal", refusal)
	}

	header := redirect.Scrub(&url.Error{Op: opGet, URL: hostileTarget, Err: &textError{
		brokenConnection + `malformed MIME header line: "X: malformed HTTP response"`,
	}})

	var headerErr *redirect.HeaderError
	if !errors.As(header, &headerErr) {
		t.Errorf("a malformed header line quoting a framing phrase gave %v, want a header error", header)
	}
}

// TestServerHeaderNameCannotSwitchOffItsMarker sends the HTTP/2 error for a
// bad response header name, whose name starts with the phrase that marks the
// caller's own request header. The phrase sits in the server's bytes, so the
// error must still be cut at the marker.
func TestServerHeaderNameCannotSwitchOffItsMarker(t *testing.T) {
	t.Parallel()

	name := "net/http: invalid header EVILBYTES" + strings.Repeat("N", 20_000)

	got := redirect.Scrub(&textError{"invalid header field name " + fmt.Sprintf("%q", name)})

	var framing *redirect.FramingError
	if !errors.As(got, &framing) {
		t.Fatalf("Scrub returned %v, want a framing error", got)
	}

	if framing.Kind != "invalid header field name" {
		t.Errorf("kind = %q, want the marker alone", framing.Kind)
	}

	if strings.Contains(got.Error(), "EVILBYTES") {
		t.Errorf("%.200q keeps the server's bytes", got.Error())
	}
}
