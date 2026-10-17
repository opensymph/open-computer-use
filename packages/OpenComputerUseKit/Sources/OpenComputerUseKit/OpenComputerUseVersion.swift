import Foundation

public let openComputerUseVersion = "1.3.0"

public struct OpenComputerUseBuildMetadata: Sendable {
    public let version: String
    public let build: String?
    public let commit: String?

    public init(version: String, build: String? = nil, commit: String? = nil) {
        self.version = version
        self.build = build
        self.commit = commit
    }

    public var descriptionText: String {
        var details: [String] = []
        if let build, !build.isEmpty {
            details.append("build \(build)")
        }
        if let commit, !commit.isEmpty {
            details.append("commit \(commit.prefix(7))")
        }
        guard !details.isEmpty else {
            return version
        }
        return "\(version) (\(details.joined(separator: ", ")))"
    }
}

public func resolvedOpenComputerUseVersion(bundle: Bundle = .main) -> String {
    if let version = bundle.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String,
       !version.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
        return version
    }

    return openComputerUseVersion
}

public func resolvedOpenComputerUseBuildMetadata(bundle: Bundle = .main) -> OpenComputerUseBuildMetadata {
    let nonEmpty: (String?) -> String? = { value in
        guard let value, !value.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            return nil
        }
        return value
    }

    return OpenComputerUseBuildMetadata(
        version: resolvedOpenComputerUseVersion(bundle: bundle),
        build: nonEmpty(bundle.object(forInfoDictionaryKey: "CFBundleVersion") as? String),
        commit: nonEmpty(bundle.object(forInfoDictionaryKey: "OpenComputerUseGitCommit") as? String)
    )
}
