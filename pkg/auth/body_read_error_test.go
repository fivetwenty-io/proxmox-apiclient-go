package auth_test

import (
	"net/http"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect/redirecttest"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/auth"
)

// partialLogin is the start of a login reply, so the decoder reads on into the
// trailer that follows it.
const partialLogin = `{"data":{"ticket":`

// TestLoginBodyReadErrorLeavesNoServerText answers the ticket login with a
// reply whose trailer line the transport cannot read, after a body the decoder
// has not finished.
func TestLoginBodyReadErrorLeavesNoServerText(t *testing.T) {
	t.Parallel()

	server := redirecttest.ServeRaw(t, redirecttest.BadTrailer(partialLogin))
	creds := &auth.Credentials{Username: testUserRoot, Password: testSecretPass, Realm: testRealm}
	authenticator := auth.NewTicketAuthenticator(buildBaseURL(server.URL()), creds, &http.Client{}, "", false)

	err := authenticator.Authenticate()
	if err == nil {
		t.Fatal("got no error, want the body read to fail")
	}

	redirecttest.AssertNoTrailerText(t, err.Error())
}

// TestTFABodyReadErrorLeavesNoServerText does the same for the TFA request.
func TestTFABodyReadErrorLeavesNoServerText(t *testing.T) {
	t.Parallel()

	server := redirecttest.ServeRaw(t, redirecttest.BadTrailer(partialLogin))
	creds := &auth.Credentials{Username: testUserRoot, Password: testSecretPass, Realm: testRealm}
	authenticator := auth.NewTicketAuthenticator(buildBaseURL(server.URL()), creds, &http.Client{}, "", false)

	_, err := authenticator.CompleteTFA(
		&auth.TFAChallenge{Ticket: "PVE:root@pam:0::partial"}, &auth.TFAResponse{Type: "totp", Response: "123456"},
	)
	if err == nil {
		t.Fatal("got no error, want the body read to fail")
	}

	redirecttest.AssertNoTrailerText(t, err.Error())
}
