package auth_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/auth"
)

// loginResponse is the JSON body returned by the mock PVE login endpoint.
type loginResponse struct {
	Data struct {
		Ticket              string `json:"ticket"`
		CSRFPreventionToken string `json:"CSRFPreventionToken"`
		Username            string `json:"username"`
	} `json:"data"`
	Success int `json:"success"`
}

// newMockPVEServer starts a minimal httptest server that accepts POST
// /api2/json/access/ticket and returns a fresh ticket on every call.
// It is safe for concurrent requests.
func newMockPVEServer(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(respWriter http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost || req.URL.Path != pathAccessTicket {
			http.NotFound(respWriter, req)

			return
		}

		_ = req.ParseForm()

		resp := loginResponse{}
		resp.Data.Ticket = "PVE:root@pam:" + time.Now().Format("15:04:05.000000000")
		resp.Data.CSRFPreventionToken = "csrf-token"
		resp.Data.Username = req.FormValue("username")
		resp.Success = 1

		respWriter.Header().Set("Content-Type", "application/json")

		_ = json.NewEncoder(respWriter).Encode(resp)
	}))

	return srv
}

// TestTicketAuthenticator_ConcurrentRefreshAndRead launches 100 goroutines
// that each call RefreshForce() and GetTicket()/GetHeaders() concurrently.
// The race detector must report no data races on the ticket field.
func TestTicketAuthenticator_ConcurrentRefreshAndRead(t *testing.T) {
	t.Parallel()

	srv := newMockPVEServer(t)
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	baseURL := u.Scheme + "://" + u.Host + "/api2/json"

	httpClient := srv.Client()
	creds := &auth.Credentials{Username: testUserRoot, Password: testSecretPass, Realm: testRealm}
	ticketAuth := auth.NewTicketAuthenticator(baseURL, creds, httpClient, "", false)

	// Seed with an initial ticket so readers see non-nil immediately.
	err := ticketAuth.Authenticate()
	if err != nil {
		t.Fatalf("initial Authenticate() failed: %v", err)
	}

	const goroutines = 100

	var waitGroup sync.WaitGroup

	waitGroup.Add(goroutines)

	for goroutineID := range goroutines {
		go func(id int) {
			defer waitGroup.Done()

			if id%3 == 0 {
				// Writer: force a ticket refresh.
				_ = ticketAuth.RefreshForce()
			} else {
				// Reader: obtain ticket and headers concurrently.
				_ = ticketAuth.GetTicket()
				_ = ticketAuth.GetHeaders()
				_ = ticketAuth.IsAuthenticated()
			}
		}(goroutineID)
	}

	waitGroup.Wait()
}

// TestTicketAuthenticator_SetGetTicketConcurrent verifies that SetTicket and
// GetTicket are safe under concurrent access.
func TestTicketAuthenticator_SetGetTicketConcurrent(t *testing.T) {
	t.Parallel()

	srv := newMockPVEServer(t)
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	baseURL := u.Scheme + "://" + u.Host + "/api2/json"

	httpClient := srv.Client()
	creds := &auth.Credentials{Username: testUserRoot, Password: testSecretPass, Realm: testRealm}
	ticketAuth := auth.NewTicketAuthenticator(baseURL, creds, httpClient, "", false)

	validUntil := time.Now().Add(2 * time.Hour)

	const goroutines = 100

	var waitGroup sync.WaitGroup

	waitGroup.Add(goroutines)

	for goroutineID := range goroutines {
		go func(id int) {
			defer waitGroup.Done()

			if id%2 == 0 {
				ticketAuth.SetTicket(&auth.Ticket{
					Value:      "ticket-value",
					CSRFToken:  "csrf",
					Username:   "root@pam",
					ValidUntil: validUntil,
				})
			} else {
				_ = ticketAuth.GetTicket()
			}
		}(goroutineID)
	}

	waitGroup.Wait()
}

// TestTicketAuthenticator_RefreshNoRace verifies the non-forced Refresh path
// is also race-free when called concurrently.
func TestTicketAuthenticator_RefreshNoRace(t *testing.T) {
	t.Parallel()

	srv := newMockPVEServer(t)
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	baseURL := u.Scheme + "://" + u.Host + "/api2/json"

	httpClient := srv.Client()
	creds := &auth.Credentials{Username: testUserRoot, Password: testSecretPass, Realm: testRealm}
	ticketAuth := auth.NewTicketAuthenticator(baseURL, creds, httpClient, "", false)

	const goroutines = 50

	var waitGroup sync.WaitGroup

	waitGroup.Add(goroutines)

	for range goroutines {
		go func() {
			defer waitGroup.Done()

			_ = ticketAuth.Refresh()
		}()
	}

	waitGroup.Wait()

	// Verify we actually authenticated.
	if !ticketAuth.IsAuthenticated() {
		t.Error("expected IsAuthenticated() == true after concurrent Refresh calls")
	}
}

// TestTicketAuthenticator_LogoutConcurrent verifies Logout is race-free when
// called while other goroutines read headers.
func TestTicketAuthenticator_LogoutConcurrent(t *testing.T) {
	t.Parallel()

	// Logout hits DELETE /access/ticket; extend the server handler to accept it.
	srv := httptest.NewServer(http.HandlerFunc(func(respWriter http.ResponseWriter, req *http.Request) {
		switch {
		case req.Method == http.MethodPost && req.URL.Path == "/api2/json/access/ticket":
			resp := loginResponse{}
			resp.Data.Ticket = "TICKET"
			resp.Data.CSRFPreventionToken = "CSRF"
			resp.Data.Username = "root@pam"
			resp.Success = 1

			respWriter.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(respWriter).Encode(resp)

		case req.Method == http.MethodDelete && req.URL.Path == "/api2/json/access/ticket":
			_, _ = io.WriteString(respWriter, `{"data":null,"success":1}`)

		default:
			http.NotFound(respWriter, req)
		}
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	baseURL := u.Scheme + "://" + u.Host + "/api2/json"

	httpClient := srv.Client()
	creds := &auth.Credentials{Username: testUserRoot, Password: testSecretPass, Realm: testRealm}
	ticketAuth := auth.NewTicketAuthenticator(baseURL, creds, httpClient, "", false)

	err := ticketAuth.Authenticate()
	if err != nil {
		t.Fatalf("Authenticate() failed: %v", err)
	}

	var waitGroup sync.WaitGroup

	const readers = 50

	waitGroup.Add(readers + 1)

	go func() {
		defer waitGroup.Done()

		_ = ticketAuth.Logout()
	}()

	for range readers {
		go func() {
			defer waitGroup.Done()

			_ = ticketAuth.GetHeaders()
			_ = ticketAuth.IsAuthenticated()
		}()
	}

	waitGroup.Wait()
}
