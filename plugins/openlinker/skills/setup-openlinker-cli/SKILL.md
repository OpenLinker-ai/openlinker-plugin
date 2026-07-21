---
name: setup-openlinker-cli
description: "Verify or explicitly install the OpenLinker CLI version required by the OpenLinker Plugin. Use when the Plugin reports that openlinker is missing, incompatible, lacks a required capability, or when a user asks to set up or repair the local OpenLinker CLI integration."
---

# Set Up the OpenLinker CLI

Use the Plugin's deterministic resolver and installer. Do not implement an
alternate downloader, install a floating `latest`, or modify a project file.

## Verify First

Run the Plugin resolver from `${PLUGIN_ROOT}/scripts/resolve-openlinker-cli`.
It checks, in order, `OPENLINKER_CLI_BIN`, PATH, and the Plugin data directory,
then validates `context` JSON, surface version, and requested capabilities.

If a compatible CLI is found, report its path, version, and capabilities and
stop. Never print `OPENLINKER_USER_TOKEN` or other environment values.

## Install Only with Explicit Intent

Running this Skill or `/openlinker-setup` is explicit installation intent.
Before invoking the installer, show:

- the pinned CLI version from `cli-lock.json`;
- the GitHub release origin;
- the platform/architecture;
- the Plugin data destination.

Then run `${PLUGIN_ROOT}/scripts/install-openlinker-cli` on POSIX systems or
`install-openlinker-cli.ps1` on Windows. The installer must select an exact
entry from `cli-lock.json`, download the archive and adjacent checksum, verify
SHA-256, reject symlink destinations, extract only the expected executable, and
install atomically into Plugin data. A failed check must leave the previous CLI
untouched.

Honor standard HTTP/HTTPS proxy configuration. Do not disable TLS validation,
follow a private-network redirect, use credentials from prompts, or fall back
to an unverified binary.

After installation, run the resolver again and report only non-sensitive
version and capability information. If installation cannot complete, provide
the exact failure and the manual fixed-release installation option; do not
silently switch the Plugin to remote MCP.
