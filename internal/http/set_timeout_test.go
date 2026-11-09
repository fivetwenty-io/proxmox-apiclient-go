package http //nolint:testpackage // white-box test: inspects the client's unexported http.Client

import (
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// newTicketVersionServer answers the ticket login, waiting loginDelay first
// unless the request is cancelled, and answers GET /version.
func newTicketVersionServer(t *testing.T, loginDelay time.Duration) string {
	t.Helper()

	srv := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case wrapTestPathTicket:
			select {
			case <-time.After(loginDelay):
			case <-request.Context().Done():
				return
			}

			writer.Header().Set(testHeaderContentType, testContentTypeJSON)
			_, _ = writer.Write([]byte(`{"data":{"ticket":"fresh","CSRFPreventionToken":"csrf"},"success":1}`))
		case wrapTestPathVersion:
			writer.Header().Set(testHeaderContentType, testContentTypeJSON)
			_, _ = writer.Write(versionEnvelope(t))
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	})

	return srv.URL
}

// TestSetTimeout_InstallsCopyAndLeavesOldUntouched verifies SetTimeout swaps
// in a copy of the http.Client carrying the new timeout, sharing the
// transport, and never writes the client in use.
func TestSetTimeout_InstallsCopyAndLeavesOldUntouched(t *testing.T) {
	t.Parallel()

	for name, wrap := range timeoutWrapCases() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts := minimalHTTPOptions()
			opts.WrapTransport = wrap

			client, err := NewClient(opts)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			before := client.httpClient.Load()

			client.SetTimeout(7 * time.Second)

			after := client.httpClient.Load()

			if after == before {
				t.Fatal("SetTimeout wrote the http.Client in use instead of installing a copy")
			}

			if before.Timeout != 5*time.Second || after.Timeout != 7*time.Second {
				t.Errorf("Timeout before/after = %v/%v, want 5s/7s", before.Timeout, after.Timeout)
			}

			if after.Transport != before.Transport {
				t.Error("the copy does not share the transport")
			}

			if client.timeout != 7*time.Second {
				t.Errorf("client.timeout = %v, want 7s", client.timeout)
			}

			client.SetTimeout(7 * time.Second)

			if client.httpClient.Load() != after {
				t.Error("SetTimeout with the current timeout replaced the http.Client")
			}
		})
	}
}

// TestSetTimeout_WithoutNewClientRecordsOnly verifies a Client built without
// NewClient, which holds no http.Client, takes the call without panicking.
func TestSetTimeout_WithoutNewClientRecordsOnly(t *testing.T) {
	t.Parallel()

	client := &Client{}
	client.SetTimeout(time.Second)

	if client.timeout != time.Second {
		t.Errorf("client.timeout = %v, want 1s", client.timeout)
	}
}

// TestSetTimeout_ReachesTicketLogin verifies the ticket login uses the new
// timeout: a login slower than the new timeout fails after SetTimeout, so the
// authenticator was handed the replacement client.
func TestSetTimeout_ReachesTicketLogin(t *testing.T) {
	t.Parallel()

	serverURL := newTicketVersionServer(t, 2*time.Second)

	client := newClientForServerURL(t, serverURL, func(o *Options) {
		o.Username = testUsername
		o.Password = testPassword
	})
	client.SetMaxRetries(0)
	client.SetTimeout(50 * time.Millisecond)

	start := time.Now()

	_, err := client.Do("GET", "/version", nil)
	if err == nil {
		t.Fatal("Do succeeded, want the login to time out under the new 50ms timeout")
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Do took %v, want the login cut off by the 50ms timeout, not the 5s one", elapsed)
	}

	// net/http words the client timeout as "context deadline exceeded" or as
	// "request canceled (Client.Timeout exceeded ...)", depending on whether
	// the context or its timer reports it first, so accept either.
	if msg := err.Error(); !strings.Contains(msg, "deadline exceeded") && !strings.Contains(msg, "Client.Timeout exceeded") {
		t.Errorf("Do error = %v, want a login timeout", err)
	}
}

