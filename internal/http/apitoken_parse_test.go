package http //nolint:testpackage // white-box tests: must access unexported createAuthenticator

import (
	"strings"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/auth"
)

// TestCreateAuthenticator_APIToken_MalformedReturnsInvalidAuthenticator verifies
// that a malformed APIToken string causes createAuthenticator to return an
// InvalidAuthenticator that fails on Authenticate(), rather than silently
// setting both Token.ID and Token.Secret to the raw unsplit string.
func TestCreateAuthenticator_APIToken_MalformedReturnsInvalidAuthenticator(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		token string
	}{
		{"no equals sign", "root@pam!mytoken"},
		{"empty secret", "root@pam!mytoken="},
		{"missing realm separator", "rootpammytoken=secret"},
		{"missing tokenid separator", "root@pam=secret"},
		// empty string is NOT a malformed token — it means no token was provided,
		// so createAuthenticator returns nil (no authenticator). That case is
		// tested separately.
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			opts := &Options{
				Host:     testHostPVE,
				Port:     8006,
				Protocol: testProtoHTTPS,
				APIToken: testCase.token,
			}

			authn := createAuthenticator(opts, nil)
			if authn == nil {
				t.Fatal("createAuthenticator returned nil for malformed token")
			}

			err := authn.Authenticate()
			if err == nil {
				t.Errorf("Authenticate() expected error for malformed token %q, got nil", testCase.token)
			}
		})
	}
}

// TestCreateAuthenticator_APIToken_ValidParsedCorrectly verifies that a
// well-formed token string is split correctly into ID and Secret, and that
// the resulting Authorization header matches the Proxmox format.
func TestCreateAuthenticator_APIToken_ValidParsedCorrectly(t *testing.T) {
	t.Parallel()

	opts := &Options{
		Host:     testHostPVE,
		Port:     8006,
		Protocol: "https",
		APIToken: testAPITokenFull,
	}

	authn := createAuthenticator(opts, nil)
	if authn == nil {
		t.Fatal("createAuthenticator returned nil")
	}

	// Must be an APITokenAuthenticator, not InvalidAuthenticator.
	ata, ok := authn.(*auth.APITokenAuthenticator)
	if !ok {
		t.Fatalf("expected *auth.APITokenAuthenticator, got %T", authn)
	}

	tok := ata.GetToken()
	if tok == nil {
		t.Fatal("GetToken() returned nil")
	}

	if tok.ID != "root@pam!mytoken" {
		t.Errorf("Token.ID = %q, want %q", tok.ID, "root@pam!mytoken")
	}

	if tok.Secret != "s3cr3t" {
		t.Errorf("Token.Secret = %q, want %q", tok.Secret, "s3cr3t")
	}

	headers := authn.GetHeaders()
	authHeader := headers["Authorization"]
	want := "PVEAPIToken=" + testAPITokenFull

	if authHeader != want {
		t.Errorf("Authorization = %q, want %q", authHeader, want)
	}

	// Old bug: both fields were the full raw string, so the header would
	// contain the token string duplicated.
	if strings.Count(authHeader, testAPITokenFull) > 1 {
		t.Errorf("Authorization header contains duplicated token string (old bug): %q", authHeader)
	}
}
