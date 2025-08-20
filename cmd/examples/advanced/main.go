package main

import (
	"crypto/x509"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/fivetwenty-io/pve-apicilent-go/internal/ssl"
	pve "github.com/fivetwenty-io/pve-apicilent-go/pkg/client"
	"github.com/fivetwenty-io/pve-apicilent-go/pkg/errors"
)

func main() {
	fmt.Println("PVE API Client - Advanced Examples")
	fmt.Println("==================================")

	host := os.Getenv("PVE_HOST")
	if host == "" {
		host = "localhost"
	}

	// Example 1: SSL/TLS Configuration
	fmt.Println("\n1. SSL/TLS Configuration:")
	demonstrateSSLConfiguration()

	// Example 2: Error Handling
	fmt.Println("\n2. Error Handling:")
	demonstrateErrorHandling()

	// Example 3: Client with Custom Options
	fmt.Println("\n3. Custom Client Configuration:")
	demonstrateCustomClient(host)

	// Example 4: Fingerprint Management
	fmt.Println("\n4. Certificate Fingerprint Management:")
	demonstrateFingerprintManagement()

	fmt.Println("\n✅ Advanced examples completed!")
}

func demonstrateSSLConfiguration() {
	// SSL verification modes
	modes := []struct {
		name string
		mode pve.SSLVerifyMode
	}{
		{"No verification (insecure)", pve.SSLVerifyNone},
		{"Verify peer certificate", pve.SSLVerifyPeer},
		{"Verify hostname", pve.SSLVerifyHost},
		{"Full verification", pve.SSLVerifyFull},
	}

	for _, m := range modes {
		fmt.Printf("  • %s (mode: %d)\n", m.name, m.mode)
	}

	// Example SSL options
	sslOpts := &pve.SSLOptions{
		VerifyHostname: true,
		VerifyMode:     pve.SSLVerifyPeer,
		CACert:         "/path/to/ca.crt",
		ClientCert:     "/path/to/client.crt",
		ClientKey:      "/path/to/client.key",
	}

	fmt.Println("\n  Example SSL configuration:")
	fmt.Printf("    - Verify Hostname: %v\n", sslOpts.VerifyHostname)
	fmt.Printf("    - Verify Mode: %v\n", sslOpts.VerifyMode)
	fmt.Printf("    - CA Certificate: %s\n", sslOpts.CACert)
	fmt.Printf("    - Client Certificate: %s\n", sslOpts.ClientCert)
	fmt.Printf("    - Client Key: %s\n", sslOpts.ClientKey)
}

func demonstrateErrorHandling() {
	// Create various error types
	apiErr := &errors.APIError{
		Message:  "API operation failed",
		Code:     500,
		HTTPCode: 500,
	}

	authErr := &errors.AuthenticationError{
		APIError: errors.APIError{
			Message:  "Invalid credentials",
			Code:     401,
			HTTPCode: 401,
		},
		Realm: "pam",
	}

	tfaErr := &errors.TFARequiredError{
		Ticket: "partial-ticket",
		Types:  []string{"totp", "recovery"},
	}

	permErr := &errors.PermissionError{
		APIError: errors.APIError{
			Message:  "Access denied",
			Code:     403,
			HTTPCode: 403,
		},
		What: "/vms/100",
	}

	connErr := &errors.ConnectionError{
		Host:    "pve.example.com",
		Port:    8006,
		Message: "Connection refused",
	}

	timeoutErr := &errors.TimeoutError{
		Operation: "login",
		Duration:  "30s",
	}

	// Demonstrate error checking
	testErrors := []error{apiErr, authErr, tfaErr, permErr, connErr, timeoutErr}

	for _, err := range testErrors {
		fmt.Printf("\n  Error: %v\n", err)

		// Check error types
		if errors.IsAPIError(err) {
			fmt.Println("    → Is API Error")
			if apiErr, ok := err.(*errors.APIError); ok {
				if apiErr.IsNotFound() {
					fmt.Println("      • Resource not found")
				}
				if apiErr.IsUnauthorized() {
					fmt.Println("      • Unauthorized")
				}
				if apiErr.IsForbidden() {
					fmt.Println("      • Forbidden")
				}
			}
		}
		if errors.IsTFARequired(err) {
			fmt.Println("    → TFA Required")
		}
		if errors.IsConnectionError(err) {
			fmt.Println("    → Connection Error")
		}
		if errors.IsTimeoutError(err) {
			fmt.Println("    → Timeout Error")
		}
	}

	// Demonstrate error code checking
	fmt.Println("\n  Error Code Analysis:")
	codes := []int{200, 401, 403, 404, 429, 500, 503}
	for _, code := range codes {
		fmt.Printf("    Code %d: ", code)
		if errors.IsSuccessCode(code) {
			fmt.Print("Success")
		} else if errors.IsClientErrorCode(code) {
			fmt.Print("Client Error")
		} else if errors.IsServerErrorCode(code) {
			fmt.Print("Server Error")
		}
		if errors.IsRetryableCode(code) {
			fmt.Print(" (Retryable)")
		}
		fmt.Printf(" - %s\n", errors.GetErrorMessage(code))
	}
}

