//go:build windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// nativeBackendImpl is the Go in-process runtime. It implements all 14 tools
// (Win32 + UIA COM + WGC) and speaks the exact psRequest/psResponse protocol
// the removed PS-era daemon spoke. Tree-content snapshots use the Go
// native semantics as the behavior baseline: the PS-era client's FX
// proxy layer (UiaCoreApi's static ctor registering .NET client-side
// proxies) rewrote pattern availability and synthesized non-client subtrees,
// while a plain COM client — what the official Swift runtime uses — reports
// the raw provider view, which is what native_uia.go reports. Every error
// string and the ok/error envelopes remain byte-identical with the retired
// PS-era runtime (dual-run verified).
type nativeBackendImpl struct{}

func (n *nativeBackendImpl) call(req psRequest) (*psResponse, error) {
	switch req.Tool {
	case "list_apps":
		return nativeListApps()
	case "list_windows":
		return nativeListWindows()
	case "get_window":
		return nativeGetWindow(req.WindowID)
	case "launch_app":
		return nativeLaunchApp(req, req.App)
	case "activate_window":
		return nativeActivateWindow(req, req.WindowID)
	case "get_window_state":
		return nativeGetWindowState(req)
	case "get_app_state":
		return nativeGetAppState(req)
	case "click", "perform_secondary_action", "scroll", "drag", "type_text", "press_key", "set_value":
		return nativeActionTool(req)
	default:
		return nil, fmt.Errorf("native backend does not implement tool %q", req.Tool)
	}
}

