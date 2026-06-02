package errors_test

import (
	"errors"
	"strconv"
	"testing"

	pveerr "github.com/fivetwenty-io/pve-apiclient-go/v3/pkg/errors"
)

// Repeated string constants used across multiple tests.
const (
	testHost     = "pve.example.com"
	typeAPIErr   = "*pveerr.APIError"
	typeParamErr = "*pveerr.ParameterError"
	testMessage  = "test"
)

func TestAPIError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      pveerr.APIError
		expected string
	}{
		{
			name: "simple message",
			err: pveerr.APIError{
				Message: "Something went wrong",
				Code:    500,
			},
			expected: "Something went wrong (code: 500)",
		},
		{
			name: "with errors map",
			err: pveerr.APIError{
				Message: "Validation failed",
				Code:    400,
				Errors: map[string]string{
					"username": "required",
					"password": "too short",
				},
			},
			expected: "Validation failed (code: 400, errors:",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result := testCase.err.Error()
			if testCase.err.Errors != nil {
				// For errors with map, just check it contains the base message
				if !contains(result, "Validation failed (code: 400, errors:") {
					t.Errorf("Error() = %v, want containing %v", result, testCase.expected)
				}
			} else if result != testCase.expected {
				t.Errorf("Error() = %v, want %v", result, testCase.expected)
			}
		})
	}
}

func TestAPIError_StatusChecks(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		err        pveerr.APIError
		isNotFound bool
		isUnauth   bool
		isForbid   bool
	}{
		{
			name:       "404 not found",
			err:        pveerr.APIError{HTTPCode: 404},
			isNotFound: true,
			isUnauth:   false,
			isForbid:   false,
		},
		{
			name:       "401 unauthorized",
			err:        pveerr.APIError{HTTPCode: 401},
			isNotFound: false,
			isUnauth:   true,
			isForbid:   false,
		},
		{
			name:       "403 forbidden",
			err:        pveerr.APIError{HTTPCode: 403},
			isNotFound: false,
			isUnauth:   false,
			isForbid:   true,
		},
		{
			name:       "500 server error",
			err:        pveerr.APIError{HTTPCode: 500},
			isNotFound: false,
			isUnauth:   false,
			isForbid:   false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if result := testCase.err.IsNotFound(); result != testCase.isNotFound {
				t.Errorf("IsNotFound() = %v, want %v", result, testCase.isNotFound)
			}

			if result := testCase.err.IsUnauthorized(); result != testCase.isUnauth {
				t.Errorf("IsUnauthorized() = %v, want %v", result, testCase.isUnauth)
			}

			if result := testCase.err.IsForbidden(); result != testCase.isForbid {
				t.Errorf("IsForbidden() = %v, want %v", result, testCase.isForbid)
			}
		})
	}
}

func TestPermissionError_Error(t *testing.T) {
	t.Parallel()

	err := pveerr.PermissionError{
		APIError: pveerr.APIError{
			Message: "access denied",
			Code:    403,
		},
		What: "/vms/100",
	}

	expected := "permission denied for /vms/100: access denied (code: 403)"

	result := err.Error()
	if result != expected {
		t.Errorf("Error() = %v, want %v", result, expected)
	}

	// Test without What field
	err.What = ""
	expected = "permission denied: access denied (code: 403)"

	result = err.Error()
	if result != expected {
		t.Errorf("Error() = %v, want %v", result, expected)
	}
}

func TestParameterError_Error(t *testing.T) {
	t.Parallel()

	err := pveerr.ParameterError{
		APIError: pveerr.APIError{
			Message: "invalid value",
			Code:    400,
		},
		Usage: "must be between 1 and 100",
	}

	expected := "parameter error: invalid value (code: 400) (usage: must be between 1 and 100)"

	result := err.Error()
	if result != expected {
		t.Errorf("Error() = %v, want %v", result, expected)
	}

	// Test without Usage field
	err.Usage = ""
	expected = "parameter error: invalid value (code: 400)"

	result = err.Error()
	if result != expected {
		t.Errorf("Error() = %v, want %v", result, expected)
	}
}

