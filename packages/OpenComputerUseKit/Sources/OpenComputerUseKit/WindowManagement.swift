import AppKit
import ApplicationServices
import CoreGraphics
import Foundation

/// Official window2 window reference: `{app, id, title}` where `id` is the
/// opaque CGWindowID from `list_windows`/`get_window_state`.
public struct WindowRef: Equatable, Sendable {
    public let app: String
    public let id: CGWindowID
    public let title: String?

    public init(app: String, id: CGWindowID, title: String?) {
        self.app = app
        self.id = id
        self.title = title
    }

    var jsonObject: [String: Any] {
        var dictionary: [String: Any] = [
            "app": app,
            "id": Int(id),
        ]
        if let title, !title.isEmpty {
            dictionary["title"] = title
        }
        return dictionary
    }
}

/// One entry from a `CGWindowListCopyWindowInfo` enumeration (front to back).
struct CGWindowEntry: Equatable {
    let windowID: CGWindowID
    let ownerPID: pid_t
    let layer: Int
    let bounds: CGRect
    let title: String?
    let frontToBackIndex: Int
}

/// Outcome of the AX-identity liveness check for one CGWindowID.
///
/// Closed windows and AppKit helper windows can linger in the full
/// (`optionAll`) CGWindow list long after the window is gone, sometimes for
/// minutes at alpha 1.0. The only reliable way to distinguish such entries
/// from live windows is the owning app's accessibility window list: a live
/// window appears in `AXWindows` and maps back to its CGWindowID via
/// `_AXUIElementGetWindow` (see `AXWindowIdentitySPI`).
enum WindowIdentity {
    /// The owning app's accessibility window list contains a window whose
    /// backing CGWindowID is exactly the checked id.
    case confirmed
    /// The owning app's accessibility window list was read completely, every
    /// listed window was mapped to its CGWindowID, and none match. The CG
    /// entry has no live accessibility identity (closed/lingering window or
    /// AppKit helper window) and must be reported stale.
    case absent
    /// The check could not be run or completed: the private mapping symbol is
    /// unavailable, the app does not expose an accessibility window list, or
    /// at least one listed window could not be mapped. No conclusion; the
    /// window must not be rejected on the basis of this check.
    case indeterminate
}

/// Injection seam for the AX-identity liveness check (`nil` in production
/// means the real check); tests pin the resolution order with fixed
/// verdicts so they never depend on the live system state.
typealias WindowIdentityCheck = (_ pid: pid_t, _ windowID: CGWindowID) -> WindowIdentity

/// A window id that passed the official liveness and safety checks: the
/// window is in the current window list, its owning process is running, the
/// owning app is not on the deny list, and the AX-identity check does not
/// definitively prove the window is closed.
struct ResolvedWindow {
    let ref: WindowRef
    let pid: pid_t
    let bundleIdentifier: String?
    let runningApplication: NSRunningApplication
    let entry: CGWindowEntry
}

// WindowArgumentParsing mirrors the Windows runtime's optionalWindow /
// windowIDFromValue semantics: the official `window` object ({app, id,
// title}) or the flat `window_id` alias. The id is authoritative; the app
// and title fields are carried for display only.
enum WindowArgumentParsing {
    static func optional(_ arguments: [String: Any]) throws -> WindowRef? {
        if arguments["window"] == nil, let rawWindowID = arguments["window_id"] {
            let id = try windowIDValue(rawWindowID)
            guard id > 0 else {
                return nil
            }
            return WindowRef(app: "", id: try windowID(from: id), title: nil)
        }

        guard let raw = arguments["window"], !(raw is NSNull) else {
            return nil
        }

        guard let object = raw as? [String: Any] else {
            throw ComputerUseError.message("window must be an object with an integer id")
        }

        let app = (object["app"] as? String)?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        let title = object["title"] as? String
        let id = try windowIDValue(object["id"])
        guard id > 0 else {
            throw ComputerUseError.message("window.id must be an integer >= 0")
        }

        return WindowRef(app: app, id: try windowID(from: id), title: title)
    }

