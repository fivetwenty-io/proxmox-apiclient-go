package http //nolint:testpackage // white-box test: reaches the client's retry setting through the shared helpers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect"
	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect/redirecttest"
)

// framingCases are responses net/http rejects while it parses them. Each puts
// the secret markers in the bytes net/http would quote, and names the kind of
// fault the error must still show.
func framingCases() map[string]struct {
	response string
	kind     string
} {
	quoted := "http://user:" + redirecttest.SecretUser + "@h/x?ticket=" + redirecttest.SecretQuery +
		"#" + redirecttest.SecretFragment

	return map[string]struct {
		response string
		kind     string
	}{
		"status code": {
			"HTTP/1.1 " + quoted + " Found\r\n\r\n",
			"malformed HTTP status code",
		},
		"status line": {
			quoted + "\r\n\r\n",
			"malformed HTTP response",
		},
		"version": {
			"HTTP/" + redirecttest.SecretUser + " 200 OK\r\n\r\n",
			"malformed HTTP version",
		},
		"content length": {
			"HTTP/1.1 200 OK\r\nContent-Length: " + quoted + "\r\n\r\n",
			"bad Content-Length",
		},
		"two content lengths": {
			"HTTP/1.1 200 OK\r\nContent-Length:  \r\nContent-Length: " + quoted + "\r\n\r\n",
			"message cannot contain multiple Content-Length headers",
		},
		"transfer encoding": {
			"HTTP/1.1 200 OK\r\nTransfer-Encoding: " + quoted + "\r\n\r\n",
			"unsupported transfer encoding",
		},
		"two transfer encodings": {
			"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nTransfer-Encoding: " + quoted + "\r\n\r\n",
			"too many transfer encodings",
		},
		"trailer key": {
			"HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\nTrailer: Content-Length\r\n\r\n0\r\n\r\n",
			"bad trailer key",
		},
	}
}

// TestFramingErrorsLeaveNoServerText sends each malformed response through the
// request path. The error must keep the kind of fault, hold none of the bytes
// the server sent, and match the retry behavior of any other transport fault,
// so a GET is attempted again because the repeat goes to the same host with
// the same credentials.
func TestFramingErrorsLeaveNoServerText(t *testing.T) {
	t.Parallel()

	for name, tc := range framingCases() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			server := redirecttest.ServeRaw(t, []byte(tc.response))
			client, captured := newHostileClient(t, server, func(*Options) {}, false)

			_, err := client.DoWithContext(context.Background(), http.MethodGet, "/version", nil)
			redirecttest.AssertClean(t, err, captured.all()...)

			if !strings.Contains(err.Error(), tc.kind) {
				t.Errorf("error = %v, want the kind %q kept", err, tc.kind)
			}

			var framing *redirect.FramingError
			if !errors.As(err, &framing) {
				t.Errorf("error = %v, want a *redirect.FramingError in the chain", err)
			}

			if IsPermanent(err) {
				t.Errorf("error = %v, want the retry behavior left as it was", err)
			}

			if got, want := server.Hits(), redirectTestRetry+1; got != want {
				t.Errorf("server read %d requests, want %d", got, want)
			}
		})
	}
}
