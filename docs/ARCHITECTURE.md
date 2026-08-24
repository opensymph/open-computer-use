# 架构总览

这个仓库当前已经从模板收敛成一个本地 `computer-use` 项目。主线仍是 Swift 实现的 macOS automation MCP server，同时新增了实验性的 Windows 和 Linux runtime，用独立 Go 二进制暴露同一组 9 个 Computer Use tools。

## 当前目录结构

- `apps/OpenComputerUse`
  主入口，负责 `mcp`、`doctor`、`list-apps`、`snapshot`、`call`、`turn-ended` 等 CLI 命令，以及 `-h` / `--help` / `-v` / `--version` 这类全局参数；不带参数启动时会先检查权限，只有缺失时才进入无 Dock 图标的 app 模式权限引导窗口，`doctor` 也只会在检测到缺失权限时拉起这套 onboarding UI。
- `apps/OpenComputerUseFixture`
  本地 GUI fixture app，用来承载低风险、可预测的点击/输入/滚动/拖拽验证路径。
- `apps/OpenComputerUseSmokeSuite`
  端到端 smoke runner，会拉起 fixture 和 MCP server，并通过 JSON-RPC 真实调用 9 个 tools；同时也支持单独的 visual cursor idle smoke，用跨进程 observation file 断言等待下一次 move 时是 anchored tip + tiny rotate wobble，而不是横向漂移。
- `apps/OpenComputerUseWindows`
  实验性 Windows runtime。它不依赖 Swift 或 `.app` bundle，是单 Go exe 的进程内 UI Automation + Win32 实现（无 PowerShell/.NET、无 CGO），构建产物是 `open-computer-use.exe`，并随已有 npm 包的 `dist/windows/<arch>/` bundled artifacts 分发。
- `apps/OpenComputerUseLinux`
  实验性 Linux runtime。它不依赖 Swift 或 `.app` bundle，Go CLI/MCP 入口进程内直连 AT-SPI2/D-Bus（单 binary 零运行时依赖），构建产物是 `open-computer-use`，并随已有 npm 包的 `dist/linux/<arch>/` bundled artifacts 分发。
- `packages/OpenComputerUseKit`
  核心库，包含：
  - MCP stdio transport 与 tool registry
  - app discovery
  - Accessibility / 窗口 snapshot
  - 键鼠输入模拟
  - software cursor overlay
  - fixture test bridge
- `scripts/`
  仓库级自动化命令，包括 smoke test、`.app` 打包入口、Windows `.exe` / Linux binary 构建入口和 npm 分发脚本。
- `skills/`
  面向 agent runtime 的可安装 skill。当前 `skills/open-computer-use/SKILL.md` 只作为轻量入口和目录，安装、MCP/CLI 使用、排障等细节拆到相邻 `references/` 文件里按需加载；`scripts/package-skill.sh` 负责校验并打包 `.zip` / `.skill` 制品。
- `plugins/`
  面向 agent 宿主的插件形态。`plugins/open-computer-use/` 同时携带 Codex（`.codex-plugin/`）与 ZCode（`.zcode-plugin/`）manifest、两者共享的 `.mcp.json` 与启动脚本，以及一份自包含 skill 副本（ZCode 校验组件路径不得逃逸插件根）；ZCode 的 marketplace 清单在仓库根 `marketplace.json`。
- `docs/`
  架构、协作约定和项目约束文档。

## 运行分层

### 1. App Mode 层

- `OpenComputerUse` 默认 app 模式会拉起 `PermissionOnboardingApp`。
- app bundle 以 `LSUIElement` agent-style 形态运行，默认不在 Dock 暴露常驻图标，但仍可按需显示权限窗口。
- 当用户从终端执行 macOS 版 `open-computer-use mcp`、`doctor`、`call`、`snapshot` 或 `list-apps` 时，CLI 会先通过 LaunchServices 启动同一个 `.app` bundle 的隐藏 app agent，并通过用户临时目录下的 Unix domain socket 转发请求；真正调用 Accessibility、ScreenCaptureKit 和动作 tools 的进程始终是 `Open Computer Use.app`，不是 iTerm / Terminal / Node launcher。
- CLI 与 MCP proxy 会把调用进程中 `OPEN_COMPUTER_USE_*` 前缀的环境变量随请求转发给 app agent，并只在该请求执行期间临时覆盖 agent 环境；这让 `click_method=global` 的进程级安全门和 debug 开关在 app-agent 架构下仍按调用方配置生效。
- 主窗口负责渲染 `Accessibility` / `Screen & System Audio Recording` 两类权限卡片、`Allow` / `Done` 状态和 relaunch 后的状态收敛；当两项权限都已完成时会自动关闭，不再要求用户手动退出。
- 辅助 drag panel 会跳转到对应的 `System Settings` 页面；点击 `Allow` 后，panel 会从主窗口里的按钮位置做一段 spring + curved frame 的入场，再落到 `System Settings` 内容区下沿。panel 默认保持在窗口右侧内容区下方居中并固定贴近窗口底边，不再依赖实时扫描权限页内部 `+ / -` 控件行；窗口层级上会显式排在当前 `System Settings` 窗口之上，避免被权限列表内容盖住，同时尽量减少对系统设置自身滚动区域的干扰。panel 内也补了显式返回按钮，允许用户中断当前 guidance、回到 onboarding 主窗口重新选择权限步骤。
- 权限状态会合并 TCC 持久授权记录与当前 app 进程的 runtime preflight：TCC 中任一匹配 client 已授权即可视为 granted，避免 CLI 子进程与 GUI app 对授权状态看到不一致的结果；如果当前 `.app` 进程已经通过 `AXIsProcessTrusted()` / `CGPreflightScreenCaptureAccess()`，也会立即视为 granted，避免 stale 或不匹配的 TCC path 记录让 onboarding 浮层继续停留。正式 release 仍以 CI 打出来的 `Open Computer Use.app` 为准，而本地 debug/dev 打包现在显式命名为 `Open Computer Use (Dev).app`，并在 dev bundle 运行时优先认当前 dev 副本，避免系统设置里出现两个完全同名的条目。

