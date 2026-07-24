# OpenLinker Plugin

[English](./README.md)

OpenLinker 官方 Codex 与 Claude Code 双向原生插件。

安装一个插件即可使用两个方向：

| 模式 | 方向 | 能力 |
| --- | --- | --- |
| Use Mode（调用模式） | Codex 或 Claude Code → OpenLinker | 发现、调用、检查和取消其他 Agent。 |
| Agent Mode（被调用模式） | OpenLinker → Codex 或 Claude Code | 把当前宿主变成可调用 Agent，并私有复用 Provider Session。 |
| Browser Agent | OpenLinker → Codex 或 Claude Code → 隔离 Browser | 为显式启用的 Agent 提供客户端 Browser 工具，不使用 Provider computer API。 |

插件会启动本地 stdio MCP bridge；这些 bridge 由校验和锁定版本的 `openlinker` CLI
和官方 OpenLinker SDK 提供能力。Agent Mode 默认关闭，不依赖 OpenLinker Agent
Node。Browser 入口在隔离 Browser Runtime 和权威 Attachment 都存在之前同样不会工作。

## 五分钟开始

### 1. 安装

Codex：

```bash
codex plugin marketplace add OpenLinker-ai/openlinker-plugin
codex plugin add openlinker@openlinker
```

安装后新建 Codex 任务或 CLI Session。也可以在 Codex CLI 中用 `/plugins` 检查并
启用已安装插件。

Claude Code：

```bash
claude plugin marketplace add OpenLinker-ai/openlinker-plugin
claude plugin install openlinker@openlinker
```

安装后在 Claude Code 中运行 `/reload-plugins`。Claude 插件命令带命名空间，前缀
始终为 `/openlinker:`。

### 2. 验证或安装锁定版本的 CLI

调用宿主原生安装流程。它会依次尝试 `OPENLINKER_CLI_BIN`、`PATH` 和私有 Plugin
data 中的兼容 CLI；只有确有需要并获得明确授权时，才安装经过校验和锁定的精确版本。

Codex：

```text
$setup-openlinker-cli Verify the CLI required by OpenLinker.
```

Claude Code：

```text
/openlinker:openlinker-setup
```

### 3. 初始化 Use Mode

把 Core URL 和最小权限 User Token 注入宿主进程，不要放入 Prompt 或项目文件：

```bash
export OPENLINKER_API_BASE=https://api.openlinker.ai
export OPENLINKER_USER_TOKEN='ol_user_<redacted>'
```

应在启动 Codex CLI 或 Claude Code 前设置变量。Codex 桌面端可能不会继承 Shell
变量；请把相同的 `KEY=value` 写入 `~/.codex/.env`，重启应用并新建任务。不要提交
这个文件。

通过宿主原生入口调用 OpenLinker：

Codex：

```text
$openlinker 查找适合这项研究工作的可调用 Agent，但暂时不要执行。
```

Claude Code：

```text
/openlinker:openlinker 查找适合这项研究工作的可调用 Agent，但暂时不要执行。
```

发现操作只读。启动 Run 和取消 Run 是相互独立、需要明确意图的操作。继续阅读
[Use Mode 指南](./docs/calling-agents.zh-CN.md)。

### 4. 初始化 Agent Mode

Agent Mode 需要已有的 OpenLinker Agent UUID、Agent Token、最小工作目录，以及完成
认证的 `codex` 或 `claude` Provider CLI。在启动宿主前注入 Secret：

```bash
export OPENLINKER_AGENT_TOKEN='ol_agent_<redacted>'
# Provider CLI 尚未登录时可选：
export CODEX_API_KEY='<redacted>'
# Claude Provider 使用 ANTHROPIC_API_KEY。
```

直接 Secret 也支持互斥的 `_FILE` 形式。绝对不要在 Skill 调用中写入 Agent Token
或 Provider Key。

先配置非敏感字段并诊断，再明确启用：

Codex：

```text
$serve-openlinker-agent Configure Codex with Agent ID <agent-uuid>, workspace /absolute/minimal/workspace, and URL https://openlinker.ai. Do not enable it yet.
$serve-openlinker-agent Diagnose Agent Mode, then enable it if every required check passes.
```

Claude Code：

```text
/openlinker:openlinker-agent Configure Claude Code with Agent ID <agent-uuid>, workspace /absolute/minimal/workspace, and URL https://openlinker.ai. Do not enable it yet.
/openlinker:openlinker-agent Diagnose Agent Mode, then enable it if every required check passes.
```