func demonstrateCustomClient(host string) {
	// Create client with all custom options
	opts := pve.Options{
		Host:     host,
		Port:     8006,
		Protocol: "https",

		// Use environment variables if available
		Username: os.Getenv("PVE_USERNAME"),
		Password: os.Getenv("PVE_PASSWORD"),
		APIToken: os.Getenv("PVE_API_TOKEN"),

		// Timeouts and connection settings
		Timeout:   45 * time.Second,
		KeepAlive: 15,

		// SSL configuration
		SSLOptions: &pve.SSLOptions{
			VerifyHostname: false,
			VerifyMode:     pve.SSLVerifyNone, // For testing only!
		},

		// Cached fingerprints
		CachedFingerprints: map[string]bool{
			"AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99": true,
		},

		// Manual verification callback
		VerifyFingerprintCallback: func(cert *x509.Certificate) bool {
			fmt.Printf("    🔐 Certificate verification requested\n")
			fmt.Printf("       Subject: %s\n", cert.Subject)
			fmt.Printf("       Issuer: %s\n", cert.Issuer)
			// In production, you would properly verify the certificate
			return true
		},

		// Cookie name (usually default is fine)
		CookieName: "PVEAuthCookie",
	}

	fmt.Println("  Custom client configuration:")
	fmt.Printf("    - Base URL: %s\n", opts.GetBaseURL())
	fmt.Printf("    - Timeout: %v\n", opts.Timeout)
	fmt.Printf("    - Keep-Alive Connections: %d\n", opts.KeepAlive)
	fmt.Printf("    - Using API Token: %v\n", opts.IsUsingAPIToken())
	fmt.Printf("    - Using Ticket: %v\n", opts.IsUsingTicket())
	fmt.Printf("    - Needs Login: %v\n", opts.NeedsLogin())

	// Try to create the client
	if opts.Username != "" || opts.APIToken != "" {
		client, err := pve.NewClient(opts)
		if err != nil {
			log.Printf("    ✗ Failed to create client: %v", err)
		} else {
			fmt.Println("    ✓ Client created successfully")

			// You can modify client settings after creation
			client.SetTimeout(60 * time.Second)
			client.SetKeepAlive(20)
			fmt.Println("    ✓ Updated timeout and keep-alive settings")
		}
	} else {
		fmt.Println("    ⚠ Skipped client creation (no credentials)")
	}
}

func demonstrateFingerprintManagement() {
	// Create a fingerprint verifier
	verifier := ssl.NewFingerprintVerifier()

	// Add trusted fingerprints
	trustedFingerprints := []string{
		"AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99",
		"11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00",
	}

	verifier.AddTrustedFingerprints(trustedFingerprints)
	fmt.Printf("  Added %d trusted fingerprints\n", len(trustedFingerprints))

	// Demonstrate fingerprint normalization
	fmt.Println("\n  Fingerprint normalization examples:")
	variations := []string{
		"aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99:aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99",
		"AABBCCDDEEFF00112233445566778899AABBCCDDEEFF00112233445566778899",
		"AA-BB-CC-DD-EE-FF-00-11-22-33-44-55-66-77-88-99-AA-BB-CC-DD-EE-FF-00-11-22-33-44-55-66-77-88-99",
		"AA BB CC DD EE FF 00 11 22 33 44 55 66 77 88 99 AA BB CC DD EE FF 00 11 22 33 44 55 66 77 88 99",
	}

	for _, fp := range variations {
		normalized := ssl.NormalizeFingerprint(fp)
		fmt.Printf("    Input:  %.30s...\n", fp)
		fmt.Printf("    Output: %.30s...\n", normalized)
		fmt.Println()
	}

	// Compare fingerprints
	fp1 := "AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99:AA:BB:CC:DD:EE:FF:00:11:22:33:44:55:66:77:88:99"
	fp2 := "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"

	if ssl.CompareFingerprints(fp1, fp2) {
		fmt.Println("  ✓ Fingerprints match (different formats, same value)")
	} else {
		fmt.Println("  ✗ Fingerprints don't match")
	}

	// Get all trusted fingerprints
	trusted := verifier.GetTrustedFingerprints()
	fmt.Printf("\n  Currently trusted fingerprints: %d\n", len(trusted))
	for i, fp := range trusted {
		fmt.Printf("    %d. %.50s...\n", i+1, fp)
	}

	// Clear cache
	verifier.ClearCache()
	fmt.Println("\n  ✓ Fingerprint cache cleared")
}
