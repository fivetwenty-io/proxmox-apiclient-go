package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	apierrors "github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/errors"
)

const (
	keySuccess = "success"
	keyErrors  = "errors"
	keyVMID    = "vmid"
)

// ---------------------------------------------------------------------------
// F7: 2xx with non-empty errors map must be promoted to an API error.
// ---------------------------------------------------------------------------

// TestParseJSON_2xxWithErrorsMap_ReturnsError captures F7: a 2xx PVE envelope
// that carries a non-empty errors map must return an *apierrors.APIError even
// when Success != 0 and Message is empty.
func TestParseJSON_2xxWithErrorsMap_ReturnsError(t *testing.T) {
	t.Parallel()

	// Payload: success flag set, no top-level message, but field errors present.
	payload := map[string]interface{}{
		keySuccess: 1,
		keyErrors: map[string]string{
			keyVMID:  "required",
			"memory": "must be positive",
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	rp := NewResponseParser()

	var target interface{}

	parseErr := rp.parseJSON(body, &target)
	if parseErr == nil {
		t.Fatal("parseJSON() returned nil error; want *apierrors.APIError for non-empty errors map")
	}

	var apiErr *apierrors.APIError
	if !isAPIError(parseErr, &apiErr) {
		t.Fatalf("parseJSON() error type = %T, want *apierrors.APIError", parseErr)
	}

	if len(apiErr.Errors) != 2 {
		t.Errorf("APIError.Errors len = %d, want 2", len(apiErr.Errors))
	}
}

// TestParseJSON_2xxWithErrorsMap_MessageAndErrors captures F7 with both
// message and errors present (Success may be anything).
func TestParseJSON_2xxWithErrorsMap_MessageAndErrors(t *testing.T) {
	t.Parallel()

	payload := map[string]interface{}{
		keySuccess: 1,
		"message":  "validation error",
		keyErrors: map[string]string{
			"name": "too long",
		},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	rp := NewResponseParser()

	var target interface{}

	parseErr := rp.parseJSON(body, &target)
	if parseErr == nil {
		t.Fatal("parseJSON() returned nil error; want error for non-empty errors map")
	}
}

// TestParseJSON_2xxNoErrors_Success verifies existing success path is unbroken:
// a 2xx envelope with no errors map and no failure indicator returns nil error.
func TestParseJSON_2xxNoErrors_Success(t *testing.T) {
	t.Parallel()

	payload := map[string]interface{}{
		"data":   "some-value",
		keySuccess: 1,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	rp := NewResponseParser()

	var target interface{}

	parseErr := rp.parseJSON(body, &target)
	if parseErr != nil {
		t.Fatalf("parseJSON() returned unexpected error: %v", parseErr)
	}
}

// TestParseJSON_Success0WithMessage_ReturnsError preserves existing behaviour:
// Success==0 && Message!="" must still return an error.
func TestParseJSON_Success0WithMessage_ReturnsError(t *testing.T) {
	t.Parallel()

	payload := map[string]interface{}{
		keySuccess: 0,
		"message":  "operation failed",
	}

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	rp := NewResponseParser()

	var target interface{}

	parseErr := rp.parseJSON(body, &target)
	if parseErr == nil {
		t.Fatal("parseJSON() returned nil error; want error for Success==0 with message")
	}
}

// ---------------------------------------------------------------------------
// R8-16: malformed JSON must return nil target population and a non-nil error.
// ---------------------------------------------------------------------------

// TestParseJSON_MalformedJSON_NilResultAndError captures R8-16: when JSON
// cannot be parsed at all, parseJSON must return a non-nil error and must NOT
// silently succeed (target should not be set to a partial/unexpected value).
func TestParseJSON_MalformedJSON_NilResultAndError(t *testing.T) {
	t.Parallel()

	body := []byte(`{this is not valid json}`)

	rp := NewResponseParser()
	rp.StrictMode = true // ensure we get an error, not silent fallback

	var target interface{}

	parseErr := rp.parseJSON(body, &target)
	if parseErr == nil {
		t.Fatal("parseJSON() returned nil error for malformed JSON; want error")
	}

	// target must not have been populated with anything meaningful.
	if target != nil {
		t.Errorf("parseJSON() populated target = %v after parse failure; want nil", target)
	}
}

// TestParseJSON_MalformedJSON_NonStrictReturnsError verifies that when neither
// envelope nor direct unmarshal succeeds, non-strict mode also returns an error
// and does not claim success.
func TestParseJSON_MalformedJSON_NonStrictReturnsError(t *testing.T) {
	t.Parallel()

	body := []byte(`<<<not json at all>>>`)

	rp := NewResponseParser()

	var target interface{}

	parseErr := rp.parseJSON(body, &target)
	if parseErr == nil {
		t.Fatal("parseJSON() returned nil error for completely malformed JSON in non-strict mode; want error")
	}
}

// ---------------------------------------------------------------------------
// Integration: ResponseParser.Parse on 2xx responses with errors map.
// ---------------------------------------------------------------------------

// TestResponseParser_Parse_2xxErrorsMap exercises the full Parse call path
// with an HTTP test server returning 200 + errors map.
func TestResponseParser_Parse_2xxErrorsMap(t *testing.T) {
	t.Parallel()

	payload := map[string]interface{}{
		keySuccess: 1,
		keyErrors: map[string]string{
			keyVMID: "already exists",
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		err := json.NewEncoder(w).Encode(payload)
		if err != nil {
			t.Errorf("encode: %v", err)
		}
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL) //nolint:noctx
	if err != nil {
		t.Fatalf("http.Get: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	rp := NewResponseParser()

	var target interface{}

	parseErr := rp.Parse(resp, &target)
	if parseErr == nil {
		t.Fatal("Parse() returned nil error for 2xx with errors map; want *apierrors.APIError")
	}

	var apiErr *apierrors.APIError
	if !isAPIError(parseErr, &apiErr) {
		t.Fatalf("Parse() error type = %T, want *apierrors.APIError", parseErr)
	}
}

// TestResponseParser_Parse_MalformedJSON verifies that a 200 response with
// malformed JSON body surfaces an error and does NOT return nil error.
func TestResponseParser_Parse_MalformedJSON(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{invalid`))
	}))
	defer srv.Close()

	resp, err := http.Get(srv.URL) //nolint:noctx
	if err != nil {
		t.Fatalf("http.Get: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	rp := NewResponseParser()
	rp.StrictMode = true

	var target interface{}

	parseErr := rp.Parse(resp, &target)
	if parseErr == nil {
		t.Fatal("Parse() returned nil error for malformed JSON; want error")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// isAPIError attempts to unwrap err into *apierrors.APIError via errors.As.
func isAPIError(err error, out **apierrors.APIError) bool {
	// Walk the error chain manually to avoid importing "errors" colliding with
	// the package name.
	type unwrapper interface{ Unwrap() error }

	for errNode := err; errNode != nil; {
		ae := &apierrors.APIError{}
		if errors.As(errNode, &ae) {
			*out = ae

			return true
		}

		if u, ok := errNode.(unwrapper); ok {
			errNode = u.Unwrap()
		} else {
			break
		}
	}

	return false
}
