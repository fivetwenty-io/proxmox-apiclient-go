package auth_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/auth"
)

func TestTicketAuthenticator_NewFormatAndCookieName(t *testing.T) {
	t.Parallel()

	// Mock API server
	var sawNewFormat bool

	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.URL.Path, "/api2/json") {
			http.NotFound(writer, request)

			return
		}

		switch request.URL.Path {
		case pathAccessTicket:
			err := request.ParseForm()
			if err == nil {
				if request.Form.Get("new-format") == "1" {
					sawNewFormat = true
				}
			}

			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"data":{"ticket":"TICKET","CSRFPreventionToken":"CSRF","username":"root@pam"},"success":1}`)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	baseURL := u.Scheme + "://" + u.Host + "/api2/json"

	httpClient := srv.Client()
	creds := &auth.Credentials{Username: testUserRoot, Password: testSecretPass, Realm: testRealm}
	ticketAuth := auth.NewTicketAuthenticator(baseURL, creds, httpClient, "CustomCookie", true)

	err := ticketAuth.Authenticate()
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}

	if !sawNewFormat {
		t.Fatalf("expected new-format=1 to be sent in login request")
	}

	headers := ticketAuth.GetHeaders()
	if got := headers["Cookie"]; !strings.HasPrefix(got, "CustomCookie=") {
		t.Fatalf("expected Cookie header to use custom name, got %q", got)
	}

	if headers["CSRFPreventionToken"] != "CSRF" {
		t.Fatalf("expected CSRFPreventionToken header to be set")
	}
}

// ---------------------------------------------------------------------------
// NewTicketAuthenticatorFromTicket
// ---------------------------------------------------------------------------

// TestNewTicketAuthenticatorFromTicket_ParsesEmbeddedTimestamp verifies that
// when the ticket value encodes a valid PVE timestamp, validity is anchored
// to that timestamp (not to now). A ticket seeded with an old timestamp is
// therefore already expired.
func TestNewTicketAuthenticatorFromTicket_ParsesEmbeddedTimestamp(t *testing.T) {
	t.Parallel()

	// Hex representation of 1705314424 (2024-01-15 10:27:04 UTC) — far in the
	// past relative to the 2-hour ticket validity window.
	const oldTicketValue = "PVE:root@pam:659F8E78::signature-data-here"

	ticketAuth := auth.NewTicketAuthenticatorFromTicket(
		"https://pve.example.com/api2/json",
		oldTicketValue,
		"csrf-token",
		testUserRootPAM,
		&auth.Credentials{Username: testUserRoot, Password: testSecretPass, Realm: testRealm},
		&http.Client{},
		"",
		false,
	)

	ticket := ticketAuth.GetTicket()
	if ticket == nil {
		t.Fatal("GetTicket() = nil, want seeded ticket")
	}

	if ticket.Value != oldTicketValue {
		t.Errorf("Ticket.Value = %q, want %q", ticket.Value, oldTicketValue)
	}

	if ticket.CSRFToken != "csrf-token" {
		t.Errorf("Ticket.CSRFToken = %q, want %q", ticket.CSRFToken, "csrf-token")
	}

	if ticket.Username != testUserRootPAM {
		t.Errorf("Ticket.Username = %q, want %q", ticket.Username, testUserRootPAM)
	}

	if !ticket.ValidUntil.Before(time.Now()) {
		t.Errorf("ValidUntil = %v, want a time before now (anchored to the embedded 2024 timestamp)", ticket.ValidUntil)
	}

	if ticketAuth.IsAuthenticated() {
		t.Error("expected IsAuthenticated() == false for a ticket expired via its embedded timestamp")
	}
}

// TestNewTicketAuthenticatorFromTicket_UnparsableTimestamp_AnchorsToNow verifies
// that when the ticket value does not encode a parseable PVE timestamp,
// validity falls back to now + the ticket validity window, so the seeded
// ticket is immediately usable.
func TestNewTicketAuthenticatorFromTicket_UnparsableTimestamp_AnchorsToNow(t *testing.T) {
	t.Parallel()

	const opaqueTicketValue = "opaque-ticket-value-without-embedded-timestamp"

	ticketAuth := auth.NewTicketAuthenticatorFromTicket(
		"https://pve.example.com/api2/json",
		opaqueTicketValue,
		"csrf-token",
		testUserRootPAM,
		nil,
		&http.Client{},
		"",
		false,
	)

	if !ticketAuth.IsAuthenticated() {
		t.Error("expected IsAuthenticated() == true when validity falls back to now + validity window")
	}

	ticket := ticketAuth.GetTicket()
	if ticket == nil || !ticket.ValidUntil.After(time.Now()) {
		t.Errorf("ValidUntil = %v, want a time after now (anchored to now on unparsable timestamp)", ticket.ValidUntil)
	}
}

// TestNewTicketAuthenticatorFromTicket_EmptyValue_NoTicketSeeded verifies that
// an empty ticket value leaves the authenticator without a ticket at all.
func TestNewTicketAuthenticatorFromTicket_EmptyValue_NoTicketSeeded(t *testing.T) {
	t.Parallel()

	ticketAuth := auth.NewTicketAuthenticatorFromTicket(
		"https://pve.example.com/api2/json",
		"",
		"",
		"",
		nil,
		&http.Client{},
		"",
		false,
	)

	if ticketAuth.GetTicket() != nil {
		t.Error("GetTicket() != nil, want nil for empty ticket value")
	}

	if ticketAuth.IsAuthenticated() {
		t.Error("IsAuthenticated() == true, want false for empty ticket value")
	}

	if ticketAuth.GetHeaders() != nil {
		t.Error("GetHeaders() != nil, want nil for empty ticket value")
	}
}

// TestNewTicketAuthenticatorFromTicket_NilCredentials_DefaultsRealmAndRenews
// verifies that passing nil credentials does not panic, defaults the realm to
// "pam", and that RefreshForce() renews using the seeded ticket as the
// ticket-as-password mechanism (since nil credentials carries no password).
func TestNewTicketAuthenticatorFromTicket_NilCredentials_DefaultsRealmAndRenews(t *testing.T) {
	t.Parallel()

	var receivedUsername, receivedPassword string

	srv := httptest.NewServer(http.HandlerFunc(func(respWriter http.ResponseWriter, httpReq *http.Request) {
		if httpReq.Method == http.MethodPost && httpReq.URL.Path == pathAccessTicket {
			_ = httpReq.ParseForm()
			receivedUsername = httpReq.FormValue("username")
			receivedPassword = httpReq.FormValue("password")

			writeFullTicket(respWriter, okFullTicket("RENEWED-TICKET", "RENEWED-CSRF", testUserRootPAM))

			return
		}

		http.NotFound(respWriter, httpReq)
	}))
	defer srv.Close()

	ticketAuth := auth.NewTicketAuthenticatorFromTicket(
		buildBaseURL(srv.URL),
		"SEEDED-TICKET-VALUE",
		"seeded-csrf",
		testUserRootPAM,
		nil, // nil credentials must default (not panic) rather than dereference nil
		srv.Client(),
		"",
		false,
	)

	err := ticketAuth.RefreshForce()
	if err != nil {
		t.Fatalf("RefreshForce() error = %v", err)
	}

	if receivedUsername != "@"+testRealm {
		t.Errorf("username sent = %q, want %q (nil credentials must default realm to %q)",
			receivedUsername, "@"+testRealm, testRealm)
	}

	if receivedPassword != "SEEDED-TICKET-VALUE" {
		t.Errorf("password sent = %q, want the seeded ticket value used as the renewal password", receivedPassword)
	}

	if ticketAuth.GetTicket() == nil || ticketAuth.GetTicket().Value != "RENEWED-TICKET" {
		t.Errorf("GetTicket() = %+v, want renewed ticket value %q", ticketAuth.GetTicket(), "RENEWED-TICKET")
	}
}
