# 发布流程

原生 MCP 与深度 Agent/Browser 执行由 `openlinker-plugin-host` 提供，迁移说明见 [HOST.md](./HOST.md)。

1. 先公开 Node 协议模块，再固定其不可变版本与真实 checksum。
2. 测试并标记 Plugin 源码；module CI 不依赖 CLI artifact。
3. 在干净不可变 tag 上运行 Release 并开启 `publish_native`，构建六平台宿主，
   校验 SDK/Node 依赖、入口和目标平台，记录源码/version/SHA-256/build info，随两个原生包分发。
4. Provider 镜像从相同 Plugin 归档源码构建宿主，传入与归档一致的 `OPENLINKER_PLUGIN_COMMIT`。
   `/opt/openlinker/host-build-info.json` 由 root 拥有，Worker 可读，不再下载 CLI。
5. 调用类 Skill 保留独立 CLI 安装器；三个 CLI lock 只能从实际发布的 CLI 生成，不能移动旧 tag。

发布前运行 `npm test`、`npm run test:go`、`npm run check:go-boundaries`、
`npm run check:agent-runtime-integration`、`GOWORK=off go mod verify` 与 `npm run release:check`。
干净提交上执行 `node scripts/package-native-hosts.mjs dist/native <tag>` 验证六平台打包。
原生 archive 包含宿主和元数据、manifest/Skill/command，不包含凭据、状态、Go 源码或浏览器引擎。
Git/source 安装需要另外构建并校验宿主，不会在任务运行时下载或回退 CLI。

发布镜像前须通过 engine/native、Linux Browser/Egress 隔离、真实 Provider Session/取消、
Codex/Claude Browser、SBOM 与来源门禁。保留 `run_live_provider`、`publish_images` 检查。
缺凭据或不可用 artifact 只能记未验证；不得以 replace、临时 proxy、虚构 checksum 代替发布证据。

twv1 部署和运行中 Worker 切换由根仓处理。先 drain/stop，再换执行文件，保留身份、
session/Profile/spool、凭据及旧发布物用于回滚。旧原生包仍需旧完整 CLI，必须配套升级包与宿主。
