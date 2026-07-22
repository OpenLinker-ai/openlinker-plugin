# 调用 OpenLinker Agent（Use Mode）

[English](./calling-agents.md) · [配置参考](./configuration.zh-CN.md) ·
[README](../README.zh-CN.md)

Use Mode 让 Codex 或 Claude Code 通过宿主原生 Skill/Command 发现并调用 OpenLinker
Agent。已安装插件会管理本地 MCP bridge；bridge 解析锁定版本的 CLI，CLI 再通过官方
SDK 调用 Core。用户无需手动添加另一个 MCP Server。

```text
Codex 或 Claude Code → 本地 Plugin MCP → openlinker CLI → SDK → Core
```

## 前置条件

需要：

1. 已安装并启用 OpenLinker Plugin。
2. `OPENLINKER_API_BASE` 指向公开 Core Base URL。
3. `ol_user_*` User Token 仅拥有目标操作需要的 grant。
4. 已有兼容 `openlinker` CLI，或通过原生安装流程安装它。

在 OpenLinker Workbench 的 **设置 → User Token** 中创建和管理 User Token。Use
Mode 不能使用 Agent Token。

## 安装并激活

### Codex

```bash
codex plugin marketplace add OpenLinker-ai/openlinker-plugin
codex plugin add openlinker@openlinker
```

新建 Codex 任务或 CLI Session。在 Codex CLI 中，`/plugins` 可显示插件是否已安装并
启用。

### Claude Code

```bash
claude plugin marketplace add OpenLinker-ai/openlinker-plugin
claude plugin install openlinker@openlinker
```

在 Claude Code 中运行：

```text
/reload-plugins
```

用 `/plugin list` 确认插件已启用，用 `/mcp` 确认插件提供的 `openlinker` Server 已连接。

## 初始化调用方配置

### 终端宿主

启动宿主前把凭据注入环境：

```bash
export OPENLINKER_API_BASE=https://api.openlinker.ai
export OPENLINKER_USER_TOKEN='ol_user_<redacted>'
codex
```

Claude Code 使用相同环境，把最后的 `codex` 改为 `claude`。优先用 Shell Credential
Manager 或进程管理器，避免 Token 留在 Shell History 中。

### Codex 桌面端

桌面应用可能不会继承终端导出的变量。把以下内容写入 `~/.codex/.env`：

```dotenv
OPENLINKER_API_BASE=https://api.openlinker.ai
OPENLINKER_USER_TOKEN=ol_user_<redacted>
```

把文件权限限制给当前用户，重启应用并新建任务。绝对不要把该文件放入仓库。

自托管 Core 应改成它的公开 HTTPS Base URL。除非是有意且可信的本地部署，否则不要
使用 Loopback 或私网目标。

## 验证 CLI

安装流程依次检查 `OPENLINKER_CLI_BIN`、`PATH` 和私有 Plugin data。它会报告解析到的
路径、CLI 版本、Surface 版本和 Capability，不打印 Token，也不向 Core 发请求。

Codex：

```text
$setup-openlinker-cli Verify the CLI required by OpenLinker.
```

Claude Code：

```text
/openlinker:openlinker-setup
```

如果单独维护了 `PATH` 中的 CLI，`openlinker context` 可执行相同的无网络检查。

## 只发现，不执行

先发起只读请求。

Codex：

```text
$openlinker 查找可用于卖家研究的 Agent，比较它们的 Skill 和可见性，但暂时不要调用。
```

Claude Code：

```text
/openlinker:openlinker 查找可用于卖家研究的 Agent，比较它们的 Skill 和可见性，但暂时不要调用。
```

需要更窄的入口时，Codex 使用 `$find-and-run-agent`，Claude Code 使用
`/openlinker:find-and-run-agent`。

## 启动 Run

明确指定选中的 Agent 并授权执行：

Codex：

```text
$openlinker 用选中的 Agent 执行这项任务。异步启动并使用稳定幂等键，先返回 Run ID。
```

Claude Code：

```text
/openlinker:openlinker 用选中的 Agent 执行这项任务。异步启动并使用稳定幂等键，先返回 Run ID。
```

插件对同步调用使用 `run_agent`，对异步调用使用 `start_agent_run`。稳定幂等键可以防止
网络响应不确定后的重试重复创建 Run。

不要把 User Token、Agent Token、Provider 凭据或无关私有上下文放进 Agent Input
或 Metadata。

## 检查进度和 Artifact

Codex：

```text
$inspect-openlinker-run 检查 Run <run-uuid>，显示状态、执行证据、持久事件和 Artifact。
```

Claude Code：

```text
/openlinker:inspect-openlinker-run 检查 Run <run-uuid>，显示状态、执行证据、持久事件和 Artifact。
```

检查操作只读。Runtime Transport Evidence 可以显示 direct、WebSocket、pull 或其他支持
的执行路径，但不会暴露 Provider Session ID。

## 延续同一会话

多轮 Agent 调用应让插件在新 Run 间保持同一个 Core `conversation_id`：

```text
$openlinker 在同一个 OpenLinker 会话中继续，按照新增约束改进前一次结果。
```

Claude Code 使用对应的 `/openlinker:openlinker` 入口。Core 拥有会话上下文；Runtime
可以私有复用 Provider 原生 Session，但调用方不会发送或收到 Codex/Claude Session ID。

## 明确取消

取消会改变远端状态，必须明确表达：

```text
$inspect-openlinker-run 取消 Run <run-uuid>，然后显示更新后的状态。
```

Claude Code 使用 `/openlinker:inspect-openlinker-run` 发出同样请求。

## 最小 grant

| 操作 | 最小 grant |
| --- | --- |
| 搜索或获取 Agent | `agents:read` |
| 把任务解析为推荐 | `tasks:create` |
| 启动同步或异步 Run | `agents:run` |
| 获取 Run、事件或 Artifact | `runs:read` |
| 取消调用方拥有的 Run | `runs:cancel` |

`agents:run` 可以限制到单个 Agent。grant 不会绕过 Core 强制的所有权、可见性或 Run
状态检查。

## 可选的独立 CLI

交互式 Codex/Claude Code 推荐使用原生 Plugin Surface；脚本可以安装独立 CLI，使用
相同契约：

```bash
openlinker agents search --query "seller research" --callable
openlinker run --async \
  --idempotency-key request-001 \
  --agent <agent-uuid> \
  --input '{"task":"research the target seller"}'
openlinker runs get --id <run-uuid>
openlinker runs events --id <run-uuid>
openlinker runs artifacts --id <run-uuid>
```

日常自动化不要传 `--token`，进程参数和 Shell History 可能暴露它。

## 故障排查

| 现象 | 检查项 |
| --- | --- |
| 找不到 Skill 或 Command | 确认插件已启用，然后新建 Codex Session，或运行 Claude `/reload-plugins`。 |
| 找不到 MCP Tool | Claude 使用 `/mcp`；Codex 检查 `/plugins` 后新建任务。 |
| 找不到兼容 CLI | 调用原生安装流程，不要下载浮动的 `latest` Binary。 |
| 认证失败 | 确认宿主进程收到 `OPENLINKER_USER_TOKEN`，且 Token 有目标 grant。 |
| 连接了错误 Core | 检查 `OPENLINKER_API_BASE`；插件不会静默切换到 Hosted MCP。 |
| 重试可能重复创建 Run | 使用异步执行和相同的稳定幂等键。 |

解析顺序和所有环境变量见[配置参考](./configuration.zh-CN.md)。
