package auth_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/auth"
	apierrors "github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/errors"
)

// ---------------------------------------------------------------------------
// Package-level constants used across tests.
// ---------------------------------------------------------------------------

const (
	pathAccessTicket = "/api2/json/access/ticket"
	pathAccessTFA    = "/api2/json/access/tfa"

	testRealm         = "pam"
	testChallengeData = "challenge-data"
	testPartialTicket = "PARTIAL"
	testCSRFValue     = "csrf-value"
	testTOTPType      = "totp"
	testUserRoot      = "root"
	testUserRootPAM   = "root@pam"
	testSecretPass    = "secret"
)

// errInvalidTokenConfig is the sentinel error for TestInvalidAuthenticator_AllMethods.
// Declared at package level to satisfy err113 (no inline error creation in tests).
var errInvalidTokenConfig = errors.New("config error: bad token")

// ---------------------------------------------------------------------------
// Response body types used by mock PVE servers.
// ---------------------------------------------------------------------------

// tfaLoginBody is the JSON PVE returns when TFA is required.
type tfaLoginBody struct {
	Data struct {
		NeedTFA   bool     `json:"NeedTFA"`
		Ticket2   string   `json:"ticket2"`
		Challenge string   `json:"challenge"`
		TFATypes  []string `json:"tfa-types"`
	} `json:"data"`
	Success int `json:"success"`
}

// fullTicketBody is the JSON PVE returns on successful (non-TFA) login or TFA completion.
type fullTicketBody struct {
	Data struct {
		Ticket              string `json:"ticket"`
		CSRFPreventionToken string `json:"CSRFPreventionToken"`
		Username            string `json:"username"`
	} `json:"data"`
	Success int `json:"success"`
}

// tfaCompleteBody is the JSON returned by POST /access/tfa on success.
type tfaCompleteBody struct {
	Data struct {
		Ticket              string `json:"ticket"`
		CSRFPreventionToken string `json:"CSRFPreventionToken"`
		Username            string `json:"username"`
	} `json:"data"`
	Success int    `json:"success,omitempty"`
	Message string `json:"message,omitempty"`
}

// ---------------------------------------------------------------------------
// Test helpers.
// ---------------------------------------------------------------------------

// writeFullTicket encodes a fullTicketBody with typed JSON.
func writeFullTicket(rw http.ResponseWriter, body fullTicketBody) {
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(body) //nolint:errchkjson // mock server; body type is known-safe
}

// writeTFALogin encodes a tfaLoginBody with typed JSON.
func writeTFALogin(rw http.ResponseWriter, body tfaLoginBody) {
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(body) //nolint:errchkjson // mock server; body type is known-safe
}

// writeTFAComplete encodes a tfaCompleteBody with typed JSON.
func writeTFAComplete(rw http.ResponseWriter, body tfaCompleteBody) {
	rw.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(rw).Encode(body) //nolint:errchkjson // mock server; body type is known-safe
}

// buildBaseURL derives baseURL from a test server URL; appends /api2/json prefix.
func buildBaseURL(srvURL string) string {
	parsed, _ := url.Parse(srvURL)

	return parsed.Scheme + "://" + parsed.Host + "/api2/json"
}

// newRootTicketAuthenticator builds a TicketAuthenticator with root@pam credentials.
func newRootTicketAuthenticator(srv *httptest.Server, password string) *auth.TicketAuthenticator {
	baseURL := buildBaseURL(srv.URL)
	creds := &auth.Credentials{Username: testUserRoot, Password: password, Realm: testRealm}

	return auth.NewTicketAuthenticator(baseURL, creds, srv.Client(), "", false)
}

// okFullTicket returns a fullTicketBody representing a successful login.
func okFullTicket(ticket, csrf, username string) fullTicketBody { //nolint:unparam // params vary per caller
	var body fullTicketBody

	body.Data.Ticket = ticket
	body.Data.CSRFPreventionToken = csrf
	body.Data.Username = username
	body.Success = 1

	return body
}

// ---------------------------------------------------------------------------
// 1. TFA challenge path — Authenticate returns NeedsTFA, CompleteTFA succeeds.
// ---------------------------------------------------------------------------

// TestAuthenticate_TFAChallengeReturnsError verifies that when the login
// endpoint signals NeedTFA, Authenticate() returns a *TFARequiredError and
// does NOT store a ticket.
func TestAuthenticate_TFAChallengeReturnsError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, httpReq *http.Request) {
		if httpReq.Method != http.MethodPost || httpReq.URL.Path != pathAccessTicket {
			http.NotFound(rw, httpReq)

			return
		}

		var body tfaLoginBody

		body.Data.NeedTFA = true
		body.Data.Ticket2 = "PARTIAL-TICKET"
		body.Data.Challenge = testChallengeData
		body.Data.TFATypes = []string{testTOTPType, "recovery"}
		body.Success = 1
		writeTFALogin(rw, body)
	}))
	defer srv.Close()

	ticketAuth := newRootTicketAuthenticator(srv, "password")

	err := ticketAuth.Authenticate()
	if err == nil {
		t.Fatal("expected error for TFA challenge, got nil")
	}

	var tfaErr *apierrors.TFARequiredError
	if !errors.As(err, &tfaErr) {
		t.Fatalf("expected *apierrors.TFARequiredError, got %T: %v", err, err)
	}

	if tfaErr.Ticket != "PARTIAL-TICKET" {
		t.Errorf("TFARequiredError.Ticket = %q, want %q", tfaErr.Ticket, "PARTIAL-TICKET")
	}

	if tfaErr.Challenge != testChallengeData {
		t.Errorf("TFARequiredError.Challenge = %q, want %q", tfaErr.Challenge, testChallengeData)
	}

	if len(tfaErr.Types) != 2 {
		t.Errorf("TFARequiredError.Types len = %d, want 2", len(tfaErr.Types))
	}

	if ticketAuth.IsAuthenticated() {
		t.Error("expected IsAuthenticated() == false after TFA challenge")
	}
}

