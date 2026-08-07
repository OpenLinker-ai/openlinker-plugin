# Release Process

OpenLinker Plugin releases are cut from `main` only after the compatible
OpenLinker CLI release exists and all local and CI release gates pass. The npm
package is private metadata for repository tooling; Plugin releases are GitHub
artifacts and native host packages, not npm publications.

## Pre-Release Checklist

1. Confirm `README.md` and `README.zh-CN.md` describe the same Use Mode, Agent
   Mode, Browser, CLI, SDK, Agent Node, Core, and Cloud boundaries.
2. Confirm `CHANGELOG.md` describes user-visible and compatibility changes.
3. Generate the immutable CLI lock for the already published compatible CLI.
4. Run `npm test` and `npm run release:check`.
5. Validate POSIX, Node.js, and PowerShell entrypoints on their supported hosts.
6. Validate Codex and Claude packages with the official host validators.
7. Inspect generated archives, checksums, SBOMs, and publication-boundary output.
8. Run a current-source secret scan on a clean checkout.
9. Confirm private state, credentials, provider sessions, build output, and local
   acceptance evidence are not tracked or packaged.

## Tagging

The release workflow requires the tag to match `v<package.json version>`.
Publishing a tag is an explicit maintainer action:

```bash
git tag v0.x.y
git push origin v0.x.y
```

Do not create a tag until the pinned CLI release and cross-platform validation
are complete. Pre-1.0 breaking changes must be explicit in the changelog and
release notes.
