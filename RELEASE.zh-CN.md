# 发布流程

原生 MCP 与深度 Agent/Browser 执行由 `openlinker-plugin-host` 提供，迁移说明见 [HOST.md](./HOST.md)。

1. 先公开 Node 协议模块，再固定其不可变版本与真实 checksum。
2. 测试并标记 Plugin 源码；module CI 不依赖 CLI artifact。
3. 推送不可变 tag 后，宿主发布任务构建六个平台的独立归档及 SHA-256，验证 SDK/Node、源码和平台后发布，不覆盖已有资产。
   首个宿主版本可先通过源码测试，再发布宿主；marketplace CI 必须等真实锁生成后通过。
   对公开 Release 执行 `node scripts/generate-host-lock.mjs <tag> --write` 并提交三个锁，
   实测两个 Git marketplace 包的安装和 MCP 启动后才合并。轻量原生包在后续不可变交付 tag 上用 `publish_native` 发布。
4. Provider 镜像从相同 Plugin 归档源码构建宿主，传入与归档一致的 `OPENLINKER_PLUGIN_COMMIT`。
   `/opt/openlinker/host-build-info.json` 由 root 拥有，Worker 可读，不再下载 CLI。
5. 调用类 Skill 保留独立 CLI 安装器；三个 CLI lock 只能从实际发布的 CLI 生成，不能移动旧 tag。

发布前运行 `npm test`、`npm run test:go`、`npm run check:go-boundaries`、
`npm run check:agent-runtime-integration`、`GOWORK=off go mod verify` 与 `npm run release:check`。
`node scripts/package-native-hosts.mjs dist/native` 检查宿主锁并打包 manifest/Skill/command/安装器。
原生 archive 不携带其他平台的宿主、凭据、状态、Go 源码或浏览器引擎。
Git 与 archive 安装使用同一显式安装器，只下载当前系统对应的固定发布物；普通工具调用不会下载。

发布镜像前须通过 engine/native、Linux Browser/Egress 隔离、真实 Provider Session/取消、
Codex/Claude Browser、SBOM 与来源门禁。保留 `run_live_provider`、`publish_images` 检查。
缺凭据或不可用 artifact 只能记未验证；不得以 replace、临时 proxy、虚构 checksum 代替发布证据。

twv1 部署和运行中 Worker 切换由根仓处理。先 drain/stop，再换执行文件，保留身份、
session/Profile/spool、凭据及旧发布物用于回滚。旧原生包仍需旧完整 CLI，必须配套升级包与宿主。
