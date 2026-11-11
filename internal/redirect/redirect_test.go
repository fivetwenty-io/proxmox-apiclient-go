package redirect_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/permanent"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect"
)

var (
	errCaller    = errors.New("caller policy says no")
	errLookalike = errors.New("redirect refused")
)

const (
	redactedTarget = "http://127.0.0.1:1/x"
	keepQueryURL   = "http://h/x?keep=1"
	opGet          = "Get"
	opPost         = "Post"
	cleanURL       = "http://h/x"
)

func request(t *testing.T, rawURL string) *http.Request {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("NewRequest(%q): %v", rawURL, err)
	}

	return req
}

func TestRefuseNamesTheLastHopAndNoCredential(t *testing.T) {
	t.Parallel()

	target := request(t, "http://user:hunter2@10.0.0.1:8080/x?ticket=abc#frag")
	target.Response = &http.Response{StatusCode: http.StatusFound}

	via := []*http.Request{
		request(t, "https://10.0.0.1:8006/first?a=b"),
		request(t, "https://10.0.0.1:8006/second?c=d"),
	}

	err := redirect.Refuse(target, via)

	var refusal *redirect.Error
	if !errors.As(err, &refusal) {
		t.Fatalf("Refuse = %v, want *redirect.Error", err)
	}

	if refusal.From != "https://10.0.0.1:8006/second" {
		t.Errorf("From = %q, want the last hop https://10.0.0.1:8006/second", refusal.From)
	}

	want := "redirect refused: https://10.0.0.1:8006/second answered 302 and asked for http://10.0.0.1:8080/x"
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}

	if !permanent.Is(err) {
		t.Error("a refusal must be permanent")
	}
}

func TestErrorIsMatchesOnlyTheSentinel(t *testing.T) {
	t.Parallel()

	refusal := &redirect.Error{Status: http.StatusFound}

	if !errors.Is(refusal, redirect.ErrRefused) {
		t.Error("a refusal must match ErrRefused")
	}

	for _, other := range []error{errCaller, http.ErrUseLastResponse, errLookalike} {
		if errors.Is(refusal, other) {
			t.Errorf("a refusal must not match %v", other)
		}
	}
}

func TestRedactDropsUserinfoQueryAndFragment(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"http://user:HUNTER2@127.0.0.1:1/x?ticket=QSECRET#frag": redactedTarget,
		"https://h/a/b?x=1": "https://h/a/b",
		"//h/p?q":           "//h/p",
		"/only/path?q=1#f":  "/only/path",
	}

	for raw, want := range cases {
		if got := redirect.RedactRaw(raw); got != want {
			t.Errorf("RedactRaw(%q) = %q, want %q", raw, got, want)
		}
	}

	if got := redirect.RedactRaw("http://[::1"); got != "" {
		t.Errorf("RedactRaw of unparsable text = %q, want empty", got)
	}

	if got := redirect.Redact(nil); got != "" {
		t.Errorf("Redact(nil) = %q, want empty", got)
	}
}

func TestFollowStopsAfterTheLimit(t *testing.T) {
	t.Parallel()

	policy := redirect.Follow(2)
	target := request(t, "http://h:1/next")
	first := request(t, "http://h:1/start")

	err := policy(target, []*http.Request{first})
	if err != nil {
		t.Errorf("first redirect: %v, want it followed", err)
	}

	err = policy(target, []*http.Request{first, first})
	if err != nil {
		t.Errorf("second redirect: %v, want it followed", err)
	}

	err = policy(target, []*http.Request{first, first, first})
	if !errors.Is(err, redirect.ErrRefused) {
		t.Errorf("third redirect: %v, want ErrRefused", err)
	}

	err = redirect.Follow(-1)(target, []*http.Request{first})
	if !errors.Is(err, redirect.ErrRefused) {
		t.Errorf("negative limit: %v, want ErrRefused", err)
	}

	err = redirect.Follow(0)(target, []*http.Request{first})
	if !errors.Is(err, redirect.ErrRefused) {
		t.Errorf("zero limit: %v, want ErrRefused", err)
	}
}

