# macOS window identity: same-bounds matching and closed-window ghosts

PR #3 的 window2 面有两个正确性问题需要收口。本文档记录调查结论、方案与验证证据；实现落在 `WindowManagement.swift`、`AccessibilitySnapshot.swift` 与新的 `AXWindowIdentitySPI.swift`。

## 问题 1：same-bounds 窗口的 AX 树错配

**现象（修复前）。** `WindowDirectory.matchAXWindow` 只按 frame 几何把 app 的 AX window 列表匹配到 CGWindowID。两个 bounds 完全相同的窗口会同时命中；tie-break 链是 title → focused → **第一个候选**。当 title 也相同（或 title 不可用，例如无 Screen Recording 授权）时返回第一个候选——即 `get_window_state` 可能把窗口 A 的截图（按 CGWindowID 抓取，本身正确）配上窗口 B 的 AX 树（root 错窗）。

**修复。** 可靠的 identity 解析优先，失败时显式报歧义、不猜测：

1. 新增 `AXWindowIdentitySPI`：runtime `dlopen`/`dlsym` 解析私有符号 `_AXUIElementGetWindow`（ApplicationServices/HIServices 导出，`OSStatus (AXUIElement, CGWindowID*)`），模式与既有 `SkyLightSPI` 一致——所有未公开 ABI 收敛在一个文件里作为 review 边界，符号缺失时 capability 降级而不是崩溃。
2. `matchAXWindow` 返回 `AXWindowMatch` 枚举：
   - `.matched(element)`：identity 精确命中（SPI 可用且某候选的 `_AXUIElementGetWindow` == 目标 CGWindowID）→ 唯一 frame 命中 → 既有 title/focused tie-break → 唯一 title 兜底。
   - `.ambiguous(count)`：多个 frame 命中且无唯一 tie-break。**只在 identity 映射不可用/不完整时可达**——映射可用且全量命中失败时直接 `.notFound`。
   - `.notFound`。
3. `SnapshotBuilder.buildWindow`（`get_window_state`）遇到 `.ambiguous` 抛出显式错误，不做任何 best-effort 恢复：
   `ambiguousWindow(<id>): <count> windows of <app> share the same bounds; the accessibility tree cannot be matched to this window. Move or resize one of them and re-observe with list_windows.`
4. `activate_window` 遇到 `.ambiguous` 抛同一错误（不能对任意一个同 bounds 窗口做 raise/focus）；`unminimizeIfNeeded` 在歧义时 no-op。

**保证。** `_AXUIElementGetWindow` 可解析时（本机 macOS 27 已验证存在且映射准确），identity 按 CGWindowID 精确解析，`get_window_state` 返回的树 root 必然承载被抓取的 CGWindowID。符号不可用时，same-bounds 请求显式失败而不是错配。

## 问题 2：closed-window ghost（CGWindow 残留条目）

**现象（修复前）。** `WindowDirectory.resolve` 以全量（`optionAll`）CGWindow 列表判活。窗口关闭后条目会在列表里残留：本机实测关闭动画期间约 350ms（alpha 1.0 → 0.003 → 清除），且 TextEdit 类 phantom shell 可长期残留（alpha 1.0）。`get_window` 因此会把已关闭窗口 echo 成 live ref。内容路径（`get_window_state`/`activate_window`/动作工具）此前靠各自失败兜底，但 `get_window` 会宣称 stale 窗口 live。

**修复。** 在 `resolve` 中新增 AX-identity 存活检查（解析序：stale-list → 进程退出 → deny 名单 → **AX identity**）：

- `WindowIdentity` 三态：
  - `.confirmed`：宿主 app 的 AX window 列表中存在映射到该 CGWindowID 的窗口；
  - `.absent`：AX window 列表**完整读出**且每个窗口都成功映射，无一命中——该 CG 条目没有 live AX identity；
  - `.indeterminate`：SPI 不可用、app 不暴露 AX window 列表、或任一候选映射失败——不下结论。
- `.absent` → 抛官方 stale 错误（与"不在列表"同串，因为窗口确实已关闭）：
  `staleWindowHandle(<id>): the window is no longer open; re-observe with list_windows.`
