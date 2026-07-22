# 把 Codex 或 Claude Code 作为 Agent（Agent Mode）

[English](./serving-as-agent.md) · [配置参考](./configuration.zh-CN.md) ·
[README](../README.zh-CN.md)

Agent Mode 通过已有 OpenLinker Agent，让当前 Codex 或 Claude Code 宿主可被调用。
Plugin 的本地 MCP bridge 运行官方 SDK Runtime Worker，并为每个远端 Run 启动专用
Provider CLI 进程。它不需要 OpenLinker Agent Node。

```text
OpenLinker Core → SDK Runtime Worker → Codex 或 Claude Provider CLI
```

Agent Mode 默认关闭。安装、更新或调用 Plugin 都不会自动启用它。

## 前置条件

在模型上下文之外准备：

1. 已存在的 OpenLinker Agent UUID。
2. 属于该 Agent 的 Agent Token。
3. 已存在、内容最小化的远端任务工作目录。
4. 已安装并认证的 `codex` 或 `claude` CLI。
5. Plugin 可解析到兼容 `openlinker` CLI。

工作目录只应包含远端任务确实需要的文件。Plugin Mode 使用宿主 Sandbox 和软件策略，
适用于个人或开发环境，不是强多租户隔离边界。

## 启动宿主前注入 Secret

模型绝不能接收 Agent Token、Provider API Key、Secret File 内容或 Provider Session ID。

直接环境变量：

```bash
export OPENLINKER_AGENT_TOKEN='ol_agent_<redacted>'
export CODEX_API_KEY='<redacted>'
codex
```

Claude Provider 使用 `ANTHROPIC_API_KEY` 并启动 `claude`。Provider CLI 已有可用本地
登录状态时，API Key 可省略。

Secret File：

```bash
export OPENLINKER_AGENT_TOKEN_FILE=/secure/path/openlinker-agent-token
export CODEX_API_KEY_FILE=/secure/path/codex-api-key
```

Claude 使用 `ANTHROPIC_API_KEY_FILE`。Secret File 必须是当前用户拥有、Group 和 Other
不可访问的普通非符号链接文件。同一 Secret 的直接形式和 `_FILE` 形式互斥。

Codex 桌面端把所需条目放进 `~/.codex/.env`，重启应用并新建任务。不要把 Secret
写进 `agent.json` 或项目 `.env`。

## 第一步：验证本地 bridge

Codex：

```text
$setup-openlinker-cli Verify Agent Mode capabilities without enabling Agent Mode.
```

Claude Code：

```text
/openlinker:openlinker-setup
```

所需 CLI Surface 包含 `agent.configure`、`agent.serve`、`agent.status`、
`agent.doctor` 和 `plugin.serve`。

## 第二步：配置非敏感字段

Codex：

```text
$serve-openlinker-agent Configure Codex as Agent <agent-uuid>. Use workspace /absolute/minimal/workspace and OpenLinker URL https://openlinker.ai. Keep transport auto, capacity 1, and session reuse enabled. Do not enable Agent Mode yet.
```

Claude Code：

```text
/openlinker:openlinker-agent Configure Claude Code as Agent <agent-uuid>. Use workspace /absolute/minimal/workspace and OpenLinker URL https://openlinker.ai. Keep transport auto, capacity 1, and session reuse enabled. Do not enable Agent Mode yet.
```

这会调用 `configure_agent_mode`，它只接受非敏感配置并写入仅 Owner 可访问的
`agent.json`。默认值、路径和 Provider 策略见配置参考。

## 第三步：诊断

Codex：

```text
$serve-openlinker-agent Diagnose the saved Codex Agent Mode configuration. Report only redacted presence and source categories. Do not enable it.
```

Claude Code：

```text
/openlinker:openlinker-agent Diagnose the saved Claude Agent Mode configuration. Report only redacted presence and source categories. Do not enable it.
```

这会调用 `diagnose_agent_mode`。必需检查包括 Provider、Agent UUID、公开 URL、
Workspace、Runtime Option、Agent Token、Provider CLI、Provider Auth、私有状态和
Token-only Runtime Security。诊断结果绝不返回 Secret Value。

## 第四步：明确启用

只有诊断通过后才启用。

Codex：

```text
$serve-openlinker-agent Enable Agent Mode now and show the redacted status.
```

Claude Code：

```text
/openlinker:openlinker-agent Enable Agent Mode now and show the redacted status.
```

