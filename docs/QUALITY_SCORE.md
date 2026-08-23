# 质量评分

## 评分标准

- `A`：覆盖完整、行为稳定、文档清楚、运行风险低。
- `B`：整体可接受，但还有明确短板。
- `C`：能用，但需要针对性补强。
- `D`：脆弱、缺少规范，或很多行为尚未定义。

## 当前水位

| 区域 | 评分 | 原因 | 下一步 |
| --- | --- | --- | --- |
| 产品面 | B | 已经有 Swift 本地 `computer-use` MCP server、默认 app 模式权限引导，以及一轮按官方 surface / result 行为收敛过的 9 个 tools。 | 继续收敛复杂 AX 场景下的 state rendering 细节、权限 UI 和更清晰的用户错误提示。 |
| Windows runtime | B | 单 Go exe 零依赖（无 PowerShell/.NET、无 CGO）进程内实现全部 14 个 tools（UIA COM 专用线程 + Win32 消息 + WGC/print/gdi 截图链），`psRequest`/`psResponse` 协议与错误文案与退役 PS runtime 逐字节对齐（双跑留档），五 flag 门禁与安全 deny 前置；常驻 MCP/CLI 进程经 `op` 牺牲子进程执行 native 调用（子进程崩溃/坏输出重试、兜底回退进程内），截图再隔离为孙进程——浸泡实证该机 DWM/驱动对"刚移动窗口 × native 读"存在异步越界写，该架构使 auto/print/gdi 三模式 ×3 轮 ×10 次（含外部移动+混合动作）长会话浸泡全程无 panic 无 NUL。已验证：14-tool e2e（动作效果实证）、门禁矩阵（实时 env + 请求级 env_flags，deny 逐字节）、延迟 `get_window` ~10ms / `list_windows` ~400ms / 快照+截图（wgc）~540ms（print 兜底 ~1.8s，PS one-shot ~570ms / daemon 热 ~130ms 的对照留档）。 | wgc 强制模式浸泡 3×10 PASS（2026-08-23 复测；深夜 "no frame arrived" 经交叉实现验证定性为单一窗口实例事件而非系统拒绝，已留档）；`list_apps` 的 .lnk COM 解析 ~3.5s 偏慢；补 Windows fixture、installer/signing；官方 UIA 事件订阅式快照失效（SnapshotLease）待评估。 |
| Linux runtime | B | 单 Go binary 零运行时依赖（无 python3、无 CGO，仅 godbus/dbus + jezek/xgb 两个纯 Go module），进程内直连 AT-SPI2/D-Bus 暴露同样 9 个 tools、MCP server 和 `call --calls`；全部纯逻辑（错误文案逐字节、行为契约含 2026-08-23 修复的 mouse_button 短名映射与 send_key 修饰键泄漏）由 56 个单测钉住（任意宿主可跑），Ubuntu GNOME VM 与 WSLg 实测跑通 `list_apps`、snapshot 树渲染和 click/type_text/press_key/set_value/perform_secondary_action 等动作链路，并已接入 npm bundled artifact 分发。截图在 GNOME Wayland / rootless XWayland 下仍会静默省略，coordinate input 也不是通用后台模型。 | 补 Linux fixture、可重复 smoke runner、portal/compositor screenshot 路径。 |
| 架构文档 | B | 顶层结构、fixture bridge、app 模式和验证路径已经落文档。 | 后续补 release artifact、code signing / notarization 和 host 集成方式。 |
| 测试 | B | `swift test` + smoke suite 已覆盖 9 个 tools 的回归，并新增了针对“前台焦点是否被抢占”的手工对比样本沉淀。 | 增加更多普通 app 的录制回归，减少只依赖 fixture 和一次性手工检查。 |
| 可观测性 | C | 已有 `doctor`、`snapshot`、smoke 输出，以及一组仓库内留档的官方 `computer-use` / 本仓库实现对比样本。 | 补统一日志级别、失败上下文和 release artifact 里的诊断信息，把一次性样本收敛成可重复采集流程。 |
| 安全 | B | 已明确本地-only、权限边界和 fixture test bridge 的作用域，并将内置 denylist 收缩到密码管理器。 | 增加 session approval 和更清楚的敏感 app policy，避免策略长期硬编码在仓库里。 |
