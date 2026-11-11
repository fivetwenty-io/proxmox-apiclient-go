package http //nolint:testpackage // white-box test: reaches the client's retry setting through the shared helpers

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect/redirecttest"
)

// TestFollowedRedirectToAnUnsupportedSchemeIsAttemptedOnce follows a redirect
// to a scheme the transport cannot speak. The failure is the same on every
// attempt, so the client sends the first request once and the second never
// leaves the process.
func TestFollowedRedirectToAnUnsupportedSchemeIsAttemptedOnce(t *testing.T) {
	t.Parallel()

	location := []byte("ftp://user:" + redirecttest.SecretUser + "@h/x?ticket=" + redirecttest.SecretQuery +
		"#" + redirecttest.SecretFragment)

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			t.Parallel()

			server := redirecttest.Serve(t, http.StatusFound, location)
			client, captured := newHostileClient(t, server, func(o *Options) {
				o.CheckRedirect = redirect.Follow(3)
			}, false)

			ctx := WithForceRetry(context.Background(), true)

			_, err := client.DoWithContext(ctx, method, "/version", nil)
			redirecttest.AssertClean(t, err, captured.all()...)

			if !IsPermanent(err) {
				t.Errorf("error = %v, want it marked permanent", err)
			}

			if !strings.Contains(err.Error(), `unsupported protocol scheme "ftp"`) {
				t.Errorf("error = %v, want the unsupported scheme named", err)
			}

			if got := server.Hits(); got != 1 {
				t.Errorf("server read %d requests, want 1", got)
			}
		})
	}
}
