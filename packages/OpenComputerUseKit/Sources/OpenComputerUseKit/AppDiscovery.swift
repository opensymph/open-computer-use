import AppKit
import CoreGraphics
import CoreServices
import Foundation

public struct RunningAppDescriptor {
    public let name: String
    public let bundleIdentifier: String?
    public let pid: pid_t
    public let runningApplication: NSRunningApplication
}

public struct AppLaunchWindow {
    public let id: CGWindowID
    public let title: String?
}

public struct AppLaunchResult {
    public let pid: pid_t
    public let name: String
    public let bundleIdentifier: String?
    public let windows: [AppLaunchWindow]
}

struct ListedAppDescriptor {
    let name: String
    let bundleIdentifier: String
    let isRunning: Bool
    let isFrontmost: Bool
    let lastUsed: Date?
    let uses: Int?

    var renderedLine: String {
        var markers: [String] = []
        if isFrontmost {
            markers.append("frontmost")
        }
        if isRunning {
            markers.append("running")
        }
        if let lastUsed {
            markers.append("last-used=\(AppDiscovery.usageDateFormatter.string(from: lastUsed))")
        }
        if let uses {
            markers.append("uses=\(uses)")
        }

        return "\(name) — \(bundleIdentifier) [\(markers.joined(separator: ", "))]"
    }
}

private struct SpotlightAppRecord {
    let name: String
    let bundleIdentifier: String
    let lastUsed: Date?
    let uses: Int?
}

private struct ResolvedAppInfo {
    let bundleIdentifier: String
    let name: String
}

enum AppDiscovery {
    private static let listAppsQuery = #"kMDItemContentType == "com.apple.application-bundle" && kMDItemFSName == "*.app""#
    private static let lastUsedDateRankingAttribute = "kMDItemLastUsedDate_Ranking"
    private static let useCountAttribute = "kMDItemUseCount"
    private static let maxRecentNonRunningApps = 10
    private static let fixtureListBundleIdentifier = "dev.opencodex.opencomputeruse.fixture"
    private static let standardApplicationSearchRoots: [URL] = [
        URL(fileURLWithPath: "/Applications", isDirectory: true),
        URL(fileURLWithPath: "/System/Applications", isDirectory: true),
        URL(fileURLWithPath: "/System/Library/CoreServices", isDirectory: true),
        FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Applications", isDirectory: true),
    ]

    static let usageDateFormatter: DateFormatter = {
        let formatter = DateFormatter()
        formatter.calendar = Calendar(identifier: .gregorian)
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.dateFormat = "yyyy-MM-dd"
        return formatter
    }()

    static func listCatalog() -> [ListedAppDescriptor] {
        let running = userFacingRunningApps()
        let frontmostBundleIdentifier = NSWorkspace.shared.frontmostApplication?.bundleIdentifier?.lowercased()
        let runningByBundle = running.reduce(into: [String: RunningAppDescriptor]()) { result, descriptor in
            guard let bundleIdentifier = listedBundleIdentifier(for: descriptor) else {
                return
            }

            let key = bundleIdentifier.lowercased()
            if result[key] == nil {
                result[key] = descriptor
            }
        }

        var entriesByBundle: [String: ListedAppDescriptor] = [:]

        for record in SpotlightAppIndex.recentApps(cutoffDate: recentUsageCutoff()) {
            let key = record.bundleIdentifier.lowercased()
            let runningDescriptor = runningByBundle[key]
            entriesByBundle[key] = ListedAppDescriptor(
                name: runningDescriptor?.name ?? record.name,
                bundleIdentifier: record.bundleIdentifier,
                isRunning: runningDescriptor != nil,
                isFrontmost: key == frontmostBundleIdentifier,
                lastUsed: record.lastUsed,
                uses: record.uses
            )
        }

        for descriptor in running {
            guard let bundleIdentifier = listedBundleIdentifier(for: descriptor) else {
                continue
            }

            let key = bundleIdentifier.lowercased()
            let existing = entriesByBundle[key]
            entriesByBundle[key] = ListedAppDescriptor(
                name: descriptor.name,
                bundleIdentifier: bundleIdentifier,
                isRunning: true,
                isFrontmost: key == frontmostBundleIdentifier,
                lastUsed: existing?.lastUsed,
                uses: existing?.uses
            )
        }

        let sorted = entriesByBundle.values.sorted(by: compareListedApps)
        let runningEntries = sorted.filter(\.isRunning)
        let recentEntries = sorted.filter { !$0.isRunning }.prefix(maxRecentNonRunningApps)
        return runningEntries + recentEntries
    }

