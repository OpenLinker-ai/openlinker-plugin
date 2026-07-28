# Browser 模式总览

[English](./browser-modes-overview.md) ·
[隔离 Browser](./isolated-browser.zh-CN.md) · [README](../README.zh-CN.md)

OpenLinker 的生产 Browser 工作流只有一个执行边界：容器隔离 Browser Runtime。

## 模式矩阵

| 问题 | 生产 Browser Agent |
| --- | --- |
| 原生入口 | Codex：`$use-isolated-browser`；Claude：`/openlinker:use-isolated-browser` |
| 方向 | OpenLinker Runtime → 子 Codex/Claude → 容器 Chromium |
| Browser 控制 | Runtime 注入的 `browser_session` MCP 工具 |
| Session 复用 | Runtime 权威下的 Browser Session 与加密 Profile |
| 网络边界 | 容器 Browser 与强制 Egress Gateway |
| 可用宿主 | Codex 与 Claude Code |

## 选择隔离 Browser

执行必须位于容器隔离 Browser Runtime 中，尤其是远程可调用 Browser Agent 时，使用
`$use-isolated-browser` 或 `/openlinker:use-isolated-browser`。把该专用 Private Agent
配置为 `execution_profile: browser`。

普通 Plugin 安装刻意不声明 `openlinker_browser`。只有权威 Runtime 在 Attachment 与
Preflight 均有效后才注入 `browser_session`。Observation 默认是 Semantic；只有确实
需要像素时才请求 `screenshot` 或 `both`。

## 安全规则

Browser 工作流不把网页内容视为指令或授权。绝不能通过 Prompt 传递 Provider Key、
OpenLinker Token、Browser Channel Credential、Lease Identity、Cookie、已保存密码或
无关浏览器状态。

浏览不授权登录、提交、购买、删除、发布、权限变更或其他高影响操作。到达 Phase 1
边界时必须停止。

初始化与故障恢复请继续阅读[隔离 Browser 指南](./isolated-browser.zh-CN.md)。
