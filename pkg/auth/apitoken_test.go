package auth_test

import (
	"testing"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/auth"
)

func TestNewAPITokenAuthenticator(t *testing.T) {
	t.Parallel()

	token := &auth.Token{
		ID:     testTokenID,
		Secret: testTokenSecret,
	}

	authenticator := auth.NewAPITokenAuthenticator(token, "")
	if authenticator == nil {
		t.Fatal("NewAPITokenAuthenticator returned nil")
	}

	if authenticator.GetToken() != token {
		t.Errorf("NewAPITokenAuthenticator() token = %v, want %v", authenticator.GetToken(), token)
	}
}

type tokenFromStringTest struct {
	name        string
	tokenString string
	wantErr     bool
	wantID      string
	wantSecret  string
}

func getTokenFromStringTestCases() []tokenFromStringTest {
	return []tokenFromStringTest{
		{
			name:        testCaseValidToken,
			tokenString: testTokenIDSecret,
			wantErr:     false,
			wantID:      testTokenID,
			wantSecret:  testTokenSecret,
		},
		{
			name:        "empty string",
			tokenString: "",
			wantErr:     true,
		},
		{
			name:        "missing secret",
			tokenString: testTokenID,
			wantErr:     true,
		},
		{
			name:        "invalid format",
			tokenString: "invalid-token-format",
			wantErr:     true,
		},
	}
}

func runTokenFromStringTest(t *testing.T, testCase tokenFromStringTest) {
	t.Helper()

	authenticator, err := auth.NewAPITokenAuthenticatorFromString(testCase.tokenString)

	if testCase.wantErr {
		if err == nil {
			t.Errorf("NewAPITokenAuthenticatorFromString() expected error, got nil")
		}

		return
	}

	if err != nil {
		t.Errorf("NewAPITokenAuthenticatorFromString() unexpected error = %v", err)

		return
	}

	if authenticator == nil {
		t.Fatal("NewAPITokenAuthenticatorFromString() returned nil authenticator")
	}

	token := authenticator.GetToken()
	if token.ID != testCase.wantID {
		t.Errorf("token.ID = %v, want %v", token.ID, testCase.wantID)
	}

	if token.Secret != testCase.wantSecret {
		t.Errorf("token.Secret = %v, want %v", token.Secret, testCase.wantSecret)
	}
}

func TestNewAPITokenAuthenticatorFromString(t *testing.T) {
	t.Parallel()

	tests := getTokenFromStringTestCases()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runTokenFromStringTest(t, tc)
		})
	}
}

