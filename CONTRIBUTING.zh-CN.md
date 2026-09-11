# 贡献 OpenLinker Plugin

English documentation: [CONTRIBUTING.md](./CONTRIBUTING.md)

本仓库维护 Codex/Claude 原生包及可复用 Provider/Browser 执行实现，原生安装 archive
包含校验过的 Plugin 宿主二进制。

## 开发环境

```bash
npm install
npm test
npm run test:go
npm run check:go-boundaries
npm run check:agent-runtime-integration
npm --prefix packages/browser-runtime/browser-engine ci --ignore-scripts
npm run test:browser-engine
npm run test:native-chrome
```

根 npm 工具没有运行时依赖；engine 有独立固定版本 npm graph。可选宿主验证器、Docker、
凭据或不可变发布 artifact 不可用时必须明确说明。

## 范围边界

- `packages/agent-adapters`：Provider/session 执行、能力选择、Agent 配置/诊断/
  生命周期，以及 SDK Browser 扩展。
- `packages/browser-runtime`：纯 Browser 协议/client/tool server、Profile、
  engine/native 资源、Browser 服务、网络策略和 egress。
- 原生 manifest、canonical Skill、轻量包、固定 CLI 解析、Dockerfile、便携 compose
  和镜像/Provider 回归门禁属于本仓库。
- Plugin 在 `internal/pluginhost` 组装原生 MCP/Agent/Browser 命令，Cobra 仅用于宿主入口；
  Plugin 不得反向依赖 CLI，CLI 不包含本地执行适配器。
- 纯 Browser 包/服务不得传递依赖 SDK；现有 SDK Runtime Worker 是唯一交付/恢复实现。
- Core/Cloud、Agent Node 应用和 twv1 运维不属于此处。原生 archive 不得嵌入 secret、
  runtime 状态或浏览器可执行文件。

## PR 要求

保留命令/MCP/wire 契约、凭据隔离、Session identity、Profile 格式、UID/GID 和卷名。
同步双语文档，保持 canonical Skill 字节一致，以及 artifact/publication fail-closed
门禁。边界扫描必须匹配真实包并检查传递依赖，缺失源码/测试根目录不得静默通过。

允许临时 workspace/proxy 验证本地源码，但它们不是依赖已发布的证据，不得添加永久相对
`replace`。明确说明发布顺序和兼容影响。

## 发布检查

遵循 [RELEASE.zh-CN.md](./RELEASE.zh-CN.md)。Module CI/发布独立于 CLI；native/image 发布须校验 Plugin 宿主、SDK/Node 模块校验和、
平台和源码身份。凭据或
artifact 不可用时明确报告未执行/阻塞，不得当作验收通过。

## 安全与许可证

私下报告漏洞请遵循 [SECURITY.md](./SECURITY.md)。贡献采用本仓库 Apache-2.0 许可证。