// TestAuthenticate_TFAViaTicket2Field verifies the ticket2 field (not NeedTFA flag)
// also triggers the TFA challenge path.
func TestAuthenticate_TFAViaTicket2Field(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, httpReq *http.Request) {
		if httpReq.Method != http.MethodPost || httpReq.URL.Path != pathAccessTicket {
			http.NotFound(rw, httpReq)

			return
		}

		var body tfaLoginBody

		body.Data.Ticket2 = "PARTIAL-TICKET-2"
		body.Success = 1
		writeTFALogin(rw, body)
	}))
	defer srv.Close()

	ticketAuth := newRootTicketAuthenticator(srv, "password")

	err := ticketAuth.Authenticate()
	if err == nil {
		t.Fatal("expected TFA error when ticket2 present, got nil")
	}

	var tfaErr *apierrors.TFARequiredError
	if !errors.As(err, &tfaErr) {
		t.Fatalf("expected *apierrors.TFARequiredError, got %T", err)
	}

	if tfaErr.Ticket != "PARTIAL-TICKET-2" {
		t.Errorf("TFARequiredError.Ticket = %q, want %q", tfaErr.Ticket, "PARTIAL-TICKET-2")
	}
}

// ---------------------------------------------------------------------------
// 2. CompleteTFA success — valid TOTP code → full ticket acquired.
// ---------------------------------------------------------------------------

// TestCompleteTFA_ValidResponse verifies that CompleteTFA() sends the response
// code to /access/tfa, parses the returned ticket, stores it in the
// authenticator, and returns AuthResult.Success == true.
//
//nolint:funlen // test verifies multiple assertions on a single TFA round-trip
func TestCompleteTFA_ValidResponse(t *testing.T) {
	t.Parallel()

	const (
		wantTicket    = "PVE:root@pam:AABBCCDD::valid-signature"
		wantCSRF      = "csrf-from-tfa"
		wantUsername  = testUserRootPAM
		partialTicket = "PARTIAL-TICKET"
		totpCode      = "123456"
	)

	var (
		receivedResponse string
		receivedCookie   string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, httpReq *http.Request) {
		if httpReq.Method == http.MethodPost && httpReq.URL.Path == pathAccessTFA {
			_ = httpReq.ParseForm()
			receivedResponse = httpReq.FormValue("response")
			receivedCookie = httpReq.Header.Get("Cookie")

			var body tfaCompleteBody

			body.Data.Ticket = wantTicket
			body.Data.CSRFPreventionToken = wantCSRF
			body.Data.Username = wantUsername
			body.Success = 1
			writeTFAComplete(rw, body)

			return
		}

		http.NotFound(rw, httpReq)
	}))
	defer srv.Close()

	ticketAuth := newRootTicketAuthenticator(srv, "password")

	challenge := &auth.TFAChallenge{
		Ticket:    partialTicket,
		Challenge: testChallengeData,
		Types:     []string{testTOTPType},
	}
	response := &auth.TFAResponse{
		Response: totpCode,
		Type:     testTOTPType,
	}

	result, err := ticketAuth.CompleteTFA(challenge, response)
	if err != nil {
		t.Fatalf("CompleteTFA() error = %v", err)
	}

	if !result.Success {
		t.Errorf("AuthResult.Success = false, want true")
	}

	if result.Ticket == nil {
		t.Fatal("AuthResult.Ticket is nil")
	}

	if result.Ticket.Value != wantTicket {
		t.Errorf("Ticket.Value = %q, want %q", result.Ticket.Value, wantTicket)
	}

	if result.Ticket.CSRFToken != wantCSRF {
		t.Errorf("Ticket.CSRFToken = %q, want %q", result.Ticket.CSRFToken, wantCSRF)
	}

	if result.Ticket.Username != wantUsername {
		t.Errorf("Ticket.Username = %q, want %q", result.Ticket.Username, wantUsername)
	}

	if !ticketAuth.IsAuthenticated() {
		t.Error("expected IsAuthenticated() == true after CompleteTFA")
	}

	if !strings.Contains(receivedCookie, partialTicket) {
		t.Errorf("TFA request cookie %q does not contain partial ticket %q", receivedCookie, partialTicket)
	}

	if receivedResponse != totpCode {
		t.Errorf("TFA request response = %q, want %q", receivedResponse, totpCode)
	}
}

// ---------------------------------------------------------------------------
// 3. CompleteTFA failure paths.
// ---------------------------------------------------------------------------