func TestAuthenticationError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      pveerr.AuthenticationError
		expected string
	}{
		{
			name:     "simple auth error",
			err:      pveerr.AuthenticationError{},
			expected: "authentication failed",
		},
		{
			name: "with realm",
			err: pveerr.AuthenticationError{
				Realm: "pam",
			},
			expected: "authentication failed for realm pam",
		},
		{
			name: "with TFA",
			err: pveerr.AuthenticationError{
				TFA: true,
			},
			expected: "authentication failed (TFA required)",
		},
		{
			name: "with realm and TFA",
			err: pveerr.AuthenticationError{
				Realm: "pve",
				TFA:   true,
			},
			expected: "authentication failed for realm pve (TFA required)",
		},
		{
			name: "with message",
			err: pveerr.AuthenticationError{
				APIError: pveerr.APIError{
					Message: "invalid credentials",
				},
			},
			expected: "authentication failed: invalid credentials",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result := testCase.err.Error()
			if result != testCase.expected {
				t.Errorf("Error() = %v, want %v", result, testCase.expected)
			}
		})
	}
}

func TestTFARequiredError_Error(t *testing.T) {
	t.Parallel()

	err := pveerr.TFARequiredError{
		Types: []string{"totp", "yubico"},
	}

	expected := "two-factor authentication required (available types: totp, yubico)"

	result := err.Error()
	if result != expected {
		t.Errorf("Error() = %v, want %v", result, expected)
	}
}

func TestConnectionError(t *testing.T) {
	t.Parallel()

	t.Run("Error message", func(t *testing.T) {
		t.Parallel()

		err := pveerr.ConnectionError{
			Host:    testHost,
			Port:    8006,
			Message: "connection refused",
		}

		expected := "connection to pve.example.com:8006 failed: connection refused"

		result := err.Error()
		if result != expected {
			t.Errorf("Error() = %v, want %v", result, expected)
		}
	})

	t.Run("With cause", func(t *testing.T) {
		t.Parallel()

		cause := &pveerr.APIError{Message: "timeout"}
		err := pveerr.ConnectionError{
			Host:  testHost,
			Port:  8006,
			Cause: cause,
		}

		if !errors.Is(err.Unwrap(), cause) {
			t.Errorf("Unwrap() = %v, want %v", err.Unwrap(), cause)
		}
	})
}

func TestSSLError(t *testing.T) {
	t.Parallel()

	t.Run("Error message", func(t *testing.T) {
		t.Parallel()

		err := pveerr.SSLError{
			Host:        testHost,
			Fingerprint: "AA:BB:CC:DD",
			Message:     "certificate verification failed",
		}

		expected := "SSL error for pve.example.com (fingerprint: AA:BB:CC:DD): certificate verification failed"

		result := err.Error()
		if result != expected {
			t.Errorf("Error() = %v, want %v", result, expected)
		}
	})

	t.Run("With cause", func(t *testing.T) {
		t.Parallel()

		cause := &pveerr.APIError{Message: "expired"}
		err := pveerr.SSLError{
			Host:  testHost,
			Cause: cause,
		}

		if !errors.Is(err.Unwrap(), cause) {
			t.Errorf("Unwrap() = %v, want %v", err.Unwrap(), cause)
		}
	})
}

func TestTimeoutError_Error(t *testing.T) {
	t.Parallel()

	err := pveerr.TimeoutError{
		Operation: "login",
		Duration:  "30s",
	}

	expected := "operation login timed out after 30s"

	result := err.Error()
	if result != expected {
		t.Errorf("Error() = %v, want %v", result, expected)
	}
}

func TestParseAPIError(t *testing.T) {
	t.Parallel()

	tests := getParseAPIErrorTestCases()
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			runParseAPIErrorTest(t, testCase)
		})
	}
}

type parseAPIErrorTestCase struct {
	name       string
	statusCode int
	body       []byte
	wantType   string
}

func getParseAPIErrorTestCases() []parseAPIErrorTestCase {
	return []parseAPIErrorTestCase{
		{
			name:       "401 authentication error",
			statusCode: 401,
			body:       []byte(`{"message": "unauthorized"}`),
			wantType:   "*pveerr.AuthenticationError",
		},
		{
			name:       "403 permission error",
			statusCode: 403,
			body:       []byte(`{"message": "forbidden"}`),
			wantType:   "*pveerr.PermissionError",
		},
		{
			name:       "400 parameter error",
			statusCode: 400,
			body:       []byte(`{"message": "bad request"}`),
			wantType:   typeParamErr,
		},
		{
			name:       "500 generic error",
			statusCode: 500,
			body:       []byte(`{"message": "internal server error"}`),
			wantType:   typeAPIErr,
		},
		{
			name:       "TFA required",
			statusCode: 401,
			body:       []byte(`{"ticket": "partial", "types": ["totp"]}`),
			wantType:   "*pveerr.TFARequiredError",
		},
		{
			name:       "invalid JSON",
			statusCode: 500,
			body:       []byte(`not json`),
			wantType:   typeAPIErr,
		},
	}
}

