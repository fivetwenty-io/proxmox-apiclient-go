# PVE API Client Compatibility Matrix

This document provides a comprehensive compatibility matrix for the PVE API Go client across different Proxmox VE versions.

## Supported PVE Versions

| PVE Version | Support Status | Notes |
|-------------|---------------|-------|
| 8.1.x | ✅ Full Support | Latest features including notification system |
| 8.0.x | ✅ Full Support | Enhanced firewall and SDN features |
| 7.4.x | ✅ Full Support | Recommended minimum version |
| 7.3.x | ✅ Full Support | SDN support introduced |
| 7.2.x | ✅ Full Support | Backup fleecing available |
| 7.1.x | ✅ Full Support | VM/CT tagging support |
| 7.0.x | ✅ Full Support | PBS integration introduced |
| 6.4.x | ⚠️ Limited Support | Approaching end of life |
| 6.3.x | ⚠️ Limited Support | Backup notes available |
| 6.2.x | ⚠️ Limited Support | Cloud-init and API tokens |
| 6.1.x | ⚠️ Limited Support | Enhanced pool permissions |
| 6.0.x | ⚠️ Limited Support | Consider upgrading |
| < 6.0 | ❌ Not Supported | Please upgrade |

## Feature Availability Matrix

### Authentication Features

| Feature | 6.0 | 6.1 | 6.2 | 6.3 | 6.4 | 7.0 | 7.1 | 7.2 | 7.3 | 7.4 | 8.0 | 8.1 |
|---------|-----|-----|-----|-----|-----|-----|-----|-----|-----|-----|-----|-----|
| Username/Password | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| API Tokens | ❌ | ❌ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Two-Factor Auth | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| WebAuthn/FIDO2 | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ |

### Virtual Machine Features

| Feature | 6.0 | 6.1 | 6.2 | 6.3 | 6.4 | 7.0 | 7.1 | 7.2 | 7.3 | 7.4 | 8.0 | 8.1 |
|---------|-----|-----|-----|-----|-----|-----|-----|-----|-----|-----|-----|-----|
| Basic VM Operations | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Cloud-Init | ❌ | ❌ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| VM Tags | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Live Migration (NBD) | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Remote Migration | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| CPU Models v2 | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ | ✅ |

### Storage Features

| Feature | 6.0 | 6.1 | 6.2 | 6.3 | 6.4 | 7.0 | 7.1 | 7.2 | 7.3 | 7.4 | 8.0 | 8.1 |
|---------|-----|-----|-----|-----|-----|-----|-----|-----|-----|-----|-----|-----|
| Storage Content API | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| PBS Integration | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Backup Notes | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Backup Fleecing | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Ceph Quincy | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Ceph Reef | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |

### Network Features

| Feature | 6.0 | 6.1 | 6.2 | 6.3 | 6.4 | 7.0 | 7.1 | 7.2 | 7.3 | 7.4 | 8.0 | 8.1 |
|---------|-----|-----|-----|-----|-----|-----|-----|-----|-----|-----|-----|-----|
| Basic Networking | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| SDN (Zones/VNets) | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ | ✅ | ✅ |
| Firewall IPSets | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Firewall IPSets v2 | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ | ✅ |

### Advanced Features

| Feature | 6.0 | 6.1 | 6.2 | 6.3 | 6.4 | 7.0 | 7.1 | 7.2 | 7.3 | 7.4 | 8.0 | 8.1 |
|---------|-----|-----|-----|-----|-----|-----|-----|-----|-----|-----|-----|-----|
| Pool Permissions | ❌ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ | ✅ |
| Notification System | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |
| PCI Device Mapping | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ❌ | ✅ |

## API Endpoint Changes

### Deprecated Endpoints

| Endpoint | Deprecated In | Removed In | Replacement |
|----------|--------------|------------|-------------|
| `/nodes/{node}/openvz` | PVE 4.0 | PVE 6.0 | `/nodes/{node}/lxc` |
| `/nodes/{node}/vzdump` | PVE 5.0 | PVE 6.0 | `/nodes/{node}/backup` |

### New Endpoints by Version

#### PVE 7.0
- `/cluster/backup-info` - Cluster-wide backup information
- `/nodes/{node}/certificates` - Certificate management
- `/access/openid` - OpenID Connect support

#### PVE 7.3
- `/cluster/sdn` - Software Defined Networking
- `/cluster/sdn/zones` - SDN zones
- `/cluster/sdn/vnets` - Virtual networks

#### PVE 8.0
- `/cluster/firewall/ipsets` (v2) - Enhanced IP sets
- `/cluster/metrics` - Metrics configuration