func TestAPITokenAuthenticator_Authenticate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		token   *auth.Token
		wantErr bool
	}{
		{
			name: testCaseValidToken,
			token: &auth.Token{
				ID:     testTokenID,
				Secret: testSecretPass,
			},
			wantErr: false,
		},
		{
			name:    testCaseNilToken,
			token:   nil,
			wantErr: true,
		},
		{
			name: testCaseEmptyID,
			token: &auth.Token{
				ID:     "",
				Secret: testSecretPass,
			},
			wantErr: true,
		},
		{
			name: testCaseEmptySecret,
			token: &auth.Token{
				ID:     testTokenID,
				Secret: "",
			},
			wantErr: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			authenticator := auth.NewAPITokenAuthenticator(testCase.token, "")

			err := authenticator.Authenticate()
			if testCase.wantErr {
				if err == nil {
					t.Errorf("Authenticate() expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("Authenticate() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestAPITokenAuthenticator_IsAuthenticated(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		token    *auth.Token
		expected bool
	}{
		{
			name: testCaseValidToken,
			token: &auth.Token{
				ID:     testTokenID,
				Secret: testSecretPass,
			},
			expected: true,
		},
		{
			name:     testCaseNilToken,
			token:    nil,
			expected: false,
		},
		{
			name: testCaseEmptyID,
			token: &auth.Token{
				ID:     "",
				Secret: testSecretPass,
			},
			expected: false,
		},
		{
			name: testCaseEmptySecret,
			token: &auth.Token{
				ID:     testTokenID,
				Secret: "",
			},
			expected: false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			authenticator := auth.NewAPITokenAuthenticator(testCase.token, "")

			result := authenticator.IsAuthenticated()
			if result != testCase.expected {
				t.Errorf("IsAuthenticated() = %v, want %v", result, testCase.expected)
			}
		})
	}
}

func TestAPITokenAuthenticator_GetHeaders(t *testing.T) {
	t.Parallel()

	token := &auth.Token{
		ID:     testTokenID,
		Secret: testTokenSecret,
	}

	authenticator := auth.NewAPITokenAuthenticator(token, "")
	headers := authenticator.GetHeaders()

	expected := "PVEAPIToken=" + testTokenIDSecret
	if headers["Authorization"] != expected {
		t.Errorf("GetHeaders()[Authorization] = %v, want %v", headers["Authorization"], expected)
	}

	// Test with unauthenticated
	authenticator = auth.NewAPITokenAuthenticator(nil, "")

	headers = authenticator.GetHeaders()
	if headers != nil {
		t.Errorf("GetHeaders() with nil token = %v, want nil", headers)
	}
}

type parseAPITokenTest struct {
	name       string
	tokenStr   string
	wantErr    bool
	wantID     string
	wantSecret string
}

func getParseAPITokenTestCases() []parseAPITokenTest {
	return []parseAPITokenTest{
		{
			name:       testCaseValidToken,
			tokenStr:   testTokenIDSecret,
			wantErr:    false,
			wantID:     testTokenID,
			wantSecret: testTokenSecret,
		},
		{
			name:     "empty string",
			tokenStr: "",
			wantErr:  true,
		},
		{
			name:     "missing equals",
			tokenStr: testTokenID,
			wantErr:  true,
		},
		{
			name:     "missing realm",
			tokenStr: "root!mytoken=secret",
			wantErr:  true,
		},
		{
			name:     "missing token name",
			tokenStr: "root@pam=secret",
			wantErr:  true,
		},
		{
			name:     testCaseEmptySecret,
			tokenStr: "root@pam!mytoken=",
			wantErr:  true,
		},
	}
}

func runParseAPITokenTest(t *testing.T, testCase parseAPITokenTest) {
	t.Helper()

	token, err := auth.ParseAPIToken(testCase.tokenStr)

	if testCase.wantErr {
		if err == nil {
			t.Errorf("ParseAPIToken() expected error, got nil")
		}

		return
	}

	if err != nil {
		t.Errorf("ParseAPIToken() unexpected error = %v", err)

		return
	}

	if token.ID != testCase.wantID {
		t.Errorf("ParseAPIToken() ID = %v, want %v", token.ID, testCase.wantID)
	}

	if token.Secret != testCase.wantSecret {
		t.Errorf("ParseAPIToken() Secret = %v, want %v", token.Secret, testCase.wantSecret)
	}
}

func TestParseAPIToken(t *testing.T) {
	t.Parallel()

	tests := getParseAPITokenTestCases()

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			runParseAPITokenTest(t, testCase)
		})
	}
}

func TestFormatAPIToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		token    *auth.Token
		expected string
	}{
		{
			name: testCaseValidToken,
			token: &auth.Token{
				ID:     testTokenID,
				Secret: testTokenSecret,
			},
			expected: testTokenIDSecret,
		},
		{
			name:     testCaseNilToken,
			token:    nil,
			expected: "",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result := auth.FormatAPIToken(testCase.token)
			if result != testCase.expected {
				t.Errorf("FormatAPIToken() = %v, want %v", result, testCase.expected)
			}
		})
	}
}

func TestValidateTokenID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{
			name:    "valid ID",
			id:      testTokenID,
			wantErr: false,
		},
		{
			name:    testCaseEmptyID,
			id:      "",
			wantErr: true,
		},
		{
			name:    "missing @",
			id:      "rootpam!mytoken",
			wantErr: true,
		},
		{
			name:    "missing !",
			id:      "root@pammytoken",
			wantErr: true,
		},
		{
			name:    "empty user",
			id:      "@pam!mytoken",
			wantErr: true,
		},
		{
			name:    "empty realm",
			id:      "root@!mytoken",
			wantErr: true,
		},
		{
			name:    "empty token name",
			id:      "root@pam!",
			wantErr: true,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			err := auth.ValidateTokenID(testCase.id)
			if testCase.wantErr {
				if err == nil {
					t.Errorf("ValidateTokenID() expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("ValidateTokenID() unexpected error = %v", err)
				}
			}
		})
	}
}

