// CLIBridge shells out to the `aimonitor` CLI for mutating operations.
//
// Why shell out: every mutation (switch, set autoswitch, rename, remove)
// already has a tested code path in Go. Calling it from Swift means we
// don't reimplement the keychain dance + SQLite writes + audit log
// inserts; we get them for free, and there's only one implementation
// to maintain.
//
// Resolution order for the binary:
//   1. AIMONITOR_BIN env var (test override + dev convenience)
//   2. /opt/homebrew/bin/aimonitor (Apple Silicon Homebrew)
//   3. /usr/local/bin/aimonitor (Intel + manual installs)
//   4. PATH lookup via `/usr/bin/env aimonitor`

import Foundation

enum CLIBridgeError: Error, LocalizedError {
    case binaryNotFound
    case exitNonZero(Int32, String)

    var errorDescription: String? {
        switch self {
        case .binaryNotFound:
            return "aimonitor CLI not found. Install via `brew install japananh/tap/aimonitor` or set AIMONITOR_BIN."
        case .exitNonZero(let code, let stderr):
            return "aimonitor exited \(code): \(stderr)"
        }
    }
}

enum CLIBridge {
    static func resolveBinaryPath() -> String? {
        if let env = ProcessInfo.processInfo.environment["AIMONITOR_BIN"],
           FileManager.default.isExecutableFile(atPath: env) {
            return env
        }
        for candidate in ["/opt/homebrew/bin/aimonitor", "/usr/local/bin/aimonitor"] {
            if FileManager.default.isExecutableFile(atPath: candidate) {
                return candidate
            }
        }
        return nil
    }

    /// Runs `aimonitor <args>` and returns stdout. Throws on non-zero exit.
    @discardableResult
    static func run(_ args: [String]) throws -> String {
        let task = Process()
        if let path = resolveBinaryPath() {
            task.executableURL = URL(fileURLWithPath: path)
            task.arguments = args
        } else {
            task.executableURL = URL(fileURLWithPath: "/usr/bin/env")
            task.arguments = ["aimonitor"] + args
        }

        let stdoutPipe = Pipe()
        let stderrPipe = Pipe()
        task.standardOutput = stdoutPipe
        task.standardError = stderrPipe

        do {
            try task.run()
        } catch {
            throw CLIBridgeError.binaryNotFound
        }
        task.waitUntilExit()

        let out = String(data: stdoutPipe.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
        let errOut = String(data: stderrPipe.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""

        if task.terminationStatus != 0 {
            throw CLIBridgeError.exitNonZero(task.terminationStatus, errOut.isEmpty ? out : errOut)
        }
        return out
    }

    static func switchTo(label: String) throws {
        try run(["switch", label])
    }

    static func setAutoSwitch(_ enabled: Bool) throws {
        try run(["config", "set", "autoswitch", enabled ? "true" : "false"])
    }
}
