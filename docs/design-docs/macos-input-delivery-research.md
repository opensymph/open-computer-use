# macOS Input Delivery Research Notes

Environment-specific observations behind `skills/open-computer-use/references/macos-input.md`. These are a research record from one machine, not product guarantees.

## Method

- Machine: one macOS 27 dev machine.
- Rig: a dedicated TextEdit window, per-trial frontmost-app logging, and a before/after text diff as the delivery oracle.
- Size: 46 verified trials across 11 runs.

## Observations

- `type_text` on a settable `AXValue` and `set_value` work on background windows regardless of user activity.
- `AXPress` on menu items runs without activation.
- `press_key` chords containing `cmd` delivered in every observed condition, including to background apps.
- Plain unmodified keys delivered 28/28 when user input was quiet and 0/14 while the user was actively typing.
- Flagged single keys (`shift+a`) were context-dependent: delivered under a launchd context, dropped from the interactive shell context.
- The gated global-HID `input` path was dropped in every observed condition.
- A new window of a background app is ordered to the front at creation. The private SkyLight reordering APIs (`SLSOrderWindow`, `SLSSetWindowLevel`) were silently ignored from CLI processes.
- TCC grants are per-binary and per launch context; after a rebuild a launchd context can silently lose Accessibility.

## Not adopted

An earlier skill revision documented an `osascript`/System Events recipe for hiding an app right after creating a window. It was removed from the skill: the project avoids AppleScript during computer use (see the server instructions), the recipe needs Automation permission, and deliberately hiding the user's apps without their notice is not an acceptable default for an agent.
