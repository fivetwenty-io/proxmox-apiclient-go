// Package redirect holds the redirect policy every HTTP client the SDK builds
// shares. Go's own policy follows up to ten redirects and keeps the
// Authorization and Cookie headers whenever the host name stays the same, even
// when a redirect changes only the port or the scheme, which would send an API
// token or a ticket cookie to another listener on the host, in plain text for
// an http target. The SDK refuses every redirect unless the caller opts in.
package redirect

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
)

// ErrRefused is matched by errors.Is on the error a client returns when a
// server answers a request with a redirect and the client is not set to follow
// it.
var ErrRefused = errors.New("redirect refused")

// Policy is the signature of http.Client.CheckRedirect.
type Policy = func(req *http.Request, via []*http.Request) error

// Error describes a redirect a client refused to follow. It matches ErrRefused
// and says it is permanent, so a retry loop does not repeat the request.
type Error struct {
	// Status is the status code of the redirect response, such as 302 or 307.
	Status int

	// From is the URL that was requested, without user information, query,
	// or fragment.
	From string

	// To is the URL the server asked for, without user information, query, or
	// fragment. It is empty when the Location header could not be parsed.
	To string

	// unparsed is set when the server answered with a redirect whose Location
	// header is not a valid URL, so there is no target to name.
	unparsed bool
}

// Error names the redirect target without any credential.
func (e *Error) Error() string {
	if e.unparsed {
		return fmt.Sprintf("redirect refused: %s answered with a Location header that is not a valid URL", e.From)
	}

	return fmt.Sprintf("redirect refused: %s answered %d and asked for %s", e.From, e.Status, e.To)
}

// Is matches ErrRefused.
func (e *Error) Is(target error) bool { return target == ErrRefused }

// Permanent tells a retry loop not to repeat the request.
func (e *Error) Permanent() bool { return true }

// Refuse is the Policy the SDK installs unless the caller sets one. It refuses
// every redirect with an *Error. The hop it names as From is the last request
// sent, which is the one that received the redirect.
func Refuse(req *http.Request, via []*http.Request) error {
	refusal := &Error{To: Redact(req.URL)}

	if req.Response != nil {
		refusal.Status = req.Response.StatusCode
	}

	if len(via) > 0 {
		refusal.From = Redact(via[len(via)-1].URL)
	}

	return refusal
}

// Redact renders u as scheme, host, and path only, so neither a password in
// the user information nor a secret in the query or fragment reaches an error
// message.
func Redact(u *url.URL) string {
	if u == nil {
		return ""
	}

	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path}).String()
}

// RedactRaw does what Redact does for a URL held as text. Text that does not
// parse as a URL yields an empty string, because nothing in it can be trusted
// to hold no credential.
func RedactRaw(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	return Redact(u)
}

// Follow returns a Policy that follows at most maxRedirects redirects and then
// refuses with an *Error. It keeps Go's behavior of sending the Authorization
// and Cookie headers to a redirect target on the same host name, so it suits a
// caller who trusts every redirect its server sends. A negative maxRedirects
// counts as zero.
func Follow(maxRedirects int) Policy {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return Refuse(req, via)
		}

		return nil
	}
}

// FollowSameOrigin returns a Policy that follows at most maxRedirects
// redirects, and only while the target has the same scheme, host name, and port
// as the first request. Any other redirect is refused with an *Error. A
// negative maxRedirects counts as zero.
func FollowSameOrigin(maxRedirects int) Policy {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects || len(via) == 0 || !sameOrigin(via[0].URL, req.URL) {
			return Refuse(req, via)
		}

		return nil
	}
}

func sameOrigin(first, target *url.URL) bool {
	if first == nil || target == nil {
		return false
	}

	return strings.EqualFold(first.Scheme, target.Scheme) &&
		strings.EqualFold(first.Hostname(), target.Hostname()) &&
		effectivePort(first) == effectivePort(target)
}

func effectivePort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}

	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}

	return "80"
}

// sameError reports whether two errors are the same pointer value. net/http
// compares http.ErrUseLastResponse by identity, and Scrub must rewrite only the
// *url.Error that Do returns itself, so == on the values is the contract and
// errors.Is is not.
func sameError(first, second error) bool {
	firstValue, secondValue := reflect.ValueOf(first), reflect.ValueOf(second)

	return firstValue.Kind() == reflect.Pointer && secondValue.Kind() == reflect.Pointer &&
		firstValue.Pointer() == secondValue.Pointer()
}

// policyError wraps an error a caller's Policy returned, so Scrub can tell a
// policy failure from a transport failure. It reads and unwraps as the
// original.
type policyError struct {
	err error
}

func (e *policyError) Error() string { return e.err.Error() }

func (e *policyError) Unwrap() error { return e.err }

// Wrap returns the Policy to install in an http.Client. A nil policy becomes
// Refuse. Any other policy is called as is, and an error it returns is marked
// so Scrub can clean the message net/http builds around it.
// http.ErrUseLastResponse passes through untouched because net/http compares
// it by identity.
func Wrap(policy Policy) Policy {
	if policy == nil {
		return Refuse
	}

	return func(req *http.Request, via []*http.Request) error {
		err := policy(req, via)
		if err == nil || sameError(err, http.ErrUseLastResponse) {
			return err
		}

		return &policyError{err: err}
	}
}

// unparsedLocationPrefix starts the message net/http gives the error it
// returns when the Location header of a redirect does not parse. The message
// quotes the whole raw header, with its user information, query, and fragment.
const unparsedLocationPrefix = "failed to parse Location header"

// Scrub cleans the error http.Client.Do returns when a redirect policy failed
// or when the server sent a Location header that does not parse.
// net/http wraps that failure in a *url.Error and then overwrites the error's
// URL with the raw Location header the server sent, which can hold user
// information, a query, and a fragment. Scrub rebuilds the *url.Error with the
// URL reduced to scheme, host, and path. A Location header that does not parse
// becomes an *Error that names only the requested URL, because nothing in the
// header text can be trusted to hold no credential, and the request is not
// followed, so the error is permanent like any other refusal. Any other error,
// including a *url.Error from the transport, comes back unchanged.
func Scrub(err error) error {
	var urlErr *url.Error
	if !errors.As(err, &urlErr) || !sameError(err, urlErr) {
		return err
	}

	var (
		policy  *policyError
		refusal *Error
	)

	if !errors.As(urlErr.Err, &policy) && !errors.As(urlErr.Err, &refusal) {
		return scrubUnparsedLocation(urlErr, err)
	}

	return &url.Error{Op: urlErr.Op, URL: RedactRaw(urlErr.URL), Err: urlErr.Err}
}

// scrubUnparsedLocation replaces the error net/http returns for a Location
// header that does not parse, and returns original for any other error.
func scrubUnparsedLocation(urlErr *url.Error, original error) error {
	if urlErr.Err == nil || !strings.HasPrefix(urlErr.Err.Error(), unparsedLocationPrefix) {
		return original
	}

	from := RedactRaw(urlErr.URL)

	return &url.Error{Op: urlErr.Op, URL: from, Err: &Error{From: from, unparsed: true}}
}
