package http //nolint:testpackage // white-box test: reads the log fields and the redaction helpers of the client

import (
	"context"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"unicode"
)

// TestSecretNameMatchesFragmentsAndSparesOrdinaryNames checks the name matcher
// on the secret names PVE returns, on the shapes of other secret names, and on
// the ordinary option names it must leave readable.
func TestSecretNameMatchesFragmentsAndSparesOrdinaryNames(t *testing.T) {
	t.Parallel()

	secrets := []string{
		"recovery", "challenge", "corosync_authkey", "cipassword", "encryption-key", "client-secret", "Passwd",
		"TOTP", "otp", "totp_code", "one-time-otp", "hotp", "CSRFPreventionToken", "PVEAuthCookie", "credential-id",
		"private_key", "api_key", "X-Api-Key", "Sec-WebSocket-Key", "authkey", "ticket", "vncticket", headerAuthorization,
		"proxy-authorization", "set-cookie", "tokenid", "monkey",
	}

	ordinary := []string{
		"keyboard", "Keyboard", "hotplug", "footprint", "vmid", "status", "balloon", "cores",
		"content-type", "server", "username", "upid", "digest", "bootdisk",
	}

	matcher := newNameMatcher(nil, alwaysRedactedHeaders())

	for _, name := range secrets {
		if !matcher.secret(name) {
			t.Errorf("name %q is not redacted, want redacted", name)
		}
	}

	for _, name := range ordinary {
		if matcher.secret(name) {
			t.Errorf("name %q is redacted, want it left readable", name)
		}
	}

	if !newNameMatcher([]string{"Custom-Field"}, nil).secret("custom-FIELD") {
		t.Error("a name the caller listed must match without regard to case")
	}
}

// TestLoggedBodyRedactsSecretFragmentsAndKeepsOrdinaryMembers checks the
// members the exact-name list used to miss, in the body, the query, and the
// headers.
func TestLoggedBodyRedactsSecretFragmentsAndKeepsOrdinaryMembers(t *testing.T) {
	t.Parallel()

	body := []byte(`{"data":{"recovery":["RECOV1","RECOV2"],"challenge":"CHALSECRET",` +
		`"corosync_authkey":"AUTHKEYSECRET","encryption-key":"ENCSECRET","keyboard":"de","hotplug":"disk,network"}}`)

	got, _ := bodyFields(body, "application/json", LogConfig{})[logFieldBody].(string)

	for _, secret := range []string{"RECOV1", "RECOV2", "CHALSECRET", "AUTHKEYSECRET", "ENCSECRET"} {
		if strings.Contains(got, secret) {
			t.Errorf("body %q holds %q", got, secret)
		}
	}

	for _, kept := range []string{`"keyboard":"de"`, `"hotplug":"disk,network"`} {
		if !strings.Contains(got, kept) {
			t.Errorf("body %q lost %q", got, kept)
		}
	}

	query := redactRawQuery("encryption-key=ENCSECRET&keyboard=de&Challenge=C", newNameMatcher(nil, nil))

	leaked := strings.Contains(query, "ENCSECRET") || strings.Contains(query, "Challenge=C")
	if leaked || !strings.Contains(query, "keyboard=de") {
		t.Errorf("query = %q, want the secret fragments redacted and keyboard kept", query)
	}

	headers := redact(map[string][]string{"X-Api-Key": {"KEYSECRET"}, "Content-Type": {"text/plain"}}, nil)
	if headers["X-Api-Key"] != redactedValue || headers["Content-Type"] == redactedValue {
		t.Errorf("headers = %v, want X-Api-Key redacted and Content-Type kept", headers)
	}
}