func TestFollowSameOriginComparesSchemeHostAndPort(t *testing.T) {
	t.Parallel()

	start := request(t, "https://pve.example:8006/api2/json/version")
	policy := redirect.FollowSameOrigin(3)

	followed := map[string]string{
		"same origin, other path":     "https://pve.example:8006/elsewhere",
		"host case differs":           "https://PVE.example:8006/x",
		"scheme case differs":         "HTTPS://pve.example:8006/x",
		"explicit default port":       "https://pve.example:8006/x?q=1",
		"same origin with userinfo":   "https://u:p@pve.example:8006/x",
		"same origin with a fragment": "https://pve.example:8006/x#f",
	}

	for name, rawURL := range followed {
		err := policy(request(t, rawURL), []*http.Request{start})
		if err != nil {
			t.Errorf("%s: %v, want it followed", name, err)
		}
	}

	refused := map[string]string{
		"other port":   "https://pve.example:9000/x",
		"other scheme": "http://pve.example:8006/x",
		"other host":   "https://other.example:8006/x",
	}

	for name, rawURL := range refused {
		err := policy(request(t, rawURL), []*http.Request{start})
		if !errors.Is(err, redirect.ErrRefused) {
			t.Errorf("%s: %v, want ErrRefused", name, err)
		}
	}

	// A scheme's default port equals the explicit one.
	plain := request(t, "http://pve.example/start")

	err := policy(request(t, "http://pve.example:80/x"), []*http.Request{plain})
	if err != nil {
		t.Errorf("default port 80 against explicit 80: %v, want it followed", err)
	}

	secure := request(t, "https://pve.example/start")

	err = policy(request(t, "https://pve.example:443/x"), []*http.Request{secure})
	if err != nil {
		t.Errorf("default port 443 against explicit 443: %v, want it followed", err)
	}

	err = policy(request(t, "https://pve.example:80/x"), []*http.Request{secure})
	if !errors.Is(err, redirect.ErrRefused) {
		t.Errorf("https default 443 against port 80: %v, want ErrRefused", err)
	}
}

func TestFollowSameOriginComparesAgainstTheFirstRequestAndCaps(t *testing.T) {
	t.Parallel()

	start := request(t, "http://h:1/start")
	hop := request(t, "http://h:1/hop")
	policy := redirect.FollowSameOrigin(2)

	// The origin is the first request's, not the last hop's.
	elsewhere := request(t, "http://h:2/start")

	err := policy(request(t, "http://h:2/x"), []*http.Request{start, elsewhere})
	if !errors.Is(err, redirect.ErrRefused) {
		t.Errorf("origin taken from the last hop: %v, want ErrRefused", err)
	}

	err = policy(hop, []*http.Request{start, hop})
	if err != nil {
		t.Errorf("second same-origin redirect: %v, want it followed", err)
	}

	err = policy(hop, []*http.Request{start, hop, hop})
	if !errors.Is(err, redirect.ErrRefused) {
		t.Errorf("third same-origin redirect: %v, want the cap to refuse", err)
	}

	err = policy(hop, nil)
	if !errors.Is(err, redirect.ErrRefused) {
		t.Errorf("no earlier request: %v, want ErrRefused", err)
	}
}

// sameError reports whether two errors are the same value, which is how
// net/http compares http.ErrUseLastResponse.
func sameError(first, second error) bool {
	return reflect.ValueOf(first).Kind() == reflect.Pointer && reflect.ValueOf(second).Kind() == reflect.Pointer &&
		reflect.ValueOf(first).Pointer() == reflect.ValueOf(second).Pointer()
}

