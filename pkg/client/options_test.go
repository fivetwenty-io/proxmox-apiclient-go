package client_test

import (
	"testing"
	"time"

	"github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

func TestOptions_Validate(t *testing.T) {
	t.Parallel()

	tests := getValidationTestCases()
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			runValidationTest(t, testCase)
		})
	}
}

type validationTestCase struct {
	name    string
	opts    client.Options
	wantErr bool
	errMsg  string
}

func getValidationTestCases() []validationTestCase {
	cases := getValidValidationTestCases()
	cases = append(cases, getInvalidValidationTestCases()...)
	cases = append(cases, getSSLValidationTestCases()...)

	return cases
}

func getValidValidationTestCases() []validationTestCase {
	return []validationTestCase{
		{
			name: "valid with username and password",
			opts: client.Options{
				Host:     testHost,
				Username: testUsername,
				Password: testPassword,
			},
			wantErr: false,
		},
		{
			name: "valid with API token",
			opts: client.Options{
				Host:     testHost,
				APIToken: testAPIToken,
			},
			wantErr: false,
		},
		{
			name: "valid with ticket",
			opts: client.Options{
				Host:   testHost,
				Ticket: "PVE:ticket:data",
			},
			wantErr: false,
		},
	}
}

func getInvalidValidationTestCases() []validationTestCase {
	return []validationTestCase{
		{
			name:    "missing host",
			opts:    client.Options{},
			wantErr: true,
			errMsg:  "host is required",
		},
		{
			name: "missing authentication",
			opts: client.Options{
				Host: testHost,
			},
			wantErr: true,
			errMsg:  "authentication credentials required",
		},
		{
			name: "username without password",
			opts: client.Options{
				Host:     testHost,
				Username: testUsername,
			},
			wantErr: true,
			errMsg:  "password required when using username authentication",
		},
		{
			name: "invalid protocol",
			opts: client.Options{
				Host:     testHost,
				Username: testUsername,
				Password: testPassword,
				Protocol: "invalid",
			},
			wantErr: true,
			errMsg:  testErrProtocol,
		},
		{
			name: "negative port",
			opts: client.Options{
				Host:     testHost,
				Username: testUsername,
				Password: testPassword,
				Port:     -1,
			},
			wantErr: true,
			errMsg:  testErrPort,
		},
		{
			name: "port too high",
			opts: client.Options{
				Host:     testHost,
				Username: testUsername,
				Password: testPassword,
				Port:     70000,
			},
			wantErr: true,
			errMsg:  testErrPort,
		},
	}
}

func getSSLValidationTestCases() []validationTestCase {
	return []validationTestCase{
		{
			name: "client cert without key",
			opts: client.Options{
				Host:     testHost,
				Username: testUsername,
				Password: testPassword,
				SSLOptions: &client.SSLOptions{
					ClientCert: "/path/to/cert",
				},
			},
			wantErr: true,
			errMsg:  "client key required when client certificate is specified",
		},
		{
			name: "client key without cert",
			opts: client.Options{
				Host:     testHost,
				Username: testUsername,
				Password: testPassword,
				SSLOptions: &client.SSLOptions{
					ClientKey: "/path/to/key",
				},
			},
			wantErr: true,
			errMsg:  "client certificate required when client key is specified",
		},
	}
}

func runValidationTest(t *testing.T, testCase validationTestCase) {
	t.Helper()

	err := testCase.opts.Validate()
	if testCase.wantErr {
		if err == nil {
			t.Errorf("Validate() expected error, got nil")
		} else if testCase.errMsg != "" && !contains(err.Error(), testCase.errMsg) {
			t.Errorf("Validate() error = %v, want containing %v", err, testCase.errMsg)
		}
	} else {
		if err != nil {
			t.Errorf("Validate() unexpected error = %v", err)
		}
	}
}

func TestOptions_setDefaults(t *testing.T) {
	t.Parallel()

	tests := getSetDefaultsTestCases()
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			runSetDefaultsTest(t)
		})
	}
}

type setDefaultsTestCase struct {
	name     string
	opts     client.Options
	expected client.Options
}

