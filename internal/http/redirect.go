package http

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

// ErrRedirectRefused is matched by errors.Is on the error the client returns
// when a server answers a request with a redirect and the client is not set to
// follow it. It is the default: Go's own policy follows up to ten redirects
// and keeps the Authorization header whenever the host name stays the same,
// even when the redirect changes only the port or the scheme, which would send
// an API token to another listener on the host, in plain text for an http
// target.
var ErrRedirectRefused = errors.New("redirect refused")

// RedirectError describes a redirect the client refused to follow. It matches
// ErrRedirectRefused, and the retry middleware never retries it.
type RedirectError struct {
	// Status is the status code of the redirect response, such as 302 or 307.
	Status int

	// From is the URL that was requested, without user information, query,
	// or fragment.
	From string

	// To is the URL the server asked for, without user information, query, or
	// fragment.
	To string
}

// Error names the redirect target without any credential.
func (e *RedirectError) Error() string {
	return fmt.Sprintf("redirect refused: %s answered %d and asked for %s", e.From, e.Status, e.To)
}

// Is matches ErrRedirectRefused.
func (e *RedirectError) Is(target error) bool { return target == ErrRedirectRefused }

// Permanent tells the retry middleware not to retry this error.
func (e *RedirectError) Permanent() bool { return true }

// refuseRedirect is the CheckRedirect policy NewClient installs unless
// Options.CheckRedirect is set. It refuses every redirect with a RedirectError.
func refuseRedirect(req *http.Request, via []*http.Request) error {
	refusal := &RedirectError{To: redactedURL(req.URL)}

	if req.Response != nil {
		refusal.Status = req.Response.StatusCode
	}

	if len(via) > 0 {
		refusal.From = redactedURL(via[len(via)-1].URL)
	}

	return refusal
}

// redactedURL renders u as scheme, host, and path only, so neither a password
// in the user information nor a secret in the query reaches an error message.
func redactedURL(u *url.URL) string {
	if u == nil {
		return ""
	}

	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}

// newHTTPClient builds the http.Client requests are sent with. Its redirect
// policy is the one in options, or the refusal when options leave it nil.
func newHTTPClient(options *Options, roundTripper http.RoundTripper) *http.Client {
	checkRedirect := options.CheckRedirect
	if checkRedirect == nil {
		checkRedirect = refuseRedirect
	}

	return &http.Client{
		Transport:     roundTripper,
		Timeout:       options.Timeout,
		CheckRedirect: checkRedirect,
	}
}