func TestWrapNilRefusesAndPassesTheCallersAnswerThrough(t *testing.T) {
	t.Parallel()

	target := request(t, "http://h:2/x")
	via := []*http.Request{request(t, "http://h:1/x")}

	err := redirect.Wrap(nil)(target, via)
	if !errors.Is(err, redirect.ErrRefused) {
		t.Errorf("nil policy: %v, want ErrRefused", err)
	}

	follow := redirect.Wrap(func(*http.Request, []*http.Request) error { return nil })

	err = follow(target, via)
	if err != nil {
		t.Errorf("a policy that returns nil: %v, want nil", err)
	}

	last := redirect.Wrap(func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse })

	err = last(target, via)
	if !sameError(err, http.ErrUseLastResponse) {
		t.Errorf("ErrUseLastResponse = %v, want the same value", err)
	}

	wrappedLast := redirect.Wrap(func(*http.Request, []*http.Request) error {
		return fmt.Errorf("context: %w", http.ErrUseLastResponse)
	})

	err = wrappedLast(target, via)
	if sameError(err, http.ErrUseLastResponse) {
		t.Error("a wrapped ErrUseLastResponse is a different error to net/http, so it must not pass as the original")
	}

	// net/http then treats it as a failure and wraps it with the raw Location,
	// so Scrub must still recognise it as a policy failure and clean the URL.
	scrubbed := redirect.Scrub(&url.Error{Op: opGet, URL: "http://u:HUNTER2@h:1/x?q=QSECRET", Err: err})
	if strings.Contains(scrubbed.Error(), "HUNTER2") || strings.Contains(scrubbed.Error(), "QSECRET") {
		t.Errorf("a wrapped ErrUseLastResponse left credentials in %q", scrubbed.Error())
	}

	fail := redirect.Wrap(func(*http.Request, []*http.Request) error { return errCaller })

	err = fail(target, via)
	if !errors.Is(err, errCaller) || err.Error() != errCaller.Error() {
		t.Errorf("caller error = %v, want it to read and unwrap as %v", err, errCaller)
	}
}

func TestScrubRedactsEveryURLError(t *testing.T) {
	t.Parallel()

	const raw = "http://user:HUNTER2@127.0.0.1:1/x?ticket=QSECRET#frag"

	refused := &url.Error{
		Op: opGet, URL: raw, Err: &redirect.Error{Status: http.StatusFound, From: "http://a/", To: redactedTarget},
	}
	callers := &url.Error{Op: opPost, URL: raw, Err: redirect.Wrap(func(*http.Request, []*http.Request) error {
		return errCaller
	})(request(t, "http://h:2/"), nil)}

	for name, err := range map[string]error{"refusal": refused, "caller policy": callers} {
		got := redirect.Scrub(err)
		if strings.Contains(got.Error(), "HUNTER2") || strings.Contains(got.Error(), "QSECRET") ||
			strings.Contains(got.Error(), "frag") {
			t.Errorf("%s: %q still holds the Location credentials", name, got.Error())
		}

		var urlErr *url.Error
		if !errors.As(got, &urlErr) || urlErr.URL != redactedTarget {
			t.Errorf("%s: %v, want a *url.Error naming %s", name, got, redactedTarget)
		}

		if !errors.Is(got, errors.Unwrap(err)) {
			t.Errorf("%s: Scrub lost the wrapped error", name)
		}
	}

	transport := &url.Error{Op: opGet, URL: keepQueryURL, Err: errCaller}

	got := redirect.Scrub(transport)
	if got.Error() != `Get "`+cleanURL+`": caller policy says no` || !errors.Is(got, errCaller) {
		t.Errorf("a transport failure came back as %q, want its URL reduced to scheme, host, and path", got)
	}

	clean := &url.Error{Op: opGet, URL: cleanURL, Err: errCaller}

	same := redirect.Scrub(clean)
	if !sameError(same, clean) {
		t.Errorf("an error with nothing to clean was rewritten to %v", same)
	}

	if redirect.Scrub(nil) != nil {
		t.Error("nil must stay nil")
	}

	got = redirect.Scrub(errCaller)
	if !errors.Is(got, errCaller) || got.Error() != errCaller.Error() {
		t.Errorf("a non-url error came back as %v, want it unchanged", got)
	}
}

