package http //nolint:testpackage // white-box test: inspects the client's unexported setter state

import (
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/auth"
	pmetrics "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/metrics"
)

// settersHookLimit bounds how many hooks the race test adds, so the hook list
// stays small while the setter loop spins.
const settersHookLimit = 32

const (
	settersTestAuthorization = "authorization"
	settersTestChanged       = "changed"
)

// cycleClientSetters returns a setter for hammerSetter that calls each of the
// client's request-path setters in turn.
func cycleClientSetters(client *Client, logger Logger, metrics *pmetrics.DefaultMetrics) func(int) {
	return func(i int) {
		switch i % 7 {
		case 0:
			client.SetMaxRetries(i % 3)
		case 1:
			client.SetRetryDelay(time.Duration(1+i%3) * time.Millisecond)
		case 2:
			if i%2 == 0 {
				client.SetLogger(logger)
			} else {
				client.SetLogger(nil)
			}
		case 3:
			cfg := defaultLogConfig()
			cfg.LogResponseHeader = i%2 == 0
			client.SetLogConfig(cfg)
		case 4:
			if i/7 < settersHookLimit {
				client.AddHook(func(*Event) {})
			}
		case 5:
			if i%2 == 0 {
				client.SetMetrics(metrics)
			} else {
				client.SetMetrics(nil)
			}
		default:
			if i%2 == 0 {
				client.SetTFAHandler(fakeTFAHandler{})
			} else {
				client.SetTFAHandler(nil)
			}
		}
	}
}

// TestClientSetters_RaceWithRequests calls SetMaxRetries, SetRetryDelay,
// SetLogger, SetLogConfig, AddHook, SetMetrics, and SetTFAHandler while ticket
// logins and requests run, and reads the state those setters change, so go
// test -race catches a field written while a request reads it.
func TestClientSetters_RaceWithRequests(t *testing.T) {
	t.Parallel()

	serverURL := newTicketVersionServer(t, 0)

	client := newClientForServerURL(t, serverURL, func(o *Options) {
		o.Username = testUsername
		o.Password = testPassword
	})

	setter := cycleClientSetters(client, &testLogger{}, pmetrics.NewDefaultMetrics())

	err := hammerSetter(setter, 4, 25, func() error {
		_, doErr := client.Do("GET", "/version", nil)
		if doErr != nil {
			return doErr
		}

		// Changing the returned config must not reach the client's copy.
		cfg := client.LogConfig()
		cfg.RedactHeaders = append(cfg.RedactHeaders, "x-extra")
		cfg.RedactHeaders[0] = settersTestChanged

		// A non-TFA error reads the handler and returns without a request.
		return client.handleTFAAuthentication(errTestPlainFail)
	})
	if err != nil {
		t.Fatalf("request during the setters: %v", err)
	}

	if got := client.LogConfig().RedactHeaders[0]; got != settersTestAuthorization {
		t.Errorf("RedactHeaders[0] = %q, want the client's own copy untouched", got)
	}

	closeErr := client.Close()
	if closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}
}

// TestSetMetrics_RequestRecordsOnCollectorItStartedOn verifies a request
// records its end on the collector it started on, so replacing the collector
// mid-request leaves both active-connections gauges balanced.
func TestSetMetrics_RequestRecordsOnCollectorItStartedOn(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})

	srv := newTestServer(t, func(writer http.ResponseWriter, _ *http.Request) {
		<-release
		writer.Header().Set(testHeaderContentType, testContentTypeJSON)
		_, _ = writer.Write(pveEnvelope(t, "ok"))
	})

	client := clientPointedAt(t, srv.URL)
	client.SetMaxRetries(0)

	first := pmetrics.NewDefaultMetrics()
	second := pmetrics.NewDefaultMetrics()

	client.SetMetrics(first)

	done := make(chan error, 1)

	go func() {
		_, err := client.Do("GET", "/version", nil)
		done <- err
	}()

	deadline := time.Now().Add(5 * time.Second)
	for first.ActiveConnections.Get() != 1 {
		if time.Now().After(deadline) {
			close(release)
			t.Fatal("the request never started on the first collector")
		}

		time.Sleep(time.Millisecond)
	}

	client.SetMetrics(second)
	close(release)

	err := <-done
	if err != nil {
		t.Fatalf("Do: %v", err)
	}

	if got := first.ActiveConnections.Get(); got != 0 {
		t.Errorf("first collector active connections = %d, want 0", got)
	}

	if got := first.RequestsTotal.Get(); got != 1 {
		t.Errorf("first collector requests = %d, want 1", got)
	}

	if got := second.ActiveConnections.Get(); got != 0 {
		t.Errorf("second collector active connections = %d, want 0", got)
	}

	if got := second.RequestsTotal.Get(); got != 0 {
		t.Errorf("second collector requests = %d, want 0", got)
	}
}

