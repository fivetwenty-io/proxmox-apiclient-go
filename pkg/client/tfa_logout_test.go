package client_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/auth"
	pve "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
	"strconv"
)

func TestClient_TFAHandler_AutoComplete(t *testing.T) {
	t.Parallel()

	// Simulate TFA required on login, then success on /access/tfa, and a simple /version call
	var tfaCalled atomic.Bool

	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.URL.Path, "/api2/json") {
			http.NotFound(writer, request)

			return
		}

		writer.Header().Set("Content-Type", "application/json")

		switch request.URL.Path {
		case testAccessTicketEndpoint:
			// Return TFA required challenge
			_, _ = io.WriteString(writer, `{"data":{"NeedTFA":true, "Ticket2":"PARTIAL","challenge":"CHALLENGE","tfa-types":["totp"]}}`)
		case "/api2/json/access/tfa":
			tfaCalled.Store(true)
			// Complete TFA, return a normal ticket
			_, _ = io.WriteString(writer, `{"data":{"ticket":"TICKET","CSRFPreventionToken":"CSRF","username":"root@pam"},"success":1}`)
		case testVersionEndpoint:
			// Normal payload
			_, _ = io.WriteString(writer, `{"data":{"version":"8.1"},"success":1}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	host := strings.Split(u.Host, ":")[0]
	port := 80

	if parts := strings.Split(u.Host, ":"); len(parts) == 2 {
		p, err := strconv.Atoi(parts[1])
		if err == nil {
			port = p
		}
	}

	opts := pve.Options{Host: host, Port: port, Protocol: testProtoHTTP, Username: testUsername, Password: testPassword}

	cli, err := pve.NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	// Install an auto TFA handler that returns a fake totp code
	handler := auth.NewAutoTFAHandler(map[auth.TFAType]string{auth.TFATypeTOTP: "123456"})
	cli.SetTFAHandler(handler)

	// Call version – this should trigger auth, see TFA, complete it, then proceed
	_, err = cli.Get("/version", nil)
	if err != nil {
		t.Fatalf("Get(/version) error = %v", err)
	}

	if !tfaCalled.Load() {
		t.Fatalf("expected TFA endpoint to be called")
	}
}

func TestClient_Logout_Ticket(t *testing.T) {
	t.Parallel()

	var logoutCalled atomic.Bool

	srv := createLogoutTestServer(&logoutCalled)
	defer srv.Close()

	cli := setupLogoutTestClient(t, srv)
	testLogoutFunctionality(t, cli, &logoutCalled)
}

func createLogoutTestServer(logoutCalled *atomic.Bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.URL.Path, "/api2/json") {
			http.NotFound(writer, request)

			return
		}

		writer.Header().Set("Content-Type", "application/json")
		handleLogoutServerRoutes(writer, request, logoutCalled)
	}))
}

func handleLogoutServerRoutes(writer http.ResponseWriter, request *http.Request, logoutCalled *atomic.Bool) {
	switch request.URL.Path {
	case "/api2/json/access/ticket":
		handleTicketEndpoint(writer, request, logoutCalled)
	case testVersionEndpoint:
		_, _ = io.WriteString(writer, `{"data":{"version":"8.1"},"success":1}`)
	default:
		http.NotFound(writer, request)
	}
}

func handleTicketEndpoint(writer http.ResponseWriter, request *http.Request, logoutCalled *atomic.Bool) {
	if request.Method == http.MethodPost {
		_, _ = io.WriteString(writer, `{"data":{"ticket":"TICKET","CSRFPreventionToken":"CSRF","username":"root@pam"},"success":1}`)

		return
	}

	if request.Method == http.MethodDelete {
		logoutCalled.Store(true)
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, `{"data":null,"success":1}`)

		return
	}

	http.NotFound(writer, request)
}

//nolint:ireturn // Test helper - returns interface for test setup
func setupLogoutTestClient(t *testing.T, srv *httptest.Server) pve.Client {
	t.Helper()

	u, _ := url.Parse(srv.URL)
	host := strings.Split(u.Host, ":")[0]
	port := extractPortFromHost(u.Host)

	opts := pve.Options{Host: host, Port: port, Protocol: testProtoHTTP, Username: testUsername, Password: testPassword}

	cli, err := pve.NewClient(opts)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	return cli
}

func extractPortFromHost(host string) int {
	port := 80

	if parts := strings.Split(host, ":"); len(parts) == 2 {
		p, err := strconv.Atoi(parts[1])
		if err == nil {
			port = p
		}
	}

	return port
}

func testLogoutFunctionality(t *testing.T, cli pve.Client, logoutCalled *atomic.Bool) {
	t.Helper()
	// Force a simple authenticated call
	_, err := cli.Get("/version", nil)
	if err != nil {
		t.Fatalf("Get(/version) error = %v", err)
	}

	err = cli.Logout()
	if err != nil {
		t.Fatalf("Logout() error = %v", err)
	}

	if !logoutCalled.Load() {
		t.Fatalf("expected DELETE /access/ticket to be called on Logout")
	}
}
