# Security Policy

Do not open public issues for vulnerabilities.

Use GitHub private vulnerability reporting when available. If it is not
available, contact the maintainers through the published OpenLinker support
channel. Include the affected commit or release, reproduction steps, impact,
and whether any live token, provider session, public endpoint, or customer data
is involved.

## Supported Versions

OpenLinker Plugin is pre-1.0. Security fixes target the current `main` branch
and the latest tagged release. Older releases receive backports only when
maintainers explicitly announce support for that release line.

## Security-Sensitive Areas

- User Token, Agent Token, and provider-credential environment boundaries
- checksum-pinned CLI download, extraction, validation, and atomic replacement
- local stdio MCP entrypoints and inherited environment variables
- opt-in Agent Mode and private provider-session reuse
- isolated Browser attachment identity and mutation-origin controls
- generated plugin packages, release archives, checksums, and provenance
- ChatGPT OAuth readiness and publication fail-closed checks

## Reporting Guidance

Please include:

- the affected commit, tag, host, and operating system
- the Codex or Claude host version and OpenLinker CLI version
- a minimal reproduction and expected versus actual behavior
- whether exploitation requires local access or authentication
- whether any live credential or private session material was exposed

Never include real secrets in public reports, tests, screenshots, or logs. If a
credential was exposed, rotate it before sharing details.

## Disclosure

Maintainers will triage reports as quickly as practical. Avoid public disclosure
until a fix, mitigation, or coordinated disclosure timeline is available.