// TestCompleteTFA_WrongCode verifies that a non-200 or success!=1 TFA response
// results in AuthResult.Success == false with a non-nil Error field.
func TestCompleteTFA_WrongCode(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, httpReq *http.Request) {
		if httpReq.Method == http.MethodPost && httpReq.URL.Path == pathAccessTFA {
			rw.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(rw, `{"message":"invalid TFA code","success":0}`)

			return
		}

		http.NotFound(rw, httpReq)
	}))
	defer srv.Close()

	ticketAuth := newRootTicketAuthenticator(srv, "password")
	challenge := &auth.TFAChallenge{Ticket: testPartialTicket, Types: []string{testTOTPType}}
	response := &auth.TFAResponse{Response: "000000", Type: testTOTPType}

	result, err := ticketAuth.CompleteTFA(challenge, response)
	if err != nil {
		t.Fatalf("CompleteTFA() returned unexpected error = %v", err)
	}

	if result.Success {
		t.Error("AuthResult.Success = true, want false for rejected TFA")
	}

	if result.Error == nil {
		t.Error("AuthResult.Error is nil, want non-nil error for rejected TFA")
	}

	if ticketAuth.IsAuthenticated() {
		t.Error("expected IsAuthenticated() == false after failed TFA")
	}
}

// TestCompleteTFA_NoTicketInResponse verifies processTFAResult handles
// success==1 but missing ticket data — returns ErrTFAFailedNoTicket.
func TestCompleteTFA_NoTicketInResponse(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, httpReq *http.Request) {
		if httpReq.Method == http.MethodPost && httpReq.URL.Path == pathAccessTFA {
			writeTFAComplete(rw, tfaCompleteBody{Success: 1})

			return
		}

		http.NotFound(rw, httpReq)
	}))
	defer srv.Close()

	ticketAuth := newRootTicketAuthenticator(srv, "password")
	challenge := &auth.TFAChallenge{Ticket: testPartialTicket, Types: []string{testTOTPType}}
	response := &auth.TFAResponse{Response: "123456", Type: testTOTPType}

	result, err := ticketAuth.CompleteTFA(challenge, response)
	if err != nil {
		t.Fatalf("CompleteTFA() returned unexpected error = %v", err)
	}

	if result.Success {
		t.Error("AuthResult.Success = true, want false when no ticket in TFA response")
	}

	if !errors.Is(result.Error, auth.ErrTFAFailedNoTicket) {
		t.Errorf("AuthResult.Error = %v, want ErrTFAFailedNoTicket", result.Error)
	}
}

// TestCompleteTFA_NetworkFailure verifies that a connection error during the
// TFA request surfaces as a *apierrors.ConnectionError.
func TestCompleteTFA_NetworkFailure(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	srvURL := srv.URL
	srv.Close()

	baseURL := buildBaseURL(srvURL)
	creds := &auth.Credentials{Username: testUserRoot, Password: "password", Realm: testRealm}
	ticketAuth := auth.NewTicketAuthenticator(baseURL, creds, &http.Client{}, "", false)

	challenge := &auth.TFAChallenge{Ticket: testPartialTicket, Types: []string{testTOTPType}}
	response := &auth.TFAResponse{Response: "123456", Type: testTOTPType}

	_, err := ticketAuth.CompleteTFA(challenge, response)
	if err == nil {
		t.Fatal("expected error for network failure, got nil")
	}

	var connErr *apierrors.ConnectionError
	if !errors.As(err, &connErr) {
		t.Errorf("expected *apierrors.ConnectionError, got %T: %v", err, err)
	}
}

// TestCompleteTFA_InvalidJSON verifies that malformed TFA response JSON is
// caught and returns a parse error.
func TestCompleteTFA_InvalidJSON(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, httpReq *http.Request) {
		if httpReq.Method == http.MethodPost && httpReq.URL.Path == pathAccessTFA {
			rw.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(rw, `{invalid json}`)

			return
		}

		http.NotFound(rw, httpReq)
	}))
	defer srv.Close()

	ticketAuth := newRootTicketAuthenticator(srv, "password")
	challenge := &auth.TFAChallenge{Ticket: testPartialTicket, Types: []string{testTOTPType}}
	response := &auth.TFAResponse{Response: "123456", Type: testTOTPType}

	_, err := ticketAuth.CompleteTFA(challenge, response)
	if err == nil {
		t.Fatal("expected parse error for invalid JSON response, got nil")
	}
}

// ---------------------------------------------------------------------------
// 4. Ticket + CSRF round trip.
// ---------------------------------------------------------------------------

