# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Initial implementation of PVE API Go client
- Core client interface with all HTTP methods (GET, POST, PUT, DELETE)
- Multiple authentication methods:
  - Username/password with ticket-based auth
  - API token authentication
  - Two-factor authentication support (TOTP, Yubico, Recovery, U2F, WebAuthn)
- SSL/TLS certificate handling:
  - Certificate fingerprint verification
  - Manual verification mode
  - Trusted fingerprint caching
- Comprehensive error handling system:
  - Typed errors for different scenarios
  - API error parsing
  - Connection and SSL errors
- HTTP client with middleware support:
  - Authentication middleware
  - Rate limiting middleware
  - Retry middleware
  - Logging middleware
- Testing infrastructure:
  - Unit tests for all packages
  - Mock implementations
  - Test utilities
- Example programs:
  - Basic API usage
  - Authentication examples
  - Advanced SSL configuration
- Build and release automation:
  - GitHub Actions CI/CD
  - Code quality checks
  - Security scanning
  - Automated releases

### Security
- Secure credential handling
- Certificate fingerprint verification
- No hardcoded secrets

## [0.1.0] - TBD

### Added
- Initial alpha release
- Core functionality for PVE API interaction
- Basic documentation and examples

[Unreleased]: https://github.com/fivetwenty-io/pve-apicilent-go/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/fivetwenty-io/pve-apicilent-go/releases/tag/v0.1.0