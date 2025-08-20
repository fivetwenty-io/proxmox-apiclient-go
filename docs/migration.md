# Migration Guide: From Perl to Go PVE API Client

This guide helps you migrate from the traditional Perl-based PVE API client to this modern Go implementation.

## Table of Contents
- [Overview](#overview)
- [Key Differences](#key-differences)
- [Authentication Migration](#authentication-migration)
- [API Call Migration](#api-call-migration)
- [Error Handling](#error-handling)
- [Advanced Features](#advanced-features)
- [Common Patterns](#common-patterns)
- [Performance Considerations](#performance-considerations)

## Overview

The Go PVE API client provides a modern, type-safe, and performant alternative to the traditional Perl client. While maintaining compatibility with the PVE API, it offers improved error handling, connection pooling, and concurrent operations.

### Benefits of Migration

- **Type Safety**: Compile-time type checking reduces runtime errors
- **Performance**: Built-in concurrency and connection pooling
- **Modern Tooling**: Better IDE support, testing, and debugging
- **Cross-Platform**: Single binary deployment without dependencies
- **Resource Efficiency**: Lower memory footprint and CPU usage

## Key Differences

### Installation

**Perl Client:**
```bash
apt-get install libpve-apiclient-perl
```

**Go Client:**
```bash
go get github.com/fivetwenty-io/pve-apicilent-go
```

### Initialization

**Perl:**
```perl
use PVE::APIClient::LWP;

my $conn = PVE::APIClient::LWP->new(
    apitoken => 'USER@REALM!TOKENID=SECRET',
    host => 'pve.example.com',
    port => 8006,
    verify_ssl => 1,
);
```

**Go:**
```go
import "github.com/fivetwenty-io/pve-apicilent-go/pkg/client"

client, err := client.NewClient(&client.Options{
    Host:     "pve.example.com",
    Port:     8006,
    TokenID:  "USER@REALM!TOKENID",
    Secret:   "SECRET",
    VerifySSL: true,
})
```

## Authentication Migration

### Username/Password Authentication

**Perl:**
```perl
my $conn = PVE::APIClient::LWP->new(
    username => 'root@pam',
    password => 'secret',
    host => 'pve.example.com',
);

$conn->login();
```

**Go:**
```go
client, err := client.NewClient(&client.Options{
    Host:     "pve.example.com",
    Username: "root@pam",
    Password: "secret",
})

if err := client.Login(ctx); err != nil {
    log.Fatal(err)
}
```

### API Token Authentication

**Perl:**
```perl
my $conn = PVE::APIClient::LWP->new(
    apitoken => 'root@pam!mytoken=xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx',
    host => 'pve.example.com',
);
```

**Go:**
```go
client, err := client.NewClient(&client.Options{
    Host:    "pve.example.com",
    TokenID: "root@pam!mytoken",
    Secret:  "xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx",
})
```

### Two-Factor Authentication

**Perl:**
```perl
# Manual TFA handling required
$conn->login();
# Enter TFA code when prompted
```

**Go:**
```go
// Automatic TFA handling with callback
client, err := client.NewClient(&client.Options{
    Host:     "pve.example.com",
    Username: "user@pve",
    Password: "secret",
    TFAHandler: func() (string, error) {
        fmt.Print("Enter TFA code: ")
        var code string
        fmt.Scanln(&code)
        return code, nil
    },
})
```

## API Call Migration

### GET Requests

**Perl:**
```perl
my $nodes = $conn->get('/nodes');
foreach my $node (@$nodes) {
    print "Node: $node->{node}\n";
}
```

**Go:**
```go
var nodes []map[string]interface{}
err := client.Get(ctx, "/nodes", &nodes)
if err != nil {
    log.Fatal(err)
}

for _, node := range nodes {
    fmt.Printf("Node: %s\n", node["node"])
}
```

### POST Requests

**Perl:**
```perl
my $result = $conn->post('/nodes/pve/qemu', {
    vmid => 100,
    name => 'test-vm',
    memory => 2048,
    cores => 2,
});
```

**Go:**
```go
params := map[string]interface{}{
    "vmid":   100,
    "name":   "test-vm",
    "memory": 2048,
    "cores":  2,
}

var result map[string]interface{}
err := client.Post(ctx, "/nodes/pve/qemu", params, &result)
```

### PUT Requests

**Perl:**
```perl
$conn->put('/nodes/pve/qemu/100/config', {
    memory => 4096,
    cores => 4,
});
```

**Go:**
```go
params := map[string]interface{}{
    "memory": 4096,
    "cores":  4,
}

err := client.Put(ctx, "/nodes/pve/qemu/100/config", params, nil)
```

### DELETE Requests

**Perl:**
```perl
$conn->delete('/nodes/pve/qemu/100');
```

**Go:**
```go
err := client.Delete(ctx, "/nodes/pve/qemu/100", nil)
```

## Error Handling

### Perl Error Handling

```perl
eval {
    my $result = $conn->get('/nodes');
};
if ($@) {
    print "Error: $@\n";
}
```

### Go Error Handling

```go
result, err := client.Get(ctx, "/nodes", nil)
if err != nil {
    switch e := err.(type) {
    case *errors.APIError:
        fmt.Printf("API Error %d: %s\n", e.Code, e.Message)
    case *errors.NetworkError:
        fmt.Printf("Network Error: %s\n", e.Message)
    default:
        fmt.Printf("Error: %s\n", err)
    }
}
```

## Advanced Features

### Connection Pooling (Go Only)

```go
import "github.com/fivetwenty-io/pve-apicilent-go/pkg/pool"

// Create connection pool
pool, err := pool.New(&pool.Config{
    MaxConnections: 10,
    MaxIdle:        5,
    IdleTimeout:    30 * time.Second,
})

client.SetTransport(pool.Transport())
```

### Request Batching (Go Only)

```go
import "github.com/fivetwenty-io/pve-apicilent-go/pkg/batch"

// Create batch
batch := batch.New(nil)
batch.Add(&batch.Request{
    Method: "GET",
    Path:   "/nodes",
})
batch.Add(&batch.Request{
    Method: "GET",
    Path:   "/cluster/status",
})

// Execute batch
executor := batch.NewExecutor(client, nil)
result, err := executor.Execute(ctx, batch)
```

### Response Streaming (Go Only)

```go
import "github.com/fivetwenty-io/pve-apicilent-go/pkg/stream"

// Stream large responses
resp, err := client.GetRaw(ctx, "/nodes/pve/tasks")
stream := stream.NewFromResponse(resp, nil)
defer stream.Close()

for {
    item, err := stream.Read()
    if err == io.EOF {
        break
    }
    // Process item
}
```

### WebSocket Support (Go Only)

```go
import "github.com/fivetwenty-io/pve-apicilent-go/pkg/websocket"

// Create WebSocket client
ws, err := websocket.New(&websocket.Config{
    Host: "pve.example.com",
    Port: 8006,
})

// Set authentication
ws.SetAuth(client.GetTicket(), client.GetCSRFToken())

// Connect and listen for events
err = ws.Connect(ctx)
ws.On("vm-status", func(event *websocket.Event) {
    fmt.Printf("VM Status: %v\n", event.Data)
})
```

## Common Patterns

### Listing VMs

**Perl:**
```perl
my $vms = $conn->get('/cluster/resources', {type => 'vm'});
foreach my $vm (@$vms) {
    print "$vm->{vmid}: $vm->{name}\n";
}
```

**Go:**
```go
params := map[string]interface{}{"type": "vm"}
var vms []map[string]interface{}
err := client.Get(ctx, "/cluster/resources", &vms, client.WithParams(params))

for _, vm := range vms {
    fmt.Printf("%v: %s\n", vm["vmid"], vm["name"])
}
```

### Creating a VM

**Perl:**
```perl
my $task = $conn->post('/nodes/pve/qemu', {
    vmid => 100,
    name => 'test-vm',
    memory => 2048,
    cores => 2,
    net0 => 'virtio,bridge=vmbr0',
    ide2 => 'local-lvm:cloudinit',
});

# Wait for task
wait_for_task($task->{data});
```

**Go:**
```go
params := map[string]interface{}{
    "vmid":   100,
    "name":   "test-vm",
    "memory": 2048,
    "cores":  2,
    "net0":   "virtio,bridge=vmbr0",
    "ide2":   "local-lvm:cloudinit",
}

var task map[string]interface{}
err := client.Post(ctx, "/nodes/pve/qemu", params, &task)

// Wait for task
err = client.WaitForTask(ctx, task["data"].(string))
```

### Backup Operations

**Perl:**
```perl
my $task = $conn->post('/nodes/pve/vzdump', {
    vmid => 100,
    storage => 'backup',
    mode => 'snapshot',
    compress => 'zstd',
});
```

**Go:**
```go
params := map[string]interface{}{
    "vmid":     100,
    "storage":  "backup",
    "mode":     "snapshot",
    "compress": "zstd",
}

var task map[string]interface{}
err := client.Post(ctx, "/nodes/pve/vzdump", params, &task)
```

## Performance Considerations

### Connection Reuse

**Perl:** Creates new connection for each request
```perl
# Each call creates new HTTPS connection
$conn->get('/nodes');
$conn->get('/cluster/status');
```

**Go:** Reuses connections automatically
```go
// Connections are pooled and reused
client.Get(ctx, "/nodes", nil)
client.Get(ctx, "/cluster/status", nil)
```

### Concurrent Operations

**Perl:** Sequential execution
```perl
foreach my $node (@nodes) {
    my $status = $conn->get("/nodes/$node->{node}/status");
    # Process status
}
```

**Go:** Concurrent execution
```go
var wg sync.WaitGroup
for _, node := range nodes {
    wg.Add(1)
    go func(nodeName string) {
        defer wg.Done()
        var status map[string]interface{}
        client.Get(ctx, fmt.Sprintf("/nodes/%s/status", nodeName), &status)
        // Process status
    }(node["node"].(string))
}
wg.Wait()
```

### Memory Usage

- **Perl Client**: Higher memory overhead due to interpreter
- **Go Client**: Lower memory footprint, efficient garbage collection

### Response Processing

**Perl:** Parse JSON for each response
```perl
use JSON;
my $json = JSON->new;
my $data = $json->decode($response);
```

**Go:** Automatic JSON marshaling
```go
var data MyStruct
err := client.Get(ctx, "/api/path", &data)
// data is automatically populated
```

## Migration Checklist

- [ ] Install Go client library
- [ ] Update authentication configuration
- [ ] Convert API calls to Go syntax
- [ ] Implement proper error handling
- [ ] Add context support for cancellation
- [ ] Test connection pooling
- [ ] Verify SSL certificate handling
- [ ] Update deployment scripts
- [ ] Test in staging environment
- [ ] Monitor performance improvements

## Troubleshooting

### SSL Certificate Issues

**Perl:**
```perl
# Disable SSL verification (not recommended)
my $conn = PVE::APIClient::LWP->new(
    verify_ssl => 0,
    ...
);
```

**Go:**
```go
// Better: Use fingerprint verification
client, err := client.NewClient(&client.Options{
    Host:        "pve.example.com",
    Fingerprint: "XX:XX:XX:XX:XX:XX:XX:XX:XX:XX:XX:XX:XX:XX:XX:XX",
})
```

### Timeout Configuration

**Go:** Fine-grained timeout control
```go
client, err := client.NewClient(&client.Options{
    Host:           "pve.example.com",
    RequestTimeout: 30 * time.Second,
    DialTimeout:    10 * time.Second,
})
```

### Debug Logging

**Go:** Built-in debug support
```go
client, err := client.NewClient(&client.Options{
    Host:  "pve.example.com",
    Debug: true,
    Logger: log.New(os.Stdout, "PVE: ", log.LstdFlags),
})
```

## Getting Help

- [API Documentation](https://pve.proxmox.com/pve-docs/api-viewer/)
- [Go Client Examples](examples/)
- [Issue Tracker](https://github.com/fivetwenty-io/pve-apicilent-go/issues)
- [Proxmox Forum](https://forum.proxmox.com/)

## Next Steps

1. Review the [examples](examples/) directory for complete working examples
2. Check the [compatibility matrix](compatibility.md) for version-specific features
3. Run the [benchmark suite](benchmarks/) to compare performance
4. Join the community for support and best practices
