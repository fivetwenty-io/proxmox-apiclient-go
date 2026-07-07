package main

import (
	"fmt"
	"log"

	pve "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
)

const exampleHost = "pve.example.com"

func main() {
	fmt.Println("=== Auto-Login Example ===")
	fmt.Println()

	demonstrateAutoLogin()
	demonstrateManualLogin()
	demonstrateTokenAuth()

	printSummary()
}

// demonstrateAutoLogin shows how auto-login enables automatic authentication on first API call.
func demonstrateAutoLogin() {
	fmt.Println("Example 1: With Auto-Login (convenient)")

	clientWithAutoLogin, err := pve.NewClient(pve.Options{
		Host:      exampleHost,
		Username:  "root@pam",
		Password:  "your-password",
		AutoLogin: true, // Enable auto-login
	})
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	// No explicit Login() call needed - authentication happens automatically
	// on the first API request
	fmt.Println("Making first API call (auto-login will happen automatically)...")

	status, err := clientWithAutoLogin.Get("/cluster/status", nil)
	if err != nil {
		log.Fatalf("API call failed: %v", err)
	}

	fmt.Printf("✓ Auto-login successful! Cluster status: %v\n\n", status)
}

// demonstrateManualLogin shows the traditional explicit login approach.
func demonstrateManualLogin() {
	fmt.Println("Example 2: Manual Login (traditional)")

	clientManual, err := pve.NewClient(pve.Options{
		Host:     exampleHost,
		Username: "root@pam",
		Password: "your-password",
		// AutoLogin: false (default)
	})
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	// Explicit login call
	fmt.Println("Explicitly logging in...")

	err = clientManual.Login()
	if err != nil {
		log.Fatalf("Login failed: %v", err)
	}

	fmt.Println("✓ Manual login successful!")

	// Now make API calls
	nodes, err := clientManual.Get("/nodes", nil)
	if err != nil {
		log.Fatalf("API call failed: %v", err)
	}

	fmt.Printf("✓ API call successful! Nodes: %v\n\n", nodes)
}

// demonstrateTokenAuth shows API token authentication which bypasses auto-login.
func demonstrateTokenAuth() {
	fmt.Println("Example 3: API Token Authentication")

	clientToken, err := pve.NewClient(pve.Options{
		Host:      exampleHost,
		APIToken:  "user@pam!tokenid=secret-value",
		AutoLogin: true, // This is ignored for API tokens
	})
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	// API token authentication doesn't require login
	resources, err := clientToken.Get("/cluster/resources", nil)
	if err != nil {
		log.Fatalf("API call failed: %v", err)
	}

	fmt.Printf("✓ Token auth successful! Resources: %v\n\n", resources)
}

// printSummary displays key takeaways about auto-login functionality.
func printSummary() {
	fmt.Println("=== Examples Complete ===")
	fmt.Println("\nKey Points:")
	fmt.Println("1. AutoLogin=true: Authenticates automatically on first API call")
	fmt.Println("2. AutoLogin=false (default): Requires explicit Login() call")
	fmt.Println("3. Auto-login only applies to username/password auth")
	fmt.Println("4. API tokens and pre-existing tickets don't use auto-login")
	fmt.Println("5. Auto-login is thread-safe for concurrent first requests")
}
