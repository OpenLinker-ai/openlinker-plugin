# 发布流程

English documentation: [RELEASE.md](./RELEASE.md)

Plugin 同时拥有可复用 Go module、轻量原生宿主 archive 和独立 Provider/Browser 镜像。
它们属于不同发布阶段；私有 npm package 只是仓库工具，不发布到 npm。

## 发布顺序

1. 使用 `GOWORK=off` 测试 Go module、运行传递依赖门禁，再发布不可变 Plugin module
   tag。源码/module 阶段不得依赖新的下游 CLI archive。
2. CLI 固定已发布 module 和真实 checksum，完成独立测试与六平台构建，再发布 CLI。
3. 从已发布 CLI 更新 `shared/cli-lock.json` 和两个宿主副本，在后续 native-package
   release tag 中提交 lock；不得移动已有 module tag 来插入新 lock。
4. 在选定不可变 tag 上显式运行 Release workflow 并开启 `publish_native`。
   native 打包前会验证 CLI artifact。全部凭据/镜像门禁通过后，显式运行 Provider
   images，并同时开启 `run_live_provider` 和 `publish_images`。

tag 触发的 `go-module` job 与 native CLI lock 就绪状态独立。本地 workspace 或临时
module proxy 只能验证本地源码，不能证明依赖已经公开发布。不得发布相对 `replace`、
虚构 checksum 或未经验证的候选版本。

## Artifact 门禁

打 tag 前运行源码检查：

```bash
npm test
npm run test:go
npm run check:go-boundaries
npm run check:agent-runtime-integration
GOWORK=off go mod verify
GOWORK=off go build ./cmd/...
```

兼容 CLI artifact 发布后运行：

```bash
npm run lock:cli -- v0.x.y --write
npm run release:check
npm run check:provider-cli -- --expected-sdk v0.2.0-rc7
```

用真实发布的 CLI 版本替换示例。Provider CLI 门禁校验 shared lock 中 archive SHA-256、
目标 OS/架构、不可变 Plugin/SDK module 版本与 checksum、没有 replacement，以及
CLI 二进制真实 40 位 `vcs.revision` 和干净 Git checkout（`vcs.modified=false`）。
旧 lock 在迁移后的 CLI 发布前明确失败。
镜像将 `cli_commit`、`cli_release`、`cli_archive_sha256`、
`plugin_module_version` 和 `openlinker_go_version` 记录到
`/opt/openlinker/cli-build-info.json`。

Provider 镜像下载固定 CLI artifact，不下载 CLI 源码。最小 native Browser 包从同一
Plugin checkout 确定性生成，不下载本仓库尚未发布的 release。原生宿主 archive 只保留
manifest、Skill、command 与 CLI resolver，不加入 Go 源码、浏览器可执行文件、
私有状态或凭据。

## 验收与回滚

镜像发布前验证原生 manifest/parity/installer、engine/Native Messaging 测试、
Provider session/取消、Browser identity/Profile 契约、compose 拓扑、网络隔离、
真实 Codex/Claude Browser Run、SBOM 和 provenance。缺少凭据或不可变 artifact 是
阻塞条件，不得记作验收成功。

不得从本仓库 release workflow 部署 twv1；宿主专用验收和部署属于 root 运维，需另行
授权。保留上一组不可变 CLI/Plugin/image 以供回滚。本次源码所有权调整不得迁移、
重新加密、删除或重建 runtime 卷。

源码 PR 执行真实 Browser/Egress 验收，不依赖下游 CLI artifact。Provider 镜像构建通过
workflow dispatch 显式运行；发布还要求不可变 tag 和成功的真实 Provider 门禁。

## 打 tag

发布必须是维护者显式操作。使用不可变语义版本 tag；native-package tag 必须符合仓库
检查所要求的版本契约。pre-1.0 breaking change 记录在 `CHANGELOG.md`。
