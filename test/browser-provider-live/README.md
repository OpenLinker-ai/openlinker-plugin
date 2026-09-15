# Credential-backed Browser Provider acceptance

This release gate proves the real Provider-host path that the deterministic
Browser image harness cannot prove. It launches the repository-pinned Codex
and Claude binaries in four independent quadrants:

- Codex with the immutable native Plugin bundle;
- Codex with Runtime-injected direct MCP configuration;
- Claude Code with the immutable native Plugin bundle; and
- Claude Code with Runtime-injected direct MCP configuration.

Each quadrant uses the same production Browser Runtime, Egress Gateway and
temporary public HTTPS fixture as `test/browser-image/run.sh`. The model is
given only the fixture URL. It must call the sole `browser_session` tool and
return a dynamic marker that is visible only in the Browser document. The
runner retains only its SHA-256 digest, then requires equivalent structured
policy, contract, origin-digest, lifecycle and final-response evidence across
all four modes and at least four real Chromium document requests in fixture
metrics.

The gate does not register an Agent in Core and does not manipulate a public
site. The test-only Provider image descendants inherit the exact production
Provider CLI, Plugin artifact, UID/GID boundary and filesystem layout; only
their entrypoint is replaced with the one-Run acceptance client.

## Run

For a deterministic protocol regression without model credentials, run the
installed repository-pinned Codex against the local fake Responses API:

```sh
OPENLINKER_TEST_CODEX_RPC_LOCAL_MODEL=1 GOWORK=off go test -count=1 -v \
  -run '^TestInstalledCodexRPCWithLocalResponsesAPI$' ./packages/agent-adapters/agentexec
```

Its five cases cover standard RPC, native Browser, native Browser Code Mode,
ordinary incomplete-response retry, and `max_messages` after a Browser tool
result. The last case must stop without another automatic model request and
retain the session for explicit follow-ups; the ordinary retry must still
recover. CI runs this test with the pinned official client. Keep it when
upgrading Codex because its error wording is part of the observed compatibility
surface. This protocol fixture does not replace the credential-backed acceptance
below or prove that an upstream gateway/model issue is resolved.

Store each Provider credential in a separate private file, then run:

```sh
umask 077
printf '%s\n' "$CODEX_API_KEY" > /tmp/openlinker-codex-api-key
printf '%s\n' "$ANTHROPIC_API_KEY" > /tmp/openlinker-anthropic-api-key

OPENLINKER_BROWSER_LIVE_CODEX_API_KEY_FILE=/tmp/openlinker-codex-api-key \
OPENLINKER_BROWSER_LIVE_ANTHROPIC_API_KEY_FILE=/tmp/openlinker-anthropic-api-key \
  ./test/browser-provider-live/run.sh
```

The credential bytes enter each one-shot container through stdin. They are
never Docker environment values or command arguments, and the emitted JSONL
is checked not to contain them. Optional non-secret settings are
`OPENLINKER_CODEX_BASE_URL`, `OPENLINKER_CODEX_MODEL` and
`OPENLINKER_CLAUDE_MODEL`. Set `OPENLINKER_BROWSER_LIVE_EVIDENCE_FILE` to an
existing-directory path to retain the redacted four-line JSONL evidence.

Missing credentials, Docker, a pinned Provider binary, the Plugin bundle, a
Provider API response, the public fixture, a Browser tool call or any quadrant
is a hard failure. There is no skip or mock-success path.

## Codex upgrade regression without model credentials

The separate `CI / validate-go` job installs the exact official Codex version
and integrity pinned in `Dockerfile.providers`, then requires
`TestInstalledCodexRPCWithLocalResponsesAPI`. Retain this gate when upgrading
Codex. Its `native-browser-code-mode-message-limit` case sends a structured
`response.incomplete` with `incomplete_details.reason=max_messages` after a tool
result, and checks the real client's classified stop and explicit next-turn
continuation. The separate `native-browser-code-mode-retry` case still requires
ordinary transient retry recovery. It does not manufacture the client's error text: changes to the
currently observed Codex 0.153.0 wording must fail the classification assertion.
Node's protocol fixtures alone cannot detect that wording change.

This test uses a local fake Responses API and synthetic credentials only. It
does not determine whether a live `max_messages` limit originated at a gateway
or further upstream, or replace the credential-backed acceptance above. That
attribution requires correlated server-side logs.

## 中文说明

该门禁使用同一套真实 Browser Runtime、Egress Gateway 和临时 HTTPS fixture，
分别验证 Codex/Claude 的原生 Plugin 与直连 MCP 四种组合。模型只拿到 URL，
必须通过唯一的 `browser_session` 工具读出页面动态标记；缺少凭证、Provider、
Plugin、浏览器调用或任一象限都会直接失败，不会跳过或伪装成通过。API key
只通过容器 stdin 注入，不进入 Docker 环境、命令参数或脱敏证据。