    static func runningApps() -> [RunningAppDescriptor] {
        NSWorkspace.shared.runningApplications
            .filter { !$0.isTerminated }
            .sorted { lhs, rhs in
                if lhs.isActive != rhs.isActive {
                    return lhs.isActive && !rhs.isActive
                }

                return appName(lhs).localizedCaseInsensitiveCompare(appName(rhs)) == .orderedAscending
            }
            .map { app in
                RunningAppDescriptor(
                    name: appName(app),
                    bundleIdentifier: app.bundleIdentifier,
                    pid: app.processIdentifier,
                    runningApplication: app
                )
            }
    }

    static func resolve(_ query: String) throws -> RunningAppDescriptor {
        let normalizedQuery = query.trimmingCharacters(in: .whitespacesAndNewlines)
        let running = runningApps()

        if let bundleIdentifier = blockedBundleIdentifier(forQuery: normalizedQuery) {
            throw AppSafetyPolicy.permissionDenied(bundleIdentifier: bundleIdentifier)
        }

        if let match = resolvedRunningApp(in: running, matching: normalizedQuery) {
            return match
        }

        try launchIfPossible(normalizedQuery)

        for _ in 0..<20 {
            if let launched = resolvedRunningApp(in: runningApps(), matching: normalizedQuery) {
                return launched
            }

            Thread.sleep(forTimeInterval: 0.25)
        }

        throw ComputerUseError.appNotFound(normalizedQuery)
    }

    // launch_app: launch through NSWorkspace/LaunchServices (never AppleScript),
    // reuse a running instance instead of spawning duplicates, and keep the
    // launch in the background so the user's foreground focus is preserved.
    static func launch(_ query: String) throws -> AppLaunchResult {
        let normalizedQuery = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !normalizedQuery.isEmpty else {
            throw ComputerUseError.invalidArguments("app must be a non-empty app name or bundle identifier")
        }

        if isBundleIdentifierQuery(normalizedQuery), AppSafetyPolicy.isBlocked(bundleIdentifier: normalizedQuery) {
            throw AppSafetyPolicy.permissionDenied(bundleIdentifier: normalizedQuery)
        }

        if let running = resolvedRunningApp(in: runningApps(), matching: normalizedQuery) {
            // Defense in depth: re-check the resolved instance's real bundle id.
            if AppSafetyPolicy.isBlocked(bundleIdentifier: running.bundleIdentifier) {
                throw AppSafetyPolicy.permissionDenied(bundleIdentifier: running.bundleIdentifier ?? normalizedQuery)
            }
            // Re-open through LaunchServices so a running app with no windows
            // receives a reopen event (like `open -a`), still without activating it.
            if let bundleURL = running.runningApplication.bundleURL {
                try openApplication(at: bundleURL, activates: false, name: normalizedQuery)
                waitForFirstWindow(pid: running.pid)
            }
            return launchResult(for: running)
        }

        let appURL: URL
        if normalizedQuery.contains("/") {
            appURL = try validatedApplicationURL(forPath: normalizedQuery)
        } else if isBundleIdentifierQuery(normalizedQuery),
                  let resolvedURL = NSWorkspace.shared.urlForApplication(withBundleIdentifier: normalizedQuery) {
            appURL = resolvedURL
        } else if let resolvedURL = applicationURL(named: normalizedQuery) {
            appURL = resolvedURL
        } else {
            throw ComputerUseError.appNotFound(normalizedQuery)
        }

        if AppSafetyPolicy.isBlocked(bundleIdentifier: Bundle(url: appURL)?.bundleIdentifier) {
            throw AppSafetyPolicy.permissionDenied(
                bundleIdentifier: Bundle(url: appURL)?.bundleIdentifier ?? normalizedQuery
            )
        }

        let launchedApplication = try openApplication(at: appURL, activates: false, name: normalizedQuery)

        guard let application = launchedApplication else {
            throw ComputerUseError.message(
                "launchFailed(\"\(normalizedQuery)\"): LaunchServices returned no application"
            )
        }

        let descriptor = RunningAppDescriptor(
            name: appName(application),
            bundleIdentifier: application.bundleIdentifier,
            pid: application.processIdentifier,
            runningApplication: application
        )

        waitForFirstWindow(pid: descriptor.pid)

        return launchResult(for: descriptor)
    }

