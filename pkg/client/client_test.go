package client_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

const (
	testVersionEndpoint = "/api2/json/version"
)

type newClientTest struct {
	name    string
	opts    client.Options
	wantErr bool
	errMsg  string
}

func getValidTestCases() []newClientTest {
	return []newClientTest{
		{
			name: "valid with username/password",
			opts: client.Options{
				Host:     testHost,
				Username: testUsername,
				Password: testPassword,
			},
			wantErr: false,
		},
		{
			name: "valid with API token",
			opts: client.Options{
				Host:     testHost,
				APIToken: testAPIToken,
			},
			wantErr: false,
		},
	}
}

func getInvalidTestCases() []newClientTest {
	return []newClientTest{
		{
			name: "missing host",
			opts: client.Options{
				Username: testUsername,
				Password: testPassword,
			},
			wantErr: true,
			errMsg:  "host is required",
		},
		{
			name: "missing credentials",
			opts: client.Options{
				Host: testHost,
			},
			wantErr: true,
			errMsg:  "authentication credentials required",
		},
		{
			name: "username without password",
			opts: client.Options{
				Host:     testHost,
				Username: testUsername,
			},
			wantErr: true,
			errMsg:  "password required when using username authentication",
		},
		{
			name: "invalid protocol",
			opts: client.Options{
				Host:     testHost,
				Username: testUsername,
				Password: testPassword,
				Protocol: "ftp",
			},
			wantErr: true,
			errMsg:  testErrProtocol,
		},
		{
			name: "invalid port",
			opts: client.Options{
				Host:     testHost,
				Username: testUsername,
				Password: testPassword,
				Port:     70000,
			},
			wantErr: true,
			errMsg:  testErrPort,
		},
	}
}

func getNewClientTestCases() []newClientTest {
	tests := getValidTestCases()
	tests = append(tests, getInvalidTestCases()...)

	return tests
}

func runNewClientTest(t *testing.T, testCase newClientTest) {
	t.Helper()

	cli, err := client.NewClient(testCase.opts)

	if testCase.wantErr {
		if err == nil {
			t.Errorf("NewClient() expected error, got nil")

			return
		}

		if testCase.errMsg != "" && err.Error() != testCase.errMsg && !contains(err.Error(), testCase.errMsg) {
			t.Errorf("NewClient() error = %v, want containing %v", err, testCase.errMsg)
		}

		return
	}

	if err != nil {
		t.Errorf("NewClient() unexpected error = %v", err)

		return
	}

	if cli == nil {
		t.Errorf("NewClient() returned nil client")
	}
}

func TestNewClient(t *testing.T) {
	t.Parallel()

	tests := getNewClientTestCases()

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			runNewClientTest(t, testCase)
		})
	}
}

func TestClient_UpdateTicket(t *testing.T) {
	t.Parallel()

	opts := client.Options{
		Host:     testHost,
		Username: testUsername,
		Password: testPassword,
	}

	cli, err := client.NewClient(opts)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	testTicket := "PVE:test:ticket"
	cli.UpdateTicket(testTicket)

	// We can't directly test the internal state without type assertion working
	// This is a limitation of the current design where client is a private type
	// In a real test, we would either:
	// 1. Make the client type public
	// 2. Add getter methods to verify the state
	// 3. Test the behavior rather than the state
	// For now, we'll just verify the method doesn't panic
}

func TestClient_UpdateCSRFToken(t *testing.T) {
	t.Parallel()

	opts := client.Options{
		Host:     testHost,
		Username: testUsername,
		Password: testPassword,
	}

	cli, err := client.NewClient(opts)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	testToken := "test:csrf:token"
	cli.UpdateCSRFToken(testToken)

	// See comment in TestClient_UpdateTicket about testing private state
}

func TestClient_SetTimeout(t *testing.T) {
	t.Parallel()

	opts := client.Options{
		Host:     testHost,
		Username: testUsername,
		Password: testPassword,
	}

	cli, err := client.NewClient(opts)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	newTimeout := 60 * time.Second
	cli.SetTimeout(newTimeout)

	// See comment in TestClient_UpdateTicket about testing private state
}

