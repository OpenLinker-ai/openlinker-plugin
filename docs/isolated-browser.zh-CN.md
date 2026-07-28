# 使用隔离 Browser

[English](./isolated-browser.md) ·
[Browser 模式](./browser-modes-overview.zh-CN.md) ·
[配置参考](./configuration.zh-CN.md) ·
[Agent Mode](./serving-as-agent.zh-CN.md) · [README](../README.zh-CN.md)

OpenLinker 把 Browser 暴露为正在执行的 Codex 或 Claude 客户端工具。模型调用
`browser_session`，而不是 OpenAI 或 Anthropic Provider 的 `computer` API。

```text
OpenLinker Core
  → Runtime Worker
  → 普通 Codex 或 Claude 客户端
  → 客户端注册的 Browser MCP 工具
  → 受信本地 broker
  → 通过 Unix socket 访问隔离 Browser Runtime
  → Chromium 经 egress gateway 联网
```

Browser 需要显式启用。标准 Agent 继续沿用原 Provider 路径，不加载 Browser 工具或
Browser 配置。

## 两种客户端上下文

同一个 Browser 工具可以出现在两种权威上下文中，但普通 Plugin Manifest 不会注册它：

| 上下文 | 谁注册 `browser_session` | 谁提供 Attachment Authority |
| --- | --- | --- |
| 已连接 Runtime 的交互式 Codex 或 Claude 宿主 | Runtime 显式生成的 MCP 配置 | 另行管理的本地 Browser Runtime 部署 |
| 可被调用的 Browser Agent | Runtime Worker 向子客户端注入隔离的 Browser-only MCP 配置 | Core 和 Runtime Worker |

当前支持的生产路径是可被调用的 Browser Agent。普通 Plugin 安装只暴露 OpenLinker
Bridge，因此不会显示一个必然失败的 Browser 工具。交互式宿主只有在私有 Runtime
Socket、Channel Credential File、Active Lease 与 Preflight 均有效后，才能使用
Runtime 显式生成的配置。不要手写 Lease，也不要把它的内容放进 Prompt。

## 配置 Browser Agent

使用专用、私有、仅 Owner 可见的 Agent Record，不要原地转换已有公开 Agent。

生产容器组包括：

- 普通 Codex 或 Claude Provider Runtime；
- 包含 Chromium 的独立 Browser Runtime；
- 现有 egress gateway；
- Browser control 和受信 MCP broker 的私有共享 mount；
- 持久 Browser Profile 存储。

把 Provider compose 文件与对应 Browser override 一起应用：

```bash
docker compose \
  -f deploy/compose.codex.yml \
  -f deploy/compose.codex.browser.yml \
  up -d
```

Claude 使用 `compose.claude.yml` 和 `compose.claude.browser.yml`。Entrypoint 会固定
私有路径、生成 Browser channel credential，并配置 `execution_profile: browser`。
Operator 仍需在模型上下文之外提供普通 Agent Token 和 Provider 认证。

通过原生 Plugin 配置时，调用 `configure_agent_mode` 并设置
`execution_profile: browser`、`capacity: 1`、`session_reuse: true`。容器部署中的
Browser 路径由镜像 Entrypoint 设置。先 Diagnose，再 Enable。

## 使用 Browser 工具

Codex Skill：

```text
$use-isolated-browser Open the requested public page and summarize it.
```

Claude Command：

```text
/openlinker:use-isolated-browser Open the requested public page and summarize it.
```

客户端使用以下 `browser_session` Operation：

- `observe`：默认获取有界 Semantic Page State；
- `act`：提交类型化的导航、指针、键盘、滚动或等待 Action；
- `checkpoint`：显式请求 Profile Checkpoint；
- `close`：关闭当前 Attachment。

`observe` 或 `act` 可选择 `observation: semantic`、`screenshot` 或 `both`。省略时使用
`semantic`，绝不隐式返回 Screenshot。多 Action Batch 不执行完整的中间 Observation，
只返回最终 Observation；失败时会返回 `completed_actions`。

Phase 1 允许公开导航、普通公开链接、非敏感 Text/Search Field 和公开 GET Search
Form；拒绝 Credential、Button、自定义 Activation Control、Select、Space 激活、
会改变状态的页面请求及高影响 Action。

工具 Schema 有意不包含 Run、Agent、Principal、Session、Attachment、Epoch、
Credential、Lease、Proxy 或 Provider Key 参数。受信 broker 会补充不可变身份元组
`(runtime_session_id, session_epoch, attachment_id)` 和 Browser control epoch。

