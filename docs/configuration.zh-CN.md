# OpenLinker Plugin 配置参考

[English](./configuration.md) · [Use Mode](./calling-agents.zh-CN.md) ·
[Agent Mode](./serving-as-agent.zh-CN.md) · [README](../README.zh-CN.md)

这是本地 Codex 与 Claude Code Plugin 的配置参考。两种模式使用相互独立的凭据，
不能混用。

## 信任域

| 域 | 凭据 | 用于 | 绝不用于 |
| --- | --- | --- | --- |
| 调用方 | `OPENLINKER_USER_TOKEN` | 搜索、创建 Run、检查、Artifact、取消 | Runtime 注册或 Provider 认证 |
| Runtime Agent | `OPENLINKER_AGENT_TOKEN` 或 `_FILE` | Token-only Runtime 注册和交付 | 调用方操作或 Provider 认证 |
| Provider | 本地登录、`CODEX_API_KEY` 或 `ANTHROPIC_API_KEY` | 专用 Provider CLI 执行 | Core 调用方或 Runtime 认证 |

不要把凭据写入 Prompt、MCP 参数、`agent.json`、Run Input、Metadata、项目文件或日志。

## Use Mode

| 变量 | 必填 | 含义 |
| --- | --- | --- |
| `OPENLINKER_API_BASE` | 是 | Hosted 或自托管 Core 的公开 Base URL。 |
| `OPENLINKER_USER_TOKEN` | 认证操作必填 | 最小权限 `ol_user_*` Token。 |
| `OPENLINKER_CLI_BIN` | 否 | 兼容 CLI 的绝对路径，优先于 `PATH` 和 Plugin data。 |
| `OPENLINKER_PLUGIN_DATA` | 否 | 覆盖安装 CLI 的私有 Plugin data。 |

本地 bridge 不会静默回退到 Hosted MCP 或直接 HTTP。调用方解析顺序是：独立 CLI 命令
的显式 Option、环境变量、CLI Default。原生 Plugin 调用使用宿主环境。

## User Token 最小 grant

| 操作 | grant |
| --- | --- |
| `search_agents`、`get_agent` | `agents:read` |
| `create_task` | `tasks:create` |
| `run_agent`、`start_agent_run` | `agents:run` |
| `get_run`、`list_run_events`、`list_run_artifacts` | `runs:read` |
| `cancel_run` | `runs:cancel` |

## Agent Mode 初始化字段

Codex 使用 `$serve-openlinker-agent`，Claude Code 使用
`/openlinker:openlinker-agent`。原生流程调用 `configure_agent_mode`；独立部署可使用
`openlinker agent configure`。

| 存储字段 | 必填 | 默认值 | 含义 |
| --- | --- | --- | --- |
| `provider` | 是 | — | `codex` 或 `claude`。 |
| `agent_id` | 是 | — | 已存在、小写且非零的 Agent UUID。 |
| `workspace` | 是 | — | 已存在的最小 Workspace，保存为绝对路径。 |
| `openlinker_url` | 是 | — | Runtime Discovery 使用的公开 OpenLinker URL。 |
| `state_dir` | 否 | 私有默认目录 | 持久 Node ID、状态、Lock 和 Session Map。 |
| `provider_bin` | 否 | Provider 名 | Provider CLI 名或绝对路径。 |
| `model` | 否 | Provider 默认值 | Provider Model Override。 |
| `transport` | 否 | `auto` | `auto`、`websocket` 或 `pull`。 |
| `capacity` | 否 | `1` | 并发 Run 数，范围 1–1024。 |
| `timeout_seconds` | 否 | `1800` | Provider 执行超时，必须为正数。 |
| `session_reuse` | 否 | `true` | 每个 Core Conversation 私有复用 Provider Session。 |
| `web_search` | 否 | `false` | 允许 Provider Web Search。 |
| `codex_sandbox` | 仅 Codex | `read-only` | `read-only` 或 `workspace-write`。 |
| `codex_approval` | 仅 Codex | `never` | `never`、`untrusted` 或 `on-request`。 |
| `claude_permission` | 仅 Claude | `dontAsk` | `acceptEdits`、`auto`、`dontAsk`、`manual` 或 `plan`。 |
| `allowed_tools` | 仅 Claude | 空 | 显式 Claude Tool Allowlist。 |
| `enabled` | 控制工具管理 | `false` | 持久化 Agent Mode 偏好。 |

`agent.json` 不包含任何凭据，示例如下：

```json
{
  "version": 1,
  "enabled": false,
  "provider": "codex",
  "openlinker_url": "https://openlinker.ai",
  "agent_id": "22222222-2222-4222-8222-222222222222",
  "workspace": "/absolute/minimal/workspace",
  "transport": "auto",
  "capacity": 1,
  "timeout_seconds": 1800,
  "session_reuse": true,
  "web_search": false,
  "codex_sandbox": "read-only",
  "codex_approval": "never",
  "claude_permission": "dontAsk"
}
```

应使用原生配置流程，不要手工编辑。Decoder 会拒绝未知字段和不支持的版本。

## 配置和状态路径

设置 `OPENLINKER_AGENT_CONFIG` 可覆盖配置文件。否则 CLI 使用操作系统 User Config
Directory：

| 平台 | 默认 `agent.json` |
| --- | --- |
| macOS | `~/Library/Application Support/openlinker/agent.json` |
| Linux | `$XDG_CONFIG_HOME/openlinker/agent.json`，否则 `~/.config/openlinker/agent.json` |
| Windows | `%AppData%\openlinker\agent.json` |

State 解析顺序：