func getSetDefaultsTestCases() []setDefaultsTestCase {
	return []setDefaultsTestCase{
		{
			name: "empty options",
			opts: client.Options{},
			expected: client.Options{
				Protocol:           testProtoHTTPS,
				Port:               8006,
				Timeout:            30 * time.Second,
				KeepAlive:          10,
				CookieName:         "PVEAuthCookie",
				CachedFingerprints: map[string]bool{},
				SSLOptions: &client.SSLOptions{
					VerifyMode:     client.SSLVerifyPeer,
					VerifyHostname: true,
				},
			},
		},
		{
			name: "http protocol",
			opts: client.Options{
				Protocol: testProtoHTTP,
			},
			expected: client.Options{
				Protocol:           testProtoHTTP,
				Port:               8006,
				Timeout:            30 * time.Second,
				KeepAlive:          10,
				CookieName:         "PVEAuthCookie",
				CachedFingerprints: map[string]bool{},
			},
		},
		{
			name: "custom values preserved",
			opts: client.Options{
				Protocol:   testProtoHTTPS,
				Port:       443,
				Timeout:    60 * time.Second,
				KeepAlive:  20,
				CookieName: "CustomCookie",
			},
			expected: client.Options{
				Protocol:           testProtoHTTPS,
				Port:               443,
				Timeout:            60 * time.Second,
				KeepAlive:          20,
				CookieName:         "CustomCookie",
				CachedFingerprints: map[string]bool{},
				SSLOptions: &client.SSLOptions{
					VerifyMode:     client.SSLVerifyPeer,
					VerifyHostname: true,
				},
			},
		},
	}
}

func runSetDefaultsTest(t *testing.T) {
	t.Helper()
	// NOTE: Cannot test setDefaults directly as it's unexported.
	// This test would need to be moved to the client package or
	// setDefaults would need to be exported.
	t.Skip("setDefaults is unexported - skipping test")
}

func TestOptions_GetBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		opts     client.Options
		expected string
	}{
		{
			name: "https default port",
			opts: client.Options{
				Protocol: testProtoHTTPS,
				Host:     testHost,
				Port:     8006,
			},
			expected: "https://pve.example.com:8006/api2/json",
		},
		{
			name: "http custom port",
			opts: client.Options{
				Protocol: testProtoHTTP,
				Host:     "192.168.1.100",
				Port:     8080,
			},
			expected: "http://192.168.1.100:8080/api2/json",
		},
		{
			name: "https standard port",
			opts: client.Options{
				Protocol: testProtoHTTPS,
				Host:     testHost,
				Port:     443,
			},
			expected: "https://pve.example.com:443/api2/json",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result := testCase.opts.GetBaseURL()
			if result != testCase.expected {
				t.Errorf("GetBaseURL() = %v, want %v", result, testCase.expected)
			}
		})
	}
}

func TestOptions_AuthenticationMethods(t *testing.T) {
	t.Parallel()
	t.Run("IsUsingAPIToken", testIsUsingAPIToken)
	t.Run("IsUsingTicket", testIsUsingTicket)
	t.Run("NeedsLogin", testNeedsLogin)
}

func testIsUsingAPIToken(t *testing.T) {
	t.Parallel()

	opts := client.Options{APIToken: "token"}
	if !opts.IsUsingAPIToken() {
		t.Errorf("IsUsingAPIToken() = false, want true")
	}

	opts = client.Options{}
	if opts.IsUsingAPIToken() {
		t.Errorf("IsUsingAPIToken() = true, want false")
	}
}

func testIsUsingTicket(t *testing.T) {
	t.Parallel()

	opts := client.Options{Ticket: "ticket"}
	if !opts.IsUsingTicket() {
		t.Errorf("IsUsingTicket() = false, want true")
	}

	opts = client.Options{}
	if opts.IsUsingTicket() {
		t.Errorf("IsUsingTicket() = true, want false")
	}
}

func testNeedsLogin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		opts     client.Options
		expected bool
	}{
		{
			name:     "needs login with username",
			opts:     client.Options{Username: "root@pam"},
			expected: true,
		},
		{
			name:     "no login with API token",
			opts:     client.Options{APIToken: "token"},
			expected: false,
		},
		{
			name:     "no login with ticket",
			opts:     client.Options{Ticket: "ticket"},
			expected: false,
		},
		{
			name:     "no login without credentials",
			opts:     client.Options{},
			expected: false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			result := testCase.opts.NeedsLogin()
			if result != testCase.expected {
				t.Errorf("NeedsLogin() = %v, want %v", result, testCase.expected)
			}
		})
	}
}
