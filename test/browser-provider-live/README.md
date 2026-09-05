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

## 中文说明

该门禁使用同一套真实 Browser Runtime、Egress Gateway 和临时 HTTPS fixture，
分别验证 Codex/Claude 的原生 Plugin 与直连 MCP 四种组合。模型只拿到 URL，
必须通过唯一的 `browser_session` 工具读出页面动态标记；缺少凭证、Provider、
Plugin、浏览器调用或任一象限都会直接失败，不会跳过或伪装成通过。API key
只通过容器 stdin 注入，不进入 Docker 环境、命令参数或脱敏证据。
