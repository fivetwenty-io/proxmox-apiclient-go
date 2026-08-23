# Contributing to proxmox-apiclient-go

Thank you for helping improve the Proxmox VE API client for Go. We welcome bug reports, documentation fixes, and code contributions. This guide explains how to report a problem, set up a development environment, and submit a change.

## Reporting issues

Open an issue on GitHub when you find a bug or want to propose a feature. A good bug report lets us reproduce the problem without guessing. Please include:

- The module version, or the commit hash if you built from source.

- The Go version and the Proxmox VE version you are running against.

- A minimal program or test that triggers the problem, as precisely as you can.

- The expected behavior and what happened instead, with relevant log output. The client redacts credentials from its logs, but please double-check before you paste.

If you believe the problem is a security vulnerability, do not open a public issue. Follow the [security policy](SECURITY.md) instead.

For feature requests, describe the problem you want to solve rather than only the change you have in mind. Knowing the goal helps us weigh alternatives.

## Before you start a large change

For small fixes, a pull request is enough. For anything larger, such as a new client option, a change to the transport, or a change in default behavior, please open an issue first and describe your plan. This avoids wasted work when a design needs discussion, and it gives us a place to record the decision.

## Development

### Prerequisites

- Go 1.27 or higher. The module pins its toolchain in `go.mod`, so a matching toolchain is fetched automatically.

- `golangci-lint` for `make lint`.

- `staticcheck`, `govulncheck`, `gosec`, and `trivy` for the corresponding make targets.

### Layout

- `pkg/client/` holds client construction and options; `internal/` holds the transport (HTTP, auth, encoding).

- `pkg/api/` holds typed per-namespace API bindings. These are generated from `_data/apidoc.json` by the `pvegen` tool in `cmd/`. Do not edit generated files by hand: change the generator or the spec, regenerate, and commit the result. `make verify-generated` confirms the generated files match the spec, and CI runs the same check.

- Code follows a 120-column line limit and uses tabs for indentation.

### Running tests

```bash
make test        # all tests
make test-race   # with race detection
make coverage    # coverage report
```

Every code change should come with tests that cover it.

### Running the full check suite

```bash
make check
```

This runs `lint`, `vet`, `staticcheck`, and `test`, stopping at the first failure. CI runs the same checks on every push, so a green `make check` locally means CI should pass too.

### Running security scans

```bash
make security
```

This runs `govulncheck`, `gosec`, and `trivy`. We run these scans before every release, and CI runs them as well.

## Submitting a pull request

1. Fork the repository and create a branch for your change.

2. Make the change, with tests. If you touched the generator or the spec, regenerate and commit the generated files, and confirm with `make verify-generated`.

3. Run `make check` and make sure it passes.

4. If the change is visible to users of the library, add an entry to [CHANGELOG.md](CHANGELOG.md). API surface, behavior, and documentation count; refactors, tests, and CI plumbing usually do not.

5. Open a pull request against `main`. Describe what the change does and why. Link the related issue if one exists.

Keep each pull request focused on one change. A small, focused pull request is easier to review and lands faster than a large one that mixes concerns.

### Commit messages

Write commit messages that describe the code change, not the process that produced it. This repository follows the Conventional Commits style: a type prefix such as `fix:`, `feat:`, `docs:`, or `ci:`, followed by a short summary in the imperative mood. Look at `git log` for examples.

## Releasing (maintainers)

Releases are tag driven. Pushing a tag of the form `vX.Y.Z` runs the release workflow, which runs the test suite with race detection, publishes a GitHub Release with generated notes, and requests the new version from the Go module proxy so it appears on pkg.go.dev.

To cut a release, first add the new version's section to [CHANGELOG.md](CHANGELOG.md) and merge that to `main`. Then tag it:

```bash
git tag -a v3.9.0 -m "Version 3.9.0"
git push origin v3.9.0
```

The module path is `github.com/fivetwenty-io/proxmox-apiclient-go/v3`, so tags stay within the v3 major version until a breaking change forces a new major version and a new module path.

## License

This project is licensed under the [Apache License, Version 2.0](LICENSE). By submitting a contribution, you agree that it will be licensed under the same terms.
