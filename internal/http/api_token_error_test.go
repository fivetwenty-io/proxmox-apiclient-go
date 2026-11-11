package http //nolint:testpackage // white-box test: reads the authenticator the client built

import (
	"strings"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect/redirecttest"
)

// TestMalformedAPITokenErrorLeavesTheTokenOut builds a client with an API token
// that does not parse. The authenticator that stands in for it fails every
// request, and the text of that failure, which callers log, must not repeat the
// token or any part of it.
func TestMalformedAPITokenErrorLeavesTheTokenOut(t *testing.T) {
	t.Parallel()

	const secret = "TOKENSECRET0123456789"

	for name, token := range map[string]string{
		"no separator":  secret,
		"bad id":        "plain-id=" + secret,
		"empty secret":  "root@pam!ci=",
		"colon form":    "plain-id:" + secret,
		"only a secret": "=" + secret,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			opts := minimalHTTPOptions()
			opts.APIToken = token

			client, err := NewClient(opts)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}

			err = client.authenticator.Authenticate()
			if err == nil {
				t.Fatal("got no error, want the malformed token refused")
			}

			for _, text := range redirecttest.Render(err) {
				if strings.Contains(text, secret) {
					t.Errorf("error %q holds the token", text)
				}
			}

			if !strings.Contains(err.Error(), "invalid APIToken") {
				t.Errorf("error = %v, want it to say the token is invalid", err)
			}
		})
	}
}
