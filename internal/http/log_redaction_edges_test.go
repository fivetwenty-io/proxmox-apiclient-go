package http //nolint:testpackage // white-box test: reads the log fields of the client

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect/redirecttest"
)

// logOf sends one request to a server that answers with body and returns every
// line the client logged.
func logOf(t *testing.T, body string) []string {
	t.Helper()

	client, captured := loggedClient(t, "application/json", body)

	_, err := client.DoWithContext(context.Background(), http.MethodPost, "/access/users/root@pam/token/x", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	lines := captured.all()
	if len(lines) == 0 {
		t.Fatal("nothing was logged")
	}

	return lines
}

// TestLoggedBodyRedactsTheSecretOfEveryTokenCreateShape sends the replies PVE,
// PBS, and PDM give when they create or regenerate an API token. PVE names the
// token with "full-tokenid", and PBS and PDM name it with "tokenid", but in
// every shape the "value" member is the secret.
func TestLoggedBodyRedactsTheSecretOfEveryTokenCreateShape(t *testing.T) {
	t.Parallel()

	bodies := map[string]string{
		"pve token":     `{"data":{"full-tokenid":"root@pam!x","info":{"privsep":1},"value":"PVETOKENSECRET"}}`,
		"pbs and pdm":   `{"data":{"tokenid":"root@pam!x","value":"PBSTOKENSECRET"}}`,
		"upper case":    `{"data":{"TokenID":"root@pam!x","Value":"CASETOKENSECRET"}}`,
		"pending entry": `{"data":[{"key":"cipassword","value":"PENDINGSECRET"}]}`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, line := range logOf(t, body) {
				if strings.Contains(line, "TOKENSECRET") || strings.Contains(line, "PENDINGSECRET") {
					t.Errorf("log line holds the secret: %s", line)
				}
			}
		})
	}
}

// TestLoggedBodyKeepsAValueWithNoSecretSibling checks that the rule for the
// token secret does not redact every member named "value".
func TestLoggedBodyKeepsAValueWithNoSecretSibling(t *testing.T) {
	t.Parallel()

	found := false

	for _, line := range logOf(t, `{"data":{"name":"cpu","value":"KEEPTHISVALUE"}}`) {
		found = found || strings.Contains(line, "KEEPTHISVALUE")
	}

	if !found {
		t.Error("the value of an object with no secret member was redacted")
	}
}

// TestLoggedBodyRedactsANestedDataString sends the reply to a read of an ACME
// plugin, which holds the DNS provider's credentials in a "data" string one
// level below the top, alone and inside a list.
func TestLoggedBodyRedactsANestedDataString(t *testing.T) {
	t.Parallel()

	bodies := map[string]string{
		"one plugin":   `{"data":{"api":"cf","data":"CF_Token=ACMEDNSCREDENTIAL","plugin":"p","type":"dns"}}`,
		"plugin list":  `{"data":[{"api":"cf","data":"CF_Token=ACMEDNSCREDENTIAL","plugin":"p","type":"dns"}]}`,
		"upper case":   `{"data":{"api":"cf","Data":"CF_Token=ACMEDNSCREDENTIAL"}}`,
		"deeper still": `{"data":{"outer":{"inner":{"data":"ACMEDNSCREDENTIAL"}}}}`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, line := range logOf(t, body) {
				if strings.Contains(line, "ACMEDNSCREDENTIAL") {
					t.Errorf("log line holds the credential: %s", line)
				}
			}
		})
	}
}

// longHeaderValue is longer than any header value a log line keeps.
func longHeaderValue() string { return strings.Repeat("A", 100_000) }

// TestLoggedHeaderValuesAreEscapedAndCapped sends a reply whose Content-Type
// holds a direction override and a C1 control, and whose other header holds
// 100,000 bytes. The content type field and the response header field must
// show neither the raw characters nor the whole long value.
func TestLoggedHeaderValuesAreEscapedAndCapped(t *testing.T) {
	t.Parallel()

	response := "HTTP/1.1 200 OK\r\n" +
		"Content-Type: text/\u202eEVILCT\u009b2J\r\n" +
		"X-Long: " + longHeaderValue() + "\r\n" +
		"Location: http://h/" + longHeaderValue() + "\r\n" +
		"Content-Length: 5\r\nConnection: close\r\n\r\nhello"

	server := redirecttest.ServeRaw(t, []byte(response))
	client, captured := newHostileClient(t, server, func(*Options) {}, false)
	client.SetLogConfig(LogConfig{
		Enabled: true, LogQueryParams: true, LogRequestHeader: true, LogResponseHeader: true, LogResponseBody: true,
	})

	_, _ = client.DoWithContext(context.Background(), http.MethodGet, "/version", nil)

	sawContentType := false
	tooLong := strings.Repeat("A", 4096)

	for _, line := range captured.all() {
		sawContentType = sawContentType || strings.Contains(line, logFieldBodyContentType)

		for _, raw := range []string{"\u202e", "\u009b"} {
			if strings.Contains(line, raw) {
				t.Errorf("log line holds the raw character %q: %.200s", raw, line)
			}
		}

		if strings.Contains(line, tooLong) {
			t.Errorf("log line holds a header value longer than the cap (%d bytes)", len(line))
		}
	}

	if !sawContentType {
		t.Fatal("no line carried the content type field")
	}
}

// TestLoggedBodyRedactsThePendingValueOfAConfigEntry sends the reply to a read
// of the pending configuration of a VM, a container, or its cloud-init
// drive. Each entry names an option in "key", gives its current setting in
// "value", and gives a change not yet applied in "pending", so both settings
// of a password option are secrets.
func TestLoggedBodyRedactsThePendingValueOfAConfigEntry(t *testing.T) {
	t.Parallel()

	bodies := map[string]string{
		"value and pending": `{"data":[{"key":"cipassword","value":"CURRENTPWSECRET","pending":"NEXTPWSECRET"}]}`,
		"pending only":      `{"data":[{"key":"cipassword","pending":"NEXTPWSECRET"}]}`,
		"upper case":        `{"data":[{"Key":"cipassword","Pending":"NEXTPWSECRET"}]}`,
		"pending delete":    `{"data":[{"delete":1,"key":"cipassword","value":"CURRENTPWSECRET"}]}`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			for _, line := range logOf(t, body) {
				if strings.Contains(line, "PWSECRET") {
					t.Errorf("log line holds the password: %s", line)
				}
			}
		})
	}
}

// TestLoggedBodyKeepsAPendingMemberWithNoSecretSibling checks that the rule for
// a pending entry does not redact every member named "pending".
func TestLoggedBodyKeepsAPendingMemberWithNoSecretSibling(t *testing.T) {
	t.Parallel()

	found := false

	for _, line := range logOf(t, `{"data":{"name":"job","pending":"KEEPTHISPENDING"}}`) {
		found = found || strings.Contains(line, "KEEPTHISPENDING")
	}

	if !found {
		t.Error("the pending member of an object with no secret member was redacted")
	}
}