### 2. MCP 层

- 面向 MCP host 的外部 transport 仍是 `stdio`；macOS 终端 CLI 到 `.app` app agent 之间额外有一层本地 Unix domain socket 代理，用来保证真实 automation 运行在 app bundle 权限身份下。
- 当 `OPEN_COMPUTER_USE_VISUAL_CURSOR` 未被显式关闭时，`mcp` 命令会切到一个最小 AppKit runtime：主线程保留 event loop 承载 overlay UI，stdio server 仍在后台线程串行读取与响应。
- 请求 framing 采用一行一个 JSON-RPC message。
- 当前支持的 method：
  - `initialize`
  - `notifications/initialized`
  - `notifications/turn-ended`
  - `ping`
  - `tools/list`
  - `tools/call`
- `notifications/turn-ended` 是开源版显式的 turn boundary hook；收到后会清理当前进程里的 visual cursor overlay。CLI `open-computer-use turn-ended [payload]` 也会通过 macOS distributed notification 通知正在运行的 AppKit MCP 进程执行同一类清理，用于接 Codex legacy notify 的 after-agent payload。

### 3. Tool Service 层

- `ComputerUseService` 负责把 Computer Use tool 请求映射到本地能力，`ComputerUseToolDispatcher` 则把 9 个 tool 的参数解析与 service 方法分发收敛成 MCP server 和 `open-computer-use call` 共用的一层。
- `list_apps` 通过 Spotlight metadata query 拉取标准 application 目录里的 app bundle，并读取 `kMDItemUseCount` / `kMDItemLastUsedDate_Ranking` 这类系统元数据；再与 `NSWorkspace` 的运行态 app 合并，输出“当前运行中 + 近 14 天用过”的视图。
- `get_app_state` 优先走真实 AX / 窗口截图；真实 app 必须同时有未最小化的 `AXWindow` 和可匹配的 on-screen `CGWindow`。如果目标 app 只是隐藏或暂时没有 on-screen window，会先 best-effort unhide / activate / `open -b` / `AXRaise` 并短暂重试，以贴近官方 `computer-use` 会把 Lark / Electron 窗口拉回再采集的行为；恢复后仍无法匹配时返回官方风格的 `Apple event error -10005: cgWindowNotFound`，不再把 application 根节点或无截图窗口伪装成可操作状态。当目标是仓库内 fixture app 时，回退到 fixture 导出的合成状态。真实 AX tree 默认在 macOS、Linux、Windows 上最多渲染 1200 个节点、64 层深度；显式 `get_app_state` / `snapshot` 可通过 `max_tree_nodes` / `max_tree_depth` 覆盖预算，action tools 的刷新结果仍使用默认预算。snapshot 文本默认截断到 500 字符；显式 `get_app_state` / `snapshot` 可通过 `text_limit` 正整数或 `"max"` 覆盖，action tools 的刷新结果仍使用 500 字符默认值。对 Electron/WebView 这类深层 UI 会压缩空 `AXGroup` / `AXUnknown` wrapper、过滤 `AXScrollToVisible` 噪音和空字符串属性，避免 action-critical 的输入框被无语义容器挤出节点预算；但通用节点中的 `AXPress` / `AXConfirm` / `AXOpen` 子节点会形成文本摘要边界，避免多个可点击选项被合并成一个 container。这类动作节点如果 frame 有效、尺寸紧凑，会保留为带窗口相对 `Frame` 的 `button`，并用短文本后代作为按钮摘要，让 icon-only 和文字 Web 控件都能获得可区分的 `element_index`，同时继续过滤零尺寸或覆盖大面积页面的通用点击容器。对原生 open panel / Finder column view 这类把内容放在 `AXContents` / `AXVisibleChildren` 里的控件，也会把可见文件项纳入元素树。
- MCP `tools/list` 的 description / input schema 当前按官方 `computer-use` 的 9 个 tools 文案和参数面收敛，尽量减少 host 侧提示词和 tool surface 偏差。
- `open-computer-use call <tool> --args '{...}'` 会直接输出 MCP-style JSON result；`open-computer-use call --calls '[...]'` / `--calls-file <path>` 会在同一进程里顺序执行 JSON 数组里的 tool calls，并复用同一个 `ComputerUseService` 内存态，因此 `get_app_state` 之后的 action tool 可以继续使用同一轮 snapshot 的 `element_index`。序列执行默认会在成功的相邻操作之间 sleep 1 秒，也可以用 `--sleep <seconds>` 覆盖；遇到 `isError=true` 的 tool result 后停止。
- 对真实 app 的 `get_app_state` / action tool 入口，当前只保留一层密码管理器 bundle denylist：bundle-id 直传时直接返回 safety denial；名称匹配时默认不解析到这些 app。终端、Chrome / Atlas 和系统组件不再属于内置阻止目标。
- 普通 app 的 element frame 当前按“窗口左上角为原点”的 window-relative 坐标输出，便于后续把 `element_index` 和截图坐标统一到同一套参考系。
- `click` / `set_value` 在执行真实动作前后，会额外驱动一层透明 `SoftwareCursorOverlay` window：两者的移动阶段现在共用一条 heading-driven 的官方风格 motion 内核，显式把“当前 cursor 朝向”和“最终 resting pose”一起喂给选路器，优先生成需要时先掉头、再沿车头方向推进的 C 形/单侧大弧轨迹；首次显示时按官方 binary 的 fresh state 从 AppKit 全局 `(0,0)` window origin 生成起点，后续动作继续复用上一帧 visible tip。真正显示出来的 cursor 不再直接等于 path sample，而是经过一层独立的 visual dynamics 状态，把 visible tip、velocity、angle 和 fog/offset 持续推进。`click` 结尾会衔接 click pulse 和更明显但仍然很小的 rotate wobble，`set_value` 则只做 settle / idle，不给 pulse；两者收尾后会在目标点继续保持 idle 状态，等待下一次动作时 tip 保持 anchored、只保留可感知的小角度摆动；只有连续 30 秒没有新动作时才做 cleanup，这样连续 tool call 不会反复从 fresh `(0,0)` 起步；如果宿主在任务 / turn 结束时发出 `turn-ended`，cursor 会立即消失并清掉本轮位置状态。
- overlay 的 visual style 不再自己从官方 app bundle 裁 `SoftwareCursor` 小图；主 MCP runtime 现在优先渲染仓库里沉淀的 `assets/cursor/official-software-cursor-window-252.png` baseline，只有资源缺失时才退回 `OpenComputerUseKit` 内部的程序化 pointer/fog fallback。命中点 anchor 仍固定在 `126x126` 画布里的同一组 tip-offset 上；glyph 自身的 neutral heading 继续沿用官方 baseline 的 `-3π/4`。主 runtime overlay window 按 AppKit 全局坐标移动，因此在把 AX / `CGWindowList` 产出的 y-down screen-space 点击目标喂给 overlay 之前，会先转换成对应屏幕的 AppKit 全局坐标；路径选路用屏幕上实际可见的 AppKit forward heading，进入 visual dynamics / render state 前则把 velocity 的 y 轴翻回 CursorMotion 的 y-down screen state，再交给 AppKit 绘制层做角度和 `dy` 翻转。程序化 fallback 保留 neutral artwork correction，把它的天然轮廓轴对齐到官方 baseline 的 `-3π/4` forward 方向。
- overlay 的层级不再固定 `.floating`；现在会跟随 snapshot 命中的目标 window id / layer，把自己排到该目标 window 之上，而不是粗暴压到所有前台 app 最上层。
- overlay 的曲线路径不再只按固定 Bezier 模板生成；当前主线采用 reverse-engineering 约束下的 heading-driven candidate 族，候选只保留 `direct` / `turn` / `brake` / `orbit` 这些能稳定产出单侧主弧的 family，并继续保留 target-window 命中策略作为同类候选间的 tie-break。候选生成不再依赖历史逆向产物，直接以行为约束为准。
- overlay 的 progress 曲线也不再是固定 `easeInOut`；主线现在复用官方 `response=1.4`、`dampingFraction=0.9`、`dt=1/240` 的 spring/`VelocityVerlet` 形状，默认 move 时长对齐已恢复出的 close-enough endpoint-lock 时间 `343 / 240 = 1.4291667s`，不再按路径距离额外压缩。
- overlay 不再依赖临时 `terminal settle` 补丁来修尾；主线现在统一改成“路径层给目标点，visual dynamics 层给可见姿态”的双层模型，所以 move 末段、pulse 和 idle 共用同一套状态，不会再出现 endpoint 锁住后只剩原地翻角的收尾。
- overlay 的渲染输入也从单一 `rotation` 扩展成 `rotation + cursorBodyOffset + fogOffset + fogScale`，让速度滞后能真正体现在画面上，而不是只存在于主循环内部状态；其中 `rotation` 现在按二进制里 `SoftwareCursorStyle.angle + CursorView._animatedAngleOffsetDegrees` 的分层去近似，不再把“跟随运动方向的主朝向”和“小幅 wiggle offset”压成同一个受限小角度。
- 动作型 tools 对普通 app 采用“非侵入优先，物理指针路径显式 opt-in”策略：
  - `perform_secondary_action` 只执行目标元素已经暴露出来的 AX action；无效 action 返回官方风格的 `... is not a valid secondary action for ...`，fixture 的 `Raise` 路径也不再为了测试去准备全局物理指针输入
  - `set_value` 会先用 `AXUIElementIsAttributeSettable(kAXValueAttribute)` 判断目标是否真的是可设置值元素，只有 settable 时才调用 `AXUIElementSetAttributeValue`；不可设置时返回官方风格的 non-settable 错误，不退到键盘输入、剪贴板或未公开的文本替换接口
  - `click.click_method` 是开源版的可选扩展，支持 `auto`（默认）、`accessibility`、`app_post`、`sky_click` 和 `global`。未传参数时继续使用原有自动路由；显式模式不会静默 fallback 到其他实现。`accessibility` 只接受 `element_index`；其余 mouse 路径可以使用 `element_index` 的计算落点或原始 `x/y` 坐标。
  - element-targeted `click` 的 `auto` 左键路径会先试原生列表的 `AXSelectedChildren` 选择，再试 `AXPress` / `AXConfirm` / `AXOpen` 这类真正语义化的激活动作；如果目标本身不可点，还会继续尝试其子孙 AX 元素（例如 Finder sidebar row 下面暴露 `AXOpen` 的 cell）和命中点附近的 AX hit-test 结果，最后进入现有 non-AX 分支：未开启全局指针环境变量时使用 `postToPid` 定向鼠标事件，开启时直接使用全局 HID 事件。`AXRaise` / `kAXMainAttribute` / `kAXFocusedAttribute` 这类 activation-only fallback 只允许窗口级元素使用，避免普通静态文本或容器把“获得焦点”误报成“点击已处理”；`click_count > 1` 也会优先重复可用的 AX action。
  - 显式 `click_method=accessibility` 只执行上述 AX 语义动作；显式 `click_method=app_post` 绕过 AX 候选和后代扫描，直接通过 `CGEvent.postToPid` 向目标进程发送 `mouseMoved` / down / up；显式 `click_method=sky_click` 只在 macOS 接受左键单击/双击，它验证当前 snapshot 的 `CGWindowID` 仍为同一 PID 所有且 on-screen，仅通过 `SLPSPostEventRecordTo` 让目标应用短暂进入 synthetic-active 状态，绝不向真实前台应用发送 defocus record，再按 Chromium-compatible recipe 发送 target move、`(-1,-1)` primer 和真实 down/up。每个鼠标事件同时经动态解析的 `SLEventPostToPid` 与公开 `postToPid` 投递，并带 window-local location、PID/window 和 click-group 字段；renderer settle 后只撤销目标的 synthetic-active 状态。`sky_click` 的 action-result snapshot 刷新也禁止 activate / `AXRaise` 恢复，目标状态瞬时不可读时直接失败。它不改变 `auto`，任一步失败也不 fallback。显式 `click_method=global` 同样绕过 AX，但通过 `.cghidEventTap` 发送系统级指针事件。`global` 还必须设置 `OPEN_COMPUTER_USE_ALLOW_GLOBAL_POINTER_FALLBACKS=1`，否则在移动 visual cursor 或发送输入前拒绝请求。
  - 对 renderer 合成的 summary `text` 行，`click` 不再默认点击父容器中心；这类元素使用左侧安全锚点。对普通 row/container/text 的后代候选，也会过滤右侧紧凑 hover action（例如 Lark / Electron 会话列表里的“完成”勾），避免把主行点击误操作成 side action；Electron/Lark 这类 app 的 WebArea 合成文本会优先寻找紧邻的行级 `AXPress` 祖先并静默执行，避免用物理鼠标点行。浏览器 WebArea（例如 Chrome/GitHub）不走这条 Electron-scoped 行级祖先点击优化，仍保留通用 link/container 点击路径。命中点如果只反查到覆盖整页的 Electron/WebArea 级 AX 元素，不再继续扫描整个大容器的子孙候选，避免一次行点击被远处的可点击元素截走。
  - `AXUIElementCopyElementAtPosition` 做坐标命中，尽量把 coordinate click 反解成可操作 AX 元素
  - `CGEvent.postToPid` 定向发送键盘事件，避免为了 `type_text` / `press_key` 抢前台；`type_text` 会把文本按 Unicode extended grapheme cluster 聚合成小批量 `keyboardSetUnicodeString` 事件，避免中文标点、emoji / 代理对和组合字符被逐个 UTF-16 code unit 拆开后在 Electron 富文本输入框里乱序或变形。如果当前 focused element 的 `AXValue` 可设置，`type_text` 会优先按可编辑内容追加并写回 `AXValue`，这覆盖 Feishu / Electron 富文本输入框不可靠接收后台键盘事件的场景，并会过滤已知占位提示，避免把 placeholder 拼进草稿；如果当前 focused element 不是可编辑文本目标，`type_text` 会报错要求先 click 文本输入区或使用 `set_value`，不再把无效果的后台键盘投递当成成功；`press_key` 的 xdotool parser 覆盖官方 binary key table 里常见的 `BackSpace`、`Page_Up`、`Prior` / `Next`、`F1...F12` 和 `KP_*` alias
  - `scroll.pages` 对齐官方 `1.0.755` 的 `number` schema，支持小数页数；整数页且目标暴露 `AXScroll*ByPage` 时优先走 AX action，否则用 `CGEvent.postToPid` 向目标进程定向发送 scroll event
  - `drag` 仍是 coordinate-only API，但默认不再使用全局 `.cghidEventTap` mouse event；默认改为 `CGEvent.postToPid` 定向发送 mouse move / down / dragged / up 事件，避免移动用户真实硬件光标；这些 coordinate tool 的 `x/y` 先按 screenshot pixel 坐标解释，再依据截图像素尺寸与目标 window bounds 的比例映射回 window point / Quartz global 坐标，避免 Retina 窗口上把 2x 像素误当成 1x point 导致点击落到错误位置
  - `click` / `scroll` / `drag` 在环境变量未开启的默认配置下不会走全局 `.cghidEventTap`，因此不会移动或抢占用户真实鼠标；`OPEN_COMPUTER_USE_ALLOW_GLOBAL_POINTER_FALLBACKS=1` 是全局指针能力的进程级安全门，显式 `click_method=global` 仍必须通过这层授权。默认路径不再为了 fallback 调用 `NSRunningApplication.activate`

