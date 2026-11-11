package http //nolint:testpackage // white-box test: reaches the client's authenticator and base URL

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect/redirecttest"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/auth"
)

// captureLines records every log line and hook event a client produces, as
// text, so a test can look for a secret in all of them.
type captureLines struct {
	mu    sync.Mutex
	lines []string
}

func (c *captureLines) Debug(msg string, fields map[string]interface{}) {
	c.addf("debug %s %v %+v", msg, fields, fields)
}
func (c *captureLines) Info(msg string, fields map[string]interface{}) {
	c.addf("info %s %v %+v", msg, fields, fields)
}
func (c *captureLines) Warn(msg string, fields map[string]interface{}) {
	c.addf("warn %s %v %+v", msg, fields, fields)
}
func (c *captureLines) Error(msg string, fields map[string]interface{}) {
	c.addf("error %s %v %+v", msg, fields, fields)
}

func (c *captureLines) addf(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.lines = append(c.lines, fmt.Sprintf(format, args...))
}

func (c *captureLines) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()

	return append([]string(nil), c.lines...)
}

// stepLogin names the ticket login in the request tables.
const stepLogin = "login"

// errNotTicketAuth is wrapped by logIn when a client was built without ticket
// authentication.
var errNotTicketAuth = errors.New("authenticator is not a ticket authenticator")

// logIn runs the ticket login of a client built for ticket authentication.
func logIn(c *Client) error {
	ticketAuth, ok := c.authenticator.(*auth.TicketAuthenticator)
	if !ok {
		return fmt.Errorf("%w, got %T", errNotTicketAuth, c.authenticator)
	}

	return ticketAuth.Authenticate() //nolint:wrapcheck // the login error is what the tests check
}

// hostileModes are the redirect settings a client can have. The default
// refuses at the transport. A following policy lets net/http read the Location
// header and follow it, and a policy that fails hands net/http an error to wrap
// around the Location target.
func hostileModes() map[string]func(*Options) {
	return map[string]func(*Options){
		"default": func(*Options) {},
		"follow":  func(o *Options) { o.CheckRedirect = redirect.Follow(3) },
		"policy error": func(o *Options) {
			o.CheckRedirect = func(*http.Request, []*http.Request) error { return Permanent(errPolicy) }
		},
	}
}

// newHostileClient builds a client for server with logging and a hook that
// capture everything, and the retry settings of a real client.
func newHostileClient(
	t *testing.T, server *redirecttest.Server, mode func(*Options), password bool,
) (*Client, *captureLines) {
	t.Helper()

	host, portText, err := net.SplitHostPort(server.Addr)
	if err != nil {
		t.Fatalf("SplitHostPort: %v", err)
	}

	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("Atoi: %v", err)
	}

	opts := minimalHTTPOptions()
	opts.Host, opts.Port = host, port

	if password {
		opts.Username, opts.Password = testUsername, testPassword
	} else {
		opts.APIToken = testAPITokenFull
	}

	mode(opts)

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	captured := &captureLines{}

	client.SetLogger(captured)
	client.SetLogConfig(LogConfig{Enabled: true, LogQueryParams: true, LogRequestHeader: true})
	client.AddHook(func(event *Event) { captured.addf("hook %v %+v", event, event) })
	client.SetMaxRetries(redirectTestRetry)
	client.SetRetryDelay(time.Millisecond)

	return client, captured
}

