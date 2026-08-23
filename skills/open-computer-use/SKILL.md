---
name: open-computer-use
description: Platform-neutral guidance for using Open Computer Use, the open-source Computer Use MCP server and CLI for macOS, Linux, and Windows. Use when an agent needs to install, verify, troubleshoot, configure, or operate Open Computer Use through its native CLI, stdio MCP server, or direct Computer Use tool calls.
---

# Open Computer Use

## Overview

Open Computer Use exposes Computer Use as a local CLI and stdio MCP server. It is not Codex.app-specific; adapt the commands and MCP config to the agent runtime you are operating in.

The macOS runtime requires macOS 14.0 or later. Windows and Linux use their own platform runtimes and are not subject to this macOS minimum.

It supports the same core tool surface across macOS, Linux, and Windows:
`list_apps`, `get_app_state`, `click`, `perform_secondary_action`, `scroll`,
`drag`, `type_text`, `press_key`, and `set_value`. The Windows runtime
additionally implements the official Codex `window2` tools: `list_windows`,
`get_window`, `launch_app`, `get_window_state`, and `activate_window`, and the
action tools accept an optional `window` argument (`{app, id, title}` with an
opaque id from `list_windows`/`get_window_state`) plus `screenshotId` for
coordinate actions. On macOS and Linux these window2 tools return an explicit
"not supported yet" error.

## Core Workflow

1. On macOS, run `sw_vers -productVersion` before invoking the CLI and require macOS 14.0 or later. On older versions, explain that the runtime cannot launch; do not recommend `doctor` or permission changes as a fix for binary incompatibility.
2. Check the CLI is installed with `open-computer-use -h` or `ocu -h`. If installation or setup is missing, read [references/installation.md](references/installation.md).
3. On supported macOS versions, run `open-computer-use doctor` before the first real GUI task. If permissions are missing, ask the user to approve Accessibility and Screen Recording in the onboarding UI.
4. Inspect available apps before acting: `open-computer-use call list_apps`.
5. Capture current UI state with `open-computer-use call get_app_state --args '{"app":"TextEdit"}'`. The default state is usually enough for UI operation.
6. When the task needs longer semantic text, such as chat history, email bodies, document text, or long form content, call `get_app_state` with `text_limit: 1000` or `text_limit: "max"`.
7. When visible long pages or lists appear incomplete even after scrolling, call `get_app_state` with a larger `max_tree_nodes` or `max_tree_depth`.
8. Prefer element-targeted actions using `element_index` from the latest `get_app_state` result.
9. For multi-step CLI work, use `open-computer-use call --calls '<json-array>'` so one process can reuse the latest element index mapping.
10. For agent runtimes that support local MCP servers, configure `open-computer-use mcp` or `ocu mcp` and call the exposed Computer Use tools directly. Read [references/usage.md](references/usage.md).
11. If communication, permission, or desktop-session access fails, read [references/troubleshooting.md](references/troubleshooting.md).

## Operating Rules