func runParseAPIErrorTest(t *testing.T, testCase parseAPIErrorTestCase) {
	t.Helper()

	err := pveerr.ParseAPIError(testCase.statusCode, testCase.body)
	if err == nil {
		t.Errorf("ParseAPIError() returned nil error")

		return
	}

	// Check error type
	gotType := typeOf(err)
	if gotType != testCase.wantType {
		t.Errorf("ParseAPIError() returned type %v, want %v", gotType, testCase.wantType)
	}
}

func TestIsErrorType(t *testing.T) {
	t.Parallel()

	apiErr := &pveerr.APIError{Message: testMessage}
	permErr := &pveerr.PermissionError{APIError: pveerr.APIError{Message: testMessage}}
	authErr := &pveerr.AuthenticationError{APIError: pveerr.APIError{Message: testMessage}}
	connErr := &pveerr.ConnectionError{Message: testMessage}
	sslErr := &pveerr.SSLError{Message: testMessage}
	timeoutErr := &pveerr.TimeoutError{Operation: testMessage}
	tfaErr := &pveerr.TFARequiredError{Types: []string{"totp"}}

	tests := []struct {
		name     string
		err      error
		checkFn  func(error) bool
		expected bool
	}{
		{"IsAPIError with pveerr.APIError", apiErr, pveerr.IsAPIError, true},
		{"IsAPIError with pveerr.PermissionError", permErr, pveerr.IsAPIError, true},
		{"IsAPIError with ConnectionError", connErr, pveerr.IsAPIError, false},
		{"IsConnectionError with ConnectionError", connErr, pveerr.IsConnectionError, true},
		{"IsConnectionError with pveerr.APIError", apiErr, pveerr.IsConnectionError, false},
		{"IsSSLError with SSLError", sslErr, pveerr.IsSSLError, true},
		{"IsSSLError with pveerr.APIError", apiErr, pveerr.IsSSLError, false},
		{"IsTimeoutError with TimeoutError", timeoutErr, pveerr.IsTimeoutError, true},
		{"IsTimeoutError with pveerr.APIError", apiErr, pveerr.IsTimeoutError, false},
		{"IsTFARequired with TFARequiredError", tfaErr, pveerr.IsTFARequired, true},
		{"IsTFARequired with AuthenticationError", authErr, pveerr.IsTFARequired, false},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result := testCase.checkFn(testCase.err)
			if result != testCase.expected {
				t.Errorf("%s = %v, want %v", testCase.name, result, testCase.expected)
			}
		})
	}
}

// -- Edge case tests --

type edgeCase struct {
	name        string
	statusCode  int
	body        []byte
	wantMsgPart string
	wantType    string
}

func edgeCases() []edgeCase {
	return []edgeCase{
		{"empty body", 503, []byte{}, "HTTP 503", typeAPIErr},
		{"whitespace-only body", 502, []byte("   \n  "), "HTTP 502", typeAPIErr},
		{"plain text body", 500, []byte("Internal Server Error"), "Internal Server Error", typeAPIErr},
		{"malformed JSON", 500, []byte(`{bad json`), "{bad json", typeAPIErr},
		{
			"JSON with nested errors map", 400,
			[]byte(`{"message":"validation failed","code":400,"errors":{"vmid":"required","memory":"must be positive"}}`),
			"validation failed", typeParamErr,
		},
		{"JSON missing message field", 404, []byte(`{"code":404}`), "", typeAPIErr},
		{"404 not found JSON", 404, []byte(`{"message":"resource not found","code":404}`), "resource not found", typeAPIErr},
		{"409 conflict JSON", 409, []byte(`{"message":"resource exists","code":409}`), "resource exists", typeAPIErr},
	}
}