// TestSetTimeout_RaceWithRequests calls SetTimeout, and SetKeepAlive beside
// it, while ticket logins and requests run, with and without a wrapper, so go
// test -race catches a write to an http.Client or transport in use.
func TestSetTimeout_RaceWithRequests(t *testing.T) {
	t.Parallel()

	for name, wrap := range timeoutWrapCases() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			serverURL := newTicketVersionServer(t, 0)

			client := newClientForServerURL(t, serverURL, func(o *Options) {
				o.Username = testUsername
				o.Password = testPassword
				o.WrapTransport = wrap
			})

			setter := func(i int) {
				if i%2 == 0 {
					client.SetTimeout(time.Duration(5+i%3) * time.Second)

					return
				}

				client.SetKeepAlive(1 + i%4)
			}

			err := hammerSetter(setter, 4, 25, func() error {
				_, doErr := client.Do("GET", "/version", nil)

				return doErr
			})
			if err != nil {
				t.Fatalf("Do during SetTimeout: %v", err)
			}

			closeErr := client.Close()
			if closeErr != nil {
				t.Fatalf("Close: %v", closeErr)
			}
		})
	}
}

// attemptTestGuard bounds how long an attempt-boundary test waits for a
// request that a correct client finishes at once. A defect that lets an
// attempt keep the wrong timeout leaves it waiting on a one-hour deadline, so
// the guard turns that hang into a prompt failure.
const attemptTestGuard = 10 * time.Second

// timeoutCaseWrapper names the test cases that run behind a WrapTransport wrapper.
const timeoutCaseWrapper = "wrapper"

// attemptTestShortTimeout is the short timeout the attempt-boundary tests
// switch to.
const attemptTestShortTimeout = 40 * time.Millisecond

// timeoutWrapCases returns the two transport arrangements the timeout tests
// run against: the client's own transport, and one behind a WrapTransport
// wrapper.
func timeoutWrapCases() map[string]func(*http.Transport) http.RoundTripper {
	return map[string]func(*http.Transport) http.RoundTripper{
		wrapTestNoWrapper:  nil,
		timeoutCaseWrapper: func(b *http.Transport) http.RoundTripper { return &recordingWrapper{base: b} },
	}
}

// attemptServer answers GET /version through a per-call handler and the ticket
// login at once, counting both.
type attemptServer struct {
	url          string
	versionCalls atomic.Int32
	tickets      atomic.Int32
}

// newAttemptServer starts an attemptServer. onVersion receives the 1-based
// number of each /version call, in arrival order.
func newAttemptServer(t *testing.T, onVersion func(call int, writer http.ResponseWriter, request *http.Request)) *attemptServer {
	t.Helper()

	attempts := &attemptServer{}

	srv := newTestServer(t, func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case wrapTestPathTicket:
			attempts.tickets.Add(1)

			writer.Header().Set(testHeaderContentType, testContentTypeJSON)
			_, _ = writer.Write([]byte(`{"data":{"ticket":"fresh","CSRFPreventionToken":"csrf"},"success":1}`))
		case wrapTestPathVersion:
			onVersion(int(attempts.versionCalls.Add(1)), writer, request)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	})

	attempts.url = srv.URL

	return attempts
}

// closeOnce returns a function that closes done once, and registers it as a
// cleanup so a handler still waiting on done never outlives the test.
func closeOnce(t *testing.T, done chan struct{}) func() {
	t.Helper()

	var once sync.Once

	closer := func() { once.Do(func() { close(done) }) }

	t.Cleanup(closer)

	return closer
}

// holdUntilGone parks a handler until the client drops the request, which a
// client timeout does, or until release closes.
func holdUntilGone(request *http.Request, release <-chan struct{}) {
	select {
	case <-request.Context().Done():
	case <-release:
	}
}

