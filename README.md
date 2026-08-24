<p align="center">
  <img src="./assets/logo/open-computer-use-256.png" width="144" alt="open-computer-use">
</p>

# open-computer-use

[![npm](https://img.shields.io/npm/v/@opensymph/open-computer-use)](https://www.npmjs.com/package/@opensymph/open-computer-use)
[![Release](https://img.shields.io/github/v/release/opensymph/open-computer-use)](https://github.com/opensymph/open-computer-use/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-informational)](./LICENSE)
[![简体中文](https://img.shields.io/badge/简体中文-点击查看-orange)](./README.zh-CN.md)

A local MCP server that gives AI agents eyes and hands on your desktop. Agents can see an app's interface, click, type, scroll, and drag — through the accessibility layer, without taking over your real mouse and keyboard. Runs entirely on your machine, on macOS, Windows, and Linux.

## The tools

Nine core tools, identical across all three platforms:

| Tool | What it does |
| --- | --- |
| `list_apps` | List running and recently used applications. |
| `get_app_state` | Read an app's accessibility tree and screenshot. |
| `click` | Click by `element_index` or screenshot coordinates. |
| `perform_secondary_action` | Invoke an element's own secondary action. |
| `scroll` | Scroll an element by pages, or a window by pixel deltas. |
| `drag` | Drag between two coordinates. |
| `type_text` | Type text, Unicode-safe, background-first. |
| `press_key` | Press a key or chord (`ctrl+s`, `return`, `page_up`…). |
| `set_value` | Set the value of a settable control directly. |

Five additional window-level tools — `list_windows`, `get_window`, `get_window_state`, `launch_app`, `activate_window` — follow the newer window2 API and are currently available on Windows (macOS and Linux in progress).

## Quick start

```bash
npm i -g @opensymph/open-computer-use
ocu doctor        # verify the install; macOS: prompts for permissions
ocu call list_apps
```

macOS 14+ needs `Accessibility` and `Screen Recording` granted once. Windows and Linux work out of the box in a signed-in desktop session (Linux desktops need AT-SPI2, which GNOME and friends ship by default).

No npm? Grab the tarball from [GitHub Releases](https://github.com/opensymph/open-computer-use/releases) and run `npm i -g <tarball>` — or install it into ZCode as a plugin (below) and it will use whatever local runtime it finds.

## Connect your agent

```bash
ocu install-codex-mcp      # Codex CLI & Codex App
ocu install-codex-plugin   # Codex App, plugin form
ocu install-claude-mcp     # Claude Code
ocu install-gemini-mcp     # Gemini CLI (--scope user for global)
ocu install-opencode-mcp   # opencode
```

Any other MCP client — add it manually:

```json
{
  "mcpServers": {
    "open-computer-use": { "command": "open-computer-use", "args": ["mcp"] }
  }
}
```

### ZCode

This repo ships as a ZCode plugin — one install gives you the skill and an auto-connected MCP server:

1. Install the runtime once (the plugin falls back to it when no local build exists):

   ```bash
   npm i -g @opensymph/open-computer-use
   ```

2. In ZCode, open **Settings → Plugin Management → Discover** and click **+**.
3. Add this repository — the GitHub URL `opensymph/open-computer-use`, or a local checkout directory.
4. Find **Open Computer Use** in the list and click **Get**.
5. Start a new session. Check **Settings → MCP** shows `open-computer-use` as connected, then just ask: *"list the windows on my screen"*.

To remove it later: Installed tab → Open Computer Use → uninstall.

**Agent skill** — installable guidance that teaches agents to use these tools well:

```bash
npx skills add opensymph/open-computer-use -g -a claude-code --skill open-computer-use -y
```

## Why this one

- **Non-intrusive by design.** Prefer the accessibility API over synthetic input; your real pointer, focus, and foreground app stay put unless you explicitly opt into global input.
- **Three platforms, one contract.** The same tool names, arguments, and results on every OS — agents don't need per-platform branching.
- **A cursor you can watch.** On macOS, actions drive a visible software cursor, so you can follow what the agent is doing.
- **Scriptable without a client.** `ocu call` runs any tool from your shell and prints MCP-style JSON; `--calls` chains sequences in one process.
- **Guardrails built in.** Password managers are always refused. Launching apps, stealing focus, and global input injection each sit behind an explicit environment-variable gate.
- **Signed where it matters.** The macOS runtime is Developer ID signed, so granted permissions survive version upgrades.

## Platform status

| Platform | Runtime | Notes |
| --- | --- | --- |
| macOS | Swift | Visual cursor, permission onboarding, `sky_click` background clicks. |
| Windows | Go, single exe | UI Automation + Win32, process-isolated operations, full window2 API. |
| Linux | Go, single binary | Native AT-SPI2 over D-Bus; plus display-level X11 commands (see below). |

### Linux display-level commands

Beyond the AT-SPI tools, the Linux runtime ships a few whole-display X11 commands that mirror the classic `xdotool` / `ffmpeg x11grab` desktop stack — handy for headless VNC desktops where you want to capture or drive the entire screen rather than a single app:

```bash
open-computer-use screenshot --display :1 --output shot.png   # full-screen PNG (pure Go X11)
open-computer-use cursor-position --display :1                # pointer x/y + screen size (JSON)
open-computer-use record start --display :1 --output rec.mp4  # ffmpeg x11grab → H.264 mp4
open-computer-use record stop
# global synthetic input (moves the real pointer/keyboard) — opt-in gate required:
OPEN_COMPUTER_USE_ALLOW_GLOBAL_POINTER_FALLBACKS=1 \
  open-computer-use input click --x 960 --y 600 --display :1
OPEN_COMPUTER_USE_ALLOW_GLOBAL_POINTER_FALLBACKS=1 \
  open-computer-use input type "hello" --display :1
```

`screenshot` and `cursor-position` are read-only pure-Go X11. `input` needs `xdotool` and `record` needs `ffmpeg` on `PATH`; `input` also requires `OPEN_COMPUTER_USE_ALLOW_GLOBAL_POINTER_FALLBACKS=1` because it drives the real pointer/keyboard. These are CLI-only and Linux-only; the AT-SPI MCP tool surface is unchanged.

## Documentation

- [Architecture](./docs/ARCHITECTURE.md) — how the three runtimes work
- [Skill references](./skills/open-computer-use) — usage, installation, troubleshooting
- [Security policy](./SECURITY.md) and [third-party notices](./THIRD_PARTY_NOTICES.md)
- [Contributing](./CONTRIBUTING.md)

## License

[MIT](./LICENSE)
