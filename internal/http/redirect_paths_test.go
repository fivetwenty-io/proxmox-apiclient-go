package http //nolint:testpackage // white-box test: reaches the client's authenticator and middleware chain

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/auth"
	apierrors "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/errors"
)

const (
	locationSecretUser  = "HUNTER2"
	locationSecretQuery = "QSECRET"
	locationSecretFrag  = "FRAGSECRET"
)

// secretLocation returns a Location value for target that carries a password,
// a query secret, and a fragment secret.
func secretLocation(_, target string) string {
	return strings.Replace(target, "http://", "http://user:"+locationSecretUser+"@", 1) +
		"/x?ticket=" + locationSecretQuery + "#" + locationSecretFrag
}

func assertNoLocationSecrets(t *testing.T, err error) {
	t.Helper()

	msg := err.Error()
	for _, secret := range []string{locationSecretUser, locationSecretQuery, locationSecretFrag, "user:"} {
		if strings.Contains(msg, secret) {
			t.Errorf("error %q holds %q from the Location header", msg, secret)
		}
	}
}

// TestRedirectRefusedForEveryRedirectStatus checks all five statuses Go
// follows, so the policy cannot be narrowed to a subset.
func TestRedirectRefusedForEveryRedirectStatus(t *testing.T) {
	t.Parallel()

	statuses := []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
	}

	for _, status := range statuses {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()

			rig := newRedirectRig(t, status, nil)

			rig.assertRefused(t, rig.do(http.MethodGet), status)
		})
	}
}

// TestRedirectRefusedWhateverChanges checks that the refusal does not depend
// on which part of the URL differs, which includes the scheme change that
// moves a token from a plain listener to a TLS one and a change of path alone.
func TestRedirectRefusedWhateverChanges(t *testing.T) {
	t.Parallel()

	cases := map[string]func(first, target string) string{
		"scheme only": func(first, _ string) string {
			return "https://" + strings.TrimPrefix(first, "http://") + "/api"
		},
		"path only": func(first, _ string) string { return first + "/elsewhere" },
		"port only": func(_, target string) string { return target + redirectTestPath },
		"host only": func(first, _ string) string {
			return "http://localhost:" + strings.TrimPrefix(first, "http://127.0.0.1:") + "/api"
		},
	}

	for name, locate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newRedirectRigTo(t, http.StatusFound, locate, nil)
			err := rig.do(http.MethodGet)

			if !errors.Is(err, ErrRedirectRefused) {
				t.Fatalf("error = %v, want ErrRedirectRefused", err)
			}

			if got := rig.attempts.Load(); got != 1 {
				t.Errorf("attempts = %d, want 1", got)
			}

			if got := rig.targetHits.Load(); got != 0 {
				t.Errorf("target read %d requests, want 0", got)
			}
		})
	}
}

// TestRedirectErrorHidesTheLocationCredentials runs the whole request path and
// checks the final error text, because net/http builds that text around the
// policy's error and puts the raw Location header in it.
func TestRedirectErrorHidesTheLocationCredentials(t *testing.T) {
	t.Parallel()

	t.Run("default refusal", func(t *testing.T) {
		t.Parallel()

		rig := newRedirectRigTo(t, http.StatusFound, secretLocation, nil)
		err := rig.do(http.MethodGet)

		if !errors.Is(err, ErrRedirectRefused) {
			t.Fatalf("error = %v, want ErrRedirectRefused", err)
		}

		assertNoLocationSecrets(t, err)

		if !strings.Contains(err.Error(), "/x") || !strings.Contains(err.Error(), "127.0.0.1") {
			t.Errorf("error %q must still name the target host and path", err.Error())
		}
	})

	t.Run("callers policy error", func(t *testing.T) {
		t.Parallel()

		rig := newRedirectRigTo(t, http.StatusFound, secretLocation, func(o *Options) {
			o.CheckRedirect = func(*http.Request, []*http.Request) error { return Permanent(errPolicy) }
		})

		err := rig.do(http.MethodGet)
		if !errors.Is(err, errPolicy) {
			t.Fatalf("error = %v, want the caller's policy error", err)
		}

		assertNoLocationSecrets(t, err)
	})

	t.Run("transport errors keep their url", func(t *testing.T) {
		t.Parallel()

		client, _ := clientWithFailingTransport(t, errBase)
		client.SetMaxRetries(0)

		_, err := client.Do(http.MethodGet, "/version?keep=1", nil)
		if err == nil || !strings.Contains(err.Error(), "keep=1") {
			t.Errorf("error = %v, want the request URL kept for a transport failure", err)
		}
	})
}

