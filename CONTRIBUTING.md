# Contributing to pve-apiclient-go

Thank you for your interest in contributing to the Proxmox VE API Go Client!

## Code of Conduct

Please be respectful and constructive in all interactions.

## How to Contribute

### Reporting Bugs

1. Check if the issue already exists
2. Create a new issue with:
   - Clear title and description
   - Steps to reproduce
   - Expected vs actual behavior
   - Go version and PVE version
   - Minimal code example

### Suggesting Features

1. Check existing issues and discussions
2. Open an issue describing:
   - The use case
   - Proposed solution
   - Alternative solutions considered

### Pull Requests

1. Fork the repository
2. Create a feature branch from `develop`
3. Make your changes
4. Add/update tests
5. Run tests and linters
6. Commit with conventional commits
7. Push and create a PR

## Development Setup

### Prerequisites

- Go 1.25.0 or later
- Make
- Git

### Getting Started

```bash
# Clone the repository
git clone https://github.com/fivetwenty-io/pve-apicilent-go.git
cd pve-apiclient-go

# Install dependencies
go mod download

# Run tests
make test

# Run linters
make lint

# Run all checks
make check
```

## Testing

### Running Tests

```bash
# Run all tests
make test

# Run with coverage
make coverage

# Run specific package tests
go test -v ./pkg/client

# Run with race detection
go test -race ./...
```

### Writing Tests

- Write unit tests for all new code
- Aim for >80% coverage
- Use table-driven tests
- Mock external dependencies
- Test error cases

Example test:

```go
func TestNewClient(t *testing.T) {
    tests := []struct {
        name    string
        opts    Options
        wantErr bool
    }{
        {
            name: "valid options",
            opts: Options{Host: "pve.example.com"},
            wantErr: false,
        },
        // Add more test cases
    }
    
    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            _, err := NewClient(tt.opts)
            if (err != nil) != tt.wantErr {
                t.Errorf("NewClient() error = %v, wantErr %v", err, tt.wantErr)
            }
        })
    }
}
```

## Code Style

### Go Standards

- Follow [Effective Go](https://golang.org/doc/effective_go.html)
- Use `gofmt` for formatting
- Use `goimports` for imports
- Follow [Go Code Review Comments](https://github.com/golang/go/wiki/CodeReviewComments)

### Project Conventions

- Package names: lowercase, no underscores
- Exported names: CamelCase
- Unexported names: camelCase
- Acronyms: All caps (HTTP, URL, ID)
- Error messages: lowercase, no punctuation
- Comments: Full sentences with punctuation

### Commit Messages

Use conventional commits:

```
type(scope): description

[optional body]

[optional footer]
```

Types:
- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation
- `style`: Formatting
- `refactor`: Code restructuring
- `perf`: Performance improvement
- `test`: Testing
- `build`: Build system
- `ci`: CI/CD
- `chore`: Maintenance

Examples:
```
feat(auth): add support for API tokens
fix(client): handle connection timeouts properly
docs: update README with examples
```

## Pull Request Process

1. **Branch naming**: `feature/description` or `fix/description`
2. **PR title**: Use conventional commit format
3. **PR description**: 
   - Describe what changed and why
   - Link related issues
   - List breaking changes
4. **Review process**:
   - At least one approval required
   - All CI checks must pass
   - Resolve all comments
5. **Merge**: Squash and merge to `develop`

## Release Process

1. Merge `develop` to `main`
2. Create release tag: `v1.2.3`
3. GitHub Actions automatically:
   - Runs tests
   - Creates GitHub release
   - Publishes to pkg.go.dev

## Documentation

- Update README for user-facing changes
- Add godoc comments for all exported items
- Include examples in documentation
- Update CHANGELOG.md

## Getting Help

- Open an issue for questions
- Join community discussions
- Check existing documentation

## License

By contributing, you agree that your contributions will be licensed under the MIT License.