    // Explicit paths must be an absolute, existing `.app` application bundle.
    // `~` is expanded and symlinks are resolved so the deny-list check that
    // follows sees the real bundle.
    private static func validatedApplicationURL(forPath path: String) throws -> URL {
        let expandedPath = (path as NSString).expandingTildeInPath
        guard expandedPath.hasPrefix("/") else {
            throw ComputerUseError.invalidArguments("app path must be an absolute path to an .app bundle")
        }

        let url = URL(fileURLWithPath: expandedPath).standardizedFileURL.resolvingSymlinksInPath()
        guard FileManager.default.fileExists(atPath: url.path) else {
            throw ComputerUseError.appNotFound(path)
        }

        let isApplication = (try? url.resourceValues(forKeys: [.isApplicationKey]))?.isApplication ?? false
        guard url.pathExtension.caseInsensitiveCompare("app") == .orderedSame, isApplication else {
            throw ComputerUseError.invalidArguments("app path must point to an .app application bundle")
        }

        return url
    }

    // Give the app a brief moment to create its first window so the response
    // can report available windows; windowless apps are still a successful
    // launch (the windows list simply stays empty).
    private static func waitForFirstWindow(pid: pid_t) {
        let windowDeadline = Date(timeIntervalSinceNow: 2.5)
        while windows(for: pid).isEmpty, Date() < windowDeadline {
            pause(0.1)
        }
    }

    private static func launchResult(for descriptor: RunningAppDescriptor) -> AppLaunchResult {
        AppLaunchResult(
            pid: descriptor.pid,
            name: descriptor.name,
            bundleIdentifier: descriptor.bundleIdentifier,
            windows: windows(for: descriptor.pid)
        )
    }

    static func windows(for pid: pid_t) -> [AppLaunchWindow] {
        guard let infoList = CGWindowListCopyWindowInfo([], kCGNullWindowID) as? [[String: Any]] else {
            return []
        }

        return infoList.compactMap { info in
            guard
                let ownerPID = info[kCGWindowOwnerPID as String] as? pid_t,
                ownerPID == pid,
                let layer = info[kCGWindowLayer as String] as? Int,
                layer == 0,
                let number = info[kCGWindowNumber as String] as? NSNumber,
                let bounds = info[kCGWindowBounds as String] as? [String: Any],
                (bounds["Width"] as? CGFloat ?? 0) > 0,
                (bounds["Height"] as? CGFloat ?? 0) > 0
            else {
                return nil
            }

            return AppLaunchWindow(
                id: CGWindowID(number.uint32Value),
                title: info[kCGWindowName as String] as? String
            )
        }
    }

    private final class LaunchedApplicationBox: @unchecked Sendable {
        var application: NSRunningApplication?
        var error: Error?
    }

    private static func resolvedRunningApp(in descriptors: [RunningAppDescriptor], matching query: String) -> RunningAppDescriptor? {
        if isBundleIdentifierQuery(query),
           let match = descriptors.first(where: { descriptor in
               descriptor.bundleIdentifier?.caseInsensitiveCompare(query) == .orderedSame
           }) {
            return match
        }

        return descriptors.first(where: { descriptor in
            guard !AppSafetyPolicy.isBlocked(bundleIdentifier: descriptor.bundleIdentifier) else {
                return false
            }

            return descriptor.name.caseInsensitiveCompare(query) == .orderedSame
                || descriptor.runningApplication.executableURL?.deletingPathExtension().lastPathComponent.caseInsensitiveCompare(query) == .orderedSame
        })
    }

