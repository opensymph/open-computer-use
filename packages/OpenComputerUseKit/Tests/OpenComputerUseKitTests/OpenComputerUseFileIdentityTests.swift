import Foundation
import XCTest
@testable import OpenComputerUseKit

final class OpenComputerUseFileIdentityTests: XCTestCase {
    private var workDirectory: URL!

    override func setUpWithError() throws {
        workDirectory = FileManager.default.temporaryDirectory
            .appendingPathComponent("ocu-file-identity-tests-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: workDirectory, withIntermediateDirectories: true)
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: workDirectory)
    }

    func testIdentityIsStableForUnchangedFile() throws {
        let path = workDirectory.appendingPathComponent("agent-exec").path
        try Data([0x41]).write(to: URL(fileURLWithPath: path))

        let first = try XCTUnwrap(OpenComputerUseFileIdentity.identity(atPath: path))
        let second = try XCTUnwrap(OpenComputerUseFileIdentity.identity(atPath: path))
        XCTAssertEqual(first, second)
    }

    func testIdentityIsNilForMissingFile() {
        let path = workDirectory.appendingPathComponent("missing").path
        XCTAssertNil(OpenComputerUseFileIdentity.identity(atPath: path))
    }

    // The U1 regression, in the review's exact terms: the agent captures its
    // identity at startup; the bundle is then replaced before the first
    // agentInfo request, with the replacement preserving the executable's
    // mtime (ditto/zip behavior). The captured identity must NOT match the
    // file now on disk, or the stale agent would be misjudged as current.
    func testPreservedMtimeReplacementChangesIdentity() throws {
        let fileManager = FileManager.default
        let path = workDirectory.appendingPathComponent("OpenComputerUse").path
        try Data([0x41]).write(to: URL(fileURLWithPath: path))
        let originalAttributes = try fileManager.attributesOfItem(atPath: path)
        let originalMtime = try XCTUnwrap(originalAttributes[.modificationDate] as? Date)

        // Eager startup capture (before the listener opens).
        let capturedAtLaunch = try XCTUnwrap(OpenComputerUseFileIdentity.identity(atPath: path))

        // Same-path replacement with preserved mtime — what ditto -x does.
        let staged = workDirectory.appendingPathComponent("staged-OpenComputerUse").path
        try Data([0x41, 0x42]).write(to: URL(fileURLWithPath: staged))
        try fileManager.setAttributes([.modificationDate: originalMtime], ofItemAtPath: staged)
        try fileManager.removeItem(atPath: path)
        try fileManager.moveItem(atPath: staged, toPath: path)

        // The mtime really is unchanged...
        let newAttributes = try fileManager.attributesOfItem(atPath: path)
        XCTAssertEqual(newAttributes[.modificationDate] as? Date, originalMtime)

        // ...but the running agent's captured identity must differ from the
        // file now at the path, so the client freshness check rejects it.
        let diskIdentity = try XCTUnwrap(OpenComputerUseFileIdentity.identity(atPath: path))
        XCTAssertNotEqual(capturedAtLaunch, diskIdentity)
    }

    // And the positive control: without a replacement, a later disk lookup
    // must agree with the captured identity, so an unchanged agent is not
    // relaunched.
    func testUnchangedFileStillMatchesLaterLookup() throws {
        let path = workDirectory.appendingPathComponent("OpenComputerUse").path
        try Data([0x41]).write(to: URL(fileURLWithPath: path))

        let capturedAtLaunch = try XCTUnwrap(OpenComputerUseFileIdentity.identity(atPath: path))
        let diskIdentity = try XCTUnwrap(OpenComputerUseFileIdentity.identity(atPath: path))
        XCTAssertEqual(capturedAtLaunch, diskIdentity)
    }
}
