# Context Detection Example

This example demonstrates the execution context detection feature that determines whether the client is running on a Proxmox VE node or remotely.

## Overview

Context detection helps the client optimize its behavior based on where it's running:

- **Local Mode**: Running on a PVE node - can access local resources
- **Remote Mode**: Running remotely - must use network API for all operations
- **Unknown Mode**: Detection inconclusive - treated as remote for safety

## Features

- **Multi-Factor Detection**: Uses 4 different checks with confidence scoring
- **Automatic Detection**: Opt-in auto-detection on client creation
- **Manual Override**: Can explicitly set execution mode
- **Safe Defaults**: Unknown mode treated as remote

## Detection Strategy

The detector performs four checks with different confidence levels:

| Check | Confidence | Points | Description |
|-------|------------|--------|-------------|
| `/etc/pve` exists | HIGH | +3 | PVE cluster filesystem |
| `pvesh` binary exists | MEDIUM | +2 | PVE shell utility |
| `pve-manager` installed | HIGH | +3 | Core PVE package |
| Hostname in `/etc/pve/nodes/` | HIGH | +3 | Node registration |

**Scoring Thresholds**:
- **0-2 points**: Remote (low/no PVE indicators)
- **3-5 points**: Unknown (some indicators, inconclusive)
- **6+ points**: Local (multiple strong indicators)

## Usage

### Quick Detection

```go
import pvectx "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/context"

if pvectx.IsRunningOnPVENode() {
    fmt.Println("Running on PVE node")
} else {
    fmt.Println("Running remotely")
}
```

### Detailed Detection

```go
detector := pvectx.NewDetector()
mode := detector.DetectMode()

switch mode {
case pvectx.ExecutionModeLocal:
    nodeName, _ := detector.GetNodeName()
    fmt.Printf("Local node: %s\n", nodeName)

case pvectx.ExecutionModeRemote:
    fmt.Println("Running remotely")

case pvectx.ExecutionModeUnknown:
    fmt.Println("Unknown - treating as remote")
}
```

### Client with Auto-Detection

```go
client, _ := pve.NewClient(pve.Options{
    Host:           "localhost",
    Username:       "root@pam",
    Password:       "secret",
    AutoDetectMode: true, // Enable auto-detection
})

// Client automatically adapts based on detected mode
```

### Manual Override

```go
client, _ := pve.NewClient(pve.Options{
    Host:          "pve.example.com",
    Username:      "root@pam",
    Password:      "secret",
    ExecutionMode: pve.ExecutionModeRemote, // Force remote mode
})
```

## Benefits of Detection

### Local Mode Benefits (Current & Future)

**Current**:
- Client knows it's running on a PVE node
- Can make different optimization decisions

**Future (Phase 3)**:
- Direct access to `/etc/pve/priv/authkey.key` for ticket generation
- No network overhead for authentication
- Offline operation support
- Faster authentication

### Remote Mode

- Standard API authentication
- Works from any location
- No special permissions required

## Running the Example

```bash
# On a developer workstation (remote)
go run main.go
# Output: Execution Mode: remote

# On a PVE node (local)
go run main.go
# Output: Execution Mode: local
# Output: Running on PVE node: pve-node1
```

## Detection Accuracy

The multi-factor scoring approach provides high accuracy:

- **False Positives**: Very unlikely (requires 6+ points from non-PVE indicators)
- **False Negatives**: Possible but unlikely (would need 3+ checks to fail)
- **Unknown Classification**: Acts as a safety net for edge cases

## When to Use Auto-Detection

**Use Auto-Detection When**:
- Running code that may execute on PVE nodes OR remotely
- Want automatic optimization based on environment
- Building tools that work in both contexts

**Use Manual Override When**:
- You know the execution context in advance
- Running in containerized environments (may give false positives)
- Need predictable behavior regardless of environment

## Integration with Other Features

Context detection integrates with:

1. **Auto-Login** (Phase 1): Works in both local and remote modes
2. **Local Tickets** (Phase 3 - Future): Only available in local mode
3. **Caching** (Phase 4 - Future): May have different strategies per mode

## Testing

The package includes comprehensive tests with mocked filesystems:

```bash
go test ./pkg/context -v
```

Tests cover:
- All scoring thresholds
- Individual check functions
- Error handling
- Hostname detection
- Convenience functions

## See Also

- [Auto-Login Example](../auto-login/) - Automatic authentication
- [Main Documentation](../../README.md) - Full client documentation
- [Context Package Docs](../../pkg/context/doc.go) - Package documentation
