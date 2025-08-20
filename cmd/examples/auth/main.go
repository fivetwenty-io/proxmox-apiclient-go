package main

import (
	"fmt"
	"log"
	"os"

	"github.com/fivetwenty-io/pve-apicilent-go/pkg/auth"
	pve "github.com/fivetwenty-io/pve-apicilent-go/pkg/client"
)

func main() {
	fmt.Println("PVE API Client - Authentication Examples")
	fmt.Println("========================================")

	host := os.Getenv("PVE_HOST")
	if host == "" {
		host = "localhost"
	}

	// Example 1: Username/Password Authentication
	fmt.Println("\n1. Username/Password Authentication:")
	if password := os.Getenv("PVE_PASSWORD"); password != "" {
		username := os.Getenv("PVE_USERNAME")
		if username == "" {
			username = "root@pam"
		}

		opts := pve.Options{
			Host:     host,
			Username: username,
			Password: password,
		}

		client, err := pve.NewClient(opts)
		if err != nil {
			log.Printf("Failed to create client with username/password: %v", err)
		} else {
			fmt.Printf("  ✓ Successfully authenticated as %s\n", username)

			// Test an API call
			if version, err := client.Get("/version", nil); err == nil {
				fmt.Printf("  ✓ API Version: %v\n", version)
			}

			// Logout
			if err := client.Logout(); err == nil {
				fmt.Println("  ✓ Successfully logged out")
			}
		}
	} else {
		fmt.Println("  ⚠ Skipped: Set PVE_PASSWORD to test")
	}

	// Example 2: API Token Authentication
	fmt.Println("\n2. API Token Authentication:")
	if apiToken := os.Getenv("PVE_API_TOKEN"); apiToken != "" {
		opts := pve.Options{
			Host:     host,
			APIToken: apiToken,
		}

		client, err := pve.NewClient(opts)
		if err != nil {
			log.Printf("Failed to create client with API token: %v", err)
		} else {
			fmt.Println("  ✓ Successfully authenticated with API token")

			// Test an API call
			if version, err := client.Get("/version", nil); err == nil {
				fmt.Printf("  ✓ API Version: %v\n", version)
			}

			// Note: API tokens don't need logout
			fmt.Println("  ℹ API tokens don't require logout")
		}
	} else {
		fmt.Println("  ⚠ Skipped: Set PVE_API_TOKEN to test")
	}

	// Example 3: Parse and validate API token
	fmt.Println("\n3. API Token Parsing:")
	exampleToken := "root@pam!mytoken=12345678-90ab-cdef-1234-567890abcdef"
	token, err := auth.ParseAPIToken(exampleToken)
	if err != nil {
		log.Printf("Failed to parse token: %v", err)
	} else {
		fmt.Printf("  ✓ Parsed token ID: %s\n", token.ID)
		fmt.Printf("  ✓ Token secret length: %d chars\n", len(token.Secret))
	}

	// Example 4: Validate token ID format
	fmt.Println("\n4. Token ID Validation:")
	validIDs := []string{
		"root@pam!mytoken",
		"user@pve!api-access",
		"automation@ldap!ci-token",
	}
	invalidIDs := []string{
		"root",         // Missing realm and token name
		"root@pam",     // Missing token name
		"root!mytoken", // Missing realm
	}

	fmt.Println("  Valid token IDs:")
	for _, id := range validIDs {
		if err := auth.ValidateTokenID(id); err == nil {
			fmt.Printf("    ✓ %s\n", id)
		}
	}

	fmt.Println("  Invalid token IDs:")
	for _, id := range invalidIDs {
		if err := auth.ValidateTokenID(id); err != nil {
			fmt.Printf("    ✗ %s: %v\n", id, err)
		}
	}

	// Example 5: Interactive credentials (demonstration only)
	fmt.Println("\n5. Interactive Authentication (Demo):")
	fmt.Println("  ℹ In a real scenario, you would use:")
	fmt.Println("    - auth.PromptUsername() to get username")
	fmt.Println("    - auth.PromptPassword() to get password securely")
	fmt.Println("    - auth.PromptCredentials() to get both")

	// Example 6: TFA Types
	fmt.Println("\n6. Two-Factor Authentication Types:")
	tfaTypes := []string{
		string(auth.TFATypeTOTP),
		string(auth.TFATypeYubico),
		string(auth.TFATypeRecovery),
		string(auth.TFATypeU2F),
		string(auth.TFATypeWebAuthn),
	}
	for _, tfaType := range tfaTypes {
		fmt.Printf("  • %s\n", tfaType)
	}

	fmt.Println("\n✅ Authentication examples completed!")
}