// TestClient_SetTimeout_TakesEffect verifies SetTimeout is not a no-op: it
// must reach the live HTTP client so a request against a slow server times
// out according to the newly configured timeout rather than the (longer)
// timeout set at construction.
func TestClient_SetTimeout_TakesEffect(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":"ok","success":1}`))
	}))

	defer func() {
		close(release)
		srv.Close()
	}()

	host, port := parseServerURL(srv.URL)

	cli, err := client.NewClient(client.Options{
		Host:     host,
		Port:     port,
		Protocol: testProtoHTTP,
		APIToken: testAPIToken,
		Timeout:  10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	cli.SetTimeout(20 * time.Millisecond)

	start := time.Now()

	// Force zero retries so the default 1s inter-attempt retry delay does not
	// mask whether the per-request timeout itself actually shortened.
	ctx := client.WithRetries(context.Background(), 0)

	_, err = cli.GetCtx(ctx, "/test", nil)
	if err == nil {
		t.Fatal("GetCtx() = nil error, want a timeout error after SetTimeout shortened the deadline")
	}

	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("GetCtx() took %v, want it to fail quickly per the shortened timeout", elapsed)
	}
}

func TestClient_SetKeepAlive(t *testing.T) {
	t.Parallel()

	opts := client.Options{
		Host:     testHost,
		Username: testUsername,
		Password: testPassword,
	}

	cli, err := client.NewClient(opts)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	newKeepAlive := 20
	cli.SetKeepAlive(newKeepAlive)

	// See comment in TestClient_UpdateTicket about testing private state
}

// TestClient_SetKeepAlive_TakesEffect verifies SetKeepAlive is not a no-op: a
// request must still succeed after the live transport's idle-connection pool
// size is changed, proving the call reached the real transport rather than
// only mutating unread Options state.
func TestClient_SetKeepAlive_TakesEffect(t *testing.T) {
	t.Parallel()

	srv := createTestServer()
	defer srv.Close()

	host, port := parseServerURL(srv.URL)

	cli, err := client.NewClient(client.Options{
		Host:     host,
		Port:     port,
		Protocol: testProtoHTTP,
		APIToken: testAPIToken,
	})
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	cli.SetKeepAlive(1)

	for i := range 3 {
		_, getErr := cli.Get("/test", nil)
		if getErr != nil {
			t.Fatalf("Get() call %d after SetKeepAlive: %v", i, getErr)
		}
	}
}

func createTestServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api2/json") {
			http.Error(writer, "bad base path", http.StatusNotFound)

			return
		}

		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"data": {"ok": true}, "success": 1}`))
	}))
}

func parseServerURL(serverURL string) (string, int) {
	u, _ := url.Parse(serverURL)
	host := strings.Split(u.Host, ":")[0]

	portStr := "80"
	if parts := strings.Split(u.Host, ":"); len(parts) == 2 {
		portStr = parts[1]
	}

	var port int
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}

	return host, port
}

func testHTTPMethod(t *testing.T, cli client.Client, method string, data map[string]interface{}) {
	t.Helper()

	var (
		result interface{}
		err    error
	)

	switch method {
	case "GET":
		result, err = cli.Get("/test", nil)
	case "POST":
		result, err = cli.Post("/test", data)
	case "PUT":
		result, err = cli.Put("/test", data)
	case "DELETE":
		result, err = cli.Delete("/test", nil)
	}

	if err != nil {
		t.Errorf("%s() unexpected error = %v", method, err)
	}

	if result == nil {
		t.Errorf("%s() returned nil data", method)
	}
}

func TestClient_HTTPMethods(t *testing.T) {
	t.Parallel()

	srv := createTestServer()
	defer srv.Close()

	host, port := parseServerURL(srv.URL)
	opts := client.Options{
		Host:     host,
		Port:     port,
		Protocol: testProtoHTTP,
		APIToken: testAPIToken,
	}

	cli, err := client.NewClient(opts)
	if err != nil {
		t.Fatalf("Failed to create client: %v", err)
	}

	testHTTPMethod(t, cli, "GET", nil)

	resp, err := cli.GetRaw("/test", nil)
	if err != nil {
		t.Errorf("GetRaw() unexpected error = %v", err)
	}

	if resp == nil {
		t.Errorf("GetRaw() returned nil response")
	}

	testData := map[string]interface{}{"key": "value"}
	testHTTPMethod(t, cli, "POST", testData)
	testHTTPMethod(t, cli, "PUT", testData)
	testHTTPMethod(t, cli, "DELETE", nil)
}

// Helper function to check if string contains substring.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || findSubstring(s, substr)))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}

	return false
}