    private static func userFacingRunningApps() -> [RunningAppDescriptor] {
        var seen: Set<String> = []
        var descriptors: [RunningAppDescriptor] = []

        for descriptor in runningApps() {
            guard isUserFacingListApp(descriptor.runningApplication) else {
                continue
            }

            guard let bundleIdentifier = listedBundleIdentifier(for: descriptor) else {
                continue
            }

            let key = bundleIdentifier.lowercased()
            guard seen.insert(key).inserted else {
                continue
            }

            descriptors.append(descriptor)
        }

        return descriptors
    }

    private static func listedBundleIdentifier(for descriptor: RunningAppDescriptor) -> String? {
        if let bundleIdentifier = descriptor.bundleIdentifier, !bundleIdentifier.isEmpty {
            return bundleIdentifier
        }

        guard descriptor.name == FixtureBridge.appName else {
            return nil
        }

        return fixtureListBundleIdentifier
    }

    static func compareListedApps(_ lhs: ListedAppDescriptor, _ rhs: ListedAppDescriptor) -> Bool {
        if lhs.isFrontmost != rhs.isFrontmost {
            return lhs.isFrontmost && !rhs.isFrontmost
        }

        if lhs.isRunning != rhs.isRunning {
            return lhs.isRunning && !rhs.isRunning
        }

        let lhsHasUsage = lhs.lastUsed != nil
        let rhsHasUsage = rhs.lastUsed != nil
        if lhsHasUsage != rhsHasUsage {
            return lhsHasUsage && !rhsHasUsage
        }

        let calendar = Calendar(identifier: .gregorian)
        if let lhsLast = lhs.lastUsed, let rhsLast = rhs.lastUsed {
            let lhsDay = calendar.startOfDay(for: lhsLast)
            let rhsDay = calendar.startOfDay(for: rhsLast)
            if lhsDay != rhsDay {
                return lhsDay > rhsDay
            }
        }

        if let lhsUses = lhs.uses, let rhsUses = rhs.uses, lhsUses != rhsUses {
            return lhsUses > rhsUses
        }

        return lhs.name.localizedCaseInsensitiveCompare(rhs.name) == .orderedAscending
    }

    private static func launchIfPossible(_ query: String) throws {
        if isBundleIdentifierQuery(query) {
            guard !AppSafetyPolicy.isBlocked(bundleIdentifier: query) else {
                return
            }

            if let appURL = NSWorkspace.shared.urlForApplication(withBundleIdentifier: query) {
                try openApplication(at: appURL, activates: true, name: query)
            }
            return
        }

        guard let appURL = applicationURL(named: query) else {
            return
        }

        if AppSafetyPolicy.isBlocked(bundleIdentifier: Bundle(url: appURL)?.bundleIdentifier) {
            return
        }

        try openApplication(at: appURL, activates: true, name: query)
    }

    private static func applicationURL(named query: String) -> URL? {
        let targetName = stripAppSuffix(from: query).trimmingCharacters(in: .whitespacesAndNewlines)
        guard !targetName.isEmpty else {
            return nil
        }

        let fileManager = FileManager.default
        let resourceKeys: [URLResourceKey] = [.isApplicationKey, .isDirectoryKey, .nameKey]
        var visitedPaths: Set<String> = []

        for root in standardApplicationSearchRoots where fileManager.fileExists(atPath: root.path) {
            guard let enumerator = fileManager.enumerator(
                at: root,
                includingPropertiesForKeys: resourceKeys,
                options: [.skipsHiddenFiles, .skipsPackageDescendants]
            ) else {
                continue
            }

            for case let candidateURL as URL in enumerator {
                guard candidateURL.pathExtension.caseInsensitiveCompare("app") == .orderedSame else {
                    continue
                }

                let normalizedPath = candidateURL.standardizedFileURL.path.lowercased()
                guard visitedPaths.insert(normalizedPath).inserted else {
                    continue
                }

                let candidateName = stripAppSuffix(from: candidateURL.lastPathComponent)
                if candidateName.caseInsensitiveCompare(targetName) == .orderedSame {
                    return candidateURL
                }
            }
        }

        return nil
    }

