# PVE API Client for Go

A Go client library for the Proxmox Virtual Environment (PVE) API.

## Features

- Full authentication support (username/password, API tokens, TFA)
- SSL/TLS certificate verification with fingerprint support
- Complete HTTP method coverage (GET, POST, PUT, DELETE)
- Error handling with detailed error types
- Connection pooling and keep-alive support
- Comprehensive test coverage

## Installation

```bash
go get github.com/fivetwenty-io/pve-apicilent-go
```

## Quick Start

```go
package main

import (
    "fmt"
    "log"
    
    pve "github.com/fivetwenty-io/pve-apicilent-go/pkg/client"
)

func main() {
    client, err := pve.NewClient(pve.Options{
        Host:     "pve.example.com",
        Username: "root@pam",
        Password: "secret",
    })
    if err != nil {
        log.Fatal(err)
    }
    
    // Get cluster status
    status, err := client.Get("/cluster/status", nil)
    if err != nil {
        log.Fatal(err)
    }
    
    fmt.Printf("Cluster status: %v\n", status)
}
```

## Documentation

See the [documentation](https://pkg.go.dev/github.com/fivetwenty-io/pve-apicilent-go) for detailed API reference.

## Examples

Check the `cmd/examples/` directory for more comprehensive examples:

- `basic/` - Basic authentication and API calls
- `auth/` - Advanced authentication scenarios including TFA
- `advanced/` - Advanced features like batching and streaming

## Development

### Building

```bash
make build
```

### Testing

```bash
make test
```

### Running all checks

```bash
make all
```

## License

See LICENSE file for details.

## Contributing

Contributions are welcome! Please read the contributing guidelines before submitting PRs.