// signal sends on entered without blocking the handler.
func signal(entered chan<- struct{}) {
	select {
	case entered <- struct{}{}:
	default:
	}
}

// startDo runs client.Do for GET /version on its own goroutine and returns the
// channel its error arrives on.
func startDo(client *Client) <-chan error {
	result := make(chan error, 1)

	go func() {
		_, err := client.Do("GET", "/version", nil)
		result <- err
	}()

	return result
}

// awaitReached waits until the request has reached the server, failing if it
// ended first, which only happens when its timeout elapsed before the handler
// ran.
func awaitReached(t *testing.T, entered <-chan struct{}, result <-chan error) {
	t.Helper()

	select {
	case <-entered:
	case err := <-result:
		t.Fatalf("the request ended before it reached the server: %v", err)
	case <-time.After(attemptTestGuard):
		t.Fatal("the request never reached the server")
	}
}

// awaitResult waits for the request to finish and returns its error.
func awaitResult(t *testing.T, result <-chan error) error {
	t.Helper()

	select {
	case err := <-result:
		return err
	case <-time.After(attemptTestGuard):
		t.Fatalf("the request did not finish within %v, so an attempt did not get the timeout it should have", attemptTestGuard)

		return nil
	}
}

// requireClientTimeout fails unless err is the client timeout expiring.
func requireClientTimeout(t *testing.T, err error) {
	t.Helper()

	if err == nil {
		t.Fatal("the request succeeded, want it cut off by the client timeout")
	}

	if msg := err.Error(); !strings.Contains(msg, "deadline exceeded") && !strings.Contains(msg, "Client.Timeout exceeded") {
		t.Fatalf("the request failed with %v, want the client timeout", err)
	}
}

// TestSetTimeout_InFlightAttemptKeepsItsShortTimeout verifies raising the
// timeout does not extend an attempt already in flight: it still ends when the
// timeout it started with elapses.
func TestSetTimeout_InFlightAttemptKeepsItsShortTimeout(t *testing.T) {
	t.Parallel()

	for name, wrap := range timeoutWrapCases() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var (
				entered = make(chan struct{}, 1)
				release = make(chan struct{})
			)

			attempts := newAttemptServer(t, func(_ int, _ http.ResponseWriter, request *http.Request) {
				signal(entered)
				holdUntilGone(request, release)
			})

			closeOnce(t, release)

			client := newClientForServerURL(t, attempts.url, func(o *Options) {
				o.APIToken = testAPITokenShort
				o.Timeout = 150 * time.Millisecond
				o.WrapTransport = wrap
			})
			client.SetMaxRetries(0)

			result := startDo(client)

			awaitReached(t, entered, result)

			client.SetTimeout(time.Hour)

			requireClientTimeout(t, awaitResult(t, result))
		})
	}
}

// TestSetTimeout_InFlightAttemptKeepsItsLongTimeout verifies shortening the
// timeout does not cut off an attempt already in flight, while attempts that
// start afterwards are cut off by the new timeout.
func TestSetTimeout_InFlightAttemptKeepsItsLongTimeout(t *testing.T) {
	t.Parallel()

	for name, wrap := range timeoutWrapCases() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var (
				entered = make(chan struct{}, 1)
				release = make(chan struct{})
				stop    = make(chan struct{})
			)

			attempts := newAttemptServer(t, func(call int, writer http.ResponseWriter, request *http.Request) {
				if call > 1 {
					holdUntilGone(request, stop)

					return
				}

				signal(entered)
				holdUntilGone(request, release)

				writer.Header().Set(testHeaderContentType, testContentTypeJSON)
				_, _ = writer.Write(versionEnvelope(t))
			})

			closeOnce(t, stop)
			closeRelease := closeOnce(t, release)

			client := newClientForServerURL(t, attempts.url, func(o *Options) {
				o.APIToken = testAPITokenShort
				o.Timeout = time.Hour
				o.WrapTransport = wrap
			})
			client.SetMaxRetries(0)

			first := startDo(client)

			awaitReached(t, entered, first)

			client.SetTimeout(attemptTestShortTimeout)

			// Each later attempt is cut off by the new timeout. Together they
			// outlast the new timeout several times over, so an in-flight
			// attempt that had adopted it would have ended by now.
			for range 3 {
				requireClientTimeout(t, awaitResult(t, startDo(client)))
			}

			select {
			case err := <-first:
				t.Fatalf("the in-flight attempt ended after SetTimeout shortened the timeout: %v", err)
			default:
			}

			closeRelease()

			err := awaitResult(t, first)
			if err != nil {
				t.Fatalf("the in-flight attempt failed once released: %v", err)
			}
		})
	}
}