    // CGWindowID is UInt32; `CGWindowID(id)` would trap (crashing the whole
    // server) for larger values. No such window can exist, so report it as stale.
    private static func windowID(from id: Int) throws -> CGWindowID {
        guard let windowID = CGWindowID(exactly: id) else {
            throw ComputerUseError.message(
                "staleWindowHandle(\(id)): the window is no longer open; re-observe with list_windows."
            )
        }
        return windowID
    }

    static func required(_ arguments: [String: Any]) throws -> WindowRef? {
        try optional(arguments)
    }

    // Mirrors windowIDFromValue: JSON numbers, integer strings, and NSNumber
    // are accepted; a non-integer number is an explicit error; anything else
    // is treated as a missing id (0).
    private static func windowIDValue(_ value: Any?) throws -> Int {
        guard let value else {
            return 0
        }

        if let number = value as? NSNumber {
            if CFGetTypeID(number as CFTypeRef) == CFBooleanGetTypeID() {
                return 0
            }

            let doubleValue = number.doubleValue
            guard doubleValue.isFinite, doubleValue.rounded(.towardZero) == doubleValue else {
                throw ComputerUseError.message("window id must be an integer >= 0")
            }
            guard doubleValue >= Double(Int.min), doubleValue <= Double(Int.max) else {
                return 0
            }
            return Int(doubleValue)
        }

        if let string = value as? String {
            let trimmed = string.trimmingCharacters(in: .whitespacesAndNewlines)
            guard let parsed = Int(trimmed) else {
                throw ComputerUseError.message("window id must be an integer >= 0")
            }
            return parsed
        }

        return 0
    }
}

/// Window enumeration, resolution, activation, and per-window capture for
/// the macOS window2 surface. CGWindowID values come from the system window
/// list; they are never invented.
enum WindowDirectory {
    // MARK: - Enumeration

    static func listEntries(onScreenOnly: Bool) -> [CGWindowEntry] {
        let options: CGWindowListOption = onScreenOnly ? [.optionOnScreenOnly] : []
        guard let infoList = CGWindowListCopyWindowInfo(options, kCGNullWindowID) as? [[String: Any]] else {
            return []
        }

        return infoList.enumerated().compactMap { offset, info -> CGWindowEntry? in
            guard
                let ownerPID = info[kCGWindowOwnerPID as String] as? pid_t,
                let number = info[kCGWindowNumber as String] as? NSNumber,
                let layer = info[kCGWindowLayer as String] as? Int,
                let boundsDictionary = info[kCGWindowBounds as String] as? NSDictionary,
                let bounds = CGRect(dictionaryRepresentation: boundsDictionary)
            else {
                return nil
            }

            return CGWindowEntry(
                windowID: CGWindowID(number.uint32Value),
                ownerPID: ownerPID,
                layer: layer,
                bounds: bounds,
                title: info[kCGWindowName as String] as? String,
                frontToBackIndex: offset
            )
        }
    }

    /// Targetable windows: on-screen, normal window level, non-zero size.
    /// Titles require Screen Recording; without it they come back empty and
    /// are omitted from the reference, while the id is always present.
    static func listWindows() -> [WindowRef] {
        listEntries(onScreenOnly: true)
            .filter { $0.layer == 0 && $0.bounds.width > 0 && $0.bounds.height > 0 }
            // Deny-listed apps (password managers) must not leak window
            // titles through the enumeration either.
            .filter { !AppSafetyPolicy.isBlocked(bundleIdentifier: NSRunningApplication(processIdentifier: $0.ownerPID)?.bundleIdentifier) }
            .map { entry in
                WindowRef(
                    app: ownerName(for: entry.ownerPID),
                    id: entry.windowID,
                    title: entry.title?.isEmpty == false ? entry.title : nil
                )
            }
    }

    static func currentBounds(for id: CGWindowID) -> CGRect? {
        listEntries(onScreenOnly: false).first(where: { $0.windowID == id })?.bounds
    }

    /// Resolves a pid to a descriptor. Uses `NSRunningApplication` directly
    /// instead of `NSWorkspace.shared.runningApplications`: that list only
    /// contains LaunchServices-registered apps and misses bare executables
    /// (such as the test fixture) that own windows.
    static func runningApp(for pid: pid_t) -> RunningAppDescriptor? {
        guard let app = NSRunningApplication(processIdentifier: pid) else {
            return nil
        }

        return RunningAppDescriptor(
            name: app.localizedName ?? "Unknown",
            bundleIdentifier: app.bundleIdentifier,
            pid: app.processIdentifier,
            runningApplication: app
        )
    }

