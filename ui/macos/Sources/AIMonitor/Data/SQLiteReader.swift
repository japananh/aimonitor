// SQLiteReader is a thin, read-only wrapper around the system libsqlite3
// for the widget. The Go daemon owns writes; the widget only reads.
//
// Why direct SQLite3 and not a Swift package: SwiftPM packages like
// stephencelis/SQLite.swift would add a dependency tree we don't need
// for ~5 queries. The system library is always present on macOS, and
// the C API surface used here is tiny.

import Foundation
import SQLite3

// SQLITE_TRANSIENT tells SQLite to copy the parameter bytes immediately
// rather than holding a pointer to caller-owned memory. Without this,
// Swift strings passed to sqlite3_bind_text get invalidated as soon as
// they go out of scope and we get garbage at query time.
private let SQLITE_TRANSIENT = unsafeBitCast(
    OpaquePointer(bitPattern: -1),
    to: sqlite3_destructor_type.self
)

/// AccountRow mirrors a relevant subset of the accounts table.
struct AccountRow: Identifiable, Hashable {
    let id: Int64
    let label: String
    let email: String?
    let organizationName: String?
    let lastUsedAt: Date?
}

/// LimitsRow mirrors a row of oauth_usage: a per-account rate-limit
/// snapshot. Populated for the active account every tick and for inactive
/// accounts round-robin (valid-token-only), so a row may be stale —
/// `fetchedAt` is how the UI tells.
struct LimitsRow: Hashable {
    let accountID: Int64
    let fiveHourPct: Double
    let sevenDayPct: Double
    let fiveHourResetAt: Date?
    let sevenDayResetAt: Date?
    let fetchedAt: Date
}

/// UsageSamplePoint is one point in an account's utilization time series
/// (the usage_history table), powering the sparkline trend.
struct UsageSamplePoint: Hashable {
    let ts: Date
    let fiveHourPct: Double
    let sevenDayPct: Double
}

/// ProbeRow mirrors a relevant subset of probe_results.
struct ProbeRow: Hashable {
    let accountID: Int64
    let probedAt: Date
    let tokensRemaining: Int64
    let resetAt: Date
    let httpStatus: Int
}

/// DaemonStatus is the JSON snapshot the Go daemon publishes to the
/// settings table every ~2s. Field names match the Go side exactly.
struct DaemonStatus: Codable {
    var published_at: Date?
    var active_label: String?
    var usage_since_reset: Int64
    var observed_budget: Int64
    var session_percent: Double
    var auto_switch_enabled: Bool
    var last_switch_at: Date?

    // OAuth-introspected utilization for the active account, written by
    // the daemon's UsageScheduler on a ~5-minute jittered cadence. Optional
    // because old daemon builds (and brand-new installs before the first
    // fetch) won't populate them — the UI hides the bars in that case.
    var five_hour_pct: Double?
    var seven_day_pct: Double?
    var five_hour_reset_at: Date?
    var seven_day_reset_at: Date?
    var limits_fetched_at: Date?

    // Set when a live account aimonitor doesn't manage is signed in
    // (another app or `claude /login`) — drives the import prompt. Absent
    // when the active account is known.
    var unknown_active_email: String?
}

enum SQLiteReaderError: Error {
    case openFailed(Int32, String)
    case prepareFailed(Int32, String)
    case decodeFailed(String)
}

final class SQLiteReader {
    private var db: OpaquePointer?

    init(path: String) throws {
        // SQLITE_OPEN_READONLY plus the URI flag isn't strictly necessary
        // since the daemon owns writes, but it's a belt-and-suspenders
        // guard against accidental writes from the widget side.
        let flags = SQLITE_OPEN_READONLY
        let rc = sqlite3_open_v2(path, &db, flags, nil)
        if rc != SQLITE_OK {
            let msg = String(cString: sqlite3_errmsg(db))
            sqlite3_close_v2(db)
            db = nil
            throw SQLiteReaderError.openFailed(rc, msg)
        }
        // Match the daemon's busy_timeout so concurrent WAL readers
        // don't fight on writes.
        sqlite3_busy_timeout(db, 5000)
    }

