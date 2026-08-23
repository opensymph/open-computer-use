# 发版指南

这份文档约束这个仓库未来的 patch / minor release 流程，目标是避免再次出现 “git tag 已经发了，但 npm staging 产物版本还是旧值” 这类版本源不一致问题。

## 什么时候必读

- 只要任务里包含这些动作之一，就先读这份文档：
  - bump 版本
  - 打 release tag
  - 推送 release tag
  - 看 GitHub Actions release 失败原因
  - 重发某个失败版本

## 什么时候才发公开版本

- 日常修复、官方 `computer-use` 对齐验证和本地回归，默认只构建本地 app / 二进制并让 MCP client 指向本地构建产物。
- 不要把 patch release 当作普通验证手段；只有用户明确要求公开发版，或某个修复已经达到需要交付给外部用户的稳定状态时，才进入下面的 release checklist。
- 如果只是为了让 Codex 使用最新本地实现，优先更新本机 `~/.codex/config.toml` 中的 `open-computer-use` MCP server command，指向仓库本地构建产物，而不是 bump 版本、打 tag、推 release。

## 当前 release 入口

- 本地 staging / 打 tgz：`./scripts/release-package.sh`
- 本地 stage npm 包目录：`node ./scripts/npm/build-packages.mjs`
- 本地 publish：`node ./scripts/npm/publish-packages.mjs`
- CI workflow：`.github/workflows/release.yml`
- 用户可见发布记录：`docs/releases/feature-release-notes.md`
- GitHub Release 正文：`docs/releases/github/vX.Y.Z.md`
- GitHub Release 页面：workflow 使用审核过的英文 notes 文件创建或更新，不直接引用 PR 标题自动生成正文。

## 当前版本源

这个仓库当前有两类 release 版本源：

- npm staging 包版本：以 `plugins/open-computer-use/.codex-plugin/plugin.json` 里的 `version` 为准。
- GitHub Release 正文：以 `docs/releases/github/<tag>.md` 为准，文件名必须与实际 tag 完全一致。

也就是说：

- 只改 git tag，不改这个 manifest，不会得到新 npm 版本。
- `scripts/npm/build-packages.mjs` 会从这个 manifest 读取版本，再生成三个 root/alias staging 包；每个包内置 macOS、Linux 和 Windows runtime artifacts。
- 所以 release 前必须先把这份 manifest bump 到目标版本。
- 如果缺少目标 tag 对应的英文 notes，或者 notes 与 manifest/tag 不一致，`release-metadata` job 会在 npm job 启动前失败。

## Release Checklist

### 1. 先统一版本号

至少检查并同步这些位置：

- `plugins/open-computer-use/.codex-plugin/plugin.json`
- `packages/OpenComputerUseKit/Sources/OpenComputerUseKit/OpenComputerUseVersion.swift`
- `apps/OpenComputerUseSmokeSuite/Sources/OpenComputerUseSmokeSuite/main.swift`
- `packages/OpenComputerUseKit/Tests/OpenComputerUseKitTests/OpenComputerUseKitTests.swift`
- `apps/OpenComputerUseLinux/main.go`
- `apps/OpenComputerUseWindows/main.go`
- `docs/releases/feature-release-notes.md`
- `docs/releases/github/vX.Y.Z.md`

如果这轮 release 还改了其他对外暴露版本字符串，也要一起对齐，不要只改一半。

### 2. 准备并验证 GitHub Release notes

从 `docs/releases/github/TEMPLATE.md` 创建目标 tag 对应文件，并运行：

```bash
node ./scripts/validate-github-release-notes.mjs --tag v0.1.14
```

校验要求：

- tag 必须是 `vX.Y.Z` 或 `X.Y.Z`，并与 plugin manifest 版本一致。
- 正文以 `## What's Changed` 开头，包含 1-3 条用户可感知的英文变化。
- 正文不能包含 CJK 字符。
- 正文必须且只能包含一个指向当前 tag 的 `Full Changelog` 链接。

任何一项不满足都不要打 tag。tag push 后，`release-metadata` job 会再次执行相同校验，并在失败时阻止 npm job 启动。

### 3. 本地验证版本源已经生效

至少跑这三步：

```bash
node ./scripts/validate-github-release-notes.mjs --tag v0.1.14
swift test
node ./scripts/npm/build-packages.mjs --out-dir dist/release/npm-staging-check
```

然后直接检查 staging 包版本：

```bash
node -p "require('./dist/release/npm-staging-check/open-computer-use/package.json').version"
test -x "dist/release/npm-staging-check/open-computer-use/dist/linux/arm64/open-computer-use"
test -f "dist/release/npm-staging-check/open-computer-use/dist/windows/arm64/open-computer-use.exe"
test -x "dist/release/npm-staging-check/open-computer-use/bin/ocu"
node -e "const bin=require('./dist/release/npm-staging-check/open-computer-use/package.json').bin; if (bin.ocu !== 'bin/ocu') process.exit(1)"
node -e "if (require('./dist/release/npm-staging-check/open-computer-use/package.json').optionalDependencies) process.exit(1)"
```

如果这里打印的不是目标版本，不要打 tag。