    // Single LaunchServices entry point shared by `resolve` and `launch`.
    // LaunchServices reuses a running instance (and delivers a reopen event
    // when it has no windows) instead of spawning a duplicate.
    @discardableResult
    private static func openApplication(at appURL: URL, activates: Bool, name: String) throws -> NSRunningApplication? {
        let configuration = NSWorkspace.OpenConfiguration()
        configuration.activates = activates
        let launchedBox = LaunchedApplicationBox()
        let semaphore = DispatchSemaphore(value: 0)

        NSWorkspace.shared.openApplication(at: appURL, configuration: configuration) { application, error in
            launchedBox.application = application
            launchedBox.error = error
            semaphore.signal()
        }

        guard waitForSignal(semaphore) else {
            throw ComputerUseError.message("launchFailed(\"\(name)\"): timed out waiting for LaunchServices")
        }

        if let launchError = launchedBox.error {
            throw ComputerUseError.message("launchFailed(\"\(name)\"): \(launchError.localizedDescription)")
        }

        return launchedBox.application
    }

    private static let launchServicesTimeout: TimeInterval = 20

    /// Returns false when LaunchServices did not call back before the timeout,
    /// so a stuck launch cannot block the serial MCP server forever.
    @discardableResult
    private static func waitForSignal(_ semaphore: DispatchSemaphore, timeout: TimeInterval = launchServicesTimeout) -> Bool {
        let deadline = Date(timeIntervalSinceNow: timeout)

        if Thread.isMainThread {
            while semaphore.wait(timeout: .now()) == .timedOut {
                if Date() >= deadline {
                    return false
                }
                RunLoop.current.run(mode: .default, before: Date(timeIntervalSinceNow: 0.01))
            }
            return true
        }

        return semaphore.wait(timeout: .now() + timeout) == .success
    }

    private static func pause(_ interval: TimeInterval) {
        if Thread.isMainThread {
            RunLoop.current.run(mode: .default, before: Date(timeIntervalSinceNow: interval))
        } else {
            Thread.sleep(forTimeInterval: interval)
        }
    }

    private static func recentUsageCutoff(referenceDate: Date = Date()) -> Date {
        let calendar = Calendar(identifier: .gregorian)
        let startOfToday = calendar.startOfDay(for: referenceDate)
        return calendar.date(byAdding: .day, value: -13, to: startOfToday) ?? startOfToday
    }

    private static func blockedBundleIdentifier(forQuery query: String) -> String? {
        guard isBundleIdentifierQuery(query), AppSafetyPolicy.isBlocked(bundleIdentifier: query) else {
            return nil
        }

        return query
    }

    private static func isBundleIdentifierQuery(_ query: String) -> Bool {
        guard query.contains(".") else {
            return false
        }

        // "TextEdit.app" and names with spaces are application names, not bundle ids.
        if query.lowercased().hasSuffix(".app") {
            return false
        }

        let allowed = CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-.")
        return query.unicodeScalars.allSatisfy { allowed.contains($0) }
            && !query.hasPrefix(".")
            && !query.hasSuffix(".")
    }

    private static func isUserFacingListApp(_ app: NSRunningApplication) -> Bool {
        if appName(app) == FixtureBridge.appName {
            return true
        }

        return app.activationPolicy == .regular
    }

    private static func bundleDisplayName(_ bundle: Bundle?) -> String? {
        guard let bundle else {
            return nil
        }

        let displayName = bundle.object(forInfoDictionaryKey: "CFBundleDisplayName") as? String
        let bundleName = bundle.object(forInfoDictionaryKey: kCFBundleNameKey as String) as? String
        return displayName ?? bundleName
    }

    private static func stripAppSuffix(from value: String) -> String {
        value.hasSuffix(".app") ? String(value.dropLast(4)) : value
    }

    static func appName(_ app: NSRunningApplication) -> String {
        app.localizedName
            ?? bundleDisplayName(Bundle(url: app.bundleURL ?? URL(fileURLWithPath: "/")))
            ?? app.bundleURL?.deletingPathExtension().lastPathComponent
            ?? app.executableURL?.lastPathComponent
            ?? "pid-\(app.processIdentifier)"
    }

