package http //nolint:testpackage // white-box test: inspects the client's unexported http.Client

import (
	"net/http"
	"strings"
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

	cases := map[string]func(*http.Transport) http.RoundTripper{
		wrapTestNoWrapper: nil,
		"wrapper":         func(b *http.Transport) http.RoundTripper { return &recordingWrapper{base: b} },
	}

	for name, wrap := range cases {
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

	cases := map[string]func(*http.Transport) http.RoundTripper{
		wrapTestNoWrapper: nil,
		"wrapper":         func(b *http.Transport) http.RoundTripper { return &recordingWrapper{base: b} },
	}

	for name, wrap := range cases {
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
