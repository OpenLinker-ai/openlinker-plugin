# OpenLinker Browser Engine

This private package is the Browser implementation behind
`openlinker.browser.v2`. It is not an MCP server, Plugin payload, Provider
adapter, or independently supported CLI.

The Go Browser Runtime supervises this process over a closed newline-delimited
JSON protocol. It starts the process with an explicit environment allowlist;
Provider, Agent, User, channel, and Profile-encryption credentials are never
inherited. The engine accepts only the Phase 1 action enum and never accepts
arbitrary JavaScript, CDP, shell, path, upload, download, clipboard, extension,
or credential-input requests.

The Browser image contains this package and a pinned Playwright/Chromium
generation. `browser-versions.json` records the exact observed Chromium
version for each published Linux architecture. A trusted Runtime Agent assigns
the authoritative lease and exposes the Browser MCP tool to the ordinary Codex
or Claude Code client. Browser execution never uses a Provider-native
computer-use API.

Local contract checks:

```bash
npm ci --ignore-scripts
npm run lint
npm test
```

## Operator-built Chrome channel

The official multi-architecture image remains Chromium-only. An operator may
locally extend a pinned `openlinker-browser-runtime` amd64 image with
`Dockerfile.browser.chrome`; OpenLinker release workflows do not publish that
result.

Prepare a normalized `chrome.tar` whose entries are all under `chrome/` and
whose executable is `chrome/chrome`, plus a strict `chrome.lock.json` matching
`test/browser-image/chrome-lock.example.json`. Then build with explicit locked
distribution and version:

```bash
docker build \
  --platform linux/amd64 \
  -f Dockerfile.browser.chrome \
  --build-arg OPENLINKER_BROWSER_BASE=openlinker-browser-runtime@sha256:<digest> \
  --build-arg OPENLINKER_BROWSER_DISTRIBUTION=chrome_for_testing \
  --build-arg OPENLINKER_BROWSER_VERSION=<full-version> \
  --build-arg OPENLINKER_BROWSER_PROFILE_GENERATION=<new-positive-generation> \
  -t operator/openlinker-browser-runtime-chrome:<version> \
  .
```

The build verifies the artifact digest, archive containment, Browser-reported
version and fixed `/opt/google/chrome/chrome` discovery path. Runtime input
cannot set an executable path, and `channel: "chrome"` never falls back to
Chromium. The required new Profile generation makes the engine/distribution
change explicit and preserves the prior Chromium generation. The operator
remains responsible for Browser acquisition, use, and distribution rights.

## Image-baked Native Chrome backend

Linux Codex can prefer an Official Chrome backend whose Chrome tree, pinned
extension, Native Messaging Host, and canonical asset lock are baked into the
Browser Runtime image. Extend an ordinary Browser image referenced by immutable
registry digest with authorized artifacts from the repository build context:

```bash
docker build \
  -f Dockerfile.browser.native-chrome \
  --build-arg OPENLINKER_BROWSER_BASE=registry.example/openlinker-browser-runtime@sha256:<digest> \
  --build-arg OPENLINKER_CHROME_VERSION=<full-version> \
  --build-arg OPENLINKER_EXTENSION_ID=hehggadaopoacecdllhhajmbjkdcmajg \
  --build-arg OPENLINKER_EXTENSION_VERSION=<full-version> \
  --build-arg OPENLINKER_NATIVE_CHROME_PROFILE_GENERATION=<new-positive-generation> \
  -t operator/openlinker-browser-runtime-native-chrome:<version> \
  .
```

The default artifact names are `chrome.tar`, `chrome.lock.json`,
`native-chrome-extension.tar`, and `native-chrome-extension.lock.json`; the two
lock examples live under `test/browser-image/`. The extension archive must
contain `extension/`, the authorized signed `extension/extension.crx`, a
Manifest V3 manifest with a fixed `key` whose derived ID matches the lock, and
the locked activation page. The image installs the CRX through Chrome's Linux
external-extension manifest and blocks other external extensions with managed
policy; it does not rely on command-line extension-loading flags. The build
rejects unsafe archive entries, mutable assets, version/digest/ID mismatches,
and missing components. Runtime startup performs no artifact download or
mount.

## 中文说明

此目录实现 `openlinker.browser.v2` 后面的容器内 Browser 引擎，不是 MCP
服务、Plugin 内容、Provider adapter 或独立 CLI。Go Browser Runtime 通过封闭的
JSON 行协议监管该进程，并使用显式环境白名单启动；Provider、Agent、User、通道
凭据和 Profile 加密密钥都不会继承给 Chromium 进程。

可信 Runtime Agent 分配权威 lease，并把 Browser MCP 工具暴露给普通 Codex 或
Claude Code 客户端；浏览器执行不使用 Provider 原生 computer-use API。

官方多架构基础镜像仍只包含 Chromium。Operator 可以用
`Dockerfile.browser.chrome`、本地规范化 `chrome.tar` 与严格
`chrome.lock.json` 构建 amd64 Chrome Channel 镜像；OpenLinker Release Workflow
不会发布该镜像。构建会校验 Digest、Archive 路径、Browser 实际版本及固定
`/opt/google/chrome/chrome` 位置，Runtime 不接受任意 Executable Path，也不会从
Chrome 静默回退到 Chromium。构建必须显式指定新的 Profile Generation，从而保留
原 Chromium 代际。Browser 的取得、使用与分发权利由 Operator 负责。

Linux Codex 还可使用 `Dockerfile.browser.native-chrome` 构建完整的 Native
Chrome Runtime 镜像。Official Chrome、固定 ID/版本的签名 CRX 扩展、Native
Messaging Host 和资产锁都会在构建时复制并校验到镜像内；扩展通过 Linux external
extension manifest 从镜像本地安装，运行时不挂载、不下载这些组件。
未显式选择时，Codex 按 Native Chrome、隔离 Chromium Native Plugin、隔离
Chromium Direct MCP 的顺序选择，并在 preflight 后锁定。