// TestLogConfig_CopiesRedactionLists verifies neither the caller's slice nor
// the one LogConfig returns shares a backing array with the client's copy.
func TestLogConfig_CopiesRedactionLists(t *testing.T) {
	t.Parallel()

	client := clientPointedAt(t, "http://127.0.0.1:1")

	cfg := LogConfig{Enabled: true, RedactHeaders: []string{settersTestAuthorization}, RedactParams: []string{"token"}}
	client.SetLogConfig(cfg)

	cfg.RedactHeaders[0] = settersTestChanged
	cfg.RedactParams[0] = settersTestChanged

	got := client.LogConfig()
	if got.RedactHeaders[0] != settersTestAuthorization || got.RedactParams[0] != "token" {
		t.Fatalf("LogConfig = %v/%v, want the caller's later change not to reach it", got.RedactHeaders, got.RedactParams)
	}

	got.RedactHeaders[0] = settersTestChanged

	if client.LogConfig().RedactHeaders[0] != settersTestAuthorization {
		t.Error("changing the returned config changed the client's copy")
	}
}

// TestAddHook_ConcurrentCallsKeepEveryHook verifies concurrent AddHook calls
// never lose a hook, and every hook fires.
func TestAddHook_ConcurrentCallsKeepEveryHook(t *testing.T) {
	t.Parallel()

	const hooks = 50

	client := clientPointedAt(t, "http://127.0.0.1:1")

	var (
		group   sync.WaitGroup
		firedMu sync.Mutex
		fired   int
	)

	for range hooks {
		group.Go(func() {
			client.AddHook(func(*Event) {
				firedMu.Lock()
				fired++
				firedMu.Unlock()
			})
		})
	}

	group.Wait()

	if got := len(client.logging().hooks); got != hooks {
		t.Fatalf("hook count = %d, want %d", got, hooks)
	}

	client.fireHook(&Event{})

	if fired != hooks {
		t.Errorf("fired = %d, want %d", fired, hooks)
	}
}

// TestSetters_WithoutNewClient verifies a Client built without NewClient
// reads as no logger, no hooks, no collector, no TFA handler, and zero
// retries, and that its setters work.
func TestSetters_WithoutNewClient(t *testing.T) {
	t.Parallel()

	client := &Client{}

	if client.logging().active() || len(client.logging().hooks) != 0 || client.tfa() != nil {
		t.Fatal("a zero Client reports logging, hooks, or a TFA handler")
	}

	if client.recordRequestStart(nil) != nil {
		t.Fatal("a zero Client reports a metrics collector")
	}

	retries, delay, _ := client.resolveRetryPolicy(mustRequest(t))
	if retries != 0 || delay != 0 {
		t.Fatalf("retry policy = %d/%v, want 0/0", retries, delay)
	}

	client.SetMaxRetries(4)
	client.SetRetryDelay(3 * time.Millisecond)
	client.SetTFAHandler(fakeTFAHandler{})
	client.SetLogger(&testLogger{})
	client.SetLogConfig(LogConfig{Enabled: true})

	retries, delay, _ = client.resolveRetryPolicy(mustRequest(t))
	if retries != 4 || delay != 3*time.Millisecond {
		t.Errorf("retry policy = %d/%v, want 4/3ms", retries, delay)
	}

	if client.tfa() == nil || !client.logging().active() {
		t.Error("the setters did not take effect on a zero Client")
	}

	var handler auth.TFAHandler

	client.SetTFAHandler(handler)

	if client.tfa() != nil {
		t.Error("SetTFAHandler(nil) left a handler installed")
	}
}

// mustRequest builds a GET request for tests that only need its context.
func mustRequest(t *testing.T) *http.Request {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://127.0.0.1:1/", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}

	return req
}