    private enum SpotlightAppIndex {
        static func recentApps(cutoffDate: Date) -> [SpotlightAppRecord] {
            let sortingAttributes = [
                lastUsedDateRankingAttribute as CFString,
                useCountAttribute as CFString,
            ] as CFArray

            guard let query = MDQueryCreate(
                kCFAllocatorDefault,
                listAppsQuery as CFString,
                nil,
                sortingAttributes
            ) else {
                return []
            }

            MDQuerySetSearchScope(query, standardSearchScopes() as CFArray, 0)
            MDQuerySetSortOptionFlagsForAttribute(query, lastUsedDateRankingAttribute as CFString, kMDQueryReverseSortOrderFlag.rawValue)
            MDQuerySetSortOptionFlagsForAttribute(query, useCountAttribute as CFString, kMDQueryReverseSortOrderFlag.rawValue)

            guard MDQueryExecute(query, CFOptionFlags(kMDQuerySynchronous.rawValue)) else {
                return []
            }

            var seen: Set<String> = []
            var records: [SpotlightAppRecord] = []

            for index in 0..<MDQueryGetResultCount(query) {
                guard let rawResult = MDQueryGetResultAtIndex(query, index) else {
                    continue
                }

                let item = unsafeBitCast(rawResult, to: MDItem.self)
                guard
                    let bundleIdentifier = stringAttribute(kMDItemCFBundleIdentifier, item: item),
                    !bundleIdentifier.isEmpty
                else {
                    continue
                }

                let key = bundleIdentifier.lowercased()
                guard seen.insert(key).inserted else {
                    continue
                }

                guard let path = stringAttribute(kMDItemPath, item: item) else {
                    continue
                }

                let appURL = URL(fileURLWithPath: path)
                let bundle = Bundle(url: appURL)
                if bundle?.object(forInfoDictionaryKey: "LSBackgroundOnly") as? Bool == true {
                    continue
                }
                if bundle?.object(forInfoDictionaryKey: "LSUIElement") as? Bool == true {
                    continue
                }

                let lastUsed = dateAttribute(lastUsedDateRankingAttribute as CFString, item: item)
                    ?? dateAttribute(kMDItemLastUsedDate, item: item)
                guard let lastUsed, lastUsed >= cutoffDate else {
                    continue
                }

                let uses = numberAttribute(useCountAttribute as CFString, item: item)?.intValue
                let displayName = bundleDisplayName(bundle)
                    ?? stringAttribute(kMDItemDisplayName, item: item).map(stripAppSuffix(from:))
                    ?? stripAppSuffix(from: appURL.lastPathComponent)

                records.append(
                    SpotlightAppRecord(
                        name: displayName,
                        bundleIdentifier: bundleIdentifier,
                        lastUsed: lastUsed,
                        uses: uses
                    )
                )
            }

            return records
        }

        private static func standardSearchScopes() -> [CFString] {
            var scopes: [String] = [
                "/Applications",
                "/System/Applications",
                "/System/Library/CoreServices",
            ]

            let homeApplications = NSString(string: "~/Applications").expandingTildeInPath
            if FileManager.default.fileExists(atPath: homeApplications) {
                scopes.append(homeApplications)
            }

            return scopes as [CFString]
        }

        private static func stringAttribute(_ name: CFString, item: MDItem) -> String? {
            MDItemCopyAttribute(item, name) as? String
        }

        private static func numberAttribute(_ name: CFString, item: MDItem) -> NSNumber? {
            MDItemCopyAttribute(item, name) as? NSNumber
        }

        private static func dateAttribute(_ name: CFString, item: MDItem) -> Date? {
            MDItemCopyAttribute(item, name) as? Date
        }
    }
}

enum AppSafetyPolicy {
    private static let blockedBundleIdentifiers: Set<String> = [
        "com.1password.1password",
        "com.1password.safari",
        "com.bitwarden.desktop",
        "com.dashlane.dashlanephonefinal",
        "com.lastpass.lastpass",
        "com.nordsec.nordpass",
        "me.proton.pass.electron",
        "me.proton.pass.catalyst",
    ]

    static func isBlocked(bundleIdentifier: String?) -> Bool {
        guard let bundleIdentifier else {
            return false
        }

        return blockedBundleIdentifiers.contains(bundleIdentifier.lowercased())
    }

    static func permissionDenied(bundleIdentifier: String) -> ComputerUseError {
        .permissionDenied("Computer Use is not allowed to use the app '\(bundleIdentifier)' for safety reasons.")
    }
}