    deinit {
        if db != nil {
            sqlite3_close_v2(db)
        }
    }

    /// Default DB path mirrors the Go side's DefaultPath() on macOS.
    static func defaultPath() -> String {
        let appSupport = FileManager.default.urls(
            for: .applicationSupportDirectory, in: .userDomainMask
        ).first!
        return appSupport
            .appendingPathComponent("aimonitor")
            .appendingPathComponent("aimonitor.db")
            .path
    }

    func listAccounts() throws -> [AccountRow] {
        let sql = "SELECT id, label, email, organization_name, last_used_at FROM accounts ORDER BY label"
        var stmt: OpaquePointer?
        let rc = sqlite3_prepare_v2(db, sql, -1, &stmt, nil)
        if rc != SQLITE_OK {
            throw SQLiteReaderError.prepareFailed(rc, String(cString: sqlite3_errmsg(db)))
        }
        defer { sqlite3_finalize(stmt) }

        var rows: [AccountRow] = []
        while sqlite3_step(stmt) == SQLITE_ROW {
            let id = sqlite3_column_int64(stmt, 0)
            let label = String(cString: sqlite3_column_text(stmt, 1))
            let email = Self.optText(stmt, 2)
            let org = Self.optText(stmt, 3)
            let lastUsedAt: Date?
            if sqlite3_column_type(stmt, 4) == SQLITE_NULL {
                lastUsedAt = nil
            } else {
                let ms = sqlite3_column_int64(stmt, 4)
                lastUsedAt = Date(timeIntervalSince1970: TimeInterval(ms) / 1000.0)
            }
            rows.append(AccountRow(id: id, label: label, email: email, organizationName: org, lastUsedAt: lastUsedAt))
        }
        return rows
    }

    /// Returns the column as a String, or nil for SQL NULL *or* the empty
    /// string. The Go side stores '' for absent identity (migration 0003
    /// columns are NOT NULL DEFAULT ''), so '' means "not set" — collapse
    /// it to nil so the UI hides the row rather than showing a blank line.
    private static func optText(_ stmt: OpaquePointer?, _ col: Int32) -> String? {
        if sqlite3_column_type(stmt, col) == SQLITE_NULL { return nil }
        let s = String(cString: sqlite3_column_text(stmt, col))
        return s.isEmpty ? nil : s
    }

    func listProbes() throws -> [ProbeRow] {
        let sql = """
            SELECT account_id, probed_at, tokens_remaining, reset_at, http_status
              FROM probe_results
            """
        var stmt: OpaquePointer?
        let rc = sqlite3_prepare_v2(db, sql, -1, &stmt, nil)
        if rc != SQLITE_OK {
            throw SQLiteReaderError.prepareFailed(rc, String(cString: sqlite3_errmsg(db)))
        }
        defer { sqlite3_finalize(stmt) }

        var rows: [ProbeRow] = []
        while sqlite3_step(stmt) == SQLITE_ROW {
            let acct = sqlite3_column_int64(stmt, 0)
            let probedMs = sqlite3_column_int64(stmt, 1)
            let remaining = sqlite3_column_int64(stmt, 2)
            let resetMs = sqlite3_column_int64(stmt, 3)
            let status = Int(sqlite3_column_int(stmt, 4))
            rows.append(ProbeRow(
                accountID: acct,
                probedAt: Date(timeIntervalSince1970: TimeInterval(probedMs) / 1000.0),
                tokensRemaining: remaining,
                resetAt: Date(timeIntervalSince1970: TimeInterval(resetMs) / 1000.0),
                httpStatus: status
            ))
        }
        return rows
    }