#### PVE 8.1
- `/cluster/notifications` - Notification configuration
- `/cluster/mapping/pci` - PCI device mapping

## Client Feature Support

| Client Feature | Implementation Status | Notes |
|----------------|---------------------|-------|
| Connection Pooling | ✅ Implemented | Improves performance |
| Request Batching | ✅ Implemented | Bulk operations support |
| Response Streaming | ✅ Implemented | For large datasets |
| WebSocket Support | ✅ Implemented | Real-time events |
| Metrics Collection | ✅ Implemented | Prometheus format |
| Automatic Retries | ✅ Implemented | With exponential backoff |
| TLS Fingerprinting | ✅ Implemented | Security feature |
| Compression | ✅ Implemented | Reduces bandwidth |

## Usage Examples

### Checking Version Compatibility

```go
import "github.com/fivetwenty-io/pve-apicilent-go/pkg/compatibility"

// Check PVE version
checker, err := compatibility.NewChecker("7.4-3")
if err != nil {
    log.Fatal(err)
}

// Check if a feature is supported
supported, message := checker.Check("sdn")
if !supported {
    log.Printf("SDN not supported: %s", message)
}

// Get all supported features
features := checker.GetSupportedFeatures()
fmt.Printf("Supported features: %v\n", features)
```

### Generating Compatibility Report

```go
// Generate report for current PVE version
report, err := compatibility.GenerateReport("8.1-2")
if err != nil {
    log.Fatal(err)
}

fmt.Printf("PVE Version: %s\n", report.PVEVersion)
fmt.Printf("Supported Features: %d\n", len(report.SupportedFeatures))
fmt.Printf("Warnings: %v\n", report.Warnings)
fmt.Printf("Recommendations: %v\n", report.Recommendations)
```

### Migration Planning

```go
// Plan migration from PVE 6.4 to 7.4
helper, err := compatibility.NewMigrationHelper("6.4-1", "7.4-1")
if err != nil {
    log.Fatal(err)
}

// Get migration steps
steps := helper.GetMigrationSteps()
fmt.Println("Migration steps:")
for _, step := range steps {
    fmt.Printf("- %s\n", step)
}

// Get new features available after migration
newFeatures := helper.GetNewFeatures()
fmt.Printf("New features after migration: %v\n", newFeatures)

// Check for breaking changes
breakingChanges := helper.GetBreakingChanges()
if len(breakingChanges) > 0 {
    fmt.Println("Breaking changes:")
    for _, change := range breakingChanges {
        fmt.Printf("⚠️ %s\n", change)
    }
}
```

### Configuration Validation

```go
// Validate configuration for target PVE version
config := map[string]interface{}{
    "sdn": map[string]interface{}{
        "zones": []string{"zone1", "zone2"},
    },
}

version := &compatibility.Version{Major: 7, Minor: 3, Patch: 0}
valid, issues := compatibility.ValidateConfiguration(config, version)

if !valid {
    fmt.Println("Configuration issues:")
    for _, issue := range issues {
        fmt.Printf("- %s\n", issue)
    }
}
```

## Breaking Changes

### PVE 6 → 7
- Corosync 3 required for clustering
- Minimum kernel version: 5.x
- OpenVZ support removed
- Changed backup job format

### PVE 7 → 8
- New notification system (replaces email-only)
- Firewall IPSet API v2
- Some Perl modules replaced with Rust
- Changed metrics collection

## Recommendations

### For PVE 6.x Users
- Plan upgrade to PVE 7.x or 8.x
- Test API token authentication
- Prepare for OpenVZ → LXC migration

### For PVE 7.x Users
- Upgrade to 7.4 for latest features
- Test SDN features if needed
- Prepare for notification system changes

### For PVE 8.x Users
- Configure new notification system
- Utilize enhanced SDN features
- Test PCI device mapping

## Testing Compatibility

Run the compatibility test suite:

```bash
go test ./pkg/compatibility/...
```

Run benchmarks:

```bash
go test -bench=. ./pkg/compatibility/...
```

## Support Policy

- **Full Support**: Current and previous major version
- **Limited Support**: Two major versions back
- **No Support**: Three or more major versions back

## Getting Help

- [API Documentation](https://pve.proxmox.com/pve-docs/api-viewer/)
- [Proxmox Forum](https://forum.proxmox.com/)
- [Issue Tracker](https://github.com/fivetwenty-io/pve-apicilent-go/issues)

## Contributing

When adding support for new PVE features:

1. Update the compatibility matrix in `pkg/compatibility/compatibility.go`
2. Add tests in `pkg/compatibility/compatibility_test.go`
3. Update this documentation
4. Test against target PVE version