// TestTicketAuthenticator_FullRoundTrip verifies:
//   - Authenticate succeeds, stores ticket + CSRF.
//   - GetHeaders returns correct Cookie and CSRFPreventionToken headers.
//   - Logout clears the ticket so IsAuthenticated returns false.
//
//nolint:funlen // test verifies multiple sequential phases of one round-trip
func TestTicketAuthenticator_FullRoundTrip(t *testing.T) {
	t.Parallel()

	const (
		wantTicketVal = "PVE:root@pam:12345678::signature"
		wantCSRF      = "csrf-token-value"
		cookieName    = "PVEAuthCookie"
	)

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, httpReq *http.Request) {
		switch {
		case httpReq.Method == http.MethodPost && httpReq.URL.Path == pathAccessTicket:
			writeFullTicket(rw, okFullTicket(wantTicketVal, wantCSRF, testUserRootPAM))

		case httpReq.Method == http.MethodDelete && httpReq.URL.Path == pathAccessTicket:
			_, _ = io.WriteString(rw, `{"data":null,"success":1}`)

		default:
			http.NotFound(rw, httpReq)
		}
	}))
	defer srv.Close()

	baseURL := buildBaseURL(srv.URL)
	creds := &auth.Credentials{Username: testUserRoot, Password: testSecretPass, Realm: testRealm}
	ticketAuth := auth.NewTicketAuthenticator(baseURL, creds, srv.Client(), cookieName, false)

	err := ticketAuth.Authenticate()
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}

	if !ticketAuth.IsAuthenticated() {
		t.Error("expected IsAuthenticated() == true after Authenticate")
	}

	headers := ticketAuth.GetHeaders()
	if headers == nil {
		t.Fatal("GetHeaders() returned nil after successful auth")
	}

	expectedCookie := fmt.Sprintf("%s=%s", cookieName, wantTicketVal)
	if headers["Cookie"] != expectedCookie {
		t.Errorf("Cookie header = %q, want %q", headers["Cookie"], expectedCookie)
	}

	if headers["CSRFPreventionToken"] != wantCSRF {
		t.Errorf("CSRFPreventionToken = %q, want %q", headers["CSRFPreventionToken"], wantCSRF)
	}

	logoutErr := ticketAuth.Logout()
	if logoutErr != nil {
		t.Fatalf("Logout() error = %v", logoutErr)
	}

	if ticketAuth.IsAuthenticated() {
		t.Error("expected IsAuthenticated() == false after Logout")
	}

	if ticketAuth.GetTicket() != nil {
		t.Error("expected GetTicket() == nil after Logout")
	}

	if ticketAuth.GetHeaders() != nil {
		t.Error("expected GetHeaders() == nil after Logout")
	}
}

// TestTicketAuthenticator_Logout_WithoutLogin verifies that Logout is a no-op
// when no ticket has been acquired.
func TestTicketAuthenticator_Logout_WithoutLogin(t *testing.T) {
	t.Parallel()

	requestCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, httpReq *http.Request) {
		requestCount++

		http.NotFound(rw, httpReq)
	}))
	defer srv.Close()

	ticketAuth := newRootTicketAuthenticator(srv, "password")

	err := ticketAuth.Logout()
	if err != nil {
		t.Fatalf("Logout() without prior auth error = %v", err)
	}

	if requestCount != 0 {
		t.Errorf("expected 0 requests to server for logout without ticket, got %d", requestCount)
	}
}

// ---------------------------------------------------------------------------
// 5. Ticket refresh.
// ---------------------------------------------------------------------------

// TestTicketAuthenticator_Refresh_WhenNotAuthenticated verifies Refresh()
// authenticates when no ticket is present.
func TestTicketAuthenticator_Refresh_WhenNotAuthenticated(t *testing.T) {
	t.Parallel()

	loginCalls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, httpReq *http.Request) {
		if httpReq.Method == http.MethodPost && httpReq.URL.Path == pathAccessTicket {
			loginCalls++

			writeFullTicket(rw, okFullTicket("FRESH-TICKET", "FRESH-CSRF", testUserRootPAM))

			return
		}

		http.NotFound(rw, httpReq)
	}))
	defer srv.Close()

	ticketAuth := newRootTicketAuthenticator(srv, "password")

	err := ticketAuth.Refresh()
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}

	if !ticketAuth.IsAuthenticated() {
		t.Error("expected IsAuthenticated() == true after Refresh on empty state")
	}

	if loginCalls != 1 {
		t.Errorf("expected 1 login call, got %d", loginCalls)
	}
}

// TestTicketAuthenticator_Refresh_WhenAlreadyAuthenticated verifies Refresh()
// is a no-op when the ticket is still valid.
func TestTicketAuthenticator_Refresh_WhenAlreadyAuthenticated(t *testing.T) {
	t.Parallel()

	loginCalls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, httpReq *http.Request) {
		if httpReq.Method == http.MethodPost && httpReq.URL.Path == pathAccessTicket {
			loginCalls++

			writeFullTicket(rw, okFullTicket("VALID-TICKET", "CSRF", testUserRootPAM))

			return
		}

		http.NotFound(rw, httpReq)
	}))
	defer srv.Close()

	ticketAuth := newRootTicketAuthenticator(srv, "password")

	err := ticketAuth.Authenticate()
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}

	refreshErr := ticketAuth.Refresh()
	if refreshErr != nil {
		t.Fatalf("Refresh() error = %v", refreshErr)
	}

	if loginCalls != 1 {
		t.Errorf("expected 1 login call (no re-auth), got %d", loginCalls)
	}
}