    /// Returns the latest rate-limit snapshot per account, keyed by
    /// account id. Accounts with no row yet are simply absent from the map.
    func limits() throws -> [Int64: LimitsRow] {
        let sql = """
            SELECT account_id, five_hour_pct, five_hour_reset_at,
                   seven_day_pct, seven_day_reset_at, fetched_at
              FROM oauth_usage
            """
        var stmt: OpaquePointer?
        let rc = sqlite3_prepare_v2(db, sql, -1, &stmt, nil)
        if rc != SQLITE_OK {
            throw SQLiteReaderError.prepareFailed(rc, String(cString: sqlite3_errmsg(db)))
        }
        defer { sqlite3_finalize(stmt) }

        func optMsDate(_ col: Int32) -> Date? {
            if sqlite3_column_type(stmt, col) == SQLITE_NULL { return nil }
            let ms = sqlite3_column_int64(stmt, col)
            return Date(timeIntervalSince1970: TimeInterval(ms) / 1000.0)
        }

        var out: [Int64: LimitsRow] = [:]
        while sqlite3_step(stmt) == SQLITE_ROW {
            let acct = sqlite3_column_int64(stmt, 0)
            let row = LimitsRow(
                accountID: acct,
                fiveHourPct: sqlite3_column_double(stmt, 1),
                sevenDayPct: sqlite3_column_double(stmt, 3),
                fiveHourResetAt: optMsDate(2),
                sevenDayResetAt: optMsDate(4),
                fetchedAt: Date(timeIntervalSince1970: TimeInterval(sqlite3_column_int64(stmt, 5)) / 1000.0)
            )
            out[acct] = row
        }
        return out
    }

    /// Returns the utilization time series for every account since `since`,
    /// grouped by account id and ordered oldest-first within each group.
    /// One query (index-backed on account_id, ts), grouped in Swift — far
    /// cheaper than one query per account on the 2s poll.
    func usageHistory(since: Date) throws -> [Int64: [UsageSamplePoint]] {
        let sql = """
            SELECT account_id, ts, five_hour_pct, seven_day_pct
              FROM usage_history
             WHERE ts >= ?
             ORDER BY account_id ASC, ts ASC
            """
        var stmt: OpaquePointer?
        let rc = sqlite3_prepare_v2(db, sql, -1, &stmt, nil)
        if rc != SQLITE_OK {
            throw SQLiteReaderError.prepareFailed(rc, String(cString: sqlite3_errmsg(db)))
        }
        defer { sqlite3_finalize(stmt) }
        sqlite3_bind_int64(stmt, 1, Int64(since.timeIntervalSince1970 * 1000.0))

        var out: [Int64: [UsageSamplePoint]] = [:]
        while sqlite3_step(stmt) == SQLITE_ROW {
            let acct = sqlite3_column_int64(stmt, 0)
            let point = UsageSamplePoint(
                ts: Date(timeIntervalSince1970: TimeInterval(sqlite3_column_int64(stmt, 1)) / 1000.0),
                fiveHourPct: sqlite3_column_double(stmt, 2),
                sevenDayPct: sqlite3_column_double(stmt, 3)
            )
            out[acct, default: []].append(point)
        }
        return out
    }

    /// Returns nil when the daemon has never published — the bar shows
    /// a "daemon not running" placeholder in that case.
    func daemonStatus() throws -> DaemonStatus? {
        let sql = "SELECT value FROM settings WHERE key = ?"
        var stmt: OpaquePointer?
        let rc = sqlite3_prepare_v2(db, sql, -1, &stmt, nil)
        if rc != SQLITE_OK {
            throw SQLiteReaderError.prepareFailed(rc, String(cString: sqlite3_errmsg(db)))
        }
        defer { sqlite3_finalize(stmt) }
        sqlite3_bind_text(stmt, 1, "daemon_status", -1, SQLITE_TRANSIENT)

        guard sqlite3_step(stmt) == SQLITE_ROW else { return nil }
        let jsonStr = String(cString: sqlite3_column_text(stmt, 0))
        guard let data = jsonStr.data(using: .utf8) else { return nil }

        let dec = JSONDecoder()
        dec.dateDecodingStrategy = .iso8601
        do {
            return try dec.decode(DaemonStatus.self, from: data)
        } catch {
            throw SQLiteReaderError.decodeFailed("\(error)")
        }
    }
}
