package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/cache"
	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/client"
)

// optsFromServer builds client options pointing at an httptest.Server URL.
func optsFromServer(serverURL string) client.Options {
	host, port := parseServerURL(serverURL)

	return client.Options{
		Host:     host,
		Port:     port,
		Protocol: testProtoHTTP,
		APIToken: testAPIToken,
	}
}

// covTestServer returns an httptest.Server that captures the last request
// method and path and always responds with a minimal PVE JSON envelope.
func covTestServer(t *testing.T, lastMethod, lastPath *atomic.Value) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()

	mux.HandleFunc("/api2/json/test", func(w http.ResponseWriter, r *http.Request) {
		lastMethod.Store(r.Method)
		lastPath.Store(r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"ok","success":1}`))
	})

	return httptest.NewServer(mux)
}

// errorServer returns an httptest.Server that always responds 500.
func errorServer(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "internal error", http.StatusInternalServerError)
	}))
}

// ---------- context-aware wrappers ----------

func TestCovGetCtx(t *testing.T) {
	t.Parallel()

	var lastMethod, lastPath atomic.Value

	srv := covTestServer(t, &lastMethod, &lastPath)
	defer srv.Close()

	cli, err := client.NewClient(optsFromServer(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	data, err := cli.GetCtx(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("GetCtx error: %v", err)
	}

	if data == nil {
		t.Fatal("GetCtx returned nil data")
	}

	if got, _ := lastMethod.Load().(string); got != http.MethodGet {
		t.Errorf("method = %q, want GET", got)
	}
}

func TestCovGetRawCtx(t *testing.T) {
	t.Parallel()

	var lastMethod, lastPath atomic.Value

	srv := covTestServer(t, &lastMethod, &lastPath)
	defer srv.Close()

	cli, err := client.NewClient(optsFromServer(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	resp, err := cli.GetRawCtx(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("GetRawCtx error: %v", err)
	}

	if resp == nil {
		t.Fatal("GetRawCtx returned nil response")
	}

	if got, _ := lastMethod.Load().(string); got != http.MethodGet {
		t.Errorf("method = %q, want GET", got)
	}

	_ = lastPath
}

func TestCovPostCtx(t *testing.T) {
	t.Parallel()

	var lastMethod, lastPath atomic.Value

	srv := covTestServer(t, &lastMethod, &lastPath)
	defer srv.Close()

	cli, err := client.NewClient(optsFromServer(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	data, err := cli.PostCtx(context.Background(), "/test", map[string]interface{}{"k": "v"})
	if err != nil {
		t.Fatalf("PostCtx error: %v", err)
	}

	if data == nil {
		t.Fatal("PostCtx returned nil data")
	}

	if got, _ := lastMethod.Load().(string); got != http.MethodPost {
		t.Errorf("method = %q, want POST", got)
	}

	_ = lastPath
}

func TestCovPostRawCtx(t *testing.T) {
	t.Parallel()

	var lastMethod, lastPath atomic.Value

	srv := covTestServer(t, &lastMethod, &lastPath)
	defer srv.Close()

	cli, err := client.NewClient(optsFromServer(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	resp, err := cli.PostRawCtx(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("PostRawCtx error: %v", err)
	}

	if resp == nil {
		t.Fatal("PostRawCtx returned nil response")
	}

	if got, _ := lastMethod.Load().(string); got != http.MethodPost {
		t.Errorf("method = %q, want POST", got)
	}

	_ = lastPath
}

func TestCovPutCtx(t *testing.T) {
	t.Parallel()

	var lastMethod, lastPath atomic.Value

	srv := covTestServer(t, &lastMethod, &lastPath)
	defer srv.Close()

	cli, err := client.NewClient(optsFromServer(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	data, err := cli.PutCtx(context.Background(), "/test", map[string]interface{}{"k": "v"})
	if err != nil {
		t.Fatalf("PutCtx error: %v", err)
	}

	if data == nil {
		t.Fatal("PutCtx returned nil data")
	}

	if got, _ := lastMethod.Load().(string); got != http.MethodPut {
		t.Errorf("method = %q, want PUT", got)
	}

	_ = lastPath
}

func TestCovPutRawCtx(t *testing.T) {
	t.Parallel()

	var lastMethod, lastPath atomic.Value

	srv := covTestServer(t, &lastMethod, &lastPath)
	defer srv.Close()

	cli, err := client.NewClient(optsFromServer(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	resp, err := cli.PutRawCtx(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("PutRawCtx error: %v", err)
	}

	if resp == nil {
		t.Fatal("PutRawCtx returned nil response")
	}

	if got, _ := lastMethod.Load().(string); got != http.MethodPut {
		t.Errorf("method = %q, want PUT", got)
	}

	_ = lastPath
}

func TestCovDeleteCtx(t *testing.T) {
	t.Parallel()

	var lastMethod, lastPath atomic.Value

	srv := covTestServer(t, &lastMethod, &lastPath)
	defer srv.Close()

	cli, err := client.NewClient(optsFromServer(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	data, err := cli.DeleteCtx(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("DeleteCtx error: %v", err)
	}

	if data == nil {
		t.Fatal("DeleteCtx returned nil data")
	}

	if got, _ := lastMethod.Load().(string); got != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", got)
	}

	_ = lastPath
}

func TestCovDeleteRawCtx(t *testing.T) {
	t.Parallel()

	var lastMethod, lastPath atomic.Value

	srv := covTestServer(t, &lastMethod, &lastPath)
	defer srv.Close()

	cli, err := client.NewClient(optsFromServer(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	resp, err := cli.DeleteRawCtx(context.Background(), "/test", nil)
	if err != nil {
		t.Fatalf("DeleteRawCtx error: %v", err)
	}

	if resp == nil {
		t.Fatal("DeleteRawCtx returned nil response")
	}

	if got, _ := lastMethod.Load().(string); got != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", got)
	}

	_ = lastPath
}

// ---------- non-2xx returns error ----------

func TestCovCtxMethodsErrorOn5xx(t *testing.T) {
	t.Parallel()

	srv := errorServer(t)
	defer srv.Close()

	cli, err := client.NewClient(optsFromServer(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	ctx := context.Background()
	params := map[string]interface{}{"k": "v"}

	_, getErr := cli.GetCtx(ctx, "/test", nil)
	if getErr == nil {
		t.Error("GetCtx: expected error on 500, got nil")
	}

	_, getRawErr := cli.GetRawCtx(ctx, "/test", nil)
	if getRawErr == nil {
		t.Error("GetRawCtx: expected error on 500, got nil")
	}

	_, postErr := cli.PostCtx(ctx, "/test", params)
	if postErr == nil {
		t.Error("PostCtx: expected error on 500, got nil")
	}

	_, postRawErr := cli.PostRawCtx(ctx, "/test", params)
	if postRawErr == nil {
		t.Error("PostRawCtx: expected error on 500, got nil")
	}

	_, putErr := cli.PutCtx(ctx, "/test", params)
	if putErr == nil {
		t.Error("PutCtx: expected error on 500, got nil")
	}

	_, putRawErr := cli.PutRawCtx(ctx, "/test", params)
	if putRawErr == nil {
		t.Error("PutRawCtx: expected error on 500, got nil")
	}

	_, delErr := cli.DeleteCtx(ctx, "/test", nil)
	if delErr == nil {
		t.Error("DeleteCtx: expected error on 500, got nil")
	}

	_, delRawErr := cli.DeleteRawCtx(ctx, "/test", nil)
	if delRawErr == nil {
		t.Error("DeleteRawCtx: expected error on 500, got nil")
	}
}

// ---------- SetLogConfig / GetLogConfig ----------

func TestCovLogConfig(t *testing.T) {
	t.Parallel()

	cli, err := client.NewClient(client.Options{
		Host:     testHost,
		APIToken: testAPIToken,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// Before any change, GetLogConfig returns a value without panic.
	before := cli.GetLogConfig()
	_ = before

	// Apply a non-zero config and verify it round-trips.
	want := client.LogConfig{
		Enabled: true,
		LogBody: true,
	}
	cli.SetLogConfig(want)

	got := cli.GetLogConfig()
	if got.Enabled != want.Enabled {
		t.Errorf("GetLogConfig().Enabled = %v, want %v", got.Enabled, want.Enabled)
	}

	if got.LogBody != want.LogBody {
		t.Errorf("GetLogConfig().LogBody = %v, want %v", got.LogBody, want.LogBody)
	}
}

// ---------- AddLogHook (smoke: must not panic) ----------

func TestCovAddLogHook(t *testing.T) {
	t.Parallel()

	cli, err := client.NewClient(client.Options{
		Host:     testHost,
		APIToken: testAPIToken,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// A nil hook. Adding it must not panic.
	cli.AddLogHook(nil)
}

// ---------- SetLogger smoke ----------

func TestCovSetLogger(t *testing.T) {
	t.Parallel()

	cli, err := client.NewClient(client.Options{
		Host:     testHost,
		APIToken: testAPIToken,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	cli.SetLogger(nil) // nil is acceptable; must not panic
}

// ---------- Cache control ----------

func covCacheOpts(serverURL string) client.Options {
	host, port := parseServerURL(serverURL)
	cacheConfig := cache.DefaultConfig()
	cacheConfig.Enabled = true

	return client.Options{
		Host:        host,
		Port:        port,
		Protocol:    testProtoHTTP,
		APIToken:    testAPIToken,
		CacheConfig: &cacheConfig,
	}
}

func TestCovCacheStats(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api2/json") {
			http.Error(w, "bad path", http.StatusNotFound)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"cached-value","success":1}`))
	}))
	defer srv.Close()

	cli, err := client.NewClient(covCacheOpts(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	closeErr := cli.Close()
	defer func() { _ = closeErr }()

	// CacheStats must return non-nil when cache is enabled.
	stats := cli.CacheStats()
	if stats == nil {
		t.Fatal("CacheStats() returned nil with cache enabled")
	}
}

func TestCovClearCache(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"v","success":1}`))
	}))
	defer srv.Close()

	cli, err := client.NewClient(covCacheOpts(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	defer func() { _ = cli.Close() }()

	// ClearCache must not panic.
	cli.ClearCache()
}

func TestCovInvalidateCache(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"v","success":1}`))
	}))
	defer srv.Close()

	cli, err := client.NewClient(covCacheOpts(srv.URL))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	defer func() { _ = cli.Close() }()

	// InvalidateCache returns the number of removed entries (≥ 0).
	removed := cli.InvalidateCache("/test/*")
	if removed < 0 {
		t.Errorf("InvalidateCache returned %d, want >= 0", removed)
	}
}