// TestHostileResponsesLeaveNoSecretOnAnyPath sends every hostile Location value
// from redirecttest through the request path and the ticket login, under each
// redirect setting. The error text, its %+v form, every log line, and every
// hook event must hold none of the secrets in the header, whether net/http
// parses the header, the transport rejects it, or the client follows it to a
// host that is down. With the default setting the request is attempted once.
func TestHostileResponsesLeaveNoSecretOnAnyPath(t *testing.T) {
	t.Parallel()

	dead := redirecttest.DeadAddr(t)

	paths := map[string]func(*Client) error{
		"get": func(c *Client) error {
			_, err := c.DoWithContext(context.Background(), http.MethodGet, "/version", nil)

			return err
		},
		"post": func(c *Client) error {
			_, err := c.DoWithContext(WithForceRetry(context.Background(), true), http.MethodPost, "/version", nil)

			return err
		},
		stepLogin: logIn,
	}

	for _, hostile := range redirecttest.Cases(dead) {
		for modeName, mode := range hostileModes() {
			for pathName, send := range paths {
				t.Run(hostile.Name+"/"+modeName+"/"+pathName, func(t *testing.T) {
					t.Parallel()

					server := redirecttest.Serve(t, http.StatusTemporaryRedirect, hostile.Location)
					client, captured := newHostileClient(t, server, mode, pathName == stepLogin)

					err := send(client)
					redirecttest.AssertClean(t, err, captured.all()...)

					if modeName == "default" {
						if !errors.Is(err, ErrRedirectRefused) && !IsPermanent(err) {
							t.Errorf("error = %v, want a permanent refusal", err)
						}

						if got := server.Hits(); got != 1 {
							t.Errorf("server read %d requests, want 1", got)
						}
					}
				})
			}
		}
	}
}

// TestTransportErrorsLeaveNoRequestSecrets sends requests whose URL holds user
// information and a query secret to a host that is down. net/http names the
// URL in the dial error, and the client must show it as scheme, host, and path
// only, on every path that sends a request.
func TestTransportErrorsLeaveNoRequestSecrets(t *testing.T) {
	t.Parallel()

	paths := map[string]func(*Client) error{
		"get": func(c *Client) error {
			_, err := c.DoWithContext(context.Background(), http.MethodGet, "/version?ticket="+redirecttest.SecretQuery, nil)

			return err
		},
		"post": func(c *Client) error {
			ctx := WithForceRetry(context.Background(), true)
			_, err := c.DoWithContext(ctx, http.MethodPost, "/version?ticket="+redirecttest.SecretQuery, nil)

			return err
		},
		stepLogin: logIn,
	}

	for name, send := range paths {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			dead := &redirecttest.Server{Addr: redirecttest.DeadAddr(t)}
			client, _ := newHostileClient(t, dead, func(o *Options) {
				o.Host = "user:" + redirecttest.SecretUser + "@" + o.Host
			}, name == stepLogin)

			// The request log lines name the request URL as the caller wrote it, so
			// only the error is checked here.
			err := send(client)
			redirecttest.AssertClean(t, err)

			if !strings.Contains(err.Error(), "dial tcp") {
				t.Errorf("error = %v, want the dial failure kept", err)
			}
		})
	}
}

// TestRedirectWithoutALocationIsRefused sends 3xx answers that carry no
// Location header. net/http returns such an answer to the caller as a plain
// response, so only the transport's refusal turns it into a permanent
// ErrRedirectRefused. A 304 is not a redirect and reaches the caller.
func TestRedirectWithoutALocationIsRefused(t *testing.T) {
	t.Parallel()

	for _, status := range []int{
		http.StatusMultipleChoices, http.StatusMovedPermanently, http.StatusFound,
		http.StatusUseProxy, http.StatusPermanentRedirect, http.StatusNotModified,
	} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			t.Parallel()

			server := redirecttest.Serve(t, status, nil)
			client, _ := newHostileClient(t, server, func(*Options) {}, false)

			_, err := client.DoWithContext(context.Background(), http.MethodGet, "/version", nil)
			if err == nil {
				t.Fatal("got no error, want the request to fail")
			}

			if status == http.StatusNotModified {
				if errors.Is(err, ErrRedirectRefused) {
					t.Errorf("error = %v, want the 304 to reach the caller", err)
				}

				return
			}

			if !errors.Is(err, ErrRedirectRefused) || !IsPermanent(err) {
				t.Fatalf("error = %v, want a permanent ErrRedirectRefused", err)
			}

			if got := server.Hits(); got != 1 {
				t.Errorf("server read %d requests, want 1", got)
			}
		})
	}
}
