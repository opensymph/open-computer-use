# CI/CD 说明

这个模板自带一套不依赖具体语言栈的 CI/CD 骨架。

## 当前 release 入口

- `scripts/release-package.sh`：构建 universal `Open Computer Use.app`，cross-compile Linux / Windows runtime，stage 三个既有 root/alias npm 包；每个包都会内置 macOS app、Linux binaries 和 Windows exes，并暴露 `open-computer-use` / `ocu` 等 npm bin 入口，产出 `dist/release/npm/*.tgz` 与 `dist/release/release-manifest.json`。npm staging 直接消费已构建好的 `dist/Open Computer Use.app`，不重复签名；App 的签名/公证只发生在 `build-macos-app.yml` 那一条链路上。
- `scripts/build-open-computer-use-app.sh`：构建 `dist/Open Computer Use.app`（release/dev、native/单架构/universal），可选 ad-hoc / identity 签名；release App 会内置 `Contents/Resources/ocu-app-install.sh` 安装引擎和 `OpenComputerUseGitCommit`。
- `scripts/package-macos-app.sh`：把构建好的 App 打成固定名字的 macOS 发行物三件套：`Open-Computer-Use-macOS.zip`、`SHA256SUMS.txt`、`app-manifest.json`（version / bundle_version / commit / sha256 / signing / 分架构 cdhash / channel）。
- `scripts/ocu-app-install.sh`：唯一的 App 安装引擎（校验 staged App、quarantine 处理、hooks、备份、原子换包、LaunchServices 注册、agent 终止、回滚），CLI 升级和 App 自升级共用。
- `scripts/build-open-computer-use-linux.sh`：本地构建实验性 Linux `open-computer-use` binary，支持 `arm64` / `amd64`；release package 会把这两个产物内置进既有 npm 包的 `dist/linux/`。
- `scripts/build-open-computer-use-windows.sh`：本地构建实验性 Windows `open-computer-use.exe`，支持 `arm64` / `amd64`；release package 会把这两个产物内置进既有 npm 包的 `dist/windows/`。
- `.github/workflows/release.yml`：支持 push semver tag 自动发布，也支持手动触发；`build-macos-app` job 调用可复用的 `build-macos-app.yml` 构建签名+公证+staple 后的 macOS App 三件套，`package-npm` job 下载该 artifact、解出 `dist/Open Computer Use.app` 后打包 npm 制品并发布，`github-release` job 把 npm 制品和 App 三件套一起挂到 `vX.Y.Z` GitHub Release 上。这条 stable 发版路径是 fail-closed 的路线 A：缺 `APPLE_*` 签名或公证 secrets 时 App 构建直接失败，不会退回 ad-hoc、不会跳过公证；打包前还会用 `codesign -R` 校验签名确实来自批准的 team `DSA846ZFPA` 且 identifier 为 `com.ifuryst.opencomputeruse`，并要求 staple 校验通过。ad-hoc 只存在于本地开发构建（`OPEN_COMPUTER_USE_CODESIGN_MODE=adhoc`），不会进入任何发布产物。
- `.github/workflows/build-macos-app.yml`：`workflow_call` 可复用的 App 构建流水线（签名配置 → 构建 → 路线 A 签名校验 → 公证 → staple → 打包 → 上传 `macos-app` artifact）。`require_identity` 输入可禁止在 secrets 缺失时静默退回 ad-hoc；发布路径（stable 与预发布 App）均以 `require_identity: true` 调用。workflow 通过 `on.workflow_call.outputs` 把 build job 的 `sha`/`sha7` 输出显式映射给调用方，调用方据此精确 checkout 实际构建的 commit 并打 tag。自动身份发现只接受 team `DSA846ZFPA` 的 `Developer ID Application:` 身份（`APPLE_SIGNING_IDENTITY` 显式指定时则由后续 `codesign -R` 团队校验兜底）。
- `.github/workflows/release-app.yml`：仅 `workflow_dispatch`（输入 `ref`），调用 `build-macos-app.yml`（同样 `require_identity: true`）对任意分支发布预签名/公证的预发布 App，tag 为 `app-build-YYYYMMDD-<sha7>`（prerelease，不会匹配 release.yml 的 tag 规则，也不会出现在 `/releases/latest`）。tag 与 release target 使用构建 job 解析出的 SHA，不会在发布时二次解析分支。

## 设计原则

这套默认流水线的目标，是在项目真正成形前先把交付链路搭起来，而不是假装已经知道未来项目该怎么 build 和 deploy。

当新项目的技术栈确定后，你应该继续在 `scripts/release-package.sh` 这条真实构建链路上扩展，而不是另起一套平行流程。

所有 GitHub Actions 都已经 pin 到 commit SHA。后续升级 action 时，也要继续保持这个约束。

## 推荐接入顺序

1. 保留 `ci.yml`，作为仓库的基础门禁。
2. 在 `scripts/ci.sh` 里继续叠加项目自己的验证命令。
3. 在 `scripts/release-package.sh` 已有的真实构建基础上继续扩展 release 产物。
4. 技术栈和环境稳定后，再补具体的部署 job。
5. 即使交付方式变化，SBOM 和 provenance 这类供应链能力也建议保留。

## 默认 release 产物

当前 release 流水线会产出：

- `dist/release/release-manifest.json`
- `dist/release/npm/opensymph-open-computer-use-<version>.tgz`
- GitHub Actions 中上传的 npm release artifact

也就是说，即使项目还没进入更复杂的部署阶段，仓库现在也已经同时具备了一条真实可复用的 npm 制品封装链路，以及一条由 git tag 驱动、签名+公证 fail-closed 的 macOS App zip 三件套（`Open-Computer-Use-macOS.zip` / `SHA256SUMS.txt` / `app-manifest.json`）交付链路。