### 4. Fixture Bridge

- `OpenComputerUseFixture` 会把自己的窗口与元素状态写到临时 JSON 文件。
- 对 fixture 的 `get_app_state` 和少量测试专用动作，会通过 `FixtureBridge` 走显式 command 通道。
- 这个 bridge 只服务于仓库内 deterministic smoke path，不是面向真实第三方 app 的能力边界。
- 因为 SwiftPM 裸 executable 形式启动的 fixture 没有稳定的 bundle identifier，`list_apps` 会仅对 `OpenComputerUseFixture` 注入一个内部 synthetic identifier，保证 smoke suite 仍能覆盖 `list_apps`，普通第三方 app 仍按真实 bundle id 输出。

### 5. Windows Runtime

- Windows runtime 位于 `apps/OpenComputerUseWindows`，以 Go 维护 CLI、`call --calls` sequence、MCP JSON-RPC、tool schema 和进程内 snapshot cache。
- 构建入口是 `scripts/build-open-computer-use-windows.sh --arch arm64|amd64`，默认输出到 `dist/windows/<arch>/open-computer-use.exe`；npm release package 会把两个 Windows artifact 内置到已有 root/alias packages，Node launcher 按 `process.platform/process.arch` 自动选择。
- Windows runtime 是单 Go exe、零外部依赖:14 个 tool 全部为 Go 进程内实现,无 PowerShell/.NET、无 CGO(依赖仅 `golang.org/x/sys/windows` 与 `go-ole`),`runtime.ps1`、C# helper 与 `go:embed` 已删除。分层:`native_win32.go`(窗口枚举/SendInput/PostMessage/GDI/激活/.lnk/EM 文本链)、`native_capture.go`(纯 Go WGC/print/gdi 截图链)、`native_uia.go`(UIA COM 绑定 + 专用 COM 线程 + 树渲染)、`native_actions.go`(元素指纹/FindElement/七动作 dispatch)、`native_backend.go`(tool dispatch)、`main.go`(CLI/MCP/service 层与门禁)。`OPEN_COMPUTER_USE_WINDOWS_BACKEND` 已废弃:设置时只打一行 deprecation warning 后忽略(有单测钉住)。
- **进程模型:牺牲子进程隔离(op worker)。** 浸泡实证该机的 DWM/驱动栈存在异步越界写:窗口刚移动的不稳定期内,任何 native 读窗口操作(UIA 树遍历与 WGC/PrintWindow 截图同样)都可能踩坏调用方进程的 Go 堆,Marshal 开始报 NUL、reflect panic,且一旦发生进程即废;单独隔离截图链不足以保护主进程。因此常驻 MCP/CLI 进程只做 JSON 编解码与 service 层门禁,每次 tool operation 序列化为 `psRequest` JSON 后交给同 exe 的短命子进程(`opencomputerusewindows.exe op`)执行,stdout 回一行 `psResponse` JSON;子进程崩溃/输出含 NUL/超时(60s)则重试(最多 3 次、间隔 600ms,等待窗口移动稳定),仍失败回退进程内执行(与旧行为一致,零新增错误文案)。截图链进一步隔离为孙进程(隐藏 `capture` 子命令,20s 超时);`go test` 二进制与 exe 不可得时自动回退进程内。域错误始终走 `psResponse.Error` 逐字节透传,协议面(`psRequest`/`psResponse` 字段名与顺序)不变。
- 实测延迟(MCP 单调用,3 轮取中位):`get_window` ~10ms、`list_windows` ~400ms、`get_window_state`(纯树)~390ms、`get_window_state`(树+截图,wgc 健康时)~540ms(print 兜底 ~1.8s)、`type_text`(动作+刷新快照)~2.0s、`list_apps` ~3.5s(Start Menu 149 个 .lnk COM 解析为主)。对比旧 PS:one-shot ~570ms、daemon 热路径 ~130ms/全树 ~790ms;op 子进程的固定开销是每次冷初始化 UIA COM(~300-400ms),换来的是常驻进程堆安全。WGC 链 2026-08-23 复测健康:强制 wgc 首帧 ~180ms,wgc 浸泡 3×10 PASS。深夜观察到的 "no frame arrived" 经交叉验证(git 老版 PS OCUCapture 同机可出图、同 exe 新窗口正常、60 次移动压力不复现、机器未重启)定性为单一窗口实例事件而非系统拒绝;同日实证并移除 frame pool 上越界的 cursor vtable 调用(IGraphicsCaptureSession2 在 build 26200 不暴露,退役 PS 实现亦从未调用)。
- Windows runtime 默认只连接已经运行的 app，不会在 `get_app_state` 找不到进程时自动 `Start-Process`，也不会默认允许 `SetFocus` secondary action；这两条前台抢占路径分别需要 `OPEN_COMPUTER_USE_WINDOWS_ALLOW_APP_LAUNCH=1` 和 `OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOCUS_ACTIONS=1` 显式打开。`type_text` 默认优先对可写文本控件的 child HWND 发送 `EM_SETSEL` / `EM_REPLACESEL`，不再默认走可能触发前台激活的 UIA `ValuePattern.SetValue` fallback；需要旧行为时必须设置 `OPEN_COMPUTER_USE_WINDOWS_ALLOW_UIA_TEXT_FALLBACK=1`。UIA pattern 和 Win32 message fallback 本身仍是 best-effort：很多控件可以在后台响应，但 Windows 没有一套对所有 GUI toolkit 都等价于 macOS AX 的后台键鼠输入模型。
- 这 9 个 tool 的协议面与 macOS 主线保持一致：`list_apps`、`get_app_state`、`click`、`perform_secondary_action`、`scroll`、`drag`、`type_text`、`press_key`、`set_value`。其中 element-targeted action 会优先复用上一轮 `get_app_state` 的 runtime id / automation metadata，coordinate action 使用 screenshot/window-relative 坐标。
- 2026-08 起协议面对齐官方 Codex `window2` API（以官方 `computer-use` 插件 `docs/api.md` 为准），在 9 个 legacy tool 之外新增 5 个官方同名 tool：`list_windows`、`get_window`、`launch_app`、`get_window_state`、`activate_window`（共 14 个）。`Window = {app, id, title}` 的 `id` 在 Windows 上是不透明 HWND 整数（macOS 未来为 CGWindowID、Linux 为 X window id），动作调用时通过可选 `window` 参数（或 `window_id`）定位，未传时回退 legacy `app` key-window 语义。
- window2 语义要点：`get_window_state` 返回结构化 `WindowState`（`accessibility.tree` + `include_text` 开关的 `focused_element`/`selected_text`、`screenshots[]` 携带 `id/originX/originY/zIndex` 的 data URL），coordinate action 可传 `screenshotId` 绑定观察时刻，stale id 会在执行前被拒绝（"stale screenshot id; re-observe..."），任何成功动作都会使该窗口已缓存的 screenshot id 失效。v1 每次 state 只有一张全窗口截图；多张有界截图（transient UI / 模态）尚未实现。2026-08-22 起坐标输入（click x/y、coordinate scroll、drag）对齐官方观察门禁（service 层，`bounds_gate.go`）：窗口定向坐标动作前必须有过观察（`call get_window_state before issuing coordinate input`）、观察必须带截图（`... with include_screenshot before ...`）、窗口 bounds 自观察后变化即拒绝（`window bounds changed; call get_window_state before continuing`）、坐标必须落在截图像素范围内（`(x, y) is outside screenshot bounds`）；element_index 定向与 legacy `app` 键控流程不受门禁影响。同批对齐：`mouse_button=right` + `click_count>=2` 显式拒绝（"right double click is not supported"）、`type_text` 上限 8192 UTF-16 code unit（"text is too large for SendInput ..."）、deny 名单新增三家 AV 套件显示名（avg internet security / avast premium security / bitdefender security center，大小写不敏感包含匹配）。窗口激活序列对齐官方：`AttachThreadInput(attach)` → `AllowSetForegroundWindow(pid)` → `SetForegroundWindow` → detach（Go `setForegroundWindow`，语义承接退役 PS 实现的 `SetForegroundWindowReliable`），解决前台锁下激活静默失败导致 SendInput 打进错误窗口的可靠性问题。
- `scroll` 支持官方坐标增量模式：`x/y`（窗口相对）+ `scrollX/scrollY` 像素增量（负=左/上），通过 `WM_MOUSEWHEEL`/`WM_MOUSEHWHEEL` 按近似 40px/notch 换算投递；legacy `element_index + direction + pages` UIA Scroll 模式不变。
- `list_windows` 枚举 UIA root 下的 Window control type 子元素，可覆盖同进程的二级窗口与模态框；`list_apps` 响应在 legacy 文本行之外追加结构化 apps 数组（运行中进程带 `windows[]`，另有 Start Menu `.lnk` 枚举的已安装 app）。
- 官方安全红线已实施：`press_key` 硬拒绝 Windows/Meta 键（`super/win/cmd/meta/command/os/windows`，包括 chord）；app 解析与 `launch_app`（受 `OPEN_COMPUTER_USE_WINDOWS_ALLOW_APP_LAUNCH=1` 门禁）实施 deny 名单（终端宿主、密码管理器、Windows 安全类 app），不受任何环境变量豁免。`activate_window` 受 `OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOCUS_ACTIONS=1` 门禁。
- macOS / Linux 侧接口定义（`ToolDefinitions.swift` / Linux `toolDefinitions()`）已同步为同样的 14-tool 面；5 个 window2 tool 与动作工具的 window/screenshotId/坐标滚动参数在这两个平台返回明确的 "not supported yet" 错误。
- Windows `click_method=accessibility` 映射到 UI Automation pattern，`app_post` 映射到 HWND `PostMessage`；macOS-only 的 `sky_click` 在 snapshot lookup 前明确返回 unsupported。2026-08 起 `global` 可用：激活目标窗口后通过 `SendInput` 注入真实指针/键盘事件（Go `realKeyChord`/`realTypeText`：chord 用 vk+scancode、文本用 `KEYEVENTF_UNICODE`），整条路径由 `OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOREGROUND_INPUT=1` 门禁（service 层与 runtime 层双侧校验），未开启时返回带开启方法的明确错误而不是静默失败。`auto` 链升级为 UIA → PostMessage → 250ms 轻量验证（Toggle/ExpandCollapse/SelectionItem 状态或焦点元素变化）：无变化且 flag 开启时激活窗口并 SendInput 重放；flag 未开启则止步 PostMessage（与旧行为一致，不抢前台）。`press_key`/`type_text`/`scroll`/`drag` 新增可选 `input_method`（`auto|global`），语义与 click 相同。
- 2026-08-23 ABI/可靠性修复（评审驱动）：`INPUT` 联合体键盘打包修正（x64 下 `ki.wVk/wScan` 叠在 `mi.dx`、`ki.dwFlags` 叠在 `mi.dy`，旧写法把 scan 写进 flags 且真 flags 落进 padding 丢失，`input_method=global` 的 `press_key`/`type_text` 静默失效）；`SendInput` 返回注入条数并校验（不足即报 `SendInput injected N of M events`，不再静默丢弃）；`oleVariant` 补齐到 x64 VARIANT 的 24 字节（原 16 字节会被 COM 出参越界写 8 字节）；UIA `GetCurrentPattern` 空指针分支补 nil 检查 + UIA 线程 job 加 recover（panic 降级为 error）；绝对坐标归一化改用虚拟屏幕（`SM_X/Y/CX/CYVIRTUALSCREEN` + `MOUSEEVENTF_VIRTUALDESK`，多屏/负坐标原点正确，原实现按主屏归一化导致副屏坐标错位）；MCP stdio 改逐行读帧（坏帧回一条 -32700 后继续服务，流式 `json.Decoder` 坏帧后永不消费输入、100% CPU 空转）；快照缓存只存去截图副本并限 16 条；`click_count`/`pages` 服务层 clamp（≤100/≤1000）。以上均有单测钉住（`native_abi_test.go`）。
- 截图链：`Windows.Graphics.Capture`（native_capture.go 纯 Go 实现，D3D11 staging texture 读取，per-HWND FramePool/session 缓存 + 尺寸变化重建 + 失效窗口清理，1903+ 起 `put_IsCursorCaptureEnabled(false)` 使截图不含系统光标）→ `PrintWindow(PW_RENDERFULLCONTENT)` → GDI `CopyFromScreen`；生产路径在牺牲子进程中执行（隐藏 `capture` 子命令）。WGC 可截取被遮挡窗口的本体内容（与官方行为对齐；GDI 路径截到的是遮挡物）。`OPEN_COMPUTER_USE_WINDOWS_CAPTURE=wgc|print|gdi|auto`（默认 auto）可强制单一模式，强制模式失败直接报错；auto 链逐级降级，全黑采样帧视为失败（参考 Linux runtime 的省略 image 行为）。
- 官方文档实档对齐（2026-08-23，参照本机 Windows Store 版 Codex app 内置插件的 docs/api.md 与 docs/guidance.md）：`get_window_state` 的 `accessibility.document_text`（最相关 Document 元素的值，回退 Edit，走 text_limit 管线）与 `accessibility.selected_elements`（当前处于选中态的 SelectionItem 元素的树格式行，实机验证能抓到 Notepad 选中的 tab 项）已实现；方法面 13/13 全命中（我们另多 legacy get_app_state）。`list_apps` 的 `lastUsedDate`/`useCount` 为官方可选字段（"when available"），Windows 无对应数据源，选择省略。尚未对齐（留档）：输入方法自动激活目标窗口（官方为前台 SendInput 模型，我们默认后台不抢焦点）、多张有界截图（transient UI/模态分层）、SnapshotLease 事件式失效、官方 guidance 硬拒清单中的 Run 对话框/认证对话框/设置页 deny。
- Windows UI Automation 需要运行在已登录用户的桌面 session 里。通过 SSH 作为脱离桌面的后台进程运行时，`open-computer-use.exe` 可以启动并返回 JSON，但系统可能不给它暴露顶层窗口；这种情况下 `list_apps` 会是空，`get_app_state` 可能返回 `appNotFound(...)`。
- 当前 Windows 侧仍是功能性第一版：没有 visual cursor overlay、没有 installer/onboarding、没有 code signing，也没有独立的 Windows smoke fixture。