// TestScrubReplacesAnUnparseableLocation checks the error net/http builds for a
// Location header that does not parse. Its text quotes the raw header, so Scrub
// must drop it, keep the refusal contract, and name only the requested URL.
func TestScrubReplacesAnUnparseableLocation(t *testing.T) {
	t.Parallel()

	raw := fmt.Sprintf("http://user:HUNTER2@127.0.0.1:%s/x?ticket=QSECRET#frag", "abc")

	_, parseErr := url.Parse(raw)
	if parseErr == nil {
		t.Fatal("fixture must not parse")
	}

	original := &url.Error{
		Op: opGet, URL: "http://127.0.0.1:1/api?keep=1",
		Err: fmt.Errorf("failed to parse Location header %q: %w", raw, parseErr),
	}

	got := redirect.Scrub(original)

	for _, secret := range []string{"HUNTER2", "QSECRET", "frag", "user:", "abc", "parse"} {
		if strings.Contains(got.Error(), secret) {
			t.Errorf("%q still holds %q from the Location header", got.Error(), secret)
		}
	}

	if !errors.Is(got, redirect.ErrRefused) || !permanent.Is(got) {
		t.Errorf("%v must match ErrRefused and be permanent", got)
	}

	var refusal *redirect.Error
	if !errors.As(got, &refusal) || refusal.From != "http://127.0.0.1:1/api" || refusal.To != "" {
		t.Errorf("%v, want a *redirect.Error naming only the requested URL", got)
	}

	var urlErr *url.Error
	if !errors.As(got, &urlErr) || urlErr.URL != "http://127.0.0.1:1/api" || urlErr.Op != opGet {
		t.Errorf("%v, want a *url.Error with the redacted request URL", got)
	}

	want := "redirect refused: http://127.0.0.1:1/api answered with a Location header that is not a valid URL"
	if refusal.Error() != want {
		t.Errorf("refusal text = %q, want %q", refusal.Error(), want)
	}

	lookalike := &url.Error{Op: opGet, URL: cleanURL, Err: errCaller}

	same := redirect.Scrub(lookalike)
	if !sameError(same, lookalike) {
		t.Errorf("a transport failure was rewritten to %v", same)
	}

	empty := &url.Error{Op: opGet, URL: keepQueryURL}

	got = redirect.Scrub(empty)
	if got.Error() != `Get "`+cleanURL+`": %!s(<nil>)` {
		t.Errorf("a *url.Error with no cause came back as %q, want only its URL reduced", got)
	}
}

// TestScrubCleansAURLErrorNestedInsideAnother checks the rewrite reaches a
// *url.Error that a transport wrapped inside the one net/http returns, because
// each one names a URL.
func TestScrubCleansAURLErrorNestedInsideAnother(t *testing.T) {
	t.Parallel()

	inner := &url.Error{Op: opPost, URL: "http://u:HUNTER2@h/y?ticket=QSECRET#frag", Err: errCaller}
	outer := &url.Error{Op: opGet, URL: cleanURL, Err: inner}

	got := redirect.Scrub(outer)

	for _, secret := range []string{"HUNTER2", "QSECRET", "frag", "user:"} {
		if strings.Contains(got.Error(), secret) {
			t.Errorf("%q still holds %q from the nested URL", got.Error(), secret)
		}
	}

	if want := `Get "` + cleanURL + `": Post "http://h/y": caller policy says no`; got.Error() != want {
		t.Errorf("error text = %q, want %q", got.Error(), want)
	}

	if !errors.Is(got, errCaller) {
		t.Error("Scrub lost the wrapped error")
	}
}
