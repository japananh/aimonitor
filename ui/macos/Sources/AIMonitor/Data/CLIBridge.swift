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
    /// `extraEnv` is merged onto the process environment — used to pass a
    /// passphrase via AIMONITOR_PASSPHRASE without putting it on the argv (where
    /// it'd show in `ps`).
    @discardableResult
    static func run(_ args: [String], extraEnv: [String: String] = [:]) throws -> String {
        let task = Process()
        if let path = resolveBinaryPath() {
            task.executableURL = URL(fileURLWithPath: path)
            task.arguments = args
        } else {
            task.executableURL = URL(fileURLWithPath: "/usr/bin/env")
            task.arguments = ["aimonitor"] + args
        }
        if !extraEnv.isEmpty {
            var env = ProcessInfo.processInfo.environment
            for (k, v) in extraEnv { env[k] = v }
            task.environment = env
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
        // auto_swap.enabled is the live setting the daemon's AutoSwapper
        // reads (the old "autoswitch" key is deprecated and now errors).
        try run(["config", "set", "auto_swap.enabled", enabled ? "true" : "false"])
    }

    /// Renames an account's label via the CLI (keychain/identity untouched).
    static func rename(from oldLabel: String, to newLabel: String) throws {
        try run(["rename", oldLabel, newLabel])
    }

    /// Fetches fresh 5h/7d usage for all inactive accounts (the active one is
    /// kept fresh by the daemon and skipped). May take a few seconds.
    static func refreshUsage() throws {
        try run(["usage", "refresh"])
    }

    /// Fetches fresh usage for one account. Throws (with the CLI's stderr)
    /// when the fetch can't complete, so the caller can show the error.
    static func refreshUsage(label: String) throws {
        try run(["usage", "refresh", label])
    }

    /// Imports whatever account is currently signed into the live slot,
    /// stashing it under `label`. Used to adopt an account another app
    /// signed into. Throws on failure (e.g. label already taken).
    static func adoptCurrent(label: String) throws {
        try run(["add", "--adopt-current", "--label", label])
    }

    /// Reads a config value (trimmed). Throws on unknown key.
    static func configGet(_ key: String) throws -> String {
        try run(["config", "get", key]).trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// Writes a config value.
    static func configSet(_ key: String, _ value: String) throws {
        try run(["config", "set", key, value])
    }

    /// Exports settings (and, with a passphrase, encrypted credentials) to a
    /// bundle file. Passphrase goes via the environment, never argv.
    static func configExport(to path: String, includeTokens: Bool, passphrase: String?) throws {
        var args = ["config", "export", "--out", path]
        var env: [String: String] = [:]
        if includeTokens {
            args.append("--include-tokens")
            if let p = passphrase { env["AIMONITOR_PASSPHRASE"] = p }
        }
        try run(args, extraEnv: env)
    }

    /// Imports a bundle. When it carries encrypted credentials, `passphrase`
    /// must be supplied. Returns the CLI's stdout summary.
    @discardableResult
    static func configImport(from path: String, passphrase: String?) throws -> String {
        var env: [String: String] = [:]
        if let p = passphrase { env["AIMONITOR_PASSPHRASE"] = p }
        return try run(["config", "import", path], extraEnv: env)
    }

    /// Result of `aimonitor update check --json`. `notes` is omitted by the
    /// CLI when the release body is empty, hence optional.
    struct UpdateCheck: Decodable {
        let available: Bool
        let current: String
        let latest: String
        let url: String
        let notes: String?
    }

    /// Checks GitHub for a newer release. Pure network read, no token cost.
    static func checkUpdate() throws -> UpdateCheck {
        let out = try run(["update", "check", "--json"])
        guard let data = out.data(using: .utf8) else {
            throw CLIBridgeError.exitNonZero(0, "update check: empty response")
        }
        return try JSONDecoder().decode(UpdateCheck.self, from: data)
    }

    /// Kicks off a detached Homebrew upgrade. Returns immediately; the
    /// upgrade quits and relaunches the app when it completes.
    static func installUpdate() throws {
        try run(["update", "install"])
    }
}

// MARK: - MCP (Slack + ClickUp integrations)

/// One integration's connection + config state, from `mcp status --json`.
struct MCPServiceStatus: Decodable, Identifiable {
    let service: String
    let connected: Bool
    let identity: String?
    let error: String?
    let enabled: Bool
    let read_only: Bool
    var id: String { service }
}

struct MCPStatus: Decodable {
    let services: [MCPServiceStatus]
    let tools: [String]
}

extension CLIBridge {
    /// Connection state + exposed tool list. Slow-ish (verifies each token
    /// against the live API) — call off the main thread.
    static func mcpStatus() throws -> MCPStatus {
        let out = try run(["mcp", "status", "--json"])
        guard let data = out.data(using: .utf8) else {
            throw CLIBridgeError.exitNonZero(1, "empty mcp status output")
        }
        return try JSONDecoder().decode(MCPStatus.self, from: data)
    }

    /// Connect via claude-bar migration. Throws when no migratable token
    /// exists (the CLI's stdin-paste fallback hits EOF) — the UI then asks
    /// for a pasted token and retries with mcpConnect(service:token:).
    static func mcpConnect(service: String) throws -> String {
        try run(["mcp", "connect", service])
    }

    /// Connect with an explicitly pasted token (verified before storing).
    static func mcpConnect(service: String, token: String) throws -> String {
        try run(["mcp", "connect", service, "--token", token])
    }

    static func mcpDisconnect(service: String) throws {
        try run(["mcp", "disconnect", service])
    }
}