### 6. Linux Runtime

- Linux runtime 位于 `apps/OpenComputerUseLinux`，以 Go 维护 CLI、`call --calls` sequence、MCP JSON-RPC、tool schema 和进程内 snapshot cache。
- 构建入口是 `scripts/build-open-computer-use-linux.sh --arch arm64|amd64`，默认输出到 `dist/linux/<arch>/open-computer-use`；npm release package 会把两个 Linux artifact 内置到已有 root/alias packages，Node launcher 按 `process.platform/process.arch` 自动选择。
- 2026-08-23 起 runtime 全量 Go 原生化：删除嵌入式 `runtime.py` 与 `python3` 子进程，单 binary 零运行时依赖（仅 `godbus/dbus/v5` + `jezek/xgb` 两个纯 Go module，无 CGO）。分层与 Windows 侧一致：`atspi_node.go` 承载与退役 Python bridge 逐字节对齐的全部纯逻辑（错误文案、行为 quirk 由 `atspi_node_test.go` 钉住），`//go:build linux` 的 `native_atspi.go` / `native_input.go` / `native_capture.go` / `native_backend.go` 承载真实 I/O。AT-SPI2 走 D-Bus 两跳连接（session bus → `org.a11y.Bus.GetAddress` → a11y bus），树结构用 per-app `Cache.GetItems` 快照、非缓存数据（extents/text/value/action）走实时调用，键鼠合成走 registry 的 DeviceEventController，截图用 X11 `GetImage` ZPixmap 解码；文本能力通过对象的 AT-SPI interface list 检测 `Text` / `EditableText`。
- Linux 上最接近 macOS AX 的是 AT-SPI2/D-Bus accessibility，而不是一套统一的后台键鼠输入模型。第一版优先使用元素暴露的 AT-SPI action、EditableText 和 Value 接口；coordinate `click` / `drag` 与 `press_key` 使用 AT-SPI event synthesis fallback，在 Wayland 下只能按 best-effort 处理。
- Linux runtime 需要运行在已登录桌面用户 session 里。缺少 `XDG_RUNTIME_DIR`、`DBUS_SESSION_BUS_ADDRESS` 或 display 环境时，Go runtime 会在连接 AT-SPI2 总线前尝试从 `/run/user/<uid>` 和常见桌面进程自动发现当前用户的 session bus、display / Wayland 值；纯 SSH tty 如果找不到已登录桌面 session，可以启动二进制，但不能直接 inspect 或操作 GUI session。
- `get_app_state` 的 accessibility tree 在 GTK/GNOME app 上可能很深，Linux bridge 使用与 macOS / Windows 一致的 1200 节点、64 层默认 tree budget，并支持显式提高 `max_tree_nodes` / `max_tree_depth`。截图是 X11 root window 的 best-effort capture；GNOME Wayland / rootless XWayland 下读取会失败或返回黑图，bridge 会静默省略 image block（黑图经 16×16 网格采样判定）。
- 这 9 个 tool 的协议面与 macOS / Windows 保持一致：`list_apps`、`get_app_state`、`click`、`perform_secondary_action`、`scroll`、`drag`、`type_text`、`press_key`、`set_value`。其中 element-targeted action 会优先复用上一轮 `get_app_state` 的 runtime path metadata，coordinate action 使用 screenshot/window-relative 坐标。
- Linux `click_method=accessibility` 映射到 AT-SPI action，`global` 映射到 AT-SPI mouse synthesis 并要求全局指针环境变量；AT-SPI 没有等价的进程定向 mouse dispatch，因此 `app_post` 和 macOS-only 的 `sky_click` 会在 snapshot lookup 前明确返回 unsupported。`auto` 仍保持 AT-SPI action 优先、mouse synthesis fallback 的现有行为。
- 除了 9 个 AT-SPI2 工具外，Linux runtime 还提供一组 **display 级 X11 命令**，对齐 Cloud Agent / VNC 宿主那套「xdotool + ffmpeg + x11grab」栈，用来补齐 AT-SPI 覆盖不到的整屏操控与录制能力（AT-SPI 工具仍保持按 app、非侵入）：
  - `open-computer-use screenshot [--display :N] [--output x.png]`：纯 Go（xgb）读取整个 X11 root window（含所有显示器）为 PNG，复用与按窗口截图相同的 ZPixmap 解码；不带 `--output` 时向 stdout 打印 base64 PNG。
  - `open-computer-use cursor-position [--display :N]`：纯 Go（xgb `QueryPointer`）返回指针坐标与屏幕尺寸的 JSON。
  - `open-computer-use input <move|click|drag|scroll|type|key|wait> [--display :N]`：通过 `xdotool` 做**全局**合成输入（会移动真实指针 / 键盘、可能改变前台焦点）。除 `wait` 外都需要 `OPEN_COMPUTER_USE_ALLOW_GLOBAL_POINTER_FALLBACKS=1`，与既有 `click_method=global` 共用同一道进程级安全门；命令构造是纯函数、单测覆盖，`xdotool` 缺失时明确报错而不是静默成功。
  - `open-computer-use record <start|stop|status> [--display :N] [--output x.mp4] [--fps N] [--pidfile p]`：通过 `ffmpeg -f x11grab`（libx264 / yuv420p）录制整屏为 mp4。`start` detach 后台 ffmpeg 并把 `{pid,output,display}` 写入 pidfile，`stop` 发 SIGINT 让 ffmpeg 收尾出可播放的 mp4，`status` 读 pidfile 报运行态；`ffmpeg` 缺失时明确报错。
  - `display` 解析顺序为 `--display` > `$DISPLAY` > `:0`（VNC/AnyOS 桌面通常是 `:1`）。这几条命令只在 Linux 生效：纯逻辑放在 `desktop.go`，X11/xdotool/ffmpeg 原生实现放在 `//go:build linux` 的 `desktop_linux.go`，`desktop_other.go` 提供非 Linux stub，保证包在任意宿主可编译、纯逻辑单测可跑。它们是 CLI-only，不进入官方对齐的 14 个 MCP tool 面。
