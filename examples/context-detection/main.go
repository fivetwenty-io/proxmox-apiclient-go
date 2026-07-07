package main

import (
	"fmt"
	"log"

	pve "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
	pvectx "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/context"
)

func main() {
	fmt.Println("=== Execution Context Detection Example ===")
	fmt.Println()

	demonstrateManualDetection()
	demonstrateConvenienceFunctions()
	demonstrateAutoDetection()
	demonstrateManualOverride()
	demonstrateDetectionDetails()

	printDetectionSummary()
}

// demonstrateManualDetection shows explicit context detection and mode interpretation.
func demonstrateManualDetection() {
	fmt.Println("Example 1: Manual Context Detection")

	detector := pvectx.NewDetector()
	mode := detector.DetectMode()

	fmt.Printf("Execution Mode: %s\n", mode)

	switch mode {
	case pvectx.ExecutionModeLocal:
		nodeName, err := detector.GetNodeName()
		if err != nil {
			log.Printf("Warning: Could not get node name: %v\n", err)
		} else {
			fmt.Printf("✓ Running on PVE node: %s\n", nodeName)
		}

		fmt.Println("  - Can use local ticket generation")
		fmt.Println("  - Can access /etc/pve directly")
		fmt.Println("  - No network overhead for some operations")

	case pvectx.ExecutionModeRemote:
		fmt.Println("✓ Running remotely (not on a PVE node)")
		fmt.Println("  - Must use API authentication")
		fmt.Println("  - All operations via network API")

	case pvectx.ExecutionModeUnknown:
		fmt.Println("⚠ Execution mode unknown (inconclusive detection)")
		fmt.Println("  - Treating as remote for safety")
		fmt.Println("  - Will use API authentication")
	}

	fmt.Println()
}

// demonstrateConvenienceFunctions shows simple boolean check for PVE node execution.
func demonstrateConvenienceFunctions() {
	fmt.Println("Example 2: Convenience Functions")

	if pvectx.IsRunningOnPVENode() {
		fmt.Println("✓ Quick check: Running on PVE node")
	} else {
		fmt.Println("✓ Quick check: Running remotely or unknown")
	}

	fmt.Println()
}

// demonstrateAutoDetection shows client with automatic context detection enabled.
func demonstrateAutoDetection() {
	fmt.Println("Example 3: Client with Auto-Detection")

	client, err := pve.NewClient(pve.Options{
		Host:           "localhost", // or specific node name
		Username:       "root@pam",
		Password:       "secret",
		AutoDetectMode: true, // Enable automatic context detection
	})
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	fmt.Println("✓ Client created with auto-detection enabled")
	fmt.Println("  The client will automatically detect if running on a PVE node")
	fmt.Println("  and optimize authentication accordingly")

	// Prevent unused variable warnings
	_ = client

	fmt.Println()
}

// demonstrateManualOverride shows explicit execution mode configuration.
func demonstrateManualOverride() {
	fmt.Println("Example 4: Manual Execution Mode Override")

	clientManual, err := pve.NewClient(pve.Options{
		Host:          "pve.example.com",
		Username:      "root@pam",
		Password:      "secret",
		ExecutionMode: pve.ExecutionModeRemote, // Explicitly set to remote
		// AutoDetectMode: false (default)
	})
	if err != nil {
		log.Fatalf("Failed to create client: %v", err)
	}

	fmt.Println("✓ Client created with manual execution mode")
	fmt.Println("  ExecutionMode explicitly set to Remote")
	fmt.Println("  Auto-detection bypassed")

	// Prevent unused variable warnings
	_ = clientManual

	fmt.Println()
}

// demonstrateDetectionDetails explains the detection algorithm and scoring.
func demonstrateDetectionDetails() {
	fmt.Println("Example 5: Detection Details")

	fmt.Println("Detection checks performed:")
	fmt.Println("  1. /etc/pve directory exists: HIGH confidence (+3 points)")
	fmt.Println("  2. pvesh binary exists: MEDIUM confidence (+2 points)")
	fmt.Println("  3. pve-manager package installed: HIGH confidence (+3 points)")
	fmt.Println("  4. Hostname matches /etc/pve/nodes/<hostname>: HIGH confidence (+3 points)")
	fmt.Println()
	fmt.Println("Scoring:")
	fmt.Println("  0-2 points → Remote")
	fmt.Println("  3-5 points → Unknown")
	fmt.Println("  6+ points  → Local")

	fmt.Println()
}

// printDetectionSummary displays key takeaways about context detection.
func printDetectionSummary() {
	fmt.Println("=== Examples Complete ===")
	fmt.Println("\nKey Points:")
	fmt.Println("1. Auto-detection is opt-in (AutoDetectMode: true)")
	fmt.Println("2. Can manually override with ExecutionMode field")
	fmt.Println("3. Unknown mode is treated as remote for safety")
	fmt.Println("4. Local mode enables optimizations (future: local ticket gen)")
}