如果当前 checkout 里已经有和目标版本一致的 `dist/Open Computer Use.app`，也可以临时加 `--skip-build` 跳过重复构建；但在干净 checkout 里不要默认加这个参数，否则 staging 脚本会因为缺少 `dist/Open Computer Use.app` 而失败。

### 4. 提交版本 bump

- 用单独 commit 提交 release version bump。
- commit message 要能直接看出这是 release 收口，而不是普通功能提交。

### 5. 打 tag 并推送

当前约定用 `vX.Y.Z`：

```bash
git tag -a v0.1.14 -m "v0.1.14"
git push origin main
git push origin v0.1.14
```

tag push 后，`.github/workflows/release.yml` 会打包 npm 制品、把它们附到一个自动创建的 GitHub Release 上；npm 发布只有在配置了 `NPM_TOKEN` secret 时才会在 tag push 中自动执行，也可以用 workflow_dispatch 手动触发（勾选 publish_to_npm，支持 OIDC trusted publishing）。

### 6. 检查 GitHub Release notes

每次 tag push 后都要检查 GitHub Release 页面，不要只确认 workflow 绿了：

```bash
gh release view v0.1.14 --json body,url
```

workflow 会使用 `docs/releases/github/<tag>.md` 创建新 Release；如果 Release 已经存在，则用同一文件更新正文。GitHub 自动生成 notes 不再是正文来源，因此 PR 标题使用中文也不会改变公开 Release 的语言。

最低要求：

- release body 必须与仓库里的目标 notes 文件一致。
- `What's Changed` 必须列出本次用户可感知的 1-3 个英文变化。
- 保留 `Full Changelog` 链接。

## Release 失败时怎么查

### 1. 先看最新 run

```bash
gh run list -R opensymph/open-computer-use --limit 10
gh run view -R opensymph/open-computer-use <run-id> --log-failed
```

### 2. 重点看哪一类错误

- `release-metadata` 失败
  - 先本地运行 `node ./scripts/validate-github-release-notes.mjs --tag <tag>`。
  - 检查 `docs/releases/github/<tag>.md` 是否存在、manifest 版本是否匹配、正文是否包含 CJK，以及 Full Changelog 是否指向当前 tag。
- `npm error 403 ... You cannot publish over the previously published versions`
  - 通常不是 token 权限问题，而是 staging 包版本仍然是旧版本。
  - 先回头检查 `plugin.json` 的 `version`，再检查 staging 包实际产出的 `package.json`。
- `npm error 404 Not Found - PUT https://registry.npmjs.org/<package>`
  - 先确认 registry 上目标包旧版本是否仍可见：`npm view <package> versions --json`。
  - 当前 publish 脚本会在发布前跳过已经存在的同版本 package，并对 publish 失败做短暂重试；如果 GitHub Actions OIDC 可用，会优先用 `--provenance` 走 trusted publishing，再回退到 `NODE_AUTH_TOKEN`。如果 tag 重发前某个 package 已经部分发布成功，重新跑同一个 release 不会因为该 package 已存在而中断。
- `npm error need auth ... You need to authorize this machine using npm adduser`
  - 如果日志显示已经选择 `GitHub Actions OIDC trusted publishing`，优先检查 CI 里的 npm CLI 版本；trusted publishing 需要 npm `11.5.1+`，当前 release workflow 的 npm package job 使用 Node `24` 并显式检查 npm 版本。
  - 如果 npm CLI 版本满足要求仍报这个错误，说明 npmjs.com 包侧还没有把当前 GitHub repo / workflow 文件配置成 trusted publisher。
- 构建阶段失败
  - 优先看 `Build npm release artifacts` 或 Swift 编译错误。
- publish 认证失败
  - 再去看 `.github/workflows/release.yml`、`scripts/npm/publish-packages.mjs` 和 npm trusted publishing / token fallback 配置。

## 当前已知边界

- `Open Computer Use` 的 npm release 产物在没有配置组织级 `APPLE_CERTIFICATE` / `APPLE_CERTIFICATE_PASSWORD` secrets 时，仍会退回 ad-hoc signing；配置后会先导入 `Developer ID Application` 证书，再按该 identity 统一签名；secrets 缺失时不会阻塞整条 release。
- `open-computer-use` npm root 包会内置六个 `os-arch` native artifacts，包体积会比 macOS-only 版本更大；release 前要确认 staging 包里包含 `dist/Open Computer Use.app`、`dist/linux/` 和 `dist/windows/`，并确认 launcher 没有声明 `optionalDependencies`。

## 如果 tag 已经打错了

如果远端 tag 已经指向错误 commit，先删 tag，再修版本源，再重打。

本地删 tag：

```bash
git tag -d v0.1.14
```

远端删 tag：

```bash
git push origin :refs/tags/v0.1.14
```

修好后再重新创建并推送同名 tag。

## 文档同步要求

每次 release 都至少同步这几类文档：

- `docs/releases/feature-release-notes.md`
- `docs/releases/github/vX.Y.Z.md`
- 如果 release 流程本身有变化，这份 `docs/releases/RELEASE_GUIDE.md`

如果一次 release 暴露出新的流程坑，就不要只在聊天里记住，直接补进这份文档。
