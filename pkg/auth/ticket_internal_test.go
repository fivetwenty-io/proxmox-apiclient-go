package auth

// ticket_internal_test.go exercises processLoginResponse and readBoundedBody
// directly (white-box). These are unexported, and Authenticate()/RefreshForce()
// do not surface AuthResult.Error to their caller, so the status-gated
// behavior of processLoginResponse can only be observed at this level.

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	apierrors "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/errors"
)

// newTestResponse builds a minimal *http.Response carrying statusCode and
// body, sufficient for exercising processLoginResponse.
func newTestResponse(statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// TestProcessLoginResponse_HTMLErrorPage verifies that a non-2xx login
// response is never handed to the JSON decoder: the returned AuthResult.Error
// carries the HTTP status even when the body is an HTML page from a reverse
// proxy rather than the expected JSON envelope.
func TestProcessLoginResponse_HTMLErrorPage(t *testing.T) {
	t.Parallel()

	const htmlBody = `<html><body><h1>502 Bad Gateway</h1></body></html>`

	ticketAuth := &TicketAuthenticator{}
	resp := newTestResponse(http.StatusBadGateway, htmlBody)

	defer func() {
		_ = resp.Body.Close()
	}()

	result, err := ticketAuth.processLoginResponse(resp)
	if err != nil {
		t.Fatalf("processLoginResponse() error = %v, want nil (status is carried in AuthResult.Error)", err)
	}

	if result.Success {
		t.Error("Success = true, want false for a 502 response")
	}

	if result.Error == nil {
		t.Fatal("Error is nil, want non-nil for a 502 response")
	}

	var apiErr *apierrors.APIError
	if !errors.As(result.Error, &apiErr) {
		t.Fatalf("Error = %T, want *apierrors.APIError", result.Error)
	}

	if apiErr.HTTPCode != http.StatusBadGateway {
		t.Errorf("HTTPCode = %d, want %d", apiErr.HTTPCode, http.StatusBadGateway)
	}

	if !strings.Contains(result.Error.Error(), "502") {
		t.Errorf("Error = %q, want it to mention HTTP status 502", result.Error.Error())
	}
}

// TestProcessLoginResponse_JSONErrorBody is a regression check: a non-2xx
// response with a valid JSON error body must still surface the server
// message, exactly as before the status gate was added.
func TestProcessLoginResponse_JSONErrorBody(t *testing.T) {
	t.Parallel()

	ticketAuth := &TicketAuthenticator{}

	body := `{"message":"authentication failure","errors":{"username":"no such user"}}`
	resp := newTestResponse(http.StatusUnauthorized, body)

	defer func() {
		_ = resp.Body.Close()
	}()

	result, err := ticketAuth.processLoginResponse(resp)
	if err != nil {
		t.Fatalf("processLoginResponse() error = %v, want nil", err)
	}

	if result.Error == nil {
		t.Fatal("Error is nil, want non-nil for a 401 response")
	}

	if !strings.Contains(result.Error.Error(), "authentication failure") {
		t.Errorf("Error = %q, want it to include the server message", result.Error.Error())
	}
}

// TestProcessLoginResponse_SuccessOnStatusOK verifies the gate does not
// interfere with the successful (200 OK) decode path.
func TestProcessLoginResponse_SuccessOnStatusOK(t *testing.T) {
	t.Parallel()

	ticketAuth := &TicketAuthenticator{}

	body := `{"data":{"ticket":"PVE:root@pam::sig","CSRFPreventionToken":"csrf","username":"root@pam"},"success":1}`
	resp := newTestResponse(http.StatusOK, body)

	defer func() {
		_ = resp.Body.Close()
	}()

	result, err := ticketAuth.processLoginResponse(resp)
	if err != nil {
		t.Fatalf("processLoginResponse() error = %v, want nil", err)
	}

	if !result.Success {
		t.Fatalf("Success = false, want true; Error = %v", result.Error)
	}

	if result.Ticket == nil || result.Ticket.Value != "PVE:root@pam::sig" {
		t.Errorf("Ticket = %+v, want ticket value %q", result.Ticket, "PVE:root@pam::sig")
	}
}

// TestReadBoundedBody verifies the bound is enforced and that bodies smaller
// than the bound are returned unmodified.
func TestReadBoundedBody(t *testing.T) {
	t.Parallel()

	t.Run("truncates oversized body", func(t *testing.T) {
		t.Parallel()

		oversized := strings.Repeat("A", maxErrorBodySnippet*2)

		got := readBoundedBody(strings.NewReader(oversized))
		if len(got) != maxErrorBodySnippet {
			t.Errorf("len(readBoundedBody()) = %d, want %d", len(got), maxErrorBodySnippet)
		}
	})

	t.Run("returns short body unmodified", func(t *testing.T) {
		t.Parallel()

		const short = "small error body"

		got := readBoundedBody(strings.NewReader(short))
		if string(got) != short {
			t.Errorf("readBoundedBody() = %q, want %q", string(got), short)
		}
	})
}