    // MARK: - Resolution

    /// Resolves a CGWindowID against the current window list and running
    /// apps: stale-window, dead-process, deny-list, and AX-identity checks
    /// in the official order. A CG entry that lingers in the full window
    /// list without a live accessibility identity (closed window) is
    /// reported with the same official stale error as an absent id.
    static func resolve(
        id: CGWindowID,
        entries: [CGWindowEntry]? = nil,
        runningApps: [RunningAppDescriptor]? = nil,
        identity: WindowIdentityCheck? = nil
    ) throws -> ResolvedWindow {
        let windowList = entries ?? listEntries(onScreenOnly: false)
        guard let entry = windowList.first(where: { $0.windowID == id }) else {
            throw ComputerUseError.message(
                "staleWindowHandle(\(id)): the window is no longer open; re-observe with list_windows."
            )
        }

        let descriptor: RunningAppDescriptor?
        if let runningApps {
            descriptor = runningApps.first { $0.pid == entry.ownerPID }
        } else {
            descriptor = runningApp(for: entry.ownerPID)
        }
        guard let descriptor else {
            throw ComputerUseError.message(
                "staleWindowHandle(\(id)): the owning process is no longer running; re-observe with list_windows."
            )
        }

        if let bundleIdentifier = descriptor.bundleIdentifier, AppSafetyPolicy.isBlocked(bundleIdentifier: bundleIdentifier) {
            throw AppSafetyPolicy.permissionDenied(bundleIdentifier: bundleIdentifier)
        }

        // The deny list runs before the AX check so a blocked app's
        // accessibility tree is never queried. `.absent` proves the CG entry
        // is a lingering closed/helper window; `.indeterminate` never
        // rejects (an unverifiable window is treated as live, per the
        // documented guarantee — no alpha/bounds/layer heuristics).
        let check = identity ?? WindowDirectory.windowIdentity
        if check(entry.ownerPID, id) == .absent {
            throw ComputerUseError.message(
                "staleWindowHandle(\(id)): the window is no longer open; re-observe with list_windows."
            )
        }

        return ResolvedWindow(
            ref: WindowRef(
                app: descriptor.name,
                id: id,
                title: entry.title?.isEmpty == false ? entry.title : nil
            ),
            pid: descriptor.pid,
            bundleIdentifier: descriptor.bundleIdentifier,
            runningApplication: descriptor.runningApplication,
            entry: entry
        )
    }

    // MARK: - Activation

    /// Brings one window to the foreground using NSRunningApplication and
    /// the accessibility API (unminimize, AXRaise, AXMain/AXFocused) — no
    /// AppleScript or shell. Bounded retries; the outcome is verified
    /// against the frontmost app and the app's focused window.
    static func activate(
        _ resolved: ResolvedWindow,
        axWindow: AXUIElement? = nil,
        attempts: Int = 3,
        delay: TimeInterval = 0.25
    ) throws -> WindowRef {
        var appElement: AXUIElement?
        var targetWindow = axWindow

        for attempt in 0..<max(attempts, 1) {
            if resolved.entry.bounds.height > 0 {
                unminimizeIfNeeded(resolved.pid, windowID: resolved.ref.id)
            }

            resolved.runningApplication.activate(options: [])

            if targetWindow == nil {
                appElement = AXUIElementCreateApplication(resolved.pid)
                switch matchAXWindow(
                    appElement: appElement!,
                    entry: WindowDirectory.currentEntry(for: resolved.ref.id) ?? resolved.entry
                ) {
                case .matched(let window):
                    targetWindow = window
                case .ambiguous(let count):
                    // Raising an arbitrary same-bounds window would focus
                    // the wrong one; fail explicitly instead.
                    throw ComputerUseError.message(
                        ambiguousWindowMessage(id: resolved.ref.id, app: resolved.ref.app, count: count)
                    )
                case .notFound:
                    break
                }
            }

            if let window = targetWindow {
                raiseAXWindow(window)
                _ = setAXBoolAttribute(window, kAXMainAttribute as String, true)
                _ = setAXBoolAttribute(window, kAXFocusedAttribute as String, true)
            }

            if isFrontmost(resolved.pid, focusedWindow: targetWindow) {
                return resolved.ref
            }

            if attempt < max(attempts, 1) - 1 {
                Thread.sleep(forTimeInterval: delay)
            }
        }

        throw ComputerUseError.message(
            "activateWindow(\(resolved.ref.id)): the window could not be brought to the foreground; re-observe with list_windows."
        )
    }

