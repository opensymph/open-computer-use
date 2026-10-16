import ApplicationServices
import CoreGraphics
import Foundation

/// Runtime-only bridge for the private accessibility symbol that maps an
/// AXUIElement to the CGWindowID backing it.
///
/// `_AXUIElementGetWindow` is exported by ApplicationServices/HIServices and
/// is the only supported-by-convention mapping from the accessibility
/// window list back to CGWindowIDs; no public API exposes it. Like
/// `SkyLightSPI`, the undocumented ABI is kept in this one file so a future
/// macOS compatibility change has a single review boundary. The symbol is
/// resolved at runtime (dlopen/dlsym): when it is missing the capability
/// degrades to `isAvailable == false` and callers fall back to frame-based
/// matching with explicit ambiguity reporting.
final class AXWindowIdentitySPI: @unchecked Sendable {
    static let shared = AXWindowIdentitySPI()

    private typealias GetWindowFunction = @convention(c) (AXUIElement, UnsafeMutablePointer<CGWindowID>) -> AXError

    private static let frameworkPath = "/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices"
    private static let getWindowSymbol = "_AXUIElementGetWindow"

    private let getWindowFunction: GetWindowFunction?

    let isAvailable: Bool

    private init() {
        let handle = dlopen(Self.frameworkPath, RTLD_LAZY)
        let symbol = handle.flatMap { dlsym($0, Self.getWindowSymbol) }
        getWindowFunction = symbol.map { unsafeBitCast($0, to: GetWindowFunction.self) }
        isAvailable = getWindowFunction != nil
    }

    /// The CGWindowID backing `window`, or nil when the symbol is
    /// unavailable or the mapping fails for this element.
    func windowID(for window: AXUIElement) -> CGWindowID? {
        guard let getWindowFunction else {
            return nil
        }

        var windowID: CGWindowID = 0
        return getWindowFunction(window, &windowID) == .success ? windowID : nil
    }
}