- `.indeterminate` **从不拒绝**。不用 alpha / layer / bounds 等任何列表级启发式（会误杀合法的低透明度、fade-in、off-screen helper 之外的真实窗口；本机实测 AppKit 会为每个 app 创建若干 off-screen、无 AX identity 的 helper CG 条目，它们本来就不应可被 target）。
- minimize 不受影响：实测 minimized 窗口仍在 AX window 列表中且 `_AXUIElementGetWindow` 照常映射（probe：minimize 后 AX-mapped=true）。

**保证（写入文档）。** `get_window` 只在以下全部成立时报告 CGWindowID live：(a) 在全量窗口列表中；(b) 宿主进程存活；(c) 不在 deny 名单；(d) AX-identity 检查为 `.confirmed` 或 `.indeterminate`。当 (d) 被确定性地证伪（`.absent`）时报 stale。检查不可用时（`.indeterminate`）保留旧行为并如实标注——不宣称无法验证的窗口 live，也不拒绝无法验证的窗口。

## 验证（2026-09-22，dev 机 macOS 27.0，全部实测通过）

- 单元测试（fixture 专用窗口，不触碰用户窗口；`swift test --filter WindowManagementTests` 53 例 / 0 失败）：
  - same-bounds：fixture 新命令 `open_window fixture-second-same-bounds` 在与主窗口完全相同 frame（可同 title）处开第二个窗口；断言 `matchAXWindow` 按 CGWindowID 返回 `AXIdentifier` 正确（`fixture-window` / `fixture-second-window`）的元素；SPI 不可用时断言 `.ambiguous`。
  - ghost：fixture 开第二个窗口 → 经 fixture 命令关闭 → 立即轮询 `get_window`：必须始终失败且为官方 stale 串、永不成功；记录拒绝发生时 CG 条目是否仍在残留（证据，不做 flaky 硬断言）。本次运行观测到 **rejected while CG entry lingering: true**。
  - minimize 守护：minimized 的 fixture 窗口必须仍可 resolve（通过 AX `kAXMinimizedAttribute` 最小化/还原）。
  - resolve 纯逻辑：identity seam 注入 `.absent`/`.confirmed`/`.indeterminate` 三态各自钉住。
- smoke：`make smoke` 全过；`OPEN_COMPUTER_USE_SMOKE_WINDOW2=1` 下 W1–W12 全过（W11 same-bounds 两窗口独立 resolve/activate，W12 关闭窗口 id 立即 stale）。
- real-MCP e2e（stdio JSON-RPC，28 项检查全过；窗口全部为 fixture 专用窗口，Finder/Safari/TextEdit 窗口集合前后逐 id 比对不变）：
  - same-bounds + 同 title 双窗口：`get_window` 双向 echo 各自 id、`get_window_state` 双向成功（无歧义错误、带截图）；
  - same-bounds + 异 title：`activate_window` 双向切换，以 fixture 状态文件 `keyWindowTitle` 作**独立** oracle 校验被 key 化的窗口正确；
  - ghost：关闭后立即轮询，实测 `t=0.158s` 时 **CG 条目仍在全量列表（前后两次 cghas 均为 yes）而 `get_window` 已返回精确 stale 串**——该拒绝只能来自 AX-identity 检查（旧列表检查会把残留条目 echo 成 live）；`activate_window`/`click` 同样精确 stale；
  - minimize 守护：经 fixture 命令 `minimize_window` 最小化后 `get_window` 仍成功。
  - 环境注记：本轮 e2e 从交互 shell 上下文运行（本机 shell 上下文进程对 debug 二进制持有有效 AX 归因，已用功能性 Finder snapshot preflight 验证）；launchd 上下文不可用——session 内重编后 launchd 归因的 TCC 授权与 debug 二进制的 cdhash 不再匹配，launchd 起 server 会静默失去 AX（所有 identity 检查退化为 indeterminate、窗口级激活失效且被 `isFrontmost` 的 app 级短路掩盖），已实测并弃用该上下文。
