package redirect_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/permanent"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect"
)

// nestedSecret is the query value a nested URL error carries.
const nestedSecret = "NESTEDSECRET"

// secretURL is a request URL with a password in its query and a ticket in its
// user information and fragment.
const secretURL = "https://user:" + nestedSecret + "@h/nodes/n/scan/cifs?password=" + nestedSecret +
	"&server=x#" + nestedSecret

// errReset is the cause a nested URL error reports.
var errReset = errors.New("connection reset by peer")

// nestedShapes wraps a URL error in each shape a third-party transport wrapper
// can return it in.
func nestedShapes(inner *url.Error) map[string]error {
	return map[string]error{
		"percent w":         fmt.Errorf("attempt 2 of 2: %w", inner),
		"percent v and w":   fmt.Errorf("attempt %v failed: %w", 2, inner),
		"join":              errors.Join(inner),
		"join of two":       errors.Join(errors.New("first endpoint down"), inner),
		"wrap of a join":    fmt.Errorf("pool: %w", errors.Join(errors.New("a"), inner)),
		"url error in url":  &url.Error{Op: opGet, URL: "https://h/x", Err: fmt.Errorf("hop: %w", inner)},
		"two wraps":         fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", inner)),
		"two percent w":     fmt.Errorf("%w and %w", errors.New("other"), inner),
		"same error twice":  fmt.Errorf("%w; %w", inner, inner),
		"wrapper own text":  &ownText{inner: inner},
		"wrapper URL alone": &urlOnly{inner: inner},
	}
}

// ownText is a wrapper that prints the nested error with its own words around
// it.
type ownText struct{ inner *url.Error }

func (e *ownText) Error() string { return "transport said [" + e.inner.Error() + "]" }

func (e *ownText) Unwrap() error { return e.inner }

// urlOnly is a wrapper that prints only the URL of the nested error.
type urlOnly struct{ inner *url.Error }

func (e *urlOnly) Error() string { return "request to " + e.inner.URL + " failed" }

func (e *urlOnly) Unwrap() error { return e.inner }

// TestScrubCleansAURLErrorBelowTheTop checks every shape for the secret, and
// checks that errors.Is still reaches the nested cause.
func TestScrubCleansAURLErrorBelowTheTop(t *testing.T) {
	t.Parallel()

	for name, input := range nestedShapes(&url.Error{Op: opGet, URL: secretURL, Err: errReset}) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			got := redirect.Scrub(input)

			for _, text := range []string{got.Error(), fmt.Sprintf("%+v", got), fmt.Sprintf("%#v", got)} {
				if strings.Contains(text, nestedSecret) {
					t.Errorf("%q holds the secret", text)
				}
			}

			if !errors.Is(got, errReset) {
				t.Errorf("Scrub lost the nested cause from %v", got)
			}

			if !strings.Contains(got.Error(), "h/nodes/n/scan/cifs") {
				t.Errorf("%q no longer names the request", got.Error())
			}
		})
	}
}

// TestScrubKeepsTheWrapperText checks that only the nested URL changes and the
// words the wrapper added stay.
func TestScrubKeepsTheWrapperText(t *testing.T) {
	t.Parallel()

	inner := &url.Error{Op: opGet, URL: secretURL, Err: errReset}

	got := redirect.Scrub(fmt.Errorf("attempt 2 of 2: %w", inner))
	want := `attempt 2 of 2: Get "https://h/nodes/n/scan/cifs": connection reset by peer`

	if got.Error() != want {
		t.Errorf("text = %q, want %q", got.Error(), want)
	}
}

// TestScrubOfANestedURLErrorKeepsASingleChain checks that the cleaned error
// still unwraps link by link to the error it was given, so a caller that walks
// errors.Unwrap sees every link it saw before.
func TestScrubOfANestedURLErrorKeepsASingleChain(t *testing.T) {
	t.Parallel()

	input := fmt.Errorf("attempt 2 of 2: %w", &url.Error{Op: opGet, URL: secretURL, Err: errReset})
	got := redirect.Scrub(input)

	reached := false

	for link := error(got); link != nil; link = errors.Unwrap(link) {
		reached = reached || link == errReset //nolint:errorlint // the walk compares each link by identity
	}

	if !reached {
		t.Errorf("walking errors.Unwrap from %v never reached the cause", got)
	}
}

// TestScrubCleansANestedURLErrorAroundAProtectedCause checks that a context
// error below a wrapped URL error, which Scrub leaves unclassified, does not
// keep the URL's secret either.
func TestScrubCleansANestedURLErrorAroundAProtectedCause(t *testing.T) {
	t.Parallel()

	inner := &url.Error{Op: opGet, URL: secretURL, Err: context.DeadlineExceeded}
	got := redirect.Scrub(fmt.Errorf("attempt 1 of 1: %w", inner))

	if strings.Contains(got.Error(), nestedSecret) {
		t.Errorf("%q holds the secret", got.Error())
	}

	if !errors.Is(got, context.DeadlineExceeded) {
		t.Errorf("Scrub lost the deadline from %v", got)
	}
}

// TestScrubOfANestedHeaderFailureIsPermanent wraps a URL error whose cause
// quotes a malformed header line. The header line must go, and the request
// must not be retried, as it is not when the URL error comes back bare.
func TestScrubOfANestedHeaderFailureIsPermanent(t *testing.T) {
	t.Parallel()

	inner := &url.Error{
		Op: opGet, URL: secretURL,
		Err: fmt.Errorf("net/http: HTTP/1.x transport connection broken: malformed MIME header line: %q",
			"Set-Cookie: PVEAuthCookie="+nestedSecret),
	}

	got := redirect.Scrub(fmt.Errorf("attempt 1 of 1: %w", inner))

	if strings.Contains(got.Error(), nestedSecret) {
		t.Errorf("%q holds the secret", got.Error())
	}

	if !permanent.Is(got) {
		t.Errorf("%v is not permanent, want the header failure to stop the retry loop", got)
	}
}

// TestScrubLeavesACleanNestedURLErrorAlone checks that a wrapped URL error with
// nothing to clean comes back as the same value.
func TestScrubLeavesACleanNestedURLErrorAlone(t *testing.T) {
	t.Parallel()

	input := fmt.Errorf("attempt 1 of 1: %w", &url.Error{Op: opGet, URL: "https://h/version", Err: errReset})

	if got := redirect.Scrub(input); got != input { //nolint:errorlint // Scrub hands back the same value
		t.Errorf("Scrub(%v) = %v, want the same value", input, got)
	}
}