func runEdgeCase(t *testing.T, tcase edgeCase) {
	t.Helper()

	err := pveerr.ParseAPIError(tcase.statusCode, tcase.body)
	if err == nil {
		t.Fatal("ParseAPIError() returned nil")
	}

	if gotType := typeOf(err); gotType != tcase.wantType {
		t.Errorf("type = %v, want %v", gotType, tcase.wantType)
	}

	if tcase.wantMsgPart != "" && !contains(err.Error(), tcase.wantMsgPart) {
		t.Errorf("Error() = %q, want containing %q", err.Error(), tcase.wantMsgPart)
	}
}

// TestParseAPIError_EdgeCases covers empty body, malformed JSON, nested errors map,
// missing data fields, and text/plain bodies.
func TestParseAPIError_EdgeCases(t *testing.T) {
	t.Parallel()

	for _, tcase := range edgeCases() {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()
			runEdgeCase(t, tcase)
		})
	}
}

// TestParseAPIError_SentinelIs verifies errors.Is works with sentinel vars.
func TestParseAPIError_SentinelIs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		sentinel   error
	}{
		{"401", 401, pveerr.ErrUnauthorized},
		{"403", 403, pveerr.ErrForbidden},
		{"404", 404, pveerr.ErrNotFound},
		{"409", 409, pveerr.ErrConflict},
		{"500", 500, pveerr.ErrServer},
		{"503", 503, pveerr.ErrServer},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			body := []byte(`{"message":"test","code":0}`)
			err := pveerr.ParseAPIError(tcase.statusCode, body)

			if !errors.Is(err, tcase.sentinel) {
				t.Errorf("errors.Is(%v, %v) = false, want true", err, tcase.sentinel)
			}
		})
	}
}

// TestParseAPIError_ErrorsAs verifies errors.As extracts *APIError with HTTPCode.
func TestParseAPIError_ErrorsAs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		body       []byte
	}{
		{"404 APIError", 404, []byte(`{"message":"not found","code":404}`)},
		{"409 APIError", 409, []byte(`{"message":"conflict","code":409}`)},
		{"500 APIError", 500, []byte(`{"message":"server error","code":500}`)},
		{"non-JSON 502", 502, []byte(`bad gateway`)},
	}

	for _, tcase := range tests {
		t.Run(tcase.name, func(t *testing.T) {
			t.Parallel()

			err := pveerr.ParseAPIError(tcase.statusCode, tcase.body)

			var apiErr *pveerr.APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("errors.As(*APIError) = false for %v", err)
			}

			if apiErr.HTTPCode != tcase.statusCode {
				t.Errorf("apiErr.HTTPCode = %d, want %d", apiErr.HTTPCode, tcase.statusCode)
			}
		})
	}
}

// TestParseAPIError_NestedErrorsMap verifies field-level errors surface in Error() string.
func TestParseAPIError_NestedErrorsMap(t *testing.T) {
	t.Parallel()

	body := []byte(`{"message":"param error","code":400,"errors":{"vmid":"must be positive","name":"required"}}`)
	err := pveerr.ParseAPIError(400, body)

	var paramErr *pveerr.ParameterError
	if !errors.As(err, &paramErr) {
		t.Fatalf("expected *ParameterError, got %T", err)
	}

	if !contains(paramErr.Error(), "param error") {
		t.Errorf("Error() missing base message: %q", paramErr.Error())
	}

	if len(paramErr.Errors) != 2 {
		t.Errorf("Errors map len = %d, want 2", len(paramErr.Errors))
	}
}

// TestSentinelVarsDistinct ensures sentinel vars are distinct errors.
func TestSentinelVarsDistinct(t *testing.T) {
	t.Parallel()

	sentinels := []error{
		pveerr.ErrUnauthorized,
		pveerr.ErrForbidden,
		pveerr.ErrNotFound,
		pveerr.ErrConflict,
		pveerr.ErrServer,
	}

	for idxA, sentA := range sentinels {
		for idxB, sentB := range sentinels {
			if idxA == idxB {
				continue
			}

			if errors.Is(sentA, sentB) {
				t.Errorf("sentinel[%d] (%v) matches sentinel[%d] (%v), want distinct", idxA, sentA, idxB, sentB)
			}
		}
	}
}

// -- codes.go helper tests, each in its own function to stay under funlen/gocognit --

