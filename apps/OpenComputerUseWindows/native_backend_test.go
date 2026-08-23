//go:build windows

package main

import (
	"strings"
	"testing"
)

// TestNativeForegroundGatesAreEnforced covers the same surface the retired runtime did
// assertions: every foreground/focus path must stay opt-in, with the exact
// protocol error text.
func TestNativeForegroundGatesAreEnforced(t *testing.T) {
	resp, err := nativeRuntime.call(psRequest{Tool: "activate_window", WindowID: 999999})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.Error != "activate_window is disabled by default to avoid stealing user focus; set OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOCUS_ACTIONS=1 to enable it." {
		t.Fatalf("activate_window gate = %+v", resp)
	}
	// With the request-level flag the gate passes and the call proceeds to
	// the (failing) window resolution.
	resp, err = nativeRuntime.call(psRequest{Tool: "activate_window", WindowID: 999999,
		EnvFlags: map[string]string{"OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOCUS_ACTIONS": "1"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || !strings.HasPrefix(resp.Error, "staleWindowHandle(999999)") {
		t.Fatalf("activate_window past gate = %+v", resp)
	}

	resp, err = nativeRuntime.call(psRequest{Tool: "launch_app", App: "notepad"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || resp.Error != "launch_app is disabled by default; set OPEN_COMPUTER_USE_WINDOWS_ALLOW_APP_LAUNCH=1 to enable it." {
		t.Fatalf("launch_app gate = %+v", resp)
	}

	setFocus := psRequest{Tool: "perform_secondary_action", WindowID: 999999,
		Element: &elementRecord{Index: 1, RuntimeID: []int{7, 7}}, Action: "setfocus"}
	resp, _ = nativeRuntime.call(setFocus)
	if resp.OK || !strings.Contains(resp.Error, "the window is no longer open") {
		t.Fatalf("setfocus should hit window resolution before the gate: %+v", resp)
	}
}

// TestNativeCaptureForcedMode keeps the screenshot-chain contract: forced
// modes raise instead of silently degrading; auto never fails the operation.
func TestNativeCaptureForcedMode(t *testing.T) {
	_, err := nativeCaptureWindowPng(psRequest{
		EnvFlags: map[string]string{"OPEN_COMPUTER_USE_WINDOWS_CAPTURE": "wgc"},
	}, 0, nil)
	if err == nil || err.Error() != "OPEN_COMPUTER_USE_WINDOWS_CAPTURE=wgc failed: wgc: wgc requires a window handle" {
		t.Fatalf("forced wgc error = %v", err)
	}
	// Forced gdi without bounds returns no image, no error (PS returns null).
	png, err := nativeCaptureWindowPng(psRequest{
		EnvFlags: map[string]string{"OPEN_COMPUTER_USE_WINDOWS_CAPTURE": "gdi"},
	}, 0, nil)
	if err != nil || png != "" {
		t.Fatalf("forced gdi without bounds = (%q, %v)", png, err)
	}
	// Auto mode with no usable backend omits the image instead of failing.
	png, err = nativeCaptureWindowPng(psRequest{}, 0, nil)
	if err != nil || png != "" {
		t.Fatalf("auto without window = (%q, %v)", png, err)
	}
}

// TestNativeTextLimitSemantics pins Resolve-TextLimit/Limit-Text behavior.
func TestNativeTextLimitSemantics(t *testing.T) {
	if limit := resolveTextLimitPS(nil); limit == nil || *limit != 500 {
		t.Fatalf("default text limit = %v, want 500", limit)
	}
	if limit := resolveTextLimitPS("max"); limit != nil {
		t.Fatalf("max text limit = %v, want nil", limit)
	}
	if limit := resolveTextLimitPS(40); limit == nil || *limit != 40 {
		t.Fatalf("explicit text limit = %v, want 40", limit)
	}
	if limit := resolveTextLimitPS(0); limit == nil || *limit != 500 {
		t.Fatalf("zero text limit = %v, want 500 fallback", limit)
	}
	limit := 5
	if got := limitTextPS("abcdefgh", &limit); got != "abcde..." {
		t.Fatalf("limitText = %q", got)
	}
}

// TestNativeTreeBudgetDefaults pins the shared 1200-node / 64-depth budget.
func TestNativeTreeBudgetDefaults(t *testing.T) {
	if accessibilityMaxNodes != 1200 {
		t.Fatalf("tree node budget = %d, want 1200", accessibilityMaxNodes)
	}
	if accessibilityMaxDepth != 64 {
		t.Fatalf("tree depth budget = %d, want 64", accessibilityMaxDepth)
	}
	if defaultTextLimit != 500 {
		t.Fatalf("default text limit = %d, want 500", defaultTextLimit)
	}
}

// TestNativeSafetyTables pins the official deny lists.
func TestNativeSafetyTables(t *testing.T) {
	for _, name := range []string{"windowsterminal", "wt", "cmd", "powershell", "pwsh", "consolehost", "conhost", "defender", "msmpeng", "nissrv", "securityhealth", "securityhealthservice", "wdav"} {
		found := false
		for _, candidate := range deniedAppExactNames {
			if candidate == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("deny list missing terminal/security app %q", name)
		}
	}
	for _, name := range []string{"super", "win", "cmd", "meta", "command", "os", "windows"} {
		found := false
		for _, candidate := range bannedModifierNames {
			if candidate == name {
				found = true
			}
		}
		if !found {
			t.Fatalf("banned modifier list missing %q", name)
		}
	}
}

// TestRunRuntimeOperationRoutesToNative verifies the single runtime boundary
// dispatches in-process (no external runtime) and keeps domain failures in
// the response envelope.
func TestRunRuntimeOperationRoutesToNative(t *testing.T) {
	resp, err := runRuntimeOperation(psRequest{Tool: "get_window", WindowID: 999999})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OK || !strings.HasPrefix(resp.Error, "staleWindowHandle(999999)") {
		t.Fatalf("get_window stale envelope = %+v", resp)
	}
	if _, err := runRuntimeOperation(psRequest{Tool: "not_a_tool"}); err == nil ||
		!strings.Contains(err.Error(), "does not implement tool") {
		t.Fatalf("unknown tool error = %v", err)
	}
	// The env folding path: the request carries current env flag values.
	req := prepareRuntimeRequestEnv(psRequest{Tool: "get_window"})
	if req.EnvFlags == nil || len(req.EnvFlags) != len(runtimeEnvFlags) {
		t.Fatalf("env flags folding = %#v", req.EnvFlags)
	}
}

// TestNativeInputPrimitivesPinKeySemantics keeps the xdotool-style key table
// and chord splitting byte-compatible.
func TestNativeInputPrimitivesPinKeySemantics(t *testing.T) {
	if vk, err := virtualKeyForName("Return"); err != nil || vk != 0x0D {
		t.Fatalf("Return = (%x, %v)", vk, err)
	}
	if vk, err := virtualKeyForName("KP_0"); err != nil || vk != 0x60 {
		t.Fatalf("KP_0 = (%x, %v)", vk, err)
	}
	if vk, err := virtualKeyForName("f12"); err != nil || vk != 0x7B {
		t.Fatalf("f12 = (%x, %v)", vk, err)
	}
	if _, err := virtualKeyForName("hyper"); err == nil || err.Error() != "Unsupported key: hyper" {
		t.Fatalf("unsupported key error = %v", err)
	}
	if _, err := modifierVirtualKeyForName("Super"); err == nil ||
		err.Error() != "press_key with the Windows/Meta key (Super) is not permitted by the official Computer Use safety policy." {
		t.Fatalf("banned modifier error = %v", err)
	}
	parts := splitChord("ctrl + Shift_L + a")
	if len(parts) != 3 || parts[0] != "ctrl" || parts[2] != "a" {
		t.Fatalf("splitChord = %#v", parts)
	}
}