- 当前 Linux 侧仍是功能性第一版：没有 visual cursor overlay、没有 installer/desktop entry，也没有独立 Linux fixture。

- 2026-08-23 修复（评审驱动）：`send_key` 主键解析提前到按下修饰键之前、出错路径倒序释放已按修饰键（`ctrl+bogus` 从「Ctrl 卡死」变为零事件报错）；MCP stdio 改逐行读帧（坏帧 -32700 后继续，`json.Decoder` 流式坏帧 100% CPU 空转）；`mouse_button` 短名 `r`/`m` 正确映射右/中键、非法值显式报错（对齐 schema enum，原实现静默按左键）；快照缓存去截图副本并限 8 条；`click_count`/`pages` clamp（≤100/≤1000）。uid 归属检查拆分到 `uid_linux.go`（非 Linux 宿主编译 stub），纯逻辑测试可任意宿主运行。

## 关键边界

- 开源版当前不复刻官方闭源实现里的 caller signing、私有 IPC、完整 overlay choreography 和 plugin 自安装逻辑。
- 官方 `SkyComputerUseClient` 带有宿主侧 launch constraints 和 service-side sender authorization，外部 raw stdio MCP client 无法稳定直连官方 bundled `computer-use`；需要真实使用官方工具时应走正常 Codex agent/tool 调用链，开源版则继续提供可直连的 `open-computer-use` MCP server。
- 当前权限引导已经具备可运行 app、深链、拖拽辅助，以及一版更接近官方的 accessory panel 入场动画和返回 affordance；点击链路也已经补上独立 visual cursor、官方 asset fallback 和相对目标 window 的排序逻辑，并且在 overlay 可见期间会持续重申“排在目标 window 之上”，避免用户手动激活目标 app 后 cursor 被目标窗口重新盖住；但整体还没有完全复刻官方那套嵌入式 choreography / host 集成 / session approval 体验。
- screenshot 当前通过 `ScreenCaptureKit` 捕获目标窗口，并以 MCP `image` content block 的 base64 PNG 返回，不再把普通 app 截图落盘到仓库或临时目录；编码前会按最大尺寸和目标字节数自适应缩小，避免复杂页面的大 PNG 触发 host 侧 MCP result 降级，同时 coordinate tools 继续按实际返回的 screenshot pixel 尺寸映射坐标；单次 ScreenCaptureKit capture 会设置超时，超时后省略 image block 而不是卡住整个 `get_app_state`。
- 会话状态现在是进程内内存态，保存每个 app 最近一次 snapshot 和 element index 映射。

