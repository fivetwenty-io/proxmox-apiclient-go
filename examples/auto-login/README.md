# Auto-Login Example

This example demonstrates the auto-login feature introduced in proxmox-apiclient-go (pve-apiclient-go) v3.1.0.

## Overview

Auto-login provides convenient automatic authentication for username/password based authentication. When enabled, the client automatically logs in on the first API call, eliminating the need for an explicit `Login()` call.

## Features

- **Convenience**: No need to manually call `Login()` before making API requests
- **Thread-Safe**: Concurrent first requests are handled safely with mutex protection
- **Backward Compatible**: Auto-login is disabled by default (opt-in feature)
- **Smart**: Only applies to username/password auth, not API tokens or pre-existing tickets

## Usage

### With Auto-Login (Recommended for Simple Scripts)

```go
client, _ := pve.NewClient(pve.Options{
    Host:      "pve.example.com",
    Username:  "root@pam",
    Password:  "your-password",
    AutoLogin: true, // Enable auto-login
})

// No Login() call needed - authentication happens automatically
status, _ := client.Get("/cluster/status", nil)
```

### Without Auto-Login (Traditional Approach)

```go
client, _ := pve.NewClient(pve.Options{
    Host:     "pve.example.com",
    Username: "root@pam",
    Password: "your-password",
    // AutoLogin: false (default)
})

// Explicit login required
client.Login()

// Now make API calls
status, _ := client.Get("/cluster/status", nil)
```

## When to Use Auto-Login

**Use Auto-Login When:**
- Writing simple automation scripts
- You want convenience over explicit control
- Using username/password authentication
- Don't need to handle login failures separately

**Use Manual Login When:**
- You need explicit control over authentication timing
- Want to handle login errors separately from API errors
- Need to pre-authenticate before a critical operation
- Building complex applications with custom auth flows

## Authentication Methods Compatibility

| Auth Method | Auto-Login Applies | Notes |
|-------------|-------------------|-------|
| Username/Password | ✅ Yes | Auto-login on first request |
| API Token | ❌ No | Token auth doesn't require login |
| Pre-existing Ticket | ❌ No | Already authenticated |

## Thread Safety

Auto-login is thread-safe for concurrent requests:

```go
client, _ := pve.NewClient(pve.Options{
    Host:      "pve.example.com",
    Username:  "root@pam",
    Password:  "your-password",
    AutoLogin: true,
})

// Multiple concurrent first requests are safe
go client.Get("/cluster/status", nil)
go client.Get("/nodes", nil)
go client.Get("/cluster/resources", nil)

// Only one login attempt will be made (mutex protected)
```

## Running the Example

```bash
# Set your PVE credentials
export PVE_HOST="pve.example.com"
export PVE_USER="root@pam"
export PVE_PASS="your-password"

# Run the example
go run main.go
```

## Error Handling

```go
client, _ := pve.NewClient(pve.Options{
    Host:      "pve.example.com",
    Username:  "root@pam",
    Password:  "wrong-password",
    AutoLogin: true,
})

// Auto-login failure will be returned as API error
_, err := client.Get("/cluster/status", nil)
if err != nil {
    // Error includes "auto-login failed:" prefix
    log.Fatalf("Request failed: %v", err)
}
```

## Best Practices

1. **Use Auto-Login for Scripts**: Simple automation scripts benefit from the convenience
2. **Use Manual Login for Apps**: Complex applications should use explicit `Login()` for better control
3. **Handle Errors**: Always check errors on API calls (where auto-login may fail)
4. **Don't Mix**: Use either auto-login OR manual login, not both
5. **Secure Credentials**: Never hardcode credentials, use environment variables or secret management

## See Also

- [Basic Example](../basic/) - Basic authentication without auto-login
- [Auth Example](../auth/) - Advanced authentication scenarios
- [Main Documentation](../../README.md) - Full client documentation
