import AppKit
import CoreGraphics
import Foundation
import ScreenCaptureKit

/// Stable machine-readable codes for the display-level capture tool. The
/// service renders them as JSON `{"code","message"}` text results with
/// `isError: true`; no image accompanies an error.
public enum DisplayCaptureErrorCode: String {
    case permissionMissing = "permission_missing"
    case displayNotFound = "display_not_found"
    case displayChanged = "display_changed"
    case captureTimeout = "capture_timeout"
    case captureFailed = "capture_failed"
    case imageTooLarge = "image_too_large"
}

public struct DisplayCaptureError: Error {
    public let code: DisplayCaptureErrorCode
    public let message: String
    public let availableDisplays: [AvailableDisplay]

    init(
        code: DisplayCaptureErrorCode,
        message: String,
        availableDisplays: [AvailableDisplay] = []
    ) {
        self.code = code
        self.message = message
        self.availableDisplays = availableDisplays
    }
}

public struct AvailableDisplay: Equatable {
    public let displayID: CGDirectDisplayID
    /// Quartz global display space, points, top-left origin.
    public let bounds: CGRect
}

extension AvailableDisplay {
    init(scDisplay: SCDisplay) {
        self.init(displayID: scDisplay.displayID, bounds: CGDisplayBounds(scDisplay.displayID))
    }
}

/// One full-display capture: a full-resolution PNG plus the geometry needed
/// to map PNG pixels back to Quartz global display points.
public struct CapturedDisplay {
    public let pngData: Data
    public let displayID: CGDirectDisplayID
    /// Quartz global display bounds in points (top-left origin).
    public let bounds: CGRect
    /// Actual encoded PNG dimensions in pixels.
    public let imageSize: CGSize
    /// Every shareable display at capture time, for callers that need to
    /// pick another display id.
    public let availableDisplays: [AvailableDisplay]
    public let capturedAt: Date
}

/// Scale/offset pair mapping final PNG pixels directly to Quartz global
/// display points: `x = offset.x + u * scale.width`,
/// `y = offset.y + v * scale.height`. The image origin is top-left and y
/// increases downward; pixel centers sit at `(u + 0.5, v + 0.5)`.
struct DisplayCoordinateMap: Equatable {
    let scale: CGSize
    let offset: CGPoint

    static func make(nativeBounds: CGRect, imageSize: CGSize) -> DisplayCoordinateMap? {
        guard imageSize.width > 0, imageSize.height > 0 else {
            return nil
        }

        return DisplayCoordinateMap(
            scale: CGSize(
                width: nativeBounds.width / imageSize.width,
                height: nativeBounds.height / imageSize.height
            ),
            offset: CGPoint(x: nativeBounds.minX, y: nativeBounds.minY)
        )
    }

    func nativePoint(forImagePoint point: CGPoint) -> CGPoint {
        CGPoint(
            x: offset.x + point.x * scale.width,
            y: offset.y + point.y * scale.height
        )
    }
}

/// Side-effect-free full-display capture. One `SCScreenshotManager` shot per
/// call — no `SCStream`, no window visibility changes, no activation, no
/// permission prompts.
public enum DisplayCapture {
    // Hard ceilings for pathological surfaces/encodes. Exceedance is an
    // explicit `image_too_large` error, never a cropped or silently resized
    // image.
    static let maxImagePixels = 64_000_000
    static let maxPNGBytes = 64 * 1024 * 1024

    public static func capture(displayID: CGDirectDisplayID?) throws -> CapturedDisplay {
        // Non-prompting preflight: fail before touching ScreenCaptureKit
        // instead of triggering any permission UI.
        guard CGPreflightScreenCaptureAccess() else {
            throw DisplayCaptureError(
                code: .permissionMissing,
                message: "Screen Recording permission is missing for this executable; enable it in System Settings > Privacy & Security > Screen Recording."
            )
        }

        let requestedID = displayID ?? CGMainDisplayID()

        do {
            return try BlockingAsyncBridge.run(timeout: screenshotCaptureTimeout) {
                try await captureOneDisplay(displayID: requestedID, wasExplicit: displayID != nil)
            }
        } catch let error as DisplayCaptureError {
            throw error
        } catch let error as ComputerUseError {
            if case let .message(text) = error, text.hasPrefix("ScreenCaptureKit screenshot task timed out") {
                throw DisplayCaptureError(
                    code: .captureTimeout,
                    message: "Display capture timed out after \(Int(screenshotCaptureTimeout)) seconds."
                )
            }

            throw DisplayCaptureError(code: .captureFailed, message: error.errorDescription ?? String(describing: error))
        }
    }