## 主要验证路径

- 单元测试：`swift test`
- 端到端 smoke：`./scripts/run-tool-smoke-tests.sh`（标准 9-tool smoke + visual cursor idle smoke；脚本默认以 headless 模式启动内部 fixture，避免在用户桌面弹出测试窗口）
- app 打包：`./scripts/build-open-computer-use-app.sh debug`
- 权限 onboarding 端到端回归：`./scripts/run-permission-onboarding-e2e.sh`（需要当前 macOS 对被测 `open-computer-use` 已授予 Accessibility 与 Screen Recording；默认禁用 app-agent proxy 来测试当前 CLI 运行态，可用 `OPEN_COMPUTER_USE_E2E_CLI=/path/to/open-computer-use` 指定被测 CLI，或用 `OPEN_COMPUTER_USE_E2E_DISABLE_APP_AGENT_PROXY=0` 显式覆盖默认代理行为）
- npm staging：`node ./scripts/npm/build-packages.mjs`
- release tgz：`./scripts/release-package.sh`
- skill 打包：`npm run package:skill`
- Windows runtime 单测：`(cd apps/OpenComputerUseWindows && go test ./...)`
- Windows exe 构建：`./scripts/build-open-computer-use-windows.sh --arch arm64`
- Linux runtime 单测：`(cd apps/OpenComputerUseLinux && go test ./...)`
- Linux binary 构建：`./scripts/build-open-computer-use-linux.sh --arch arm64`
- 手工诊断：
  - `open-computer-use doctor`
  - `open-computer-use snapshot <app>`
  - `open-computer-use call list_apps`
  - `open-computer-use call --calls '[{"tool":"get_app_state","args":{"app":"TextEdit"}}]'`
- Linux display 级命令手工验证（需要一个 X11 桌面，VNC/AnyOS 通常是 `:1`）：
  - `open-computer-use screenshot --display :1 --output /tmp/shot.png`
  - `open-computer-use cursor-position --display :1`
  - `OPEN_COMPUTER_USE_ALLOW_GLOBAL_POINTER_FALLBACKS=1 open-computer-use input move 960 600 --display :1`（再用 `cursor-position` 验证指针已移动）
  - `open-computer-use record start --display :1 --output /tmp/rec.mp4 && sleep 2 && open-computer-use record stop`（用 `ffprobe /tmp/rec.mp4` 验证产物）
