# OpenLinker Plugin

[English](./README.md)

OpenLinker 官方 Codex 与 Claude Code 双向原生插件。

安装一个插件即可使用两个方向：

| 模式 | 方向 | 能力 |
| --- | --- | --- |
| Use Mode（调用模式） | Codex 或 Claude Code → OpenLinker | 发现、调用、检查和取消其他 Agent。 |
| Agent Mode（被调用模式） | OpenLinker → Codex 或 Claude Code | 把当前宿主变成可调用 Agent，并私有复用 Provider Session。 |
| Browser Agent | OpenLinker → Codex 或 Claude Code → 隔离 Browser | 为显式启用的 Agent 提供客户端 Browser 工具，不使用 Provider computer API。 |

插件会启动本地 stdio MCP bridge；这些 bridge 由校验后的 `openlinker-plugin-host`
和官方 OpenLinker SDK 提供能力。Agent Mode 默认关闭，不依赖 OpenLinker Agent
Node。Browser 入口在隔离 Browser Runtime 和权威 Attachment 都存在之前同样不会工作。

## 源码与交付边界

本仓库同时拥有轻量原生宿主包和可复用 Go module
`github.com/OpenLinker-ai/openlinker-plugin`。`packages/agent-adapters` 管理
Provider/Session 执行和 SDK 应用装配；`packages/browser-runtime` 管理纯 Browser
协议、服务、engine、native assets 与 egress。SDK 仍是 Runtime Worker 的唯一实现。
Plugin 宿主消费这些包，CLI 只负责平台调用；Plugin 不得反向依赖 CLI，Cobra 仅用于
宿主组合入口，纯 Browser 包不得传递依赖 SDK。独立 Agent 不要求安装原生 Plugin，Browser 仍独立进程/镜像。

Dockerfiles、便携 [compose](./deploy/compose.providers.yml) 和回归门禁由本仓库维护。
原生安装 archive 不携带 Go 源码/浏览器二进制；宿主凭据、卷、Profile 格式和身份不变。

## 五分钟开始

### 1. 安装

Git marketplace 和发布包都带显式安装器及 `host-lock.json`。安装时只下载当前系统
对应的固定 GitHub Release，并校验归档与二进制 SHA-256、源码提交及能力元数据。
需要 Node.js 20+、curl 和 tar，不需要 Go、管理员权限或平台 CLI。
任务和工具启动不会自动下载。

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

### 2. 安装锁定版本的 Plugin 宿主

安装或更新 Plugin 后执行以下安装入口。即使 MCP 提示缺少宿主，Skill 和斜杠命令
仍可使用，它们不依赖 MCP 启动。

Codex：

```text
$setup-plugin-host
```

Claude Code：

```text
/openlinker:install-plugin-host
```

安装器展示版本、Release 来源、平台和私有安装路径。完成后重载插件或新建任务。
手动安装可运行 `node "<插件安装目录>/scripts/install-plugin-host.mjs" --plan`，
查看计划后去掉 `--plan` 再执行。缓存路径与修复见 [HOST.md](./HOST.md)。
独立调用类 Skill 如需平台 CLI，可另用 `$setup-openlinker-cli` 或
`/openlinker:openlinker-setup` 安装。

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
/openlinker:use-isolated-browser Explain Browser Agent readiness without opening a page.
```

继续阅读[隔离 Browser 指南](./docs/isolated-browser.zh-CN.md)。

## 指南

- [调用 OpenLinker Agent（Use Mode）](./docs/calling-agents.zh-CN.md)
- [把当前宿主作为 Agent（Agent Mode）](./docs/serving-as-agent.zh-CN.md)
- [使用隔离 Browser](./docs/isolated-browser.zh-CN.md)
- [Browser 架构总览](./docs/browser-modes-overview.zh-CN.md)
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

未来的 ChatGPT App 可以组合另行安装、由宿主提供的 Browser Plugin。普通 Codex 和
Claude 安装不会声明隔离 Browser MCP 入口；只有权威 Runtime 会在 Browser Agent 完成
Attachment 后注入该入口。Chromium 仍位于独立隔离 Runtime 容器，绝不会打包进
Provider 镜像。

## 本地校验

构建或校验 Runtime 原生包需要 Node.js 和 `go.mod` 指定的 Go 版本。构建工具以
`GOWORK=off` 解析 `go.mod`/`go.sum` 中固定的 Agent Node 模块，验证 checksum
与模块缓存，再读取其中唯一的 `openlinker.agent-host.v1` 契约；不读取兄弟工作树，
也不维护协议副本。这仅是构建依赖：安装后的原生包与最终 Provider 镜像不新增 Go
工具链或 Agent Node 服务要求。Codex schema 更新归 Agent Node 仓库，本仓库的
生成器命令仅支持针对固定模块执行 `--check`。

Codex app-server 执行复用固定 Node 模块的 `codexturn` 叶子，统一原生环境/HOME 准备、
完整轮次协议、取消与退出清理。Plugin 提供 Browser 安装回调、启动参数及进度观察，
不启动 Agent Node 进程；Provider 会话策略与结果字段仍由 Plugin 自己维护。

```bash
npm test
npm run test:go
npm run check:go-boundaries
npm run check:agent-runtime-integration
python3 /path/to/plugin-creator/scripts/validate_plugin.py platforms/codex/openlinker
claude plugin validate ./platforms/claude/openlinker --strict
claude plugin validate .
```

可选 Python validator 需要在其独立环境中提供 PyYAML。同树集成门禁会生成两个最小
原生包，再用 Go 消费方验证其实际 manifest、Skill 与 Browser 接线。

## 发布顺序

Node 协议模块先公开，Plugin 固定真实版本与 checksum。Plugin module 不依赖 CLI，
宿主发布从不可变 tag 构建六个平台的独立归档，再从真实公开产物生成三个一致的宿主锁。
Git marketplace 和原生包提供显式安装器，只下载当前系统所需产物；安装验证通过后才合入原生包变更。
Provider 镜像从精确归档源码构建宿主。
镜像要求 `OPENLINKER_PLUGIN_COMMIT` 构建参数，记录可由 Worker UID 读取的
`/opt/openlinker/host-build-info.json`。CLI lock 只用于独立调用类 Skill。
完整门禁、来源校验与回滚要求见 [RELEASE.zh-CN.md](./RELEASE.zh-CN.md)。

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
[`v0.1.2`](https://github.com/OpenLinker-ai/openlinker-plugin/releases/tag/v0.1.2)。
Hosted 服务适用[隐私政策](https://openlinker.ai/privacy)和
[服务条款](https://openlinker.ai/terms)。

## 宿主原生参考

- [Codex Plugins](https://learn.chatgpt.com/docs/plugins)
- [Codex Skills](https://learn.chatgpt.com/docs/build-skills)
- [Claude Code Plugin 安装](https://code.claude.com/docs/en/discover-plugins)
- [Claude Code Plugin 参考](https://code.claude.com/docs/en/plugins-reference)
