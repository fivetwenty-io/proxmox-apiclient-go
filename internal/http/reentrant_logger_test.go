package http //nolint:testpackage // white-box test: builds a Client with a wrapper to reach the SetKeepAlive warning

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// reentrantLogger calls back into the client from Warn, once, the way a
// logger that adjusts the client in response to a warning would.
type reentrantLogger struct {
	client  *Client
	entered atomic.Bool
	warns   atomic.Int32
}

func (*reentrantLogger) Debug(string, map[string]interface{}) {}
func (*reentrantLogger) Info(string, map[string]interface{})  {}
func (*reentrantLogger) Error(string, map[string]interface{}) {}

func (l *reentrantLogger) Warn(string, map[string]interface{}) {
	l.warns.Add(1)

	if l.entered.Swap(true) {
		return
	}

	l.client.SetTimeout(3 * time.Second)
	l.client.SetKeepAlive(2)
}

// TestSetKeepAlive_ReentrantLoggerDoesNotDeadlock verifies SetKeepAlive
// releases its lock before warning, so a logger whose Warn calls SetTimeout
// and SetKeepAlive returns instead of blocking forever.
func TestSetKeepAlive_ReentrantLoggerDoesNotDeadlock(t *testing.T) {
	t.Parallel()

	opts := minimalHTTPOptions()
	opts.WrapTransport = func(b *http.Transport) http.RoundTripper { return &recordingWrapper{base: b} }

	client, err := NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	logger := &reentrantLogger{client: client}
	client.SetLogger(logger)
	client.SetLogConfig(LogConfig{Enabled: true})

	done := make(chan struct{})

	go func() {
		defer close(done)

		client.SetKeepAlive(4)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("SetKeepAlive deadlocked when its logger called SetTimeout and SetKeepAlive")
	}

	if got := logger.warns.Load(); got != 2 {
		t.Errorf("Warn calls = %d, want 2 (the outer call and the reentrant one)", got)
	}

	if got := client.httpClient.Load().Timeout; got != 3*time.Second {
		t.Errorf("Timeout = %v, want 3s from the reentrant SetTimeout", got)
	}
}
