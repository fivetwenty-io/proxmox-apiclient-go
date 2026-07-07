# LXC Container Management Example

This example demonstrates comprehensive LXC container management using the `pkg/api/lxc` package.

## Overview

The LXC API provides full lifecycle management for Linux Containers in Proxmox VE, including creation, configuration, cloning, and deletion operations.

## Features

- List all LXC containers on a node
- Create containers from OS templates
- Start, stop, shutdown, and reboot containers
- Get real-time container status
- Retrieve and update container configuration
- Clone containers (full or linked)
- Resize container disks
- Delete containers with optional purge

## Basic Usage

```go
import (
    pve "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/client"
    "github.com/fivetwenty-io/proxmox-apiclient-go/v3/pkg/api/lxc"
)

// Create clients
client, _ := pve.NewClient(pve.Options{
    Host:      "pve.example.com",
    Username:  "root@pam",
    Password:  "secret",
    AutoLogin: true,
})

lxcClient := lxc.NewClient(client, "pve") // Node name
ctx := context.Background()

// List containers
containers, _ := lxcClient.List(ctx)
for _, ct := range containers {
    fmt.Printf("CT %d: %s (%s)\n", ct.VMID, ct.Name, ct.Status)
}
```

## Common Operations

### Create Container

```go
config := lxc.ContainerConfig{
    VMID:         200,
    OSTemplate:   "local:vztmpl/ubuntu-22.04-standard_22.04-1_amd64.tar.zst",
    Hostname:     "test-ct",
    Description:  "Test container",
    Memory:       1024,  // MB
    Swap:         512,   // MB
    Cores:        2,
    RootFS:       "local-lvm:8",  // 8GB
    Net0:         "name=eth0,bridge=vmbr0,ip=dhcp",
    Unprivileged: true,
    Password:     "secret",
    Start:        false,
}

upid, err := lxcClient.Create(ctx, config)
// upid is a task ID - use tasks API to monitor
```

### Get Status

```go
status, err := lxcClient.Status(ctx, 200)
fmt.Printf("Status: %s\n", status.Status)
fmt.Printf("Uptime: %d seconds\n", status.Uptime)
fmt.Printf("Memory: %d MB / %d MB\n",
    status.Mem/(1024*1024),
    status.MaxMem/(1024*1024))
```

### Start/Stop/Reboot

```go
// Start container
upid, err := lxcClient.Start(ctx, 200)

// Graceful shutdown with 60 second timeout
upid, err = lxcClient.Shutdown(ctx, 200, 60)

// Forceful stop
upid, err = lxcClient.Stop(ctx, 200)

// Reboot
upid, err = lxcClient.Reboot(ctx, 200)
```

### Update Configuration

```go
updates := map[string]interface{}{
    "memory":      2048,  // Increase to 2GB
    "cores":       4,     // Increase to 4 cores
    "description": "Updated via API",
}

err := lxcClient.UpdateConfig(ctx, 200, updates)
```

### Clone Container

```go
cloneOpts := lxc.CloneOptions{
    Hostname:    "test-ct-clone",
    Description: "Clone of test-ct",
    Full:        true,  // Full copy (not linked)
    Storage:     "local-lvm",
}

upid, err := lxcClient.Clone(ctx, 200, 201, cloneOpts)
```

### Resize Disk

```go
// Add 2GB to rootfs
err := lxcClient.Resize(ctx, 200, "rootfs", "+2G")

// Set absolute size
err = lxcClient.Resize(ctx, 200, "rootfs", "10G")
```

### Delete Container

```go
// Delete with purge (removes all data)
upid, err := lxcClient.Delete(ctx, 200, true)

// Delete without purge (keeps backup)
upid, err = lxcClient.Delete(ctx, 200, false)
```

## Container Configuration

### ContainerConfig Fields

| Field | Type | Description |
|-------|------|-------------|
| VMID | int | Container ID (required) |
| OSTemplate | string | OS template path (required) |
| Hostname | string | Container hostname |
| Description | string | Container description |
| Memory | int | RAM in MB |
| Swap | int | Swap in MB |
| Cores | int | CPU cores |
| CPULimit | int | CPU limit (0-128) |
| CPUUnits | int | CPU weight |
| RootFS | string | Root filesystem (e.g., "local:8") |
| Net0 | string | Network configuration |
| Unprivileged | bool | Unprivileged container |
| Features | map | Features (nesting, fuse, etc.) |
| Password | string | Root password |
| SSHKeys | string | SSH public keys |
| Nameserver | string | DNS nameserver |
| Searchdomain | string | DNS search domain |
| Start | bool | Start after creation |
| Storage | string | Target storage |
| Pool | string | Resource pool |

### Network Configuration