// unparseableLocations returns Location values that net/http cannot parse, each
// with a password, a query secret, and a fragment secret, in the three places
// a parse can fail, which are the port, a percent escape, and an IPv6 bracket.
func unparseableLocations() map[string]func(first, target string) string {
	return map[string]func(first, target string) string{
		"invalid port": func(_, _ string) string {
			return "http://user:" + locationSecretUser + "@127.0.0.1:abc/x?ticket=" + locationSecretQuery +
				"#" + locationSecretFrag
		},
		"bad escape": func(_, target string) string {
			return strings.Replace(target, "http://", "http://user:"+locationSecretUser+"@", 1) +
				"/%zz?ticket=" + locationSecretQuery + "#" + locationSecretFrag
		},
		"open bracket": func(_, _ string) string {
			return "http://user:" + locationSecretUser + "@[::1/x?ticket=" + locationSecretQuery +
				"#" + locationSecretFrag
		},
	}
}

// TestUnparseableLocationIsARefusalThatHidesTheHeader checks a Location value
// that does not parse. net/http quotes the whole raw header in that error, so
// the client must replace it with a permanent refusal, for every redirect
// status, and attempt the request once.
func TestUnparseableLocationIsARefusalThatHidesTheHeader(t *testing.T) {
	t.Parallel()

	statuses := []int{
		http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther,
		http.StatusTemporaryRedirect, http.StatusPermanentRedirect,
	}

	for name, locate := range unparseableLocations() {
		for _, status := range statuses {
			t.Run(name+"/"+strconv.Itoa(status), func(t *testing.T) {
				t.Parallel()

				rig := newRedirectRigTo(t, status, locate, nil)
				err := rig.do(http.MethodGet)

				if !errors.Is(err, ErrRedirectRefused) {
					t.Fatalf("error = %v, want ErrRedirectRefused", err)
				}

				if !IsPermanent(err) {
					t.Errorf("error = %v, want the permanent marker", err)
				}

				assertNoLocationSecrets(t, err)

				if strings.Contains(err.Error(), "failed to parse") {
					t.Errorf("error %q still carries the net/http parse failure", err.Error())
				}

				if got := rig.attempts.Load(); got != 1 {
					t.Errorf("attempts = %d, want 1", got)
				}

				if got := rig.firstHits.Load(); got != 1 {
					t.Errorf("first listener read %d requests, want 1", got)
				}
			})
		}
	}
}

// TestUnparseableLocationOnAPostAndAPolicyThatFollows covers the two paths the
// default GET case does not. A POST never retries, and a policy that follows
// every redirect never sees a Location header that does not parse.
func TestUnparseableLocationOnAPostAndAPolicyThatFollows(t *testing.T) {
	t.Parallel()

	locate := unparseableLocations()["invalid port"]

	t.Run("post", func(t *testing.T) {
		t.Parallel()

		rig := newRedirectRigTo(t, http.StatusTemporaryRedirect, locate, nil)
		err := rig.do(http.MethodPost)

		if !errors.Is(err, ErrRedirectRefused) {
			t.Fatalf("error = %v, want ErrRedirectRefused", err)
		}

		assertNoLocationSecrets(t, err)
	})

	t.Run("following policy", func(t *testing.T) {
		t.Parallel()

		rig := newRedirectRigTo(t, http.StatusFound, locate, func(o *Options) {
			o.CheckRedirect = redirect.Follow(2)
		})
		err := rig.do(http.MethodGet)

		if !errors.Is(err, ErrRedirectRefused) || !IsPermanent(err) {
			t.Fatalf("error = %v, want a permanent ErrRedirectRefused", err)
		}

		assertNoLocationSecrets(t, err)

		if got := rig.attempts.Load(); got != 1 {
			t.Errorf("attempts = %d, want 1", got)
		}
	})
}