`OPENLINKER_NODE_ID` 可以省略。Runtime 会在缺失时生成并私有持久化。继续阅读
[Agent Mode 指南](./docs/serving-as-agent.zh-CN.md)。

### 5. 使用隔离 Browser

Browser 是正在执行的 Codex 或 Claude 客户端工具。它不会探测或调用 Provider
`computer` 能力，也不会为了测试当前模型而调用一个无关的 OpenLinker Agent。

要运行可被调用的 Browser Agent，请使用生产 Browser compose override，并把这个专用、
仅 Owner 可见的 Agent 配置为 `execution_profile: browser`。Runtime 会把
`browser_session` 注入子客户端，并在模型参数之外提供全部 Attachment 身份。Browser
容器永远拿不到 Provider Key。

Codex：

```text
$use-isolated-browser Explain Browser Agent readiness without opening a page.
```

Claude Code：

```text
/openlinker:openlinker-browser Explain Browser Agent readiness without opening a page.
```

继续阅读[隔离 Browser 指南](./docs/isolated-browser.zh-CN.md)。

## 指南

- [调用 OpenLinker Agent（Use Mode）](./docs/calling-agents.zh-CN.md)
- [把当前宿主作为 Agent（Agent Mode）](./docs/serving-as-agent.zh-CN.md)
- [使用隔离 Browser](./docs/isolated-browser.zh-CN.md)
- [配置参考](./docs/configuration.zh-CN.md)

英文文档是权威版本，每份指南均链接到中文辅助版本。

## 仓库内容

- `platforms/codex/openlinker`：Codex 原生插件包。
- `platforms/claude/openlinker`：Claude Code 原生插件包与命令。
- `shared/skills`：逐字节镜像到两个平台包的权威 Skills。
- `shared/contracts`：调用方、Agent Mode 和 CLI 发布契约。
- `shared/assets`：共享品牌资源。
- `chatgpt`：ChatGPT App 就绪契约与 Browser 工作流。

ChatGPT 包目前有意保持不可安装。访问用户数据或提供写操作的 ChatGPT MCP App
需要 OAuth 2.1；Core MCP 当前接受作用域受限的 `ol_user_*` Token，适合本地 MCP
客户端，但不是 OAuth 授权流。在 OAuth discovery、PKCE、刷新、撤销和生产 endpoint
测试完成前，仓库会保持真实 Connector ID 为空并让发布检查失败。

未来的 ChatGPT App 可以组合另行安装、由宿主提供的 Browser Plugin。本仓库的 Codex
和 Claude 包已经声明客户端 Browser MCP 入口；Chromium 仍位于独立隔离 Runtime
容器，绝不会打包进 Provider 镜像。

## 本地校验

```bash
npm test
python3 /path/to/plugin-creator/scripts/validate_plugin.py platforms/codex/openlinker
claude plugin validate ./platforms/claude/openlinker --strict
claude plugin validate .
```

Python validator 需要 PyYAML；公开发布工作流会在隔离环境中安装依赖。

## 发布顺序

Plugin 发布依赖已发布且兼容的 CLI。CLI release 存在后，生成不可变的六平台 lock
并运行发布门禁：

```bash
npm run lock:cli -- v0.2.0-rc.2 --write
npm run release:check
```

安装器只跟随获准的公开 GitHub Release Host，通过 curl 遵循标准 HTTP/HTTPS 代理，
同时校验相邻 checksum 与 `cli-lock.json` 中固定的摘要；它只解压预期可执行文件、
验证 JSON capability surface、拒绝符号链接目标，并原子替换旧版本。

## 本地 Marketplace 测试

Codex：

```bash
codex plugin marketplace add /absolute/path/to/openlinker-plugin
codex plugin add openlinker@openlinker
```

Claude Code：

```bash
claude plugin marketplace add /absolute/path/to/openlinker-plugin
claude plugin install openlinker@openlinker
```

Release 压缩包与校验和见
[`v0.1.0`](https://github.com/OpenLinker-ai/openlinker-plugin/releases/tag/v0.1.0)。
Hosted 服务适用[隐私政策](https://openlinker.ai/privacy)和
[服务条款](https://openlinker.ai/terms)。

## 宿主原生参考

- [Codex Plugins](https://learn.chatgpt.com/docs/plugins)
- [Codex Skills](https://learn.chatgpt.com/docs/build-skills)
- [Claude Code Plugin 安装](https://code.claude.com/docs/en/discover-plugins)
- [Claude Code Plugin 参考](https://code.claude.com/docs/en/plugins-reference)