func TestAPITokenAuthenticator_GetHeaders_FormatDetection(t *testing.T) {
	t.Parallel()

	tests := buildFormatDetectionTestCases()

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			token := &auth.Token{
				ID:     testCase.tokenID,
				Secret: testCase.tokenSecret,
			}

			authenticator := auth.NewAPITokenAuthenticator(token, testCase.customTokenName)
			headers := authenticator.GetHeaders()

			assertAuthorizationHeader(t, headers, testCase.expectedAuthHeader, testCase.description)
		})
	}
}

// buildFormatDetectionTestCases creates test cases for format detection validation.
func buildFormatDetectionTestCases() []struct {
	name               string
	tokenID            string
	tokenSecret        string
	customTokenName    string
	expectedAuthHeader string
	description        string
} {
	return []struct {
		name               string
		tokenID            string
		tokenSecret        string
		customTokenName    string
		expectedAuthHeader string
		description        string
	}{
		{
			name:               "default token name with raw token",
			tokenID:            testTokenID,
			tokenSecret:        testTokenSecret,
			customTokenName:    "",
			expectedAuthHeader: "PVEAPIToken=root@pam!mytoken=secret-value",
			description:        "Should add PVEAPIToken= prefix to raw token",
		},
		{
			name:               "custom token name",
			tokenID:            testTokenID,
			tokenSecret:        testTokenSecret,
			customTokenName:    "CustomAuth",
			expectedAuthHeader: "CustomAuth=root@pam!mytoken=secret-value",
			description:        "Should use custom token name prefix",
		},
		{
			name:               "bearer token format",
			tokenID:            testTokenID,
			tokenSecret:        testTokenSecret,
			customTokenName:    "Bearer",
			expectedAuthHeader: "Bearer=root@pam!mytoken=secret-value",
			description:        "Should support Bearer token format",
		},
		{
			name:               "pre-formatted token should not double-prefix",
			tokenID:            "PVEAPIToken=user@pam!token",
			tokenSecret:        testSecretPass,
			customTokenName:    "PVEAPIToken",
			expectedAuthHeader: "PVEAPIToken=user@pam!token=secret",
			description:        "Already formatted tokens should not be double-prefixed",
		},
	}
}

// assertAuthorizationHeader validates that headers contain the expected Authorization value.
func assertAuthorizationHeader(t *testing.T, headers map[string]string, expected, description string) {
	t.Helper()

	if headers == nil {
		t.Fatal("GetHeaders() returned nil")
	}

	authHeader, ok := headers["Authorization"]
	if !ok {
		t.Fatal("GetHeaders() missing Authorization header")
	}

	if authHeader != expected {
		t.Errorf("GetHeaders()[Authorization] = %v, want %v. %s", authHeader, expected, description)
	}
}

func TestAPITokenAuthenticator_CustomTokenName(t *testing.T) {
	t.Parallel()

	token := &auth.Token{
		ID:     testTokenID,
		Secret: testTokenSecret,
	}

	tests := []struct {
		name           string
		tokenName      string
		expectedPrefix string
	}{
		{
			name:           "empty token name defaults to PVEAPIToken",
			tokenName:      "",
			expectedPrefix: "PVEAPIToken=",
		},
		{
			name:           "explicit PVEAPIToken",
			tokenName:      "PVEAPIToken",
			expectedPrefix: "PVEAPIToken=",
		},
		{
			name:           "custom Bearer token",
			tokenName:      "Bearer",
			expectedPrefix: "Bearer=",
		},
		{
			name:           "custom API token",
			tokenName:      "X-API-Token",
			expectedPrefix: "X-API-Token=",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			authenticator := auth.NewAPITokenAuthenticator(token, testCase.tokenName)
			headers := authenticator.GetHeaders()

			authHeader := headers["Authorization"]
			if !hasPrefix(authHeader, testCase.expectedPrefix) {
				t.Errorf("Authorization header %q does not start with expected prefix %q",
					authHeader, testCase.expectedPrefix)
			}
		})
	}
}

// hasPrefix checks if a string has the given prefix.
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}
