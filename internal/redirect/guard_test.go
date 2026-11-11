package redirect_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/permanent"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect/redirecttest"
)

// testURL is the URL the guard tests request.
const testURL = "http://h/x"

// closeTracker is a response body that records being closed.
type closeTracker struct {
	closed atomic.Bool
}

func (b *closeTracker) Read([]byte) (int, error) { return 0, io.EOF }

func (b *closeTracker) Close() error {
	b.closed.Store(true)

	return nil
}

// stubTransport answers every request with a fixed response or error.
type stubTransport struct {
	resp      *http.Response
	err       error
	idleCalls atomic.Int32
}

func (s *stubTransport) RoundTrip(*http.Request) (*http.Response, error) { return s.resp, s.err }

func (s *stubTransport) CloseIdleConnections() { s.idleCalls.Add(1) }

// roundTripOnly hides every method but RoundTrip.
type roundTripOnly struct{ inner http.RoundTripper }

func (r roundTripOnly) RoundTrip(req *http.Request) (*http.Response, error) {
	return r.inner.RoundTrip(req) //nolint:wrapcheck // test double hands back the inner result
}

// answering returns a transport that answers every request with the status, a
// Location header when location is set, and the body.
func answering(status int, location string, body io.ReadCloser) *stubTransport {
	header := http.Header{}
	if location != "" {
		header.Set("Location", location)
	}

	return &stubTransport{resp: &http.Response{StatusCode: status, Header: header, Body: body}}
}

func guarded(next http.RoundTripper) http.RoundTripper { return redirect.Guard(next, nil) }

// errQuotedHeader is the error a transport returns for a header line it cannot
// read, shaped like net/http's and holding the secrets of a hostile Location.
var errQuotedHeader = errors.New(`net/http: HTTP/1.x transport connection broken: malformed MIME header line: ` +
	`"Location: http://user:` + redirecttest.SecretUser + `@h/?ticket=` + redirecttest.SecretQuery + `"`)

var errConnectionReset = errors.New("connection reset by peer")

// roundTrip sends req through rt and returns the error alone. It closes any
// response body, and reports whether the transport handed back a response.
func roundTrip(rt http.RoundTripper, req *http.Request) (bool, error) {
	resp, err := rt.RoundTrip(req)
	if resp == nil {
		return false, err //nolint:wrapcheck // the transport's own error is what the test checks
	}

	if resp.Body != nil {
		_ = resp.Body.Close()
	}

	return true, err //nolint:wrapcheck // the transport's own error is what the test checks
}

// send sends req with client and returns the error alone, closing any body.
func send(client *http.Client, req *http.Request) error {
	resp, err := client.Do(req)
	if resp != nil {
		_ = resp.Body.Close()
	}

	return err //nolint:wrapcheck // the client's own error is what the test checks
}

// TestGuardRefusesEveryRedirectStatusAndClosesTheBody sends every 3xx status.
// All but 304 must come back as a permanent refusal that names the resolved
// target without its credentials, with the response body closed and no
// response handed to the client.
func TestGuardRefusesEveryRedirectStatusAndClosesTheBody(t *testing.T) {
	t.Parallel()

	for status := http.StatusMultipleChoices; status < http.StatusBadRequest; status++ {
		if status == http.StatusNotModified {
			continue
		}

		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()

			body := &closeTracker{}
			next := answering(status, "/next?ticket="+redirecttest.SecretQuery, body)
			req := request(t, "http://user:"+redirecttest.SecretUser+"@h:8006/api?q=1")

			gotResponse, err := roundTrip(guarded(next), req)
			if gotResponse {
				t.Fatal("got a response, want none, so the client never reads Location")
			}

			var refusal *redirect.Error
			if !errors.As(err, &refusal) || !errors.Is(err, redirect.ErrRefused) || !permanent.Is(err) {
				t.Fatalf("error = %v, want a permanent *redirect.Error", err)
			}

			if refusal.Status != status || refusal.From != "http://h:8006/api" || refusal.To != "http://h:8006/next" {
				t.Errorf("refusal = %+v, want status %d from http://h:8006/api to http://h:8006/next", refusal, status)
			}

			if !body.closed.Load() {
				t.Error("the redirect response body was not closed")
			}

			redirecttest.AssertClean(t, err)
		})
	}
}

// TestGuardPassesEveryOtherStatusThrough checks the statuses that are not
// redirects. A 304 answers a conditional request and net/http never follows
// it, so it reaches the caller.
func TestGuardPassesEveryOtherStatusThrough(t *testing.T) {
	t.Parallel()

	for _, status := range []int{
		http.StatusOK, http.StatusNoContent, http.StatusNotModified, http.StatusBadRequest,
		http.StatusUnauthorized, http.StatusNotFound, http.StatusInternalServerError,
	} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()

			body := &closeTracker{}
			stub := answering(status, "http://elsewhere/", body)
			want := stub.resp

			got, err := guarded(stub).RoundTrip(request(t, testURL))
			if err != nil || got != want {
				t.Fatalf("got %v, %v, want the response unchanged", got, err)
			}

			if body.closed.Load() {
				t.Error("the body of a response passed through was closed")
			}

			_ = got.Body.Close()
		})
	}
}