// TestTicketAuthenticator_RefreshForce_RenewsEvenWhenValid verifies that
// RefreshForce() always contacts the server regardless of current ticket state.
func TestTicketAuthenticator_RefreshForce_RenewsEvenWhenValid(t *testing.T) {
	t.Parallel()

	loginCalls := 0

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, httpReq *http.Request) {
		if httpReq.Method == http.MethodPost && httpReq.URL.Path == pathAccessTicket {
			loginCalls++
			writeFullTicket(rw, okFullTicket(fmt.Sprintf("TICKET-%d", loginCalls), "CSRF", testUserRootPAM))

			return
		}

		http.NotFound(rw, httpReq)
	}))
	defer srv.Close()

	ticketAuth := newRootTicketAuthenticator(srv, "password")

	err := ticketAuth.Authenticate()
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}

	ticketAfterAuth := ticketAuth.GetTicket()

	forceErr := ticketAuth.RefreshForce()
	if forceErr != nil {
		t.Fatalf("RefreshForce() error = %v", forceErr)
	}

	if loginCalls != 2 {
		t.Errorf("expected 2 login calls (initial + forced), got %d", loginCalls)
	}

	ticketAfterForce := ticketAuth.GetTicket()
	if ticketAfterForce == nil {
		t.Fatal("GetTicket() nil after RefreshForce")
	}

	if ticketAfterAuth.Value == ticketAfterForce.Value {
		t.Error("expected ticket to change after RefreshForce, but it did not")
	}
}

// TestTicketAuthenticator_RefreshForce_TFAChallenge verifies that RefreshForce
// surfaces a TFARequiredError when the server demands TFA again.
func TestTicketAuthenticator_RefreshForce_TFAChallenge(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, httpReq *http.Request) {
		if httpReq.Method == http.MethodPost && httpReq.URL.Path == pathAccessTicket {
			var body tfaLoginBody

			body.Data.NeedTFA = true
			body.Data.Ticket2 = testPartialTicket
			body.Success = 1
			writeTFALogin(rw, body)

			return
		}

		http.NotFound(rw, httpReq)
	}))
	defer srv.Close()

	ticketAuth := newRootTicketAuthenticator(srv, "password")

	err := ticketAuth.RefreshForce()
	if err == nil {
		t.Fatal("expected TFARequiredError from RefreshForce, got nil")
	}

	var tfaErr *apierrors.TFARequiredError
	if !errors.As(err, &tfaErr) {
		t.Errorf("expected *apierrors.TFARequiredError, got %T: %v", err, err)
	}
}

// ---------------------------------------------------------------------------
// 6. Authenticate error paths.
// ---------------------------------------------------------------------------

// TestAuthenticate_LoginFailedNoTicket verifies that a 200 OK with no ticket
// results in an error from Authenticate().
func TestAuthenticate_LoginFailedNoTicket(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, httpReq *http.Request) {
		if httpReq.Method == http.MethodPost && httpReq.URL.Path == pathAccessTicket {
			// 200 OK but no ticket in data — triggers ErrLoginFailedNoTicket.
			writeTFAComplete(rw, tfaCompleteBody{Success: 1})

			return
		}

		http.NotFound(rw, httpReq)
	}))
	defer srv.Close()

	ticketAuth := newRootTicketAuthenticator(srv, "wrong-password")

	err := ticketAuth.Authenticate()
	if err == nil {
		t.Fatal("expected error when no ticket in response, got nil")
	}

	if !errors.Is(err, auth.ErrAuthenticationFailedNoTicket) && !errors.Is(err, auth.ErrLoginFailedNoTicket) {
		t.Errorf("error = %v, want ErrAuthenticationFailedNoTicket or ErrLoginFailedNoTicket", err)
	}
}

// TestAuthenticate_NetworkFailure verifies that a connection error surfaces
// as *apierrors.ConnectionError.
func TestAuthenticate_NetworkFailure(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {}))
	srvURL := srv.URL
	srv.Close()

	baseURL := buildBaseURL(srvURL)
	creds := &auth.Credentials{Username: testUserRoot, Password: "password", Realm: testRealm}
	ticketAuth := auth.NewTicketAuthenticator(baseURL, creds, &http.Client{}, "", false)

	err := ticketAuth.Authenticate()
	if err == nil {
		t.Fatal("expected network error, got nil")
	}

	var connErr *apierrors.ConnectionError
	if !errors.As(err, &connErr) {
		t.Errorf("expected *apierrors.ConnectionError, got %T: %v", err, err)
	}
}

// TestAuthenticate_HTTP401 verifies that a 401 Unauthorized from the server
// results in an error (non-success).
func TestAuthenticate_HTTP401(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, httpReq *http.Request) {
		if httpReq.Method == http.MethodPost && httpReq.URL.Path == pathAccessTicket {
			rw.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(rw, `{"message":"authentication failure","errors":{}}`)

			return
		}

		http.NotFound(rw, httpReq)
	}))
	defer srv.Close()

	ticketAuth := newRootTicketAuthenticator(srv, "wrong")

	err := ticketAuth.Authenticate()
	if err == nil {
		t.Fatal("expected error for 401 response, got nil")
	}
}

// ---------------------------------------------------------------------------
// 7. API token authenticator — positive paths.
// ---------------------------------------------------------------------------

