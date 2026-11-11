package http //nolint:testpackage // white-box test: reads the log fields of the client

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/internal/redirect/redirecttest"
)

// headerFieldBytes returns the bytes of names and values in a logged
// resp_headers field, and the most values any one header kept.
func headerFieldBytes(t *testing.T, field map[string]interface{}) (int, int) {
	t.Helper()

	total, most := 0, 0

	for name, value := range field {
		total += len(name)

		switch typed := value.(type) {
		case []string:
			most = max(most, len(typed))

			for _, text := range typed {
				total += len(text)
			}
		case string:
			total += len(typed)
		}
	}

	return total, most
}

// TestLoggedHeaderNamesAndRepeatsAreBounded logs the headers of a reply with
// one header whose name is 200,000 bytes and another that repeats a 1,000-byte
// value 2,000 times. The field must stay small, keep the ordinary headers, and
// say how many headers it left out.
func TestLoggedHeaderNamesAndRepeatsAreBounded(t *testing.T) {
	t.Parallel()

	repeated := make([]string, 2000)
	for i := range repeated {
		repeated[i] = strings.Repeat("V", 1000)
	}

	header := map[string][]string{
		"Content-Type":                      {contentTypeJSON},
		"X-" + strings.Repeat("N", 200_000): {"x"},
		"X-Repeated":                        repeated,
	}

	field := redactResponseHeaders(header, LogConfig{})

	total, most := headerFieldBytes(t, field)
	if total > maxLoggedHeaderBytes {
		t.Errorf("the field holds %d bytes of names and values, want at most %d", total, maxLoggedHeaderBytes)
	}

	if most > maxLoggedHeaderValues+1 {
		t.Errorf("a header kept %d values, want at most %d and a count", most, maxLoggedHeaderValues)
	}

	for name := range field {
		if len(name) > redactedHeaderValueBytes+len("...(truncated)") {
			t.Errorf("a header name of %d bytes was logged", len(name))
		}
	}

	if _, kept := field["Content-Type"]; !kept {
		t.Error("an ordinary header was left out")
	}

	repeats, ok := field["X-Repeated"].([]string)
	if !ok || !strings.Contains(repeats[len(repeats)-1], "1984 more values") {
		t.Errorf("the repeated header does not say how many values it left out: %.200v", field["X-Repeated"])
	}
}

// TestLoggedHeaderFieldCountsDroppedHeaders fills the field past its total
// and checks that the headers left out are counted.
func TestLoggedHeaderFieldCountsDroppedHeaders(t *testing.T) {
	t.Parallel()

	header := map[string][]string{}
	for i := range 80 {
		header["X-Filler-"+strings.Repeat(string(rune('a'+i%26)), 1+i/26)] = []string{strings.Repeat("F", 1000)}
	}

	field := redactResponseHeaders(header, LogConfig{})

	dropped, ok := field[droppedHeadersField].(int)
	if !ok || dropped == 0 {
		t.Fatalf("the field does not count the headers it left out: %v", field[droppedHeadersField])
	}

	if kept := len(field) - 1; kept+dropped != len(header) {
		t.Errorf("kept %d and dropped %d of %d headers", kept, dropped, len(header))
	}
}

// TestLoggedHeaderLineIsBoundedOverTheWire sends the reply from the test above
// through the client and checks the length of every log line.
func TestLoggedHeaderLineIsBoundedOverTheWire(t *testing.T) {
	t.Parallel()

	var response strings.Builder

	response.WriteString("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n")
	response.WriteString("X-" + strings.Repeat("N", 200_000) + ": x\r\n")

	for range 2000 {
		response.WriteString("X-Repeated: " + strings.Repeat("V", 1000) + "\r\n")
	}

	response.WriteString("Content-Length: 2\r\nConnection: close\r\n\r\n{}")

	server := redirecttest.ServeRaw(t, []byte(response.String()))
	client, captured := newHostileClient(t, server, func(*Options) {}, false)
	client.SetLogConfig(LogConfig{Enabled: true, LogResponseHeader: true})

	_, _ = client.DoWithContext(context.Background(), http.MethodGet, "/version", nil)

	lines := captured.all()
	if len(lines) == 0 {
		t.Fatal("nothing was logged")
	}

	for _, line := range lines {
		if len(line) > 4*maxLoggedHeaderBytes {
			t.Errorf("a log line is %d bytes", len(line))
		}
	}
}