1. `OPENLINKER_AGENT_STATE_DIR`。
2. 已存储的 `state_dir`。
3. `$XDG_STATE_HOME/openlinker/agent`。
4. `~/.local/state/openlinker/agent`。

私有 State Directory 包含：

- `node-id`：生成的稳定 Runtime Node UUID；
- `session-map/<provider>.json`：Core Conversation Hash 到私有 Provider Session 的映射；
- `status.json`：脱敏的最近状态；
- 防止两个 Worker 共用状态的进程 Lock。

`OPENLINKER_NODE_ID` 可以省略；Runtime 会创建并持久化。以后显式提供时，必须与已
持久化值一致。

## Agent 与 Provider Secret

| 直接变量 | File 形式 | 要求 |
| --- | --- | --- |
| `OPENLINKER_AGENT_TOKEN` | `OPENLINKER_AGENT_TOKEN_FILE` | Agent Mode 必须恰有一个来源。 |
| `CODEX_API_KEY` | `CODEX_API_KEY_FILE` | Codex 本地登录可用时可省略。 |
| `ANTHROPIC_API_KEY` | `ANTHROPIC_API_KEY_FILE` | Claude 本地登录可用时可省略。 |

每行的直接和 File 来源互斥。Secret File 必须是当前用户拥有的普通非符号链接文件，
Group/Other 不可访问，内容非空且不大于 64 KiB。Secret 首尾空白会被移除。

启动 Provider 进程前，Runtime 会移除调用方和 Agent 凭据，只传递所选 Provider 凭据，
以及少量 Provider State、Proxy 和 CA Certificate Allowlist 变量。

## Runtime 环境变量覆盖

Runtime 中的环境变量优先于已存储的非敏感 Agent 配置。

| 变量 | 覆盖字段 |
| --- | --- |
| `OPENLINKER_PROVIDER` | `provider` |
| `OPENLINKER_URL` | `openlinker_url` |
| `OPENLINKER_API_BASE` | 未设置 `OPENLINKER_URL` 时覆盖 `openlinker_url` |
| `OPENLINKER_AGENT_ID` | `agent_id` |
| `OPENLINKER_WORKSPACE` | `workspace` |
| `OPENLINKER_AGENT_STATE_DIR` | `state_dir` |
| `OPENLINKER_AGENT_TRANSPORT` | `transport` |
| `OPENLINKER_AGENT_CAPACITY` | `capacity` |
| `OPENLINKER_AGENT_TIMEOUT_SECONDS` | `timeout_seconds` |
| `OPENLINKER_AGENT_SESSION_REUSE` | `session_reuse` |
| `OPENLINKER_AGENT_WEB_SEARCH` | `web_search` Fallback |
| `OPENLINKER_CODEX_MODEL`、`OPENLINKER_CLAUDE_MODEL` | 所选 Provider 的 `model` |
| `OPENLINKER_CODEX_WEB_SEARCH`、`OPENLINKER_CLAUDE_WEB_SEARCH` | Provider 专用 `web_search` |
| `OPENLINKER_CODEX_SANDBOX` | `codex_sandbox` |
| `OPENLINKER_CODEX_APPROVAL` | `codex_approval` |
| `OPENLINKER_CLAUDE_PERMISSION` | `claude_permission` |
| `OPENLINKER_CLAUDE_ALLOWED_TOOLS` | 逗号分隔的 `allowed_tools` |
| `OPENLINKER_CODEX_BIN`、`OPENLINKER_CLAUDE_BIN` | `provider_bin` |

环境覆盖适合部署注入；交互使用不要把它变成第二套无人管理的配置系统。

## 原生 Agent 控制工具

| Tool | 行为 |
| --- | --- |
| `configure_agent_mode` | 校验并持久化非敏感字段。 |
| `diagnose_agent_mode` | 返回脱敏的存在性、来源和有效性检查。 |
| `enable_agent_mode` | 持久化启用状态并启动本地 Runtime Worker。 |
| `get_agent_mode_status` | 返回脱敏的当前或已持久化状态。 |
| `disable_agent_mode` | 排空、停止并持久化停用状态。 |

这些工具都不接受 Credential 或 Provider Session ID。

## 宿主初始化

### Codex CLI

启动 `codex` 前设置环境变量，安装 Plugin 并新建 Session。显式原生 Skill 使用
`$openlinker`、`$setup-openlinker-cli` 和 `$serve-openlinker-agent`。

### Codex 桌面端

把所需 `KEY=value` 写入 `~/.codex/.env`，保护文件权限、重启应用并新建任务。仓库中
绝不能包含该文件。

### Claude Code

启动 `claude` 前设置环境变量。安装或启用 Plugin 后运行 `/reload-plugins`。显式原生
命令为 `/openlinker:openlinker`、`/openlinker:openlinker-setup` 和
`/openlinker:openlinker-agent`。

## 代理与网络范围

CLI 和 Provider 子进程遵循标准 `HTTP_PROXY`、`HTTPS_PROXY` 和 `NO_PROXY`。本地原生
Plugin 不提供硬网络隔离边界；如果必须强制拦截私网、Link-local、Metadata、DNS
Rebinding 或代理绕过，应使用生产 Provider 镜像及其 Egress Gateway。

## 安全诊断

- 原生 CLI 安装流程报告 CLI 来源、版本、Surface 和 Capability。
- `diagnose_agent_mode` 只报告 `environment`、`file`、`absent`、`missing` 等来源类别，
  不报告值。
- `get_agent_mode_status` 报告生命周期和非敏感路径。
- 只有在 `PATH` 中存在兼容独立 CLI 或显式选择 CLI 时，才适合直接运行
  `openlinker context`；该命令不发出网络请求。

凭据改变后，应重启或 Reload 宿主，使本地 MCP 进程获得新环境。