// TestAPITokenAuthenticator_GetHeaders_PVEFormat verifies the Authorization
// header uses the PVEAPIToken=user@realm!tokenid=secret format.
func TestAPITokenAuthenticator_GetHeaders_PVEFormat(t *testing.T) {
	t.Parallel()

	tok := &auth.Token{ID: "alice@pve!myjob", Secret: "s3cr3t"}
	ata := auth.NewAPITokenAuthenticator(tok, "")

	headers := ata.GetHeaders()
	if headers == nil {
		t.Fatal("GetHeaders() returned nil for valid token")
	}

	want := "PVEAPIToken=alice@pve!myjob=s3cr3t"
	if headers["Authorization"] != want {
		t.Errorf("Authorization = %q, want %q", headers["Authorization"], want)
	}
}

// TestAPITokenAuthenticator_Refresh_Noop verifies Refresh is always nil error.
func TestAPITokenAuthenticator_Refresh_Noop(t *testing.T) {
	t.Parallel()

	ata := auth.NewAPITokenAuthenticator(&auth.Token{ID: "u@r!t", Secret: "s"}, "")

	err := ata.Refresh()
	if err != nil {
		t.Errorf("Refresh() = %v, want nil", err)
	}
}

// TestAPITokenAuthenticator_Logout_Noop verifies Logout is always nil error.
func TestAPITokenAuthenticator_Logout_Noop(t *testing.T) {
	t.Parallel()

	ata := auth.NewAPITokenAuthenticator(&auth.Token{ID: "u@r!t", Secret: "s"}, "")

	err := ata.Logout()
	if err != nil {
		t.Errorf("Logout() = %v, want nil", err)
	}
}

// TestAPITokenAuthenticator_SetToken verifies SetToken replaces the stored token.
func TestAPITokenAuthenticator_SetToken(t *testing.T) {
	t.Parallel()

	tok1 := &auth.Token{ID: "u@r!t1", Secret: "s1"}
	tok2 := &auth.Token{ID: "u@r!t2", Secret: "s2"}

	ata := auth.NewAPITokenAuthenticator(tok1, "")
	ata.SetToken(tok2)

	got := ata.GetToken()
	if got != tok2 {
		t.Errorf("GetToken() after SetToken = %v, want tok2", got)
	}
}

// TestAPITokenAuthenticator_FromString_Valid verifies that NewAPITokenAuthenticatorFromString
// produces a working authenticator with correct headers.
func TestAPITokenAuthenticator_FromString_Valid(t *testing.T) {
	t.Parallel()

	ata, err := auth.NewAPITokenAuthenticatorFromString("bob@pam!token=my-secret")
	if err != nil {
		t.Fatalf("NewAPITokenAuthenticatorFromString() error = %v", err)
	}

	if !ata.IsAuthenticated() {
		t.Error("expected IsAuthenticated() == true for valid token string")
	}

	headers := ata.GetHeaders()

	want := "PVEAPIToken=bob@pam!token=my-secret"
	if headers["Authorization"] != want {
		t.Errorf("Authorization = %q, want %q", headers["Authorization"], want)
	}
}

// ---------------------------------------------------------------------------
// 8. InvalidAuthenticator coverage.
// ---------------------------------------------------------------------------

// TestInvalidAuthenticator_AllMethods verifies every method of InvalidAuthenticator.
func TestInvalidAuthenticator_AllMethods(t *testing.T) {
	t.Parallel()

	invalidAuth := auth.NewInvalidAuthenticator(errInvalidTokenConfig)

	err := invalidAuth.Authenticate()
	if !errors.Is(err, errInvalidTokenConfig) {
		t.Errorf("Authenticate() = %v, want sentinel error", err)
	}

	if invalidAuth.IsAuthenticated() {
		t.Error("IsAuthenticated() = true, want false")
	}

	if invalidAuth.GetHeaders() != nil {
		t.Error("GetHeaders() != nil, want nil")
	}

	refreshErr := invalidAuth.Refresh()
	if !errors.Is(refreshErr, errInvalidTokenConfig) {
		t.Errorf("Refresh() = %v, want sentinel error", refreshErr)
	}

	logoutErr := invalidAuth.Logout()
	if logoutErr != nil {
		t.Errorf("Logout() = %v, want nil", logoutErr)
	}
}

// ---------------------------------------------------------------------------
// 9. AutoTFAHandler coverage.
// ---------------------------------------------------------------------------

// TestAutoTFAHandler_MatchesFirstAvailableType verifies that HandleTFAChallenge
// returns the response for the first matching type in the challenge types list.
func TestAutoTFAHandler_MatchesFirstAvailableType(t *testing.T) {
	t.Parallel()

	handler := auth.NewAutoTFAHandler(map[auth.TFAType]string{
		auth.TFATypeTOTP:     "654321",
		auth.TFATypeRecovery: "recovery-code",
	})

	challenge := &auth.TFAChallenge{
		Types: []string{string(auth.TFATypeRecovery), string(auth.TFATypeTOTP)},
	}

	resp, err := handler.HandleTFAChallenge(challenge)
	if err != nil {
		t.Fatalf("HandleTFAChallenge() error = %v", err)
	}

	if resp.Response != "recovery-code" {
		t.Errorf("Response = %q, want %q", resp.Response, "recovery-code")
	}

	if resp.Type != string(auth.TFATypeRecovery) {
		t.Errorf("Type = %q, want %q", resp.Type, auth.TFATypeRecovery)
	}
}