// TestGuardNamesAMissingOrUnparseableTarget covers a redirect with no Location
// header and one whose header cannot be parsed. Neither names a target, and the
// second never quotes the header.
func TestGuardNamesAMissingOrUnparseableTarget(t *testing.T) {
	t.Parallel()

	missing := guarded(answering(http.StatusFound, "", http.NoBody))

	_, err := roundTrip(missing, request(t, testURL))
	if err == nil || err.Error() != "redirect refused: http://h/x answered 302 without a Location header it could use" {
		t.Errorf("missing Location: %v", err)
	}

	bad := "http://user:" + redirecttest.SecretUser + "@h:abc/x?ticket=" + redirecttest.SecretQuery

	unparsed := guarded(answering(http.StatusTemporaryRedirect, bad, http.NoBody))

	_, err = roundTrip(unparsed, request(t, testURL))
	if err == nil || err.Error() !=
		"redirect refused: http://h/x answered 307 with a Location header that is not a valid URL" {
		t.Errorf("unparseable Location: %v", err)
	}

	redirecttest.AssertClean(t, err)
}

// TestGuardRefusesBeforeNetHTTPParsesLocation runs a real http.Client against a
// server that sends each hostile Location value, and checks that the client
// fails once with a refusal that holds no secret. Without the guard the client
// parses the header itself and quotes it in the error.
func TestGuardRefusesBeforeNetHTTPParsesLocation(t *testing.T) {
	t.Parallel()

	for _, hostile := range redirecttest.Cases(redirecttest.DeadAddr(t)) {
		t.Run(hostile.Name, func(t *testing.T) {
			t.Parallel()

			server := redirecttest.Serve(t, http.StatusFound, hostile.Location)
			client := &http.Client{
				Transport:     redirect.Guard(&http.Transport{}, nil),
				CheckRedirect: redirect.Refuse,
			}

			req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL()+"/api?a=b", nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}

			err = redirect.Scrub(send(client, req))

			if !permanent.Is(err) {
				t.Errorf("error = %v, want it permanent", err)
			}

			redirecttest.AssertClean(t, err)

			if got := server.Hits(); got != 1 {
				t.Errorf("server read %d requests, want 1", got)
			}
		})
	}
}

// TestGuardRewritesATransportErrorThatQuotesAHeader checks the error a
// transport returns for a header line it cannot read, which holds the raw line.
// Any other transport error comes back as the same value.
func TestGuardRewritesATransportErrorThatQuotesAHeader(t *testing.T) {
	t.Parallel()

	_, err := roundTrip(guarded(&stubTransport{err: errQuotedHeader}), request(t, "http://h/x?q=1"))

	var header *redirect.HeaderError
	if !errors.As(err, &header) || header.From != testURL || !permanent.Is(err) {
		t.Fatalf("error = %v, want a permanent *redirect.HeaderError naming %s", err, testURL)
	}

	if want := testURL + " answered with a header that could not be read"; err.Error() != want {
		t.Errorf("error text = %q, want %q", err.Error(), want)
	}

	redirecttest.AssertClean(t, err)

	_, err = roundTrip(guarded(&stubTransport{err: errConnectionReset}), request(t, testURL))
	if !sameError(err, errConnectionReset) {
		t.Errorf("error = %v, want the transport's own error unchanged", err)
	}
}

// TestGuardIsInstalledOnlyWithoutAPolicy checks the wrapper is dropped when the
// caller sets a policy, which then decides alone, and that a nil transport
// means the default one.
func TestGuardIsInstalledOnlyWithoutAPolicy(t *testing.T) {
	t.Parallel()

	next := answering(http.StatusFound, "http://h/y", http.NoBody)
	follow := func(*http.Request, []*http.Request) error { return nil }

	if got := redirect.Guard(next, follow); got != http.RoundTripper(next) {
		t.Errorf("Guard with a policy = %T, want the transport itself", got)
	}

	got := redirect.Guard(next, nil)
	if got == http.RoundTripper(next) {
		t.Error("Guard without a policy returned the transport itself")
	}

	if redirect.Unwrap(got) != http.RoundTripper(next) {
		t.Errorf("Unwrap = %T, want the wrapped transport", redirect.Unwrap(got))
	}

	if redirect.Unwrap(next) != http.RoundTripper(next) {
		t.Error("Unwrap of a transport that is not a guard must return it")
	}

	if redirect.Unwrap(redirect.Guard(nil, nil)) != http.DefaultTransport {
		t.Error("Guard of a nil transport must wrap http.DefaultTransport")
	}
}

// TestGuardForwardsCloseIdleConnections checks http.Client.CloseIdleConnections
// reaches the wrapped transport, and does not panic when it has no such method.
func TestGuardForwardsCloseIdleConnections(t *testing.T) {
	t.Parallel()

	next := &stubTransport{}
	client := &http.Client{Transport: guarded(next)}

	client.CloseIdleConnections()

	if got := next.idleCalls.Load(); got != 1 {
		t.Errorf("CloseIdleConnections calls = %d, want 1", got)
	}

	hidden := &http.Client{Transport: guarded(roundTripOnly{inner: next})}

	hidden.CloseIdleConnections()

	if got := next.idleCalls.Load(); got != 1 {
		t.Errorf("a RoundTrip-only transport was called %d times, want it skipped", got)
	}
}

// TestGuardKeepsTheStatusThroughTheClient checks the refusal survives the
// *url.Error net/http wraps around it, so callers can still match it.
func TestGuardKeepsTheStatusThroughTheClient(t *testing.T) {
	t.Parallel()

	next := answering(http.StatusSeeOther, "http://h/y", http.NoBody)
	client := &http.Client{Transport: guarded(next), CheckRedirect: redirect.Refuse}

	err := send(client, request(t, testURL))
	if err == nil {
		t.Fatal("got no error")
	}

	var urlErr *url.Error

	var refusal *redirect.Error

	if !errors.As(err, &urlErr) || !errors.As(err, &refusal) || refusal.Status != http.StatusSeeOther ||
		!strings.Contains(err.Error(), "http://h/y") {
		t.Errorf("error = %v, want a *url.Error around a 303 refusal naming http://h/y", err)
	}
}