// TestTicketRequestsHideAnUnparseableLocation checks login, renewal, TFA, and
// logout, which send through the authenticator's own client.
func TestTicketRequestsHideAnUnparseableLocation(t *testing.T) {
	t.Parallel()

	validTicket := &auth.Ticket{
		Value: "PVE:root@pam:ticket", CSRFToken: "csrf", Username: testUsername,
		ValidUntil: time.Now().Add(time.Hour),
	}

	steps := map[string]func(rig *ticketRedirectRig) error{
		"login":   func(rig *ticketRedirectRig) error { return rig.ticketAuth.Authenticate() },
		"renewal": func(rig *ticketRedirectRig) error { return rig.ticketAuth.RefreshForce() },
		"logout": func(rig *ticketRedirectRig) error {
			rig.ticketAuth.SetTicket(validTicket)

			return rig.ticketAuth.Logout()
		},
	}

	for name, step := range steps {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newTicketRedirectRigTo(t, http.StatusTemporaryRedirect, unparseableLocations()["invalid port"])

			rig.assertRefused(t, step(rig))
		})
	}
}

// TestRedirectUseLastResponseStillWorks checks the caller's policy may return
// http.ErrUseLastResponse, which net/http compares by identity, so the policy
// wrapper must pass it through unwrapped.
func TestRedirectUseLastResponseStillWorks(t *testing.T) {
	t.Parallel()

	rig := newRedirectRig(t, http.StatusFound, func(o *Options) {
		o.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	})

	err := rig.do(http.MethodGet)
	if err == nil {
		t.Fatal("a bare 302 must surface as an error")
	}

	if errors.Is(err, ErrRedirectRefused) {
		t.Errorf("error = %v, want the 302 response handled as an API response", err)
	}

	if got := rig.targetHits.Load(); got != 0 {
		t.Errorf("target read %d requests, want 0", got)
	}

	if !strings.Contains(err.Error(), "302") {
		t.Errorf("error %q must carry the 302 status", err.Error())
	}
}

// TestRedirectRefusalSurvivesSetTimeout checks the copy SetTimeout makes keeps
// the redirect policy, for the request path and for the authenticator.
func TestRedirectRefusalSurvivesSetTimeout(t *testing.T) {
	t.Parallel()

	rig := newRedirectRig(t, http.StatusTemporaryRedirect, nil)
	rig.client.SetTimeout(7 * time.Second)

	rig.assertRefused(t, rig.do(http.MethodGet), http.StatusTemporaryRedirect)
}

// ticketRedirectRig is a client that logs in with a user name and password
// against a listener that answers every request with a redirect to a second
// listener on another port. The second listener records what it reads.
type ticketRedirectRig struct {
	client     *Client
	ticketAuth *auth.TicketAuthenticator

	firstHits  atomic.Int32
	targetHits atomic.Int32
}

func newTicketRedirectRig(t *testing.T, status int) *ticketRedirectRig {
	t.Helper()

	return newTicketRedirectRigTo(t, status, secretLocation)
}

// newTicketRedirectRigTo builds the rig with the Location header set by
// locate, which receives the target's base URL.
func newTicketRedirectRigTo(t *testing.T, status int, locate func(first, target string) string) *ticketRedirectRig {
	t.Helper()

	rig := &ticketRedirectRig{}

	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		rig.targetHits.Add(1)
	}))
	t.Cleanup(target.Close)

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		rig.firstHits.Add(1)
		w.Header().Set("Location", locate("", target.URL))
		w.WriteHeader(status)
	}))
	t.Cleanup(first.Close)

	host, portText, err := net.SplitHostPort(strings.TrimPrefix(first.URL, "http://"))
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}

	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("Atoi: %v", err)
	}

	opts := minimalHTTPOptions()
	opts.Host, opts.Port = host, port
	opts.Username, opts.Password = testUsername, testPassword

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ticketAuth, ok := client.authenticator.(*auth.TicketAuthenticator)
	if !ok {
		t.Fatalf("authenticator is %T, want *auth.TicketAuthenticator", client.authenticator)
	}

	rig.client, rig.ticketAuth = client, ticketAuth

	return rig
}

func (rig *ticketRedirectRig) assertRefused(t *testing.T, err error) {
	t.Helper()

	if !errors.Is(err, ErrRedirectRefused) {
		t.Fatalf("error = %v, want ErrRedirectRefused", err)
	}

	assertNoLocationSecrets(t, err)

	if got := rig.targetHits.Load(); got != 0 {
		t.Errorf("redirect target read %d requests, want 0", got)
	}

	if got := rig.firstHits.Load(); got != 1 {
		t.Errorf("first listener read %d requests, want 1", got)
	}
}

