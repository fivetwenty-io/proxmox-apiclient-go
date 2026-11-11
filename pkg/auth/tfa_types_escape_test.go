package auth_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/auth"
)

// hostileTFATypes is a login reply with a success status that asks for a second
// factor and lists a type holding an escape sequence and a direction override,
// and another type of 200,000 bytes.
func hostileTFATypes() string {
	return `{"data":{"NeedTFA":true,"ticket2":"t","tfa-types":["totp\u001b[2J\u202eEVIL","` +
		strings.Repeat("A", 200_000) + `"]}}`
}

// TestTFATypesFromASuccessfulLoginAreEscaped checks the error Authenticate
// returns for the reply for the raw characters and for its length.
func TestTFATypesFromASuccessfulLoginAreEscaped(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(hostileTFATypes()))
	}))
	t.Cleanup(server.Close)

	creds := &auth.Credentials{Username: testUserRoot, Password: testSecretPass, Realm: testRealm}
	authenticator := auth.NewTicketAuthenticator(buildBaseURL(server.URL), creds, server.Client(), "", false)

	for name, call := range map[string]func() error{
		"authenticate":  authenticator.Authenticate,
		"refresh force": authenticator.RefreshForce,
	} {
		err := call()
		if err == nil {
			t.Fatalf("%s: got no error, want a TFA request", name)
		}

		text := err.Error()
		for _, raw := range []string{"\u001b", "\u202e"} {
			if strings.Contains(text, raw) {
				t.Errorf("%s: %.80q holds the raw character %q", name, text, raw)
			}
		}

		if !strings.Contains(text, `totp\x1b[2J\u202eEVIL`) {
			t.Errorf("%s: %.120q does not show the escaped type", name, text)
		}

		if len(text) > 8192 {
			t.Errorf("%s: error is %d bytes, want at most 8192", name, len(text))
		}
	}
}