// requestEnvFlagEnabled mirrors Get-RequestEnvVar + Test-EnvFlagEnabled:
// request-level env_flags take precedence over the process environment.
func requestEnvFlagEnabled(req psRequest, name string) bool {
	value, ok := "", false
	if req.EnvFlags != nil {
		value, ok = req.EnvFlags[name]
	}
	if !ok {
		value = os.Getenv(name)
	}
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// nativeAssertAppAllowed mirrors Assert-AppAllowed.
func nativeAssertAppAllowed(name string) error {
	if denied, leaf := deniedAppName(name); denied {
		return fmt.Errorf("appDenied(%q): automating terminal apps, password managers, or Windows security apps is not permitted (official Computer Use safety policy).", leaf)
	}
	return nil
}

// nativeListApps mirrors List-Apps: one text line per windowed process
// (sorted by process name then pid) plus the structured apps payload with
// Start Menu entries that are not running.
func nativeListApps() (*psResponse, error) {
	processes := windowedProcesses()
	lines := make([]string, 0, len(processes))
	apps := make([]listAppsApp, 0, len(processes))
	running := map[string]bool{}
	for _, process := range processes {
		title := process.mainTitle
		if strings.TrimSpace(title) == "" {
			title = "untitled"
		}
		lines = append(lines, fmt.Sprintf("%s -- %s [running, pid=%d, window=%s]",
			process.name, process.name, process.pid, title))
		running[strings.ToLower(process.name)] = true
		app := listAppsApp{
			DisplayName: process.name,
			ID:          process.name,
			IsRunning:   true,
			Windows:     uiaWindowsForProcess(process.name, process.pid, uintptr(process.mainHWND), process.mainTitle),
		}
		apps = append(apps, app)
	}
	for _, installed := range startMenuApps() {
		if running[installed.ID] {
			continue
		}
		apps = append(apps, installed)
	}
	return &psResponse{OK: true, Text: strings.Join(lines, "\n"), Apps: apps}, nil
}

// resolveWindowFromHandle mirrors Get-WindowFromHandle.
func resolveWindowFromHandle(windowID int64) (*windowRef, string, error) {
	if windowID <= 0 || !isWindowHwnd(windowID) {
		return nil, "", fmt.Errorf("staleWindowHandle(%d): the window is no longer open; re-observe with list_windows.", windowID)
	}
	var pid uint32
	getWindowThreadProcessID(windowID, &pid)
	name := ""
	if pid > 0 {
		name = processNameByPID(pid)
	}
	if name == "" {
		return nil, "", fmt.Errorf("staleWindowHandle(%d): the owning process is no longer running; re-observe with list_windows.", windowID)
	}
	if err := nativeAssertAppAllowed(name); err != nil {
		return nil, "", err
	}
	return newWindowRef(name, windows.HWND(windowID), windowText(windows.HWND(windowID))), name, nil
}

// nativeGetWindow mirrors the get_window operation.
func nativeGetWindow(windowID int64) (*psResponse, error) {
	window, _, err := resolveWindowFromHandle(windowID)
	if err != nil {
		return &psResponse{Error: err.Error()}, nil
	}
	return &psResponse{OK: true, Window: window}, nil
}

// nativeActivateWindow mirrors Invoke-ActivateWindow.
func nativeActivateWindow(req psRequest, windowID int64) (*psResponse, error) {
	const flag = "OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOCUS_ACTIONS"
	if !requestEnvFlagEnabled(req, flag) {
		return &psResponse{Error: "activate_window is disabled by default to avoid stealing user focus; set OPEN_COMPUTER_USE_WINDOWS_ALLOW_FOCUS_ACTIONS=1 to enable it."}, nil
	}
	window, _, err := resolveWindowFromHandle(windowID)
	if err != nil {
		return &psResponse{Error: err.Error()}, nil
	}
	hwnd := windows.HWND(windowID)
	showWindow(hwnd, swRestore)
	setForegroundWindow(hwnd)
	return &psResponse{OK: true, Window: window}, nil
}

// nativeLaunchApp mirrors Invoke-LaunchApp.
func nativeLaunchApp(req psRequest, app string) (*psResponse, error) {
	if err := nativeAssertAppAllowed(app); err != nil {
		return &psResponse{Error: err.Error()}, nil
	}
	const flag = "OPEN_COMPUTER_USE_WINDOWS_ALLOW_APP_LAUNCH"
	if !requestEnvFlagEnabled(req, flag) {
		return &psResponse{Error: "launch_app is disabled by default; set OPEN_COMPUTER_USE_WINDOWS_ALLOW_APP_LAUNCH=1 to enable it."}, nil
	}
	path, err := resolveLaunchPath(app)
	if err != nil {
		return &psResponse{Error: fmt.Sprintf("launchFailed(%q): %s", app, err.Error())}, nil
	}
	process, err := startProcess(path)
	if err != nil {
		return &psResponse{Error: fmt.Sprintf("launchFailed(%q): %s", app, err.Error())}, nil
	}
	processName := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	for i := 0; i < 20; i++ {
		sleepMs(250)
		for _, candidate := range windowedProcesses() {
			// Get-Process -Name matching is case-insensitive (Store Notepad
			// runs as "Notepad" even when launched via "notepad").
			if strings.EqualFold(candidate.name, processName) || candidate.pid == uint32(process.Pid) {
				return &psResponse{OK: true, Window: newWindowRef(candidate.name, candidate.mainHWND, candidate.mainTitle)}, nil
			}
		}
		if !processAlive(process.Pid) {
			// Keep waiting anyway: PS only skips its liveness check's
			// continue branch, and still throws after the loop.
			continue
		}
	}
	return &psResponse{Error: fmt.Sprintf("launchFailed(%q): the app started but no top-level window appeared within 5 seconds.", app)}, nil
}

// nativeGetWindowState mirrors the get_window_state operation
// (Build-SnapshotForWindowId + include_screenshot=false clears the image).
func nativeGetWindowState(req psRequest) (*psResponse, error) {
	snapshot, err := nativeSnapshotForWindowId(req, req.WindowID)
	if err != nil {
		return &psResponse{Error: err.Error()}, nil
	}
	if req.IncludeScreenshot != nil && !*req.IncludeScreenshot {
		snapshot.ScreenshotPNGBase64 = ""
	}
	return &psResponse{OK: true, Snapshot: snapshot}, nil
}

// nativeGetAppState mirrors the get_app_state operation (Build-Snapshot).
func nativeGetAppState(req psRequest) (*psResponse, error) {
	snapshot, err := nativeSnapshotForApp(req)
	if err != nil {
		return &psResponse{Error: err.Error()}, nil
	}
	return &psResponse{OK: true, Snapshot: snapshot}, nil
}

// nativeListWindows mirrors the list_windows operation.
func nativeListWindows() (*psResponse, error) {
	windows, err := uiaListWindows()
	if err != nil {
		return nil, err
	}
	return &psResponse{OK: true, Windows: windows}, nil
}

// captureFirstPng measures the window rect and runs the screenshot chain
// BEFORE any UIA tree walk of the same operation. When the caller explicitly
// declined the screenshot (include_screenshot=false) the capture is skipped
// entirely — the image would be discarded right after, and skipping keeps
// child-process churn out of text-only observations.
func captureFirstPng(req psRequest, hwnd int64) (*frame, string) {
	bounds := windowRectFrame(windows.HWND(hwnd))
	if bounds == nil {
		return nil, ""
	}
	if req.IncludeScreenshot != nil && !*req.IncludeScreenshot {
		return bounds, ""
	}
	png, err := nativeCaptureWindowPng(req, windows.HWND(hwnd), bounds)
	if err != nil {
		return bounds, ""
	}
	return bounds, png
}

// nativeSnapshotForWindowId mirrors Build-SnapshotForWindowId. The capture
// chain runs first (caller thread, no UIA state); every UIA read is dispatched
// to the dedicated COM thread — calling UIA from an uninitialized thread
// corrupts memory (goroutines migrate between OS threads; see history
// 2026-08-22).
func nativeSnapshotForWindowId(req psRequest, windowID int64) (*appSnapshot, error) {
	_, processName, err := resolveWindowFromHandle(windowID)
	if err != nil {
		return nil, err
	}
	var pid uint32
	getWindowThreadProcessID(windowID, &pid)
	bounds, png := captureFirstPng(req, windowID)
	var snapshot *appSnapshot
	var jobErr error
	err = uiaOnThread(func() {
		element, err := uiaElementFromHandle(windowID)
		if err != nil {
			jobErr = err
			return
		}
		defer element.release()
		snapshot = uiaBuildSnapshotForWindow(processName, int32(pid), windowID, element,
			bounds, png, resolveTextLimitPS(req.TextLimit), req.MaxTreeNodes, req.MaxTreeDepth)
	})
	if err != nil {
		return nil, err
	}
	if snapshot == nil {
		return nil, jobErr
	}
	return snapshot, nil
}

// nativeResolveApp mirrors Resolve-App: pid / exact name / ".exe" / title
// match over windowed processes, then the gated launch fallback.
func nativeResolveApp(query string) (windowedProcess, error) {
	normalized := strings.TrimSpace(query)
	if err := nativeAssertAppAllowed(normalized); err != nil {
		return windowedProcess{}, err
	}
	processQuery := normalized
	if strings.HasSuffix(strings.ToLower(normalized), ".exe") {
		processQuery = normalized[:len(normalized)-4]
	}
	processes := windowedProcesses()
	if pid, err := strconv.Atoi(normalized); err == nil {
		for _, candidate := range processes {
			if int(candidate.pid) == pid {
				return candidate, nil
			}
		}
	}
	for _, candidate := range processes {
		if strings.EqualFold(candidate.name, processQuery) ||
			strings.EqualFold(candidate.name+".exe", normalized) ||
			strings.EqualFold(candidate.mainTitle, normalized) ||
			strings.Contains(strings.ToLower(candidate.mainTitle), strings.ToLower(normalized)) {
			return candidate, nil
		}
	}
	if requestEnvFlagEnabled(psRequest{}, "OPEN_COMPUTER_USE_WINDOWS_ALLOW_APP_LAUNCH") {
		// Start-Process fallback mirrors Invoke-LaunchApp's wait loop.
		resp, err := nativeLaunchApp(psRequest{Tool: "launch_app", App: normalized}, normalized)
		if err == nil && resp.OK && resp.Window != nil {
			for _, candidate := range windowedProcesses() {
				if candidate.mainHWND == windows.HWND(resp.Window.ID) {
					return candidate, nil
				}
			}
		}
	}
	return windowedProcess{}, fmt.Errorf("appNotFound(%q)", query)
}

// --- Launch helpers ---------------------------------------------------------

func isWindowHwnd(windowID int64) bool {
	return windows.IsWindow(windows.HWND(windowID))
}

func getWindowThreadProcessID(windowID int64, pid *uint32) {
	windows.GetWindowThreadProcessId(windows.HWND(windowID), pid)
}

// resolveLaunchPath mirrors Start-Process resolution: explicit paths pass
// through; bare names resolve through the App Paths registry then PATH
// (ShellExecute's order).
func resolveLaunchPath(app string) (string, error) {
	trimmed := strings.TrimSpace(app)
	if trimmed == "" {
		return "", fmt.Errorf("The system cannot find the file specified")
	}
	if strings.ContainsAny(trimmed, `/\`) {
		if _, err := os.Stat(trimmed); err == nil {
			return trimmed, nil
		}
		return "", fmt.Errorf("The system cannot find the file specified")
	}
	if path, ok := appPathsLookup(trimmed); ok {
		return path, nil
	}
	if path, err := exec.LookPath(trimmed); err == nil {
		return path, nil
	}
	if path, ok := appPathsLookup(trimmed + ".exe"); ok {
		return path, nil
	}
	return "", fmt.Errorf("The system cannot find the file specified")
}

// appPathsLookup checks HKLM\...\App Paths\<name> (default value).
func appPathsLookup(name string) (string, bool) {
	key, err := registry.OpenKey(registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Windows\CurrentVersion\App Paths\`+name,
		registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer key.Close()
	value, _, err := key.GetStringValue("")
	if err != nil || strings.TrimSpace(value) == "" {
		return "", false
	}
	return strings.Trim(value, `"`), true
}

func startProcess(path string) (*os.Process, error) {
	cmd := exec.Command(path)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go func() { _ = cmd.Wait() }()
	return cmd.Process, nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false
	}
	return exitCode == 259 // STILL_ACTIVE
}
