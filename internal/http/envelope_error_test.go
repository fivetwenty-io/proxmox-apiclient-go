package http //nolint:testpackage // white-box test: uses the package's test server helpers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	apierrors "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/errors"
)

// maxEscapedServerText is the cap on one text field, plus the room for the
// truncation marker the cap appends.
const bodyKeyMessage = "message"

const maxEscapedServerText = apierrors.MaxServerTextBytes + len("...(truncated)")

func hasRawServerByte(text string) bool {
	if !utf8.ValidString(text) {
		return true
	}

	for _, r := range text {
		if r == '\n' || r == '\t' {
			continue
		}

		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\u2028' || r == '\u2029' {
			return true
		}
	}

	return false
}

// TestSuccessStatusErrorEnvelopeIsEscapedAndCapped answers a request with a
// success status and an envelope that reports a failure, with hostile text in
// the message and the field errors, and checks the error the client returns
// holds none of it raw and keeps each field under the cap.
func TestSuccessStatusErrorEnvelopeIsEscapedAndCapped(t *testing.T) {
	t.Parallel()

	bodies := map[string]string{
		bodyKeyMessage: `{"message":"\u001b[2J\r\u202eFLIP\nsecond\tline","success":0}`,
		"field errors": `{"message":"m","errors":{"k\u001b":"\u001b]0;pwn\u0007\u202e"}}`,
		"huge message": `{"message":"` + strings.Repeat("x", 3<<20) + `","success":0}`,
		"huge field": `{"message":"m","errors":{"` + strings.Repeat("k", 5000) + `":"` +
			strings.Repeat("v", 5000) + `"}}`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(body))
			})
			client := newClientForServerURL(t, server.URL, func(o *Options) { o.APIToken = testAPITokenFull })

			_, err := client.DoWithContext(context.Background(), http.MethodGet, "/x", nil)

			var apiErr *apierrors.APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error = %v, want an *APIError", err)
			}

			if hasRawServerByte(err.Error()) {
				t.Errorf("error text %q holds a raw control or direction character", err.Error())
			}

			if len(apiErr.Message) > maxEscapedServerText {
				t.Errorf("Message is %d bytes, want at most %d", len(apiErr.Message), maxEscapedServerText)
			}

			for field, msg := range apiErr.Errors {
				if len(field) > maxEscapedServerText || len(msg) > maxEscapedServerText {
					t.Errorf("Errors entry is %d and %d bytes, want at most %d",
						len(field), len(msg), maxEscapedServerText)
				}
			}

			if len(err.Error()) > 4*maxEscapedServerText {
				t.Errorf("error text is %d bytes, want it bounded", len(err.Error()))
			}
		})
	}
}

// TestResponseParserEnvelopeErrorIsEscapedAndCapped checks the parser's own
// envelope error takes the same path.
func TestResponseParserEnvelopeErrorIsEscapedAndCapped(t *testing.T) {
	t.Parallel()

	body := []byte(`{"message":"\u001b[2J\u202eFLIP` + strings.Repeat("x", 4000) +
		`","errors":{"k\u0007":"v\r"}}`)

	var target interface{}

	err := NewResponseParser().parseJSON(body, &target)

	var apiErr *apierrors.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want an *APIError", err)
	}

	if hasRawServerByte(err.Error()) || len(apiErr.Message) > maxEscapedServerText {
		t.Errorf("error text is %d bytes and holds a raw character: %q", len(err.Error()), err.Error())
	}
}