    private static func captureOneDisplay(
        displayID: CGDirectDisplayID,
        wasExplicit: Bool
    ) async throws -> CapturedDisplay {
        let shareableContent: SCShareableContent
        do {
            shareableContent = try await SCShareableContent.current
        } catch {
            throw mappedCaptureError(error, context: "enumerating shareable content")
        }

        let displays = shareableContent.displays
        let availableDisplays = displays.map(AvailableDisplay.init(scDisplay:))

        guard let selected = displays.first(where: { $0.displayID == displayID }) else {
            let target = wasExplicit ? "display \(displayID)" : "the main display (id \(displayID))"
            throw DisplayCaptureError(
                code: .displayNotFound,
                message: "\(target) is not available; available displays: \(availableDisplaysDescription(availableDisplays))",
                availableDisplays: availableDisplays
            )
        }

        let preCaptureBounds = CGDisplayBounds(displayID)
        guard !preCaptureBounds.isNull else {
            throw DisplayCaptureError(
                code: .displayNotFound,
                message: "display \(displayID) has no valid Quartz bounds; available displays: \(availableDisplaysDescription(availableDisplays))",
                availableDisplays: availableDisplays
            )
        }

        let filter = SCContentFilter(display: selected, excludingWindows: [])
        let contentRect = filter.contentRect
        let pixelScale = CGFloat(filter.pointPixelScale)
        let pixelWidth = Int((contentRect.width * pixelScale).rounded())
        let pixelHeight = Int((contentRect.height * pixelScale).rounded())

        guard pixelWidth > 0, pixelHeight > 0 else {
            throw DisplayCaptureError(
                code: .captureFailed,
                message: "Display \(displayID) resolved to an empty capture surface (\(pixelWidth)x\(pixelHeight) pixels)."
            )
        }

        guard pixelWidth * pixelHeight <= maxImagePixels else {
            throw DisplayCaptureError(
                code: .imageTooLarge,
                message: "Display \(displayID) capture surface is \(pixelWidth)x\(pixelHeight) pixels (\(pixelWidth * pixelHeight) total), exceeding the \(maxImagePixels) pixel limit."
            )
        }

        let configuration = SCStreamConfiguration()
        // No explicit sourceRect: the default already spans the full display
        // content rect in content-local space (contentRect is expressed in
        // global Quartz coordinates, which would shift captures on
        // negative-origin secondary displays).
        configuration.width = pixelWidth
        configuration.height = pixelHeight
        configuration.showsCursor = false
        configuration.capturesAudio = false
        configuration.scalesToFit = false
        // Menu bar: macOS SDK headers (SCStream.h, macOS 14.2+) document that
        // display-excluding content filters default includeMenuBar to YES, so
        // the menu bar is captured without touching a macOS 15-only property.

        let image: CGImage
        do {
            image = try await SCScreenshotManager.captureImage(contentFilter: filter, configuration: configuration)
        } catch {
            throw mappedCaptureError(error, context: "capturing display \(displayID)")
        }

        // Re-read geometry after the shot: a disconnect, rotation, or mode
        // change invalidates the coordinate map, so refuse instead of
        // attaching stale coordinates to a fresh image.
        let postCaptureBounds = CGDisplayBounds(displayID)
        guard !postCaptureBounds.isNull, postCaptureBounds == preCaptureBounds else {
            throw DisplayCaptureError(
                code: .displayChanged,
                message: "Display \(displayID) geometry changed during capture (before \(boundsDescription(preCaptureBounds)), after \(boundsDescription(postCaptureBounds))); request a new capture.",
                availableDisplays: availableDisplays
            )
        }

        guard image.width > 0, image.height > 0 else {
            throw DisplayCaptureError(
                code: .captureFailed,
                message: "ScreenCaptureKit returned an empty image for display \(displayID)."
            )
        }

        guard let pngData = pngData(for: image) else {
            throw DisplayCaptureError(
                code: .captureFailed,
                message: "Failed to encode the display \(displayID) capture as PNG."
            )
        }

        guard pngData.count <= maxPNGBytes else {
            throw DisplayCaptureError(
                code: .imageTooLarge,
                message: "Display \(displayID) PNG is \(pngData.count) bytes, exceeding the \(maxPNGBytes) byte limit."
            )
        }

        let imageSize = pngPixelSize(of: pngData) ?? CGSize(width: image.width, height: image.height)

        return CapturedDisplay(
            pngData: pngData,
            displayID: displayID,
            bounds: preCaptureBounds,
            imageSize: imageSize,
            availableDisplays: availableDisplays,
            capturedAt: Date()
        )
    }

    private static func mappedCaptureError(_ error: Error, context: String) -> DisplayCaptureError {
        let nsError = error as NSError
        return DisplayCaptureError(
            code: .captureFailed,
            message: "ScreenCaptureKit failed while \(context): \(nsError.domain) \(nsError.code) (\(nsError.localizedDescription))"
        )
    }

    private static func availableDisplaysDescription(_ displays: [AvailableDisplay]) -> String {
        guard !displays.isEmpty else {
            return "none"
        }

        return displays.map { display in
            "id \(display.displayID) at (\(Int(display.bounds.minX)), \(Int(display.bounds.minY))) \(Int(display.bounds.width))x\(Int(display.bounds.height))"
        }.joined(separator: ", ")
    }

    private static func boundsDescription(_ bounds: CGRect) -> String {
        "(\(Int(bounds.minX)), \(Int(bounds.minY))) \(Int(bounds.width))x\(Int(bounds.height))"
    }
}