这会调用 `enable_agent_mode`。bridge 持久化 `enabled: true`、启动 Runtime Worker、
向 Core 注册 Capacity，并返回脱敏状态。如果启动失败，bridge 会恢复为 Disabled，
不会留下虚假的 Enabled 配置。

以后宿主重启时，只要 Plugin 已启用且凭据仍可用，本地 bridge 会按已持久化的状态重新
启动 Agent Mode。关闭宿主会移除在线 Capacity。

## 第五步：查看状态

Codex：

```text
$serve-openlinker-agent Show the current redacted Agent Mode status.
```

Claude Code：

```text
/openlinker:openlinker-agent Show the current redacted Agent Mode status.
```

这会调用 `get_agent_mode_status`。生命周期状态包括 `disabled`、`starting`、`ready`、
`draining`、`stopped` 和 `error`。

## 第六步：验证多轮 Session 复用

从另一个 OpenLinker 调用方，用同一个 Core Conversation 连续调用该 Agent 两次；
第二个 Run 发送跟进任务，两次调用间保持 Runtime 在线。

当 `session_reuse: true` 时：

1. Core 提供稳定 Conversation Key 和持久化的对话历史。
2. Runtime 把该 Key 哈希为私有 Provider Session Mapping。
3. 第一个 Run 创建专用 Codex 或 Claude Session。
4. Provider Session 仍存在时，后续 Run 恢复该 Session。
5. Provider Session 已消失时，Runtime 删除过期 Mapping，并使用 Core 持有的历史重试一次。

Run Output 可以报告是否启用了 Reuse、是否 Resume 或 Recovery，但不会包含原始 Provider
Session ID。Mapping 保存在私有 Agent State Directory 下。

## 第七步：停用并排空

Codex：

```text
$serve-openlinker-agent Disable Agent Mode and drain active work gracefully.
```

Claude Code：

```text
/openlinker:openlinker-agent Disable Agent Mode and drain active work gracefully.
```

这会调用 `disable_agent_mode`，排空 Runtime Worker 并持久化 `enabled: false`。

## Agent 控制工具

| Tool | 用途 | 允许 Secret 参数 |
| --- | --- | --- |
| `configure_agent_mode` | 保存非敏感配置。 | 否 |
| `diagnose_agent_mode` | 检查配置和凭据是否存在。 | 否 |
| `enable_agent_mode` | 持久化启用状态并启动 Runtime Worker。 | 否 |
| `get_agent_mode_status` | 返回脱敏状态。 | 否 |
| `disable_agent_mode` | 排空、停止并持久化停用状态。 | 否 |

## Headless 或 24×7 运行

原生 Plugin Mode 跟随 Codex/Claude Code 宿主生命周期。需要受监管进程时，单独安装
CLI 并使用同一套 SDK Runtime 实现：

```bash
openlinker agent configure \
  --provider codex \
  --agent-id <agent-uuid> \
  --workspace /absolute/minimal/workspace \
  --url https://openlinker.ai
openlinker agent doctor --provider codex
openlinker agent serve --provider codex
```

`agent serve` 在前台运行，不要求 `enabled: true`。生产镜像增加持久 Runtime Volume、
非 Root Provider 进程和强制出站网络策略；它们仍直接使用 CLI 和 SDK，不使用 Agent Node。

## 故障排查

| 现象 | 检查项 |
| --- | --- |
| Agent 一直离线 | 确认宿主和 Plugin MCP bridge 正在运行，Agent Mode 状态为 `ready`。 |
| `agent_token` 缺失 | 只注入 `OPENLINKER_AGENT_TOKEN` 或 `OPENLINKER_AGENT_TOKEN_FILE` 之一，然后重启宿主。 |
| 找不到 Provider CLI | 安装并认证所选 `codex` 或 `claude` CLI；必要时配置其绝对路径。 |
| Secret File 被拒绝 | 必须是当前用户拥有、非符号链接，且 Group/Other 无权限的普通文件。 |
| Node ID 不匹配 | 移除显式 `OPENLINKER_NODE_ID`，或令其与已持久化值一致；不要随意删除状态。 |
| Worker 已运行 | 同一 State Directory 只允许一个 Agent Mode 进程；停止另一个进程或改用独立目录。 |
| Session 未恢复 | 确认两个 Run 使用相同 Core Conversation 且启用 `session_reuse`；Recovery 可能新建一次替代 Session。 |
| 宿主必须持续在线 | 使用受监管的 `openlinker agent serve` 或生产 Provider 镜像。 |

所有字段、优先级和存储路径见[配置参考](./configuration.zh-CN.md)。
