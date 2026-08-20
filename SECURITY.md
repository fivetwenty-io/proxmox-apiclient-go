# Security Policy

This library holds Proxmox VE credentials on behalf of every program that embeds it: API tokens, ticket cookies, passwords for ticket login, and the TLS settings that protect them in transit. A vulnerability here can leak credentials or weaken transport security for every consumer of the module. Here is how to report one and what happens after you do.

## Reporting a vulnerability

Please report vulnerabilities privately through [GitHub private vulnerability reporting](https://github.com/fivetwenty-io/proxmox-apiclient-go/security/advisories/new). Open the Security tab of this repository and choose "Report a vulnerability".

Do not report vulnerabilities through public GitHub issues, pull requests, or discussions. A public report exposes every consumer of the library before a fix exists.

A good report lets us reproduce the problem without guessing. Please include:

- The module version, or the commit hash if you built from source.

- The Go version, and the Proxmox VE version if the problem involves a live API.

- The steps that trigger the problem, as precisely as you can. A minimal program that demonstrates it is ideal but not required.

- The impact you believe the problem has, and any configuration it depends on.

- Relevant log output. The client redacts credentials from its logs, but please double-check before you paste.

You do not need a proof-of-concept exploit. A credible description of the problem is enough to start.

## What to expect

- We acknowledge your report within three business days.

- Within ten business days, usually sooner, we tell you whether we consider it a vulnerability. If the assessment takes longer, we keep you informed.

- We develop a fix privately, and we welcome your review of it.

- The fix ships in a new release, along with a GitHub security advisory that describes the problem, the affected versions, and the upgrade path.

- The advisory credits you unless you prefer to stay anonymous.

## Coordinated disclosure

We ask that you keep the details of a report private until we release a fix. In return, we commit to releasing a fix promptly and to publishing the advisory no later than 90 days after your report, even if a complete fix is not ready by then. If we need more time and you agree, we can extend that window.

## Supported versions

We fix vulnerabilities in the latest tagged release. We do not backport fixes to earlier releases. If you run an older version, the upgrade path is the current release.

## Scope

This policy covers the code in this repository: the client and transport code under `pkg/` and `internal/`, the generated API bindings under `pkg/api/`, the `pvegen` generator under `cmd/`, and the GitHub Actions workflows.

Vulnerabilities in the platforms this library talks to or builds on belong upstream:

- For Proxmox VE itself, write to the Proxmox security team at security@proxmox.com.

- For the Go toolchain and standard library, follow the [Go security policy](https://go.dev/security/policy).

If you are unsure where a problem belongs, report it here and we will help route it.