// TestTicketRequestsRefuseRedirects covers every request the ticket
// authenticator sends: login, forced renewal, TFA completion, and logout. The
// login and the TFA request carry the password and the partial ticket.
func TestTicketRequestsRefuseRedirects(t *testing.T) {
	t.Parallel()

	validTicket := &auth.Ticket{
		Value: "PVE:root@pam:ticket", CSRFToken: "csrf", Username: testUsername,
		ValidUntil: time.Now().Add(time.Hour),
	}

	steps := map[string]func(rig *ticketRedirectRig) error{
		"login": func(rig *ticketRedirectRig) error { return rig.ticketAuth.Authenticate() },
		"renewal": func(rig *ticketRedirectRig) error {
			return rig.ticketAuth.RefreshForce()
		},
		"tfa": func(rig *ticketRedirectRig) error {
			challenge, answer := &auth.TFAChallenge{Ticket: "PVE:tfa"}, &auth.TFAResponse{Response: "123456"}

			_, err := rig.ticketAuth.CompleteTFA(challenge, answer)
			if err != nil {
				return fmt.Errorf("complete TFA: %w", err)
			}

			return nil
		},
		"logout": func(rig *ticketRedirectRig) error {
			rig.ticketAuth.SetTicket(validTicket)

			return rig.ticketAuth.Logout()
		},
	}

	for name, step := range steps {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			rig := newTicketRedirectRig(t, http.StatusTemporaryRedirect)

			rig.assertRefused(t, step(rig))
		})
	}
}

// TestTicketRequestsRefuseRedirectsAfterSetTimeout checks the copy SetTimeout
// hands the authenticator keeps the policy.
func TestTicketRequestsRefuseRedirectsAfterSetTimeout(t *testing.T) {
	t.Parallel()

	rig := newTicketRedirectRig(t, http.StatusFound)
	rig.client.SetTimeout(9 * time.Second)

	rig.assertRefused(t, rig.ticketAuth.Authenticate())
}

// TestPermanentConnectionFailureKeepsItsType checks a wrapper that marks a
// dial failure permanent still gets the typed ConnectionError, in one attempt.
func TestPermanentConnectionFailureKeepsItsType(t *testing.T) {
	t.Parallel()

	dialFailure := &net.OpError{Op: "dial", Net: "tcp", Err: errReset}
	client, wrapper := clientWithFailingTransport(t, Permanent(dialFailure))

	_, err := client.Do(http.MethodGet, "/version", nil)

	var connErr *apierrors.ConnectionError
	if !errors.As(err, &connErr) {
		t.Fatalf("error = %v, want a *ConnectionError", err)
	}

	if !errors.Is(err, errReset) {
		t.Errorf("error = %v, want the dial failure reachable", err)
	}

	if got := wrapper.count.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1", got)
	}
}

// TestPermanentErrorOnAPostIsNotRetried covers the method that never retries
// without the opt-in. The loop's message says the request was not retried
// because of the marker, not because of the method.
func TestPermanentErrorOnAPostIsNotRetried(t *testing.T) {
	t.Parallel()

	client, wrapper := clientWithFailingTransport(t, Permanent(errRefused))

	_, err := client.Do(http.MethodPost, "/version", nil)
	if !errors.Is(err, errRefused) || !IsPermanent(err) {
		t.Fatalf("error = %v, want the wrapper's permanent error", err)
	}

	if !strings.Contains(err.Error(), "not retried") {
		t.Errorf("error %q must say the request was not retried", err.Error())
	}

	if got := wrapper.count.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1", got)
	}
}

// TestPermanentJoinedErrorIsAttemptedOnce checks the marker is found when the
// wrapper joins its refusal with another error.
func TestPermanentJoinedErrorIsAttemptedOnce(t *testing.T) {
	t.Parallel()

	client, wrapper := clientWithFailingTransport(t, errors.Join(errBase, Permanent(errReset)))

	_, err := client.Do(http.MethodGet, "/version", nil)
	if !IsPermanent(err) {
		t.Fatalf("error = %v, want a permanent error", err)
	}

	if got := wrapper.count.Load(); got != 1 {
		t.Errorf("attempts = %d, want 1", got)
	}
}