// TestAutoTFAHandler_EmptyTypes_FallsBackToTOTP verifies that an empty
// challenge types list falls back to the TOTP entry if present.
func TestAutoTFAHandler_EmptyTypes_FallsBackToTOTP(t *testing.T) {
	t.Parallel()

	handler := auth.NewAutoTFAHandler(map[auth.TFAType]string{
		auth.TFATypeTOTP: "111111",
	})

	challenge := &auth.TFAChallenge{Types: []string{}}

	resp, err := handler.HandleTFAChallenge(challenge)
	if err != nil {
		t.Fatalf("HandleTFAChallenge() error = %v", err)
	}

	if resp.Response != "111111" {
		t.Errorf("Response = %q, want %q", resp.Response, "111111")
	}
}

// TestAutoTFAHandler_NoMatchReturnsError verifies error when no handler covers
// the challenge types.
func TestAutoTFAHandler_NoMatchReturnsError(t *testing.T) {
	t.Parallel()

	handler := auth.NewAutoTFAHandler(map[auth.TFAType]string{
		auth.TFATypeTOTP: "111111",
	})

	challenge := &auth.TFAChallenge{Types: []string{string(auth.TFATypeYubico)}}

	_, err := handler.HandleTFAChallenge(challenge)
	if err == nil {
		t.Fatal("expected error when no matching TFA type, got nil")
	}

	if !errors.Is(err, auth.ErrNoTFAResponseConfigured) {
		t.Errorf("error = %v, want ErrNoTFAResponseConfigured", err)
	}
}

// ---------------------------------------------------------------------------
// 10. Ticket.GetHeaders (Ticket method, not TicketAuthenticator).
// ---------------------------------------------------------------------------

// TestTicket_GetHeaders verifies the standalone Ticket.GetHeaders method.
func TestTicket_GetHeaders(t *testing.T) {
	t.Parallel()

	t.Run("full ticket", func(t *testing.T) {
		t.Parallel()

		tkt := &auth.Ticket{
			Value:      "PVE:root@pam:AABBCCDD::sig",
			CSRFToken:  testCSRFValue,
			Username:   testUserRootPAM,
			ValidUntil: time.Now().Add(time.Hour),
		}

		headers := tkt.GetHeaders()
		if headers["Cookie"] != "PVEAuthCookie=PVE:root@pam:AABBCCDD::sig" {
			t.Errorf("Cookie = %q", headers["Cookie"])
		}

		if headers["CSRFPreventionToken"] != testCSRFValue {
			t.Errorf("CSRFPreventionToken = %q", headers["CSRFPreventionToken"])
		}
	})

	t.Run("empty value no cookie header", func(t *testing.T) {
		t.Parallel()

		tkt := &auth.Ticket{
			Value:      "",
			CSRFToken:  testCSRFValue,
			ValidUntil: time.Now().Add(time.Hour),
		}

		headers := tkt.GetHeaders()
		if _, ok := headers["Cookie"]; ok {
			t.Error("expected no Cookie header for empty ticket value")
		}
	})

	t.Run("no csrf token", func(t *testing.T) {
		t.Parallel()

		tkt := &auth.Ticket{
			Value:      "ticket-val",
			CSRFToken:  "",
			ValidUntil: time.Now().Add(time.Hour),
		}

		headers := tkt.GetHeaders()
		if _, ok := headers["CSRFPreventionToken"]; ok {
			t.Error("expected no CSRFPreventionToken header for empty CSRF token")
		}
	})
}

// ---------------------------------------------------------------------------
// 11. prepareLoginData OTP + ticket-as-password paths.
// ---------------------------------------------------------------------------

// TestAuthenticate_OTPIncludedInLogin verifies that Credentials.OTP is forwarded
// in the login request form body.
func TestAuthenticate_OTPIncludedInLogin(t *testing.T) {
	t.Parallel()

	var receivedOTP string

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, httpReq *http.Request) {
		if httpReq.Method == http.MethodPost && httpReq.URL.Path == pathAccessTicket {
			_ = httpReq.ParseForm()
			receivedOTP = httpReq.FormValue("otp")

			writeFullTicket(rw, okFullTicket("TICKET", "CSRF", testUserRootPAM))

			return
		}

		http.NotFound(rw, httpReq)
	}))
	defer srv.Close()

	baseURL := buildBaseURL(srv.URL)
	creds := &auth.Credentials{Username: testUserRoot, Password: testSecretPass, Realm: testRealm, OTP: "otp-code"}
	ticketAuth := auth.NewTicketAuthenticator(baseURL, creds, srv.Client(), "", false)

	err := ticketAuth.Authenticate()
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}

	if receivedOTP != "otp-code" {
		t.Errorf("otp in request = %q, want %q", receivedOTP, "otp-code")
	}
}

