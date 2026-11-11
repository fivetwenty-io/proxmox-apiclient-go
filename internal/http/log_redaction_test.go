package http //nolint:testpackage // white-box test: reads the log fields and the redaction helpers of the client

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

const (
	logSecretPassword = "CIFSPW"
	logSecretTicket   = "PVE:root@pam:5F3A2B1C::TICKETSECRET"
	logSecretCSRF     = "5F3A2B1C:CSRFSECRET"
	logSecretValue    = "11111111-2222-3333-4444-TOKENSECRET"
)

// logSecrets are the markers no log line holds.
func logSecrets() []string {
	return []string{logSecretPassword, "TICKETSECRET", "CSRFSECRET", "TOKENSECRET", "USERINFOPW", "FRAGSECRET"}
}

// loggedClient builds a client for a server that answers with status and body,
// with every logging switch on and a logger and a hook that capture all
// output.
func loggedClient(t *testing.T, contentType, body string) (*Client, *captureLines) {
	t.Helper()

	server := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", contentType)
		w.Header().Add("Set-Cookie", "PVEAuthCookie=COOKIESECRET")
		_, _ = w.Write([]byte(body))
	})

	client := newClientForServerURL(t, server.URL, func(o *Options) { o.APIToken = testAPITokenFull })
	captured := &captureLines{}

	client.SetLogger(captured)
	client.SetLogConfig(LogConfig{
		Enabled: true, LogQueryParams: true, LogRequestHeader: true, LogResponseHeader: true,
		LogResponseBody: true, RedactHeaders: []string{"authorization"}, RedactParams: []string{"password"},
	})
	client.AddHook(func(event *Event) { captured.addf("hook %v %+v", event, event) })

	return client, captured
}

func assertNoSecrets(t *testing.T, captured *captureLines, extra ...string) {
	t.Helper()

	lines := captured.all()
	if len(lines) == 0 {
		t.Fatal("nothing was logged")
	}

	for _, line := range lines {
		for _, secret := range append(logSecrets(), append(extra, "COOKIESECRET", "s3cr3t")...) {
			if strings.Contains(line, secret) {
				t.Errorf("log line holds %q: %s", secret, line)
			}
		}
	}
}

// TestLoggedURLsCarryNoSecretQueryValue sends a request with a password
// parameter and checks every log line and hook event, in every field, for the
// secret. The url field must pass through the redaction the query field uses.
func TestLoggedURLsCarryNoSecretQueryValue(t *testing.T) {
	t.Parallel()

	client, captured := loggedClient(t, "application/json", `{"data":[]}`)

	_, err := client.DoWithContext(context.Background(), http.MethodGet, "/nodes/pve1/scan/cifs", map[string]interface{}{
		"server": "10.0.0.1", "username": "u", "password": logSecretPassword, "vncticket": "TICKETSECRET",
	})
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	assertNoSecrets(t, captured)

	joined := strings.Join(captured.all(), "\n")
	for _, want := range []string{"/nodes/pve1/scan/cifs", "server=10.0.0.1", "REDACTED"} {
		if !strings.Contains(joined, want) {
			t.Errorf("log output lost %q, want enough left to debug with", want)
		}
	}
}

// TestRedactedURLStripsUserinfoFragmentAndSecrets checks the URL rendering on
// its own, including the cases a request cannot reach through the client.
func TestRedactedURLStripsUserinfoFragmentAndSecrets(t *testing.T) {
	t.Parallel()

	raw := "https://root:USERINFOPW@h:8006/api2/json/x%20y?Password=CIFSPW&ticket=TICKETSECRET&node=pve1" +
		"&b%61d=%zz&PASSWORD&token=TOKENSECRET#FRAGSECRET"

	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	cfg := LogConfig{LogQueryParams: true, RedactParams: []string{"PassWord"}}
	got := redactedURL(parsed, cfg)

	for _, secret := range logSecrets() {
		if strings.Contains(got, secret) {
			t.Errorf("%q holds %q", got, secret)
		}
	}

	for _, want := range []string{"https://h:8006/api2/json/x%20y?", "node=pve1", "Password=REDACTED", "ticket=REDACTED"} {
		if !strings.Contains(got, want) {
			t.Errorf("%q lost %q", got, want)
		}
	}

	if without := redactedURL(parsed, LogConfig{}); strings.Contains(without, "?") {
		t.Errorf("with query logging off the URL is %q, want no query", without)
	}

	if redactedURL(nil, cfg) != "" {
		t.Error("a nil URL must render as an empty string")
	}
}