    static func currentEntry(for id: CGWindowID) -> CGWindowEntry? {
        listEntries(onScreenOnly: false).first(where: { $0.windowID == id })
    }

    /// AX-identity liveness check for one CGWindowID: reads the owning app's
    /// accessibility window list and maps every window to its backing
    /// CGWindowID. `.confirmed` on an exact match, `.absent` when the list
    /// was read completely and nothing matched (the window is closed and
    /// only its CG entry lingers), `.indeterminate` whenever any step of the
    /// check is unavailable (missing private symbol, no or empty AX window
    /// list, a window that cannot be mapped, or a window that is not on the
    /// current Space) — indeterminate never rejects.
    static func windowIdentity(pid: pid_t, windowID: CGWindowID) -> WindowIdentity {
        guard AXWindowIdentitySPI.shared.isAvailable else {
            return .indeterminate
        }

        let appElement = AXUIElementCreateApplication(pid)
        guard let windows = axArray(of: appElement, attribute: kAXWindowsAttribute as String) else {
            return .indeterminate
        }

        var mappedIDs: [CGWindowID?] = []
        for element in windows {
            // Real windows carry CGWindowIDs; skip anything provably not a
            // window, but attempt the mapping when the role is unreadable so
            // an unknown element can never cause a false rejection.
            if let role = axString(of: element, attribute: kAXRoleAttribute as String),
               role != kAXWindowRole as String
            {
                continue
            }

            mappedIDs.append(AXWindowIdentitySPI.shared.windowID(for: element))
        }

        // `kAXWindowsAttribute` only lists windows on the current Space, so a
        // window that is not on-screen here cannot be proven closed.
        let isOnCurrentSpace = listEntries(onScreenOnly: true).contains(where: { $0.windowID == windowID })
        return identityVerdict(mappedIDs: mappedIDs, windowID: windowID, isOnCurrentSpace: isOnCurrentSpace)
    }

    /// Pure verdict for the AX-identity check. `mappedIDs` holds the mapped
    /// CGWindowID of every AX window element (`nil` = could not be mapped).
    /// `.absent` is only returned when the window is provably gone: the AX
    /// list is non-empty and fully mapped, nothing matches, and the CG entry
    /// is on the current Space (other Spaces and full-screen Spaces are
    /// invisible to `kAXWindowsAttribute`).
    static func identityVerdict(mappedIDs: [CGWindowID?], windowID: CGWindowID, isOnCurrentSpace: Bool) -> WindowIdentity {
        if mappedIDs.contains(where: { $0 == windowID }) {
            return .confirmed
        }

        guard !mappedIDs.isEmpty, !mappedIDs.contains(where: { $0 == nil }), isOnCurrentSpace else {
            return .indeterminate
        }

        return .absent
    }

    /// Success requires both that the owning app is frontmost and that the
    /// target window is the app's focused (or main) window. Without a
    /// resolved AX target window there is nothing to verify, so it fails.
    static func isFrontmost(_ pid: pid_t, focusedWindow: AXUIElement?) -> Bool {
        guard NSWorkspace.shared.frontmostApplication?.processIdentifier == pid,
              let focusedWindow
        else {
            return false
        }

        let appElement = AXUIElementCreateApplication(pid)
        for attribute in [kAXFocusedWindowAttribute as String, kAXMainWindowAttribute as String] {
            if let candidate = axElement(of: appElement, attribute: attribute), CFEqual(candidate, focusedWindow) {
                return true
            }
        }

        return false
    }

