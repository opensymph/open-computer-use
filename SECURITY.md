# 安全策略

## 支持版本

仅对最新发布版本（npm `open-computer-use` latest tag）提供安全修复。

## 漏洞反馈

如果发现疑似安全漏洞，请不要提公开 issue。

请通过 GitHub 私有漏洞报告通道反馈：

```text
https://github.com/opensymph/open-computer-use/security/advisories/new
```

并尽量附上：

- 影响范围和潜在风险。
- 受影响的平台（macOS / Windows / Linux）与安装方式（npm 版本、agent client）。
- 复现步骤或 PoC。
- 已知的缓解方式或临时绕过方案。

## 处理预期

- 收到报告后会尽快确认并评估影响范围。
- 修复会在协调后随常规发布流程交付，并在发布说明中致谢报告者（除非要求匿名）。

## 适用范围

`open-computer-use` 会在本机已登录桌面 session 内执行 Accessibility / 键鼠自动化操作，天然具备高权限。请只在你拥有或被明确授权的机器上使用；由使用方式本身引入的风险不属于本仓库的安全范畴。
