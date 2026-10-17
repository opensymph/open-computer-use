#if canImport(Darwin)
import Darwin
#elseif canImport(Glibc)
import Glibc
#endif

import Foundation

/// Launch-time file identity: the `(st_dev, st_ino)` pair of the file a
/// process image was started from.
///
/// The app agent uses this to answer "am I still the current executable?":
/// zip-based upgrades preserve mtimes, so mtime comparisons let a stale
/// agent pass the client's freshness check, while an inode identity changes
/// on every real replacement. Identity must be captured eagerly at process
/// start — a lookup performed later stats whatever file now sits at the
/// path, which after an in-place upgrade is the replacement.
public struct OpenComputerUseFileIdentity: Sendable, Equatable {
    public let device: UInt64
    public let inode: UInt64

    public init(device: UInt64, inode: UInt64) {
        self.device = device
        self.inode = inode
    }

    /// The identity of the file currently at `path`, or nil when it cannot
    /// be stated (missing file, permissions).
    public static func identity(atPath path: String) -> OpenComputerUseFileIdentity? {
        var status = stat()
        guard stat(path, &status) == 0 else {
            return nil
        }
        return OpenComputerUseFileIdentity(
            device: UInt64(clamping: status.st_dev),
            inode: UInt64(clamping: status.st_ino)
        )
    }
}
