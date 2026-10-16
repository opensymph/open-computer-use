# macOS Input Delivery & Background Operation

Read this reference when a macOS task involves keyboard input (`press_key`, `type_text`, `set_value`) or driving background windows, and you want to choose the reliable path up front instead of trial and error.

The findings below were characterized on a single macOS 27 dev machine (see `docs/design-docs/macos-input-delivery-research.md` for the trial record). They describe an observed condition, not a universal guarantee; re-validate on a new machine or OS version.

## Choosing the Input Path (reliability order)

1. **`type_text` on an editable element** — first choice for putting text into a document or field. When the element exposes a settable `AXValue`, the runtime uses the AX value-write path, which works on background windows and is independent of user activity. Some apps (e.g. terminals) do not expose a settable text value; for those, `type_text` falls back to keyboard input, which is subject to the conditions in item 4.
2. **`set_value` on a settable control** — AX value write; activity-independent and background-safe.
3. **Menu commands via `perform_secondary_action` (`AXPress`)** — the macOS `get_app_state` tree includes the menu bar (the window2 `get_window_state` tree does not). `AXPress` on a menu item element executes the command without activation and is activity-independent — verified working in the background. First choice for menu commands.
4. **`press_key` on macOS:**
   - Chords containing `cmd` (`cmd+n`, `cmd+s`, …): the macOS runtime accepts them and they delivered in every observed condition, including to background apps. Destructive chords (`cmd+q`, `cmd+w`, …) fall under the usual rule of asking the user before destructive actions; prefer the menu `AXPress` path. (The Windows runtime rejects Windows/Meta key chords outright.)
   - Plain unmodified keys (`a`, `Return`, …): app-targeted (pid-targeted) posting. Delivered when the user's real input is quiet — idle, reading, browsing: 28/28. **Dropped while the user is actively typing: 0/14.** Window state is not a variable: foreground or background, visible or hidden, any frontmost app (Safari, ChatGPT, Finder), recently activated or not — all deliver when input is quiet.
   - Flagged single keys (`shift+a`): context-dependent — delivered under a launchd (LaunchAgent) context, dropped from the interactive shell context on the test machine. Not a primary path.
5. **Do not use as a primary path:** the gated global-HID `input` path — dropped in every observed condition (the post succeeds, the key never lands). A separate event-source/shape issue, not the activity effect.

## Verifying Delivery (the habit that removes trial and error)

- After any key input to a text target, **re-read the text state** (`get_app_state` / `get_window_state` with `include_text`, or the element value) and diff it against the pre-action read. A successful return is not proof of delivery.
- If a plain key did not land:
  1. Retry once after a short pause (~0.5–1 s) — delivery works in lulls between the user's keystrokes.
  2. If the user is actively typing, wait for a lull or switch to an activity-independent path (`type_text` / value write).
- Log the frontmost app before and after each post. An unexpected change means a focus steal (the user's or your own) — re-observe before continuing.
- Do not interpret a single drop as a code defect: check user input activity and launch context first.

## Driving Background Apps Without Disturbing the User

- These work without activation (no focus steal): element clicks, `set_value`, `type_text` (AX path), `perform_secondary_action` (`AXPress`), and app-targeted `press_key` (subject to the activity condition above).
- `activate_window` / any activation is visible to the user; use it only when the task requires the foreground, and return focus to the user's app when done. It reports success only when the app is frontmost and the target window is its focused or main window; on macOS 14+ activation is cooperative, so the system may decline a request issued from a background process.
- `get_window_state` and the post-action refresh are read-only: they never restore minimized windows or activate the app. Only `activate_window` does that.
- Minimized windows still resolve, observe, and accept `set_value`; key-event input was not characterized for minimized windows — restore to visible before relying on it.

## Creating Windows

- A new window of a background app is ordered to the front of the global window stack at creation, and it cannot be reordered from outside the owning app. Do not script AppleScript/System Events to hide the app or to make windows "imperceptible" to the user; use `launch_app` and the Computer Use tools, and accept that a new window may become visible. Ask the user first if the visible change would be disruptive.

## Environment Caveats (observed behavior, not defects)

- **TCC grants are per-binary and per launch context.** After an in-session rebuild, a launchd (LaunchAgent) context silently loses Accessibility (stale cdhash attribution) while the interactive shell context keeps working. Symptom: AX-dependent behavior degrades without errors after a rebuild. Remedy: run from the shell context or re-grant.
- **Input drops are OS/app-side input-pipeline behavior.** The same event-posting code delivers chords 100% and plain keys 100% when real user input is quiet. Document the condition and route around it; do not patch the posting code.