// ---------- Close ----------

func TestCovClose(t *testing.T) {
	t.Parallel()

	cli, err := client.NewClient(client.Options{
		Host:     testHost,
		APIToken: testAPIToken,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	firstCloseErr := cli.Close()
	if firstCloseErr != nil {
		t.Fatalf("Close() first call: %v", firstCloseErr)
	}

	// Second call must not panic and must not return an error.
	secondCloseErr := cli.Close()
	if secondCloseErr != nil {
		t.Fatalf("Close() second call: %v", secondCloseErr)
	}
}

func TestCovCloseWithCacheEnabled(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"v","success":1}`))
	}))
	defer srv.Close()

	cacheConfig := cache.DefaultConfig()
	cacheConfig.Enabled = true

	host, port := parseServerURL(srv.URL)

	cli, err := client.NewClient(client.Options{
		Host:        host,
		Port:        port,
		Protocol:    testProtoHTTP,
		APIToken:    testAPIToken,
		CacheConfig: &cacheConfig,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	firstCloseErr := cli.Close()
	if firstCloseErr != nil {
		t.Fatalf("Close() first call: %v", firstCloseErr)
	}

	// Second call must also succeed (idempotent).
	secondCloseErr := cli.Close()
	if secondCloseErr != nil {
		t.Fatalf("Close() second call: %v", secondCloseErr)
	}
}