// TestLargeResponseBodyIsNotParsedForLogging checks that a body above the cap
// is logged by length and content type, and that the memory the redactor uses
// does not follow the size of the body.
//
//nolint:paralleltest // not parallel, because the memory counter is process wide
func TestLargeResponseBodyIsNotParsedForLogging(t *testing.T) {
	const members = 400000

	var builder strings.Builder

	builder.WriteString(`{"data":[`)

	for i := range members {
		if i > 0 {
			builder.WriteByte(',')
		}

		builder.WriteString(`{"a":[],"ticket":"TICKETSECRET"}`)
	}

	builder.WriteString(`]}`)

	body := []byte(builder.String())
	builder.Reset()

	var before, after runtime.MemStats

	runtime.GC()
	runtime.ReadMemStats(&before)

	fields := bodyFields(body, "application/json", LogConfig{MaxBodyBytes: 4096})

	runtime.ReadMemStats(&after)

	if _, logged := fields[logFieldBody]; logged {
		t.Error("a body above the cap must not be logged")
	}

	if fields[logFieldBodyLength] != len(body) || fields[logFieldBodyContentType] != "application/json" {
		t.Errorf("fields = %v, want the length %d and the content type", fields, len(body))
	}

	const budget = 1 << 20

	if used := after.TotalAlloc - before.TotalAlloc; used > budget {
		t.Errorf("logging a %d byte body allocated %d bytes, want at most %d", len(body), used, budget)
	}

	atCap := []byte(`{"data":"` + strings.Repeat("x", maxRedactBodyBytes-len(`{"data":""}`)) + `"}`)
	if _, logged := bodyFields(atCap, "application/json", LogConfig{})[logFieldBody]; !logged {
		t.Errorf("a body of %d bytes, at the cap, must still be redacted and logged", len(atCap))
	}
}

// TestLoggedResponseHeadersRedactLocationAndEscapeValues sends a Location
// header with user information, a query, and a fragment, and checks every log
// line and hook event for the secrets.
func TestLoggedResponseHeadersRedactLocationAndEscapeValues(t *testing.T) {
	t.Parallel()

	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", "http://root:USERINFOPW@host:8006/x?ticket=TICKETSECRET&node=pve1#FRAGSECRET")
		w.Header().Set("Content-Location", "/y?password=CIFSPW")
		w.Header().Set("X-Note", "a\u202eb")
		_, _ = w.Write([]byte(`{"data":[]}`))
	})

	client := newClientForServerURL(t, server.URL, func(o *Options) { o.APIToken = testAPITokenFull })
	captured := &captureLines{}

	client.SetLogger(captured)
	client.SetLogConfig(LogConfig{Enabled: true, LogQueryParams: true, LogResponseHeader: true})

	_, err := client.DoWithContext(context.Background(), http.MethodGet, "/version", nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	assertNoSecrets(t, captured)

	joined := strings.Join(captured.all(), "\n")

	for _, want := range []string{"http://host:8006/x?ticket=REDACTED&node=pve1", "/y?password=REDACTED"} {
		if !strings.Contains(joined, want) {
			t.Errorf("log output lost %q, want enough left to debug with", want)
		}
	}

	for _, r := range joined {
		if unicode.Is(unicode.Cf, r) {
			t.Errorf("log output holds the raw format character %U", r)
		}
	}

	headers := redactResponseHeaders(map[string][]string{"Location": {"http://[::1"}}, LogConfig{})
	if values, _ := headers["Location"].([]string); len(values) != 1 || values[0] != redactedValue {
		t.Errorf("an unparsable Location logged as %v, want %q", headers["Location"], redactedValue)
	}
}

// TestLoggedResponseBodyEscapesDirectionCharacters checks that a text direction
// override in a JSON member is logged escaped.
func TestLoggedResponseBodyEscapesDirectionCharacters(t *testing.T) {
	t.Parallel()

	body := []byte(`{"message":"a\u202eb\u2066c\u200ed\u061ce\u007f"}`)

	got, _ := bodyFields(body, "application/json", LogConfig{})[logFieldBody].(string)

	for _, r := range got {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			t.Errorf("logged body %q holds the raw character %U", got, r)
		}
	}

	if !strings.Contains(got, "\\u202e") {
		t.Errorf("logged body %q lost the escaped override", got)
	}
}