// TestAuthenticate_TicketUsedAsPassword verifies that when Credentials.Password
// is empty and a ticket is already stored, the ticket value is sent as the
// password in the login request (PVE renewal mechanism).
func TestAuthenticate_TicketUsedAsPassword(t *testing.T) {
	t.Parallel()

	var receivedPassword string

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, httpReq *http.Request) {
		if httpReq.Method == http.MethodPost && httpReq.URL.Path == pathAccessTicket {
			_ = httpReq.ParseForm()
			receivedPassword = httpReq.FormValue("password")

			writeFullTicket(rw, okFullTicket("RENEWED-TICKET", "CSRF", testUserRootPAM))

			return
		}

		http.NotFound(rw, httpReq)
	}))
	defer srv.Close()

	baseURL := buildBaseURL(srv.URL)
	// Empty password — renewal should send the existing ticket as password.
	creds := &auth.Credentials{Username: testUserRoot, Password: "", Realm: testRealm}
	ticketAuth := auth.NewTicketAuthenticator(baseURL, creds, srv.Client(), "", false)

	existingTicket := &auth.Ticket{
		Value:      "EXISTING-TICKET",
		CSRFToken:  "OLD-CSRF",
		Username:   testUserRootPAM,
		ValidUntil: time.Now().Add(time.Hour),
	}
	ticketAuth.SetTicket(existingTicket)

	err := ticketAuth.RefreshForce()
	if err != nil {
		t.Fatalf("RefreshForce() error = %v", err)
	}

	if receivedPassword != "EXISTING-TICKET" {
		t.Errorf("password sent = %q, want %q", receivedPassword, "EXISTING-TICKET")
	}
}

// ---------------------------------------------------------------------------
// 12. NewTicketAuthenticator default realm.
// ---------------------------------------------------------------------------

// TestNewTicketAuthenticator_DefaultRealm verifies that an empty Realm in
// Credentials is defaulted to "pam".
func TestNewTicketAuthenticator_DefaultRealm(t *testing.T) {
	t.Parallel()

	var receivedUsername string

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, httpReq *http.Request) {
		if httpReq.Method == http.MethodPost && httpReq.URL.Path == pathAccessTicket {
			_ = httpReq.ParseForm()
			receivedUsername = httpReq.FormValue("username")

			writeFullTicket(rw, okFullTicket("TICKET", "CSRF", testUserRootPAM))

			return
		}

		http.NotFound(rw, httpReq)
	}))
	defer srv.Close()

	baseURL := buildBaseURL(srv.URL)
	// Realm intentionally empty — must default to "pam".
	creds := &auth.Credentials{Username: testUserRoot, Password: testSecretPass, Realm: ""}
	ticketAuth := auth.NewTicketAuthenticator(baseURL, creds, srv.Client(), "", false)

	err := ticketAuth.Authenticate()
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}

	wantUser := testUserRoot + "@" + testRealm
	if receivedUsername != wantUser {
		t.Errorf("username in request = %q, want %q", receivedUsername, wantUser)
	}
}

// ---------------------------------------------------------------------------
// 13. Ticket.IsValid edge cases.
// ---------------------------------------------------------------------------

// TestTicket_IsValid verifies the IsValid predicate under all edge cases.
func TestTicket_IsValid(t *testing.T) {
	t.Parallel()

	t.Run("valid ticket", func(t *testing.T) {
		t.Parallel()

		tkt := &auth.Ticket{Value: "v", ValidUntil: time.Now().Add(time.Hour)}
		if !tkt.IsValid() {
			t.Error("expected IsValid() == true")
		}
	})

	t.Run("expired ticket", func(t *testing.T) {
		t.Parallel()

		tkt := &auth.Ticket{Value: "v", ValidUntil: time.Now().Add(-time.Second)}
		if tkt.IsValid() {
			t.Error("expected IsValid() == false for expired ticket")
		}
	})

	t.Run("empty value", func(t *testing.T) {
		t.Parallel()

		tkt := &auth.Ticket{Value: "", ValidUntil: time.Now().Add(time.Hour)}
		if tkt.IsValid() {
			t.Error("expected IsValid() == false for empty value")
		}
	})
}

// ---------------------------------------------------------------------------
// 14. firstNonEmpty (internal helper — exercised via NewTicketAuthenticator).
// ---------------------------------------------------------------------------

// TestNewTicketAuthenticator_CookieNameFallback verifies firstNonEmpty:
// empty cookieName → defaults to "PVEAuthCookie".
func TestNewTicketAuthenticator_CookieNameFallback(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, httpReq *http.Request) {
		if httpReq.Method == http.MethodPost && httpReq.URL.Path == pathAccessTicket {
			writeFullTicket(rw, okFullTicket("TICKET", "CSRF", testUserRootPAM))

			return
		}

		http.NotFound(rw, httpReq)
	}))
	defer srv.Close()

	baseURL := buildBaseURL(srv.URL)
	creds := &auth.Credentials{Username: testUserRoot, Password: testSecretPass, Realm: testRealm}
	// Empty cookieName — should default to "PVEAuthCookie".
	ticketAuth := auth.NewTicketAuthenticator(baseURL, creds, srv.Client(), "", false)

	err := ticketAuth.Authenticate()
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}

	headers := ticketAuth.GetHeaders()
	if !strings.HasPrefix(headers["Cookie"], "PVEAuthCookie=") {
		t.Errorf("Cookie = %q, want prefix %q", headers["Cookie"], "PVEAuthCookie=")
	}
}
