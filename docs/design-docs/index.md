# 设计文档索引

用这个目录集中管理架构设计和产品设计文档。

建议约定：

- 一个主题一份文档。
- 每份文档写清当前状态和简短摘要。
- 关联引入它的 execution plan 或 spec。

## 初始文档

- `core-beliefs.md`
- `macos-window-identity.md`：macOS window2 的窗口身份解析——same-bounds 窗口的 AX 树错配与 closed-window ghost（CGWindow 残留条目）的调查结论、identity 方案（`_AXUIElementGetWindow` SPI + 显式歧义错误 + AX-identity 存活检查）与保证。