    private static func unminimizeIfNeeded(_ pid: pid_t, windowID: CGWindowID) {
        let appElement = AXUIElementCreateApplication(pid)
        // No unminimize on an ambiguous or missing match: the activation
        // path reports the ambiguity explicitly instead.
        guard case .matched(let window) = matchAXWindow(appElement: appElement, entry: currentEntry(for: windowID) ?? CGWindowEntry(windowID: windowID, ownerPID: pid, layer: 0, bounds: .zero, title: nil, frontToBackIndex: 0)) else {
            return
        }

        guard axBool(of: window, attribute: kAXMinimizedAttribute as String) == true else {
            return
        }

        _ = AXUIElementSetAttributeValue(window, kAXMinimizedAttribute as CFString, kCFBooleanFalse)
    }

    private static func raiseAXWindow(_ window: AXUIElement) {
        guard axActions(of: window).contains(where: { $0.caseInsensitiveCompare(kAXRaiseAction as String) == .orderedSame }) else {
            return
        }

        _ = AXUIElementPerformAction(window, kAXRaiseAction as CFString)
    }

    private static func setAXBoolAttribute(_ element: AXUIElement, _ attribute: String, _ value: Bool) -> Bool {
        AXUIElementSetAttributeValue(element, attribute as CFString, value as CFBoolean) == .success
    }

    // MARK: - Accessibility window matching

    /// Result of matching the app's accessibility windows to one CGWindowID.
    /// `.ambiguous` is only reachable when the identity mapping is
    /// unavailable or incomplete: several windows share the target's bounds
    /// and no tie-breaker identifies one, so the caller must surface an
    /// explicit error instead of guessing (a wrong guess would pair window
    /// A's capture with window B's accessibility tree).
    enum AXWindowMatch {
        case matched(AXUIElement)
        case ambiguous(count: Int)
        case notFound
    }

    /// Matches the app's accessibility windows to a CGWindowID. Identity
    /// first: when the private AX→CGWindowID mapping is available and any
    /// candidate maps to exactly the entry's id, that window is the match
    /// regardless of geometry (same-bounds windows are unambiguous).
    /// Otherwise frame geometry with a unique-title tie-breaker; several
    /// same-bounds candidates with no unique tie-break report
    /// `.ambiguous` rather than returning the first candidate.
    static func matchAXWindow(appElement: AXUIElement, entry: CGWindowEntry) -> AXWindowMatch {
        guard let windows = axArray(of: appElement, attribute: kAXWindowsAttribute as String) else {
            return .notFound
        }

        let candidates: [(element: AXUIElement, frame: CGRect, title: String?)] = windows.compactMap { element in
            guard
                axString(of: element, attribute: kAXRoleAttribute as String) == kAXWindowRole as String,
                let frame = axFrame(of: element)
            else {
                return nil
            }

            return (
                element: element,
                frame: frame,
                title: axString(of: element, attribute: kAXTitleAttribute as String)
            )
        }

        // Exact identity: the private mapping resolves the window by its
        // CGWindowID, independent of bounds (windows at identical bounds are
        // common — e.g. a window opened at the previous window's frame).
        if AXWindowIdentitySPI.shared.isAvailable {
            var allMapped = true
            for candidate in candidates {
                guard let mapped = AXWindowIdentitySPI.shared.windowID(for: candidate.element) else {
                    allMapped = false
                    break
                }
                if mapped == entry.windowID {
                    return .matched(candidate.element)
                }
            }
            if allMapped {
                // Every accessibility window was mapped and none is this
                // id: the window is not among the app's live windows (a
                // closed window whose CG entry lingers).
                return .notFound
            }
            // At least one candidate could not be mapped: fall back to
            // frame geometry below rather than concluding absence.
        }

        let target = entry.bounds
        let tolerance: CGFloat = 2
        func frameMatches(_ frame: CGRect) -> Bool {
            abs(frame.minX - target.minX) <= tolerance
                && abs(frame.minY - target.minY) <= tolerance
                && abs(frame.width - target.width) <= tolerance
                && abs(frame.height - target.height) <= tolerance
        }

        let frameMatches = candidates.filter { frameMatches($0.frame) }
        if !frameMatches.isEmpty {
            if frameMatches.count == 1 {
                return .matched(frameMatches[0].element)
            }
            // Several same-bounds windows: only a unique title match is
            // positive evidence. Focus is not (it says which window is
            // focused, not which one is the target).
            if let title = entry.title, !title.isEmpty {
                let titled = frameMatches.filter { $0.title == title }
                if titled.count == 1 {
                    return .matched(titled[0].element)
                }
            }
            return .ambiguous(count: frameMatches.count)
        }

        // Last resort: a uniquely titled window (never titles alone when
        // several candidates share the title).
        guard let title = entry.title, !title.isEmpty else {
            return .notFound
        }

        let titleMatches = candidates.filter { $0.title == title }
        return titleMatches.count == 1 ? .matched(titleMatches.first!.element) : .notFound
    }