// TestLoggedHeadersRedactSetCookieAndIgnoreCase checks the headers the
// configured list does not name, which the built-in list covers.
func TestLoggedHeadersRedactSetCookieAndIgnoreCase(t *testing.T) {
	t.Parallel()

	out := redact(map[string][]string{
		"Set-Cookie": {"PVEAuthCookie=COOKIESECRET"}, "AUTHORIZATION": {"s3cr3t"}, "Accept": {"application/json"},
	}, []string{"Authorization"}, alwaysRedactedHeaders()...)

	if out["Set-Cookie"] != redactedValue || out["AUTHORIZATION"] != redactedValue {
		t.Errorf("redact = %v, want Set-Cookie and AUTHORIZATION redacted", out)
	}

	if _, ok := out["Accept"].([]string); !ok {
		t.Errorf("Accept = %v, want it kept", out["Accept"])
	}
}

// TestLoggedResponseBodyRedactsSecretMembers logs the response to the login
// call and to a token create call, and checks the secrets stay out while the
// rest of the body is kept.
func TestLoggedResponseBodyRedactsSecretMembers(t *testing.T) {
	t.Parallel()

	bodies := map[string]string{
		"login": `{"data":{"ticket":"` + logSecretTicket + `","CSRFPreventionToken":"` + logSecretCSRF +
			`","username":"root@pam","cap":{"vms":{"VM.Audit":1}}}}`,
		"token create": `{"data":{"full-tokenid":"root@pam!t","info":{"privsep":"1"},"value":"` + logSecretValue + `"}}`,
		"nested array": `{"data":[{"vncticket":"TICKETSECRET","password":"CSRFSECRET","id":7}]}`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client, captured := loggedClient(t, "application/json", body)

			_, err := client.DoWithContext(context.Background(), http.MethodGet, "/access/ticket", nil)
			if err != nil {
				t.Fatalf("request: %v", err)
			}

			assertNoSecrets(t, captured)

			if !strings.Contains(strings.Join(captured.all(), "\n"), "REDACTED") {
				t.Errorf("the body was not logged with its secrets replaced")
			}
		})
	}
}

// TestLoggedResponseBodyKeepsOrdinaryMembers checks that redaction leaves the
// members a developer reads, including a "value" outside a token create.
func TestLoggedResponseBodyKeepsOrdinaryMembers(t *testing.T) {
	t.Parallel()

	fields := bodyFields([]byte(`{"data":{"value":"visible","count":12345678901234567890}}`), "application/json", LogConfig{})

	got, _ := fields[logFieldBody].(string)
	if got != `{"data":{"count":12345678901234567890,"value":"visible"}}` {
		t.Errorf("body = %q", got)
	}
}

// TestLoggedResponseBodyThatIsNotJSONIsNotLogged checks that text the logger
// cannot parse is replaced by its length and content type.
func TestLoggedResponseBodyThatIsNotJSONIsNotLogged(t *testing.T) {
	t.Parallel()

	bodies := map[string]string{
		"text":            "ticket=TICKETSECRET password=CIFSPW",
		"truncated json":  `{"data":{"ticket":"TICKETSECRET`,
		"two documents":   `{"a":1} {"ticket":"TICKETSECRET"}`,
		"trailing secret": `{"a":1}TICKETSECRET`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			client, captured := loggedClient(t, "text/plain", body)

			_, _ = client.DoWithContext(context.Background(), http.MethodGet, "/x", nil)

			assertNoSecrets(t, captured)

			joined := strings.Join(captured.all(), "\n")
			if !strings.Contains(joined, "resp_body_length") || !strings.Contains(joined, "text/plain") {
				t.Errorf("log output has no length and content type: %s", joined)
			}
		})
	}
}

// TestLoggedResponseBodyIsCutAtTheLimit checks that the limit applies to the
// redacted text, on a character boundary, and marks the cut.
func TestLoggedResponseBodyIsCutAtTheLimit(t *testing.T) {
	t.Parallel()

	body := []byte(`{"data":"` + strings.Repeat("é", 50) + `","ticket":"TICKETSECRET"}`)

	fields := bodyFields(body, "application/json", LogConfig{MaxBodyBytes: 21})

	got, _ := fields[logFieldBody].(string)
	if !strings.HasSuffix(got, truncatedSuffix) || strings.Contains(got, "TICKETSECRET") {
		t.Errorf("body = %q, want a cut body with the marker", got)
	}

	if strings.ToValidUTF8(got, "") != got {
		t.Errorf("body = %q is not valid UTF-8", got)
	}
}