// TestSetTimeout_RetryAdoptsNewTimeout verifies a retry of the same request
// uses the timeout SetTimeout set while the first attempt was in flight.
func TestSetTimeout_RetryAdoptsNewTimeout(t *testing.T) {
	t.Parallel()

	for name, wrap := range timeoutWrapCases() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var (
				entered = make(chan struct{}, 1)
				proceed = make(chan struct{})
				stop    = make(chan struct{})
			)

			attempts := newAttemptServer(t, func(call int, writer http.ResponseWriter, request *http.Request) {
				if call > 1 {
					holdUntilGone(request, stop)

					return
				}

				signal(entered)
				holdUntilGone(request, proceed)

				writer.WriteHeader(http.StatusServiceUnavailable)
			})

			closeOnce(t, stop)
			closeProceed := closeOnce(t, proceed)

			client := newClientForServerURL(t, attempts.url, func(o *Options) {
				o.APIToken = testAPITokenShort
				o.Timeout = time.Hour
				o.WrapTransport = wrap
			})
			client.SetMaxRetries(1)
			client.SetRetryDelay(time.Millisecond)

			result := startDo(client)

			awaitReached(t, entered, result)

			client.SetTimeout(attemptTestShortTimeout)
			closeProceed()

			requireClientTimeout(t, awaitResult(t, result))

			if got := attempts.versionCalls.Load(); got != 2 {
				t.Errorf("/version calls = %d, want the first attempt and one retry", got)
			}
		})
	}
}

// TestSetTimeout_ResendAfterReloginAdoptsNewTimeout verifies the resend after
// a forced re-login uses the timeout SetTimeout set while the rejected attempt
// was in flight.
func TestSetTimeout_ResendAfterReloginAdoptsNewTimeout(t *testing.T) {
	t.Parallel()

	for name, wrap := range timeoutWrapCases() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var (
				entered = make(chan struct{}, 1)
				proceed = make(chan struct{})
				stop    = make(chan struct{})
			)

			attempts := newAttemptServer(t, func(call int, writer http.ResponseWriter, request *http.Request) {
				if call > 1 {
					holdUntilGone(request, stop)

					return
				}

				signal(entered)
				holdUntilGone(request, proceed)

				writer.WriteHeader(http.StatusUnauthorized)
			})

			closeOnce(t, stop)
			closeProceed := closeOnce(t, proceed)

			client := newClientForServerURL(t, attempts.url, func(o *Options) {
				o.Username = testUsername
				o.Password = testPassword
				o.Timeout = time.Hour
				o.WrapTransport = wrap
			})
			client.SetMaxRetries(0)

			result := startDo(client)

			awaitReached(t, entered, result)

			client.SetTimeout(attemptTestShortTimeout)
			closeProceed()

			requireClientTimeout(t, awaitResult(t, result))

			if got := attempts.tickets.Load(); got != 2 {
				t.Errorf("ticket logins = %d, want the first login and the forced re-login", got)
			}

			if got := attempts.versionCalls.Load(); got != 2 {
				t.Errorf("/version calls = %d, want the rejected attempt and the resend", got)
			}
		})
	}
}