## 可靠性结果与证据

每个 MCP Session/control epoch 的首个结构化 Browser 结果会包含
`attachment_evidence`，其中是已验证的 Browser Engine、Distribution、Major
Version、Locale、Timezone 与 Font Contract。Provider/MCP 恢复后会重新发送一次，
但不会在每个 Action 中重复。

按稳定结果处理，不要猜测：

- `BROWSER_ACCESS_DENIED`：当前 Attachment 仍可使用；同一 Origin 连续出现三次顶层
  HTTP 403 后，只在当前 Attachment 内阻止该 Origin。
- `BROWSER_RATE_LIMITED` 或 `BROWSER_ORIGIN_RATE_LIMITED`：遵守有界
  `retry_after_ms`；不要自动重试，也不要切换 Identity、Profile、Proxy 或 Browser
  Engine。
- `BROWSER_CHALLENGE_SUSPECTED`：不要点击、输入、提交或与疑似挑战交互；只读检查或
  导航离开可能仍可用。`challenge_release_unavailable: true` 表示该限制在当前
  Attachment 内无法解除。
- `BROWSER_CHALLENGE_REQUIRED`：Attachment 会被围栏并关闭。当前版本没有远程
  Viewer/controller，用户无法在该 Browser Process 内解决挑战。

`classifier_rules_version` 标识已发布的挑战规则版本。这些结果都不授权自动解决
CAPTCHA、回退宿主 Browser、轮换 Proxy 或规避自动化检测。

## 隔离与凭据

Provider Runtime 持有 Provider API Key 或本地登录状态。Browser 容器永远拿不到这些
凭据。子模型进程只拿到受信 broker socket 路径，无法读取 Browser channel
credential 或 active lease。

Browser 容器不暴露 TCP control port、CDP、WebDriver、VNC 或宿主 Browser。Chromium
只能通过 egress gateway 访问外部目标。Private、Loopback、Link-local、Metadata、
Rebinding、直连 DNS、QUIC、WebRTC UDP 和 DoH 绕过均被拒绝。

除非用户明确授权该项操作且部署策略允许，否则不要向网页输入密码、Token、支付数据或
其他 Secret。

## Session 复用与围栏

Core Conversation Identity 选择私有 Browser Session。同一 Conversation 的后续 Run
可以复用其 Provider Session 和 Browser Profile。每个 Run 都会获得新的 Attachment
和 control epoch。

Provider Session Recovery、Runtime Reattachment、Cancel、Expiry 或 Completion
都会使旧 Attachment 失效。只有 Runtime 验证 Lease、Profile、Chromium、Gateway 和
有界 Blank Observation 后才会发出 `ready`。`close` 会持久吊销精确 Attachment，
新的 MCP Connection 或 Runtime 重启后仍然有效。迟到 Action 会根据完整不可变身份
被拒绝，无法影响替代连接。

不同 Conversation 或 Principal 不能复用同一个 active Browser Attachment。持久登录
Profile 的部署边界是一个专用私有 Browser Agent。明文 Profile State 只在 Active
期间存在于 Browser tmpfs；Checkpoint 使用 Browser-only Root Key 和按身份派生的
Wrapping Key 加密。Root Key 与加密 Profile Payload 使用两个独立的 Browser-only
Volume，Provider Runtime 均不挂载。Inactive Profile 会在 30 天后过期。

## 持久历史

Screenshot、Frame、Page Metadata 和单个 Browser Action 都是临时数据，不能进入持久
Runtime Event 提交通道。OpenLinker 只保存固定数量的粗粒度 Browser Lifecycle Event
和最终 Run Result。持久 Event 数量不能随 `action_count` 增长。

## 故障排查

| 现象 | 检查项 |
| --- | --- |
| `browser_session` 不可用 | 确认兼容 CLI 暴露 `plugin.browser.serve`，然后重启或 Reload 宿主。 |
| Browser Runtime 不可用 | 确认 Browser compose override 已启用，私有 socket、channel credential 和 lease mount 一致。 |
| Agent Diagnose 拒绝 Capacity | Browser Profile 强制要求 `capacity: 1`。 |
| Attachment 已过期 | 让当前 Run 使用新 Attachment 重试；绝不要复用或编辑旧 lease。 |
| 页面无法访问私网地址 | 这是预期 egress 策略，不是 Browser 故障。 |
| 普通 Agent 行为改变 | 确认 `execution_profile` 仍为 `standard`；该 Profile 不加载 Browser。 |

Browser 失败不代表可以回退到宿主 Browser 或不受限网络客户端。