- Treat the target desktop as the user's real session. Do not inspect password managers, unrelated private content, or sensitive apps unless the user explicitly asked for that task.
- Ask before sending, deleting, purchasing, approving, uploading, or making other externally visible changes.
- Do not assume Codex.app plugin helpers are available. Use the installed `open-computer-use` / `ocu` CLI or an explicit MCP config.
- Always run `get_app_state` (or `get_window_state`) before using `element_index`; do not guess indexes across sessions or after large UI changes.
- Prefer semantic actions and `set_value` for editable controls. Use coordinate `click`, `scroll`, and `drag` only when the element tree does not expose a safer target.
- Never press the Windows/Meta key or chords containing it (`win`, `super`, `cmd`, `meta`, `OS`); the runtime rejects these outright per the official safety policy.
- Never automate terminal apps (Windows Terminal, cmd, PowerShell), password managers, or Windows security apps; the runtime denies these apps regardless of opt-in flags. On Windows the deny list also covers third-party security suites by display name (AVG Internet Security, Avast Premium Security, Bitdefender Security Center).
- Window2 coordinates, screenshot ids, and element indexes are valid only for the observation that produced them; re-observe (`get_window_state`) after any action or error before retrying. In coordinate scroll mode pass `x`/`y` plus `scrollX`/`scrollY` pixel deltas and never `element_index`. On Windows, coordinate clicks/scrolls/drags additionally require a prior `get_window_state` with `include_screenshot: true` for that window, and are rejected if the window has moved or resized since the observation (re-observe and retry) or the point falls outside the screenshot bounds.
- `mouse_button: "right"` with `click_count >= 2` is rejected on Windows (official policy: right double click is not supported); `type_text` payloads above 8192 UTF-16 code units are rejected — split long text into chunks.
- `launch_app` and `activate_window` on Windows are gated by `OPEN_COMPUTER_USE_WINDOWS_ALLOW_APP_LAUNCH=1` and `OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOCUS_ACTIONS=1` respectively.
- On Windows, `click_method: "global"` (and `input_method: "global"` on `press_key`/`type_text`/`scroll`/`drag`) activates the target window and injects real SendInput events — it moves the mouse pointer and steals keyboard focus. Only use it when the user explicitly opted in via `OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOREGROUND_INPUT=1`; without the flag the runtime returns an explicit gated error.
- On Windows, screenshots prefer Windows.Graphics.Capture (captures the window itself even when occluded, without the OS cursor), falling back to `PrintWindow` and GDI screen copy. Force one backend with `OPEN_COMPUTER_USE_WINDOWS_CAPTURE=wgc|print|gdi` (default `auto`); a forced backend that fails raises an error instead of silently degrading.
- The Windows runtime is a single self-contained Go executable: all 14 tools (UI Automation tree reads, Win32 window-message and SendInput actions, and the WGC/PrintWindow/GDI screenshot chain) run in-process with no PowerShell/.NET dependency. `OPEN_COMPUTER_USE_WINDOWS_BACKEND` no longer selects anything (setting it only prints a one-line deprecation warning). For crash isolation, each tool operation executes in a short-lived child of the same executable and the screenshot chain runs in a grandchild worker; this is internal and invisible to tool callers.
- On macOS, do not enable `OPEN_COMPUTER_USE_ALLOW_GLOBAL_POINTER_FALLBACKS=1` unless the user explicitly requested `click_method: "global"` or other diagnostic behavior that may move the real pointer.
- On Windows and Linux, confirm the command is running inside the logged-in desktop session before assuming GUI automation is available.

## Common CLI Actions

```sh
open-computer-use -h
ocu -h
open-computer-use doctor
open-computer-use call list_apps
ocu call list_apps
open-computer-use call get_app_state --args '{"app":"TextEdit"}'
open-computer-use call get_app_state --args '{"app":"TextEdit","text_limit":1000}'
open-computer-use call get_app_state --args '{"app":"TextEdit","text_limit":"max"}'
open-computer-use call get_app_state --args '{"app":"Google Chrome","max_tree_nodes":3000,"max_tree_depth":96}'
open-computer-use call click --args '{"app":"TextEdit","element_index":"0"}'
open-computer-use call type_text --args '{"app":"TextEdit","text":"Hello from Open Computer Use"}'
```

For a short sequence that reuses state in one process:

```sh
open-computer-use call --calls '[
  {"tool":"get_app_state","args":{"app":"TextEdit"}},
  {"tool":"press_key","args":{"app":"TextEdit","key":"Return"}}
]'
```

Windows window2 flow (multi-window / modal targeting, opaque window id = HWND):

```sh
open-computer-use call list_windows
open-computer-use call get_window_state --args '{"window":{"app":"Notepad","id":1051836},"include_text":true}'
open-computer-use call click --args '{"window":{"id":1051836},"x":40,"y":20}'
open-computer-use call scroll --args '{"window":{"id":1051836},"x":200,"y":200,"scrollY":600}'
```

## MCP Usage

For runtimes that can launch local MCP servers over stdio, use:

```toml
[mcp_servers.open_computer_use]
command = "open-computer-use"
args = ["mcp"]
```

Read [references/usage.md](references/usage.md) for JSON config examples, direct tool-call patterns, and platform notes.

## References

- [references/installation.md](references/installation.md): one-time CLI install, agent MCP install commands, and macOS permissions.
- [references/usage.md](references/usage.md): MCP config, direct CLI calls, sequencing, and platform behavior.
- [references/troubleshooting.md](references/troubleshooting.md): permission, desktop-session, app discovery, and action failures.