// TestGetErrorMessage covers known and unknown status codes.
func TestGetErrorMessage(t *testing.T) {
	t.Parallel()

	cases := []struct {
		code int
		want string
	}{
		{200, "Success"},
		{201, "Created"},
		{204, "No Content"},
		{400, "Bad Request"},
		{401, "Unauthorized"},
		{403, "Forbidden"},
		{404, "Not Found"},
		{409, "Conflict"},
		{429, "Too Many Requests"},
		{500, "Internal Server Error"},
		{502, "Bad Gateway"},
		{503, "Service Unavailable"},
		{504, "Gateway Timeout"},
		{999, "Unknown Error"},
	}

	for _, tcase := range cases {
		t.Run(strconv.Itoa(tcase.code), func(t *testing.T) {
			t.Parallel()

			got := pveerr.GetErrorMessage(tcase.code)
			if got != tcase.want {
				t.Errorf("GetErrorMessage(%d) = %q, want %q", tcase.code, got, tcase.want)
			}
		})
	}
}

// TestIsSuccessCode verifies 2xx detection.
func TestIsSuccessCode(t *testing.T) {
	t.Parallel()

	if !pveerr.IsSuccessCode(200) {
		t.Error("IsSuccessCode(200) = false")
	}

	if !pveerr.IsSuccessCode(204) {
		t.Error("IsSuccessCode(204) = false")
	}

	if pveerr.IsSuccessCode(400) {
		t.Error("IsSuccessCode(400) = true")
	}
}

// TestIsClientErrorCode verifies 4xx detection.
func TestIsClientErrorCode(t *testing.T) {
	t.Parallel()

	if !pveerr.IsClientErrorCode(400) {
		t.Error("IsClientErrorCode(400) = false")
	}

	if !pveerr.IsClientErrorCode(404) {
		t.Error("IsClientErrorCode(404) = false")
	}

	if pveerr.IsClientErrorCode(500) {
		t.Error("IsClientErrorCode(500) = true")
	}
}

// TestIsServerErrorCode verifies 5xx detection.
func TestIsServerErrorCode(t *testing.T) {
	t.Parallel()

	if !pveerr.IsServerErrorCode(500) {
		t.Error("IsServerErrorCode(500) = false")
	}

	if !pveerr.IsServerErrorCode(503) {
		t.Error("IsServerErrorCode(503) = false")
	}

	if pveerr.IsServerErrorCode(400) {
		t.Error("IsServerErrorCode(400) = true")
	}
}

// TestIsRetryableCode verifies retryable code classification.
func TestIsRetryableCode(t *testing.T) {
	t.Parallel()

	for _, code := range []int{429, 502, 503, 504} {
		if !pveerr.IsRetryableCode(code) {
			t.Errorf("IsRetryableCode(%d) = false, want true", code)
		}
	}

	for _, code := range []int{400, 401, 403, 404, 500} {
		if pveerr.IsRetryableCode(code) {
			t.Errorf("IsRetryableCode(%d) = true, want false", code)
		}
	}
}

// TestConnectionError_NoMessage tests the no-message path.
func TestConnectionError_NoMessage(t *testing.T) {
	t.Parallel()

	err := pveerr.ConnectionError{Host: "h", Port: 8006}
	got := err.Error()

	if !contains(got, "h:8006") {
		t.Errorf("Error() = %q, want h:8006 present", got)
	}
}

// TestSSLError_NoCause tests SSL error without cause.
func TestSSLError_NoCause(t *testing.T) {
	t.Parallel()

	err := pveerr.SSLError{Host: "h"}
	if err.Unwrap() != nil {
		t.Errorf("Unwrap() = %v, want nil", err.Unwrap())
	}
}

// Helper functions.
func contains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}

	return false
}

func typeOf(v any) string {
	if v == nil {
		return "nil"
	}

	switch v.(type) {
	case *pveerr.APIError:
		return typeAPIErr
	case *pveerr.PermissionError:
		return "*pveerr.PermissionError"
	case *pveerr.ParameterError:
		return typeParamErr
	case *pveerr.AuthenticationError:
		return "*pveerr.AuthenticationError"
	case *pveerr.TFARequiredError:
		return "*pveerr.TFARequiredError"
	default:
		return "unknown"
	}
}