```go
// DHCP
Net0: "name=eth0,bridge=vmbr0,ip=dhcp"

// Static IP
Net0: "name=eth0,bridge=vmbr0,ip=192.168.1.100/24,gw=192.168.1.1"

// With VLAN
Net0: "name=eth0,bridge=vmbr0,tag=10,ip=dhcp"
```

### Features

```go
Features: map[string]string{
    "nesting": "1",  // Enable container nesting
    "fuse":    "1",  // Enable FUSE
    "keyctl":  "1",  // Enable keyctl
}
```

## Task Monitoring

Most operations return a task UPID. Monitor task completion:

```go
// Start container and monitor task
upid, err := lxcClient.Start(ctx, 200)

// Use tasks API to monitor (if implemented)
// status := tasksClient.GetStatus(ctx, upid)
// for status != "stopped" {
//     time.Sleep(1 * time.Second)
//     status = tasksClient.GetStatus(ctx, upid)
// }
```

## Best Practices

### Container Creation

1. **Use Templates**: Always create from OS templates stored in PVE
2. **Set Unprivileged**: Use unprivileged containers for better security
3. **Configure Network**: Specify network configuration at creation
4. **Set Resource Limits**: Define memory and CPU limits upfront

### Resource Management

```go
// Conservative resource allocation
config := lxc.ContainerConfig{
    Memory:   512,   // Start small
    Swap:     256,
    Cores:    1,
    CPULimit: 50,    // 50% CPU limit
}

// Scale up as needed via UpdateConfig
```

### Cloning Strategy

```go
// Linked clone (faster, less space)
cloneOpts := lxc.CloneOptions{
    Full: false,  // Linked clone
}

// Full clone (independent copy)
cloneOpts := lxc.CloneOptions{
    Full:    true,
    Storage: "local-lvm",  // Specify target storage
}
```

### Shutdown vs Stop

```go
// Prefer graceful shutdown with timeout
upid, err := lxcClient.Shutdown(ctx, vmid, 60)

// Use forceful stop only when necessary
if err != nil {
    upid, err = lxcClient.Stop(ctx, vmid)
}
```

## Error Handling

```go
// Check for specific errors
upid, err := lxcClient.Start(ctx, 200)
if err != nil {
    if strings.Contains(err.Error(), "already running") {
        fmt.Println("Container is already running")
    } else if strings.Contains(err.Error(), "does not exist") {
        fmt.Println("Container not found")
    } else {
        return fmt.Errorf("failed to start: %w", err)
    }
}
```

## Integration with Other APIs

### With Caching

```go
// Enable caching for read operations
client, _ := pve.NewClient(pve.Options{
    Host:        "pve.example.com",
    Username:    "root@pam",
    Password:    "secret",
    AutoLogin:   true,
    CacheConfig: &pve.CacheConfig{
        Enabled:    true,
        DefaultTTL: 30 * time.Second,
    },
})

lxcClient := lxc.NewClient(client, "pve")

// Status calls are cached
status1, _ := lxcClient.Status(ctx, 200)  // API call
status2, _ := lxcClient.Status(ctx, 200)  // Cached

// Invalidate cache after modifications
client.InvalidateCache("/nodes/pve/lxc/200/*")
```

### With Context Detection

```go
// Auto-detect execution context
client, _ := pve.NewClient(pve.Options{
    Host:           "localhost",
    Username:       "root@pam",
    Password:       "secret",
    AutoDetectMode: true,  // Detect local vs remote
})

lxcClient := lxc.NewClient(client, "pve")
// Works optimally in both local and remote contexts
```

## Troubleshooting

### Container Won't Start

**Check**: Configuration and resource availability

```go
config, err := lxcClient.GetConfig(ctx, vmid)
// Verify:
// - rootfs exists and has space
// - memory/CPU within node limits
// - network bridge exists
```

### Cannot Delete Container

**Check**: Container is stopped and has no locks

```go
status, err := lxcClient.Status(ctx, vmid)
if status.Status != "stopped" {
    // Stop first
    lxcClient.Stop(ctx, vmid)
    time.Sleep(5 * time.Second)
}
```

### Clone Fails

**Check**: Source container is stopped (for full clone)

```go
// Stop source before full clone
lxcClient.Stop(ctx, sourceVMID)
time.Sleep(5 * time.Second)

cloneOpts := lxc.CloneOptions{Full: true}
upid, err := lxcClient.Clone(ctx, sourceVMID, newVMID, cloneOpts)
```

## Running the Example

```bash
# Edit main.go with your PVE details
# Ensure you have:
# - Valid PVE host and credentials
# - OS template available
# - Sufficient storage space

go run main.go
```

## See Also

- [Main Documentation](../../README.md) - Full client documentation
- [Auto-Login Example](../auto-login/) - Automatic authentication
- [Caching Example](../caching/) - Request caching
- [LXC Package Docs](../../pkg/api/lxc/) - API reference
