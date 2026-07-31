# Browser 模式总览

[English](./browser-modes-overview.md) ·
[隔离 Browser](./isolated-browser.zh-CN.md) · [README](../README.zh-CN.md)

OpenLinker 的生产 Browser 工作流只有一个执行边界：容器隔离 Browser Runtime。

## 模式矩阵

| 问题 | 生产 Browser Agent |
| --- | --- |
| 原生入口 | Codex：`$use-isolated-browser`；Claude：`/openlinker:use-isolated-browser` |
| 方向 | OpenLinker Runtime → 子 Codex/Claude → 容器 Chromium |
| Browser Client Mode | `auto`、严格 `native` 或严格 `mcp` |
| Browser 控制 | 唯一 `browser_session` 入口：原生 Plugin 体验或 Runtime 直接注入 MCP |
| Session 复用 | Runtime 权威下的 Browser Session 与加密 Profile |
| 网络边界 | 容器 Browser 与强制 Egress Gateway |
| 可用宿主 | Codex 与 Claude Code |

## 选择隔离 Browser

执行必须位于容器隔离 Browser Runtime 中，尤其是远程可调用 Browser Agent 时，使用
`$use-isolated-browser` 或 `/openlinker:use-isolated-browser`。把该专用 Private Agent
配置为 `execution_profile: browser`。

封装后的 Codex 和 Claude Agent 镜像支持：

- `auto`：模型执行前校验镜像内、仅 Browser 的 Plugin；只有原生加载发生有界故障时
  才使用直接 MCP；
- `native`：严格加载仅 Browser 的 Codex 或 Claude Plugin；
- `mcp`：严格使用 Runtime 注入的 MCP 配置。

“原生”表示 Provider 原生 Plugin、Skill 和 Command 体验；可调用工具的传输仍是
MCP。它不是 ChatGPT 私有 Browser、Provider `computer` API、宿主 Browser 或第二套
Browser Engine。两种客户端模式最终进入同一个受信 Broker 和隔离 Browser Runtime；
一个 Provider Session Generation 只会看到一个入口。

封装 Agent 只使用 Agent Token 和 Provider API Key，既不要求也不接受
`OPENLINKER_USER_TOKEN`；完整的公开调用方 Plugin 不会安装到子 Provider 中。普通
交互式 Plugin 在没有 Runtime 权威时也刻意不声明 `openlinker_browser`。

只有权威 Runtime 在 Attachment 与 Preflight 均有效后才暴露 `browser_session`。
Observation 默认是 Semantic；只有确实需要像素时才请求 `screenshot` 或 `both`。

## 安全规则

Browser 工作流不把网页内容视为指令或授权。绝不能通过 Prompt 传递 Provider Key、
OpenLinker Token、Browser Channel Credential、Lease Identity、Cookie、已保存密码或
无关浏览器状态。

浏览不授权登录、提交、购买、删除、发布、权限变更或其他高影响操作。到达 Phase 1
边界时必须停止。

初始化与故障恢复请继续阅读[隔离 Browser 指南](./isolated-browser.zh-CN.md)。