    /// Explicit error for same-bounds ambiguity: the capture is per
    /// CGWindowID (correct) but the accessibility tree could not be matched
    /// to the same window, so the pair must not be served.
    static func ambiguousWindowMessage(id: CGWindowID, app: String, count: Int) -> String {
        "ambiguousWindow(\(id)): \(count) windows of \(app) share the same bounds; the accessibility tree cannot be matched to this window. Move or resize one of them and re-observe with list_windows."
    }

    // MARK: - Capture

    /// Captures one specific window by CGWindowID through the shared
    /// ScreenCaptureKit path (no second screenshot implementation).
    static func capture(entry: CGWindowEntry) -> WindowCapture {
        WindowCapture.capture(windowID: entry.windowID, bounds: entry.bounds, layer: entry.layer)
    }

    // MARK: - App name

    private static func ownerName(for pid: pid_t) -> String {
        if let running = NSRunningApplication(processIdentifier: pid),
           let name = running.localizedName, !name.isEmpty
        {
            return name
        }

        return "Unknown"
    }

    // MARK: - AX helpers (local to this file; the snapshot layer keeps its own)

    private static func axUIElement(from value: CFTypeRef) -> AXUIElement? {
        guard CFGetTypeID(value) == AXUIElementGetTypeID() else {
            return nil
        }

        return (value as! AXUIElement)
    }

    private static func axValue(from value: CFTypeRef) -> AXValue? {
        guard CFGetTypeID(value) == AXValueGetTypeID() else {
            return nil
        }

        return (value as! AXValue)
    }

    private static func axElement(of element: AXUIElement, attribute: String) -> AXUIElement? {
        var value: CFTypeRef?
        guard AXUIElementCopyAttributeValue(element, attribute as CFString, &value) == .success, let value else {
            return nil
        }
        return axUIElement(from: value)
    }

    private static func axArray(of element: AXUIElement, attribute: String) -> [AXUIElement]? {
        var value: CFTypeRef?
        guard AXUIElementCopyAttributeValue(element, attribute as CFString, &value) == .success, let value else {
            return nil
        }
        return value as? [AXUIElement]
    }

    private static func axString(of element: AXUIElement, attribute: String) -> String? {
        var value: CFTypeRef?
        guard AXUIElementCopyAttributeValue(element, attribute as CFString, &value) == .success, let value else {
            return nil
        }
        return value as? String
    }

    private static func axBool(of element: AXUIElement, attribute: String) -> Bool? {
        var value: CFTypeRef?
        guard AXUIElementCopyAttributeValue(element, attribute as CFString, &value) == .success, let value else {
            return nil
        }
        return (value as? NSNumber)?.boolValue
    }

    private static func axActions(of element: AXUIElement) -> [String] {
        var actions: CFArray?
        guard AXUIElementCopyActionNames(element, &actions) == .success else {
            return []
        }
        return actions as? [String] ?? []
    }

    private static func axFrame(of element: AXUIElement) -> CGRect? {
        var positionValue: CFTypeRef?
        var sizeValue: CFTypeRef?
        guard
            AXUIElementCopyAttributeValue(element, kAXPositionAttribute as CFString, &positionValue) == .success,
            AXUIElementCopyAttributeValue(element, kAXSizeAttribute as CFString, &sizeValue) == .success,
            let positionValue,
            let sizeValue,
            let positionAXValue = axValue(from: positionValue),
            let sizeAXValue = axValue(from: sizeValue)
        else {
            return nil
        }

        var position = CGPoint.zero
        var size = CGSize.zero
        guard AXValueGetValue(positionAXValue, .cgPoint, &position), AXValueGetValue(sizeAXValue, .cgSize, &size) else {
            return nil
        }

        return CGRect(origin: position, size: size)
    }
}
