// AppModel is the observable state backing every SwiftUI view in the
// widget. Polls SQLite on a 2-second cadence and exposes the latest
// snapshot via @Published properties.
//
// Concurrency: refresh() is run on a background queue so SQLite reads
// don't block the main thread; published updates are routed back to
// MainActor.

import Foundation
import Combine

@MainActor
final class AppModel: ObservableObject {
    @Published var status: DaemonStatus? = nil
    @Published var accounts: [AccountRow] = []
    // Per-account rate-limit snapshots keyed by account id, for the
    // per-account 5h/7d bars. A row may be stale (see LimitsRow.fetchedAt).
    @Published var limitsByAccount: [Int64: LimitsRow] = [:]
    // Per-account utilization time series (last ~24h) for the sparkline
    // trend. Keyed by account id; empty until the daemon has logged a couple
    // of points.
    @Published var historyByAccount: [Int64: [UsageSamplePoint]] = [:]
    // Per-account token usage (usage_samples) bucketed by local-time day or
    // hour, for the Tokens tab. Reloaded every poll at the current
    // granularity (tokensHourly); keyed by account id, oldest bucket first.
    @Published var tokenUsageByAccount: [Int64: [TokenBucketRow]] = [:]
    // Tokens-tab granularity: false = daily (last 14d), true = hourly (last
    // 48h). Flipping it triggers a refresh (see TokenUsageView.onChange).
    @Published var tokensHourly = false
    // True while a manual "Refresh usage" (all-accounts) fetch is in flight.
    @Published var refreshingUsage = false
    /// Label of the account a Switch is currently in flight for (nil when
    /// idle). Drives the Switch buttons' spinner + disabled state — a switch
    /// takes a few seconds (token refresh + keychain writes) and double
    /// clicks must not queue a second one.
    @Published var switchingLabel: String? = nil
    // Account ids whose per-row refresh is in flight (spinner on that row).
    @Published var refreshingAccounts: Set<Int64> = []
    // Per-account last refresh error, shown on the row until the next
    // successful refresh of that account.
    @Published var usageErrors: [Int64: String] = [:]
    @Published var lastError: String? = nil

    /// activeEmail is the Claude email of the currently-active account,
    /// resolved by joining the daemon's active_label against the accounts
    /// table. nil when there's no active account or its identity hasn't
    /// been captured yet (legacy rows added before identity capture).
    var activeEmail: String? {
        guard let label = status?.active_label, !label.isEmpty else { return nil }
        guard let acct = accounts.first(where: { $0.label == label }) else { return nil }
        if let email = acct.email, !email.isEmpty { return email }
        return nil
    }

    /// activeDisplayName is the active account's NAME for the menu-bar title
    /// (e.g. "Gem 2") — the user-facing label, which is more legible beside
    /// the icon than the full email. The popover header still shows the
    /// email via activeEmail. Empty only when no account is active. Updates
    /// on a switch (active_label changes) and on a rename (the daemon
    /// republishes the new label within a tick).
    var activeDisplayName: String {
        guard let label = status?.active_label, !label.isEmpty else { return "" }
        return label
    }

    private let dbPath: String
    private var timer: AnyCancellable?
    private let workQueue = DispatchQueue(label: "dev.aimonitor.dbpoll", qos: .utility)

    // When the app launches — notably the relaunch right after a self-update —
    // we haven't read the DB yet, so `status` is nil. That is NOT the same as
    // the daemon being down. `launchedAt` bounds a startup grace window during
    // which a still-empty status reads as "starting up", not "daemon not
    // running", so a normal launch doesn't flash the alarm banner before the
    // first status read lands. Sized above the observed 1–3s first-read latency
    // (and any brief window where the DB is unreadable while the .app is
    // swapped mid-upgrade) with margin.
    private let launchedAt = Date()
    private let startupGrace: TimeInterval = 10

    /// Whether to surface the "daemon not running" banner. Once a status has
    /// been published we use staleness (last publish older than ~30s → down).
    /// Before the first status read (status == nil) we suppress it during the
    /// startup grace window — distinguishing "still loading / daemon restarting
    /// after an update" from "daemon genuinely down" (a fresh install with no
    /// daemon, or a real outage that outlasts the grace). Re-evaluated on every
    /// poll (status is @Published and reassigned each refresh), so once the
    /// grace elapses with still-nil status the banner appears on the next tick.
    var daemonAppearsDown: Bool {
        if let pub = status?.published_at {
            return Date().timeIntervalSince(pub) > 30
        }
        return Date().timeIntervalSince(launchedAt) > startupGrace
    }

    // What's on screen drives how much each poll fetches and how fast it runs.
    // When nothing is open, only the menu-bar title needs data (status +
    // accounts), so the per-account detail and the heavy token scan are
    // skipped and the poll slows down. A long-running idle app otherwise
    // refreshed all six queries every 2s for days, and that churn accumulated
    // reachable memory in the offscreen SwiftUI graph (#32).
    private var panelVisible = false
    private var tokenWindowVisible = false
    private let activeInterval = 2.0
    private let idleInterval = 5.0

    init(dbPath: String = AppModel.defaultDBPath()) {
        self.dbPath = dbPath
    }

    /// DB path, overridable via AIMONITOR_STORE_PATH (mirrors the CLI) so the
    /// widget can run against a throwaway database for QA without touching the
    /// real one. Falls back to the platform default. `nonisolated` so it can
    /// be used as the init default argument (evaluated off the main actor).
    nonisolated static func defaultDBPath() -> String {
        if let p = ProcessInfo.processInfo.environment["AIMONITOR_STORE_PATH"], !p.isEmpty {
            return p
        }
        return SQLiteReader.defaultPath()
    }

    func start() {
        // Immediate refresh on launch so the menu-bar title isn't blank.
        // forceDetail warms the per-account limits (and trend history) cache
        // even though the popover is closed: the inactive accounts aren't
        // polled in the background, so without this the FIRST popover open
        // after launch shows "no usage data yet" until the on-demand network
        // fetch lands ~2-3s later. Priming the cache makes that open render
        // the last-known numbers instantly, then refresh in place.
        Task { await refresh(forceDetail: true) }
        // Launches with the popover closed → idle cadence.
        scheduleTimer(every: idleInterval)
    }

    /// (Re)schedules the poll at `interval`, overridable via AIMONITOR_POLL_MS
    /// (debug/QA only) so a dev build can compress days of polling into minutes
    /// to profile the long-running memory footprint.
    private func scheduleTimer(every interval: TimeInterval) {
        timer?.cancel()
        timer = Timer.publish(every: AppModel.pollInterval(default: interval), on: .main, in: .common)
            .autoconnect()
            .sink { [weak self] _ in
                Task { [weak self] in await self?.refresh() }
            }
    }

    nonisolated static func pollInterval(default def: TimeInterval) -> TimeInterval {
        if let s = ProcessInfo.processInfo.environment["AIMONITOR_POLL_MS"],
           let ms = Double(s), ms > 0 {
            return ms / 1000.0
        }
        return def
    }

    /// Popover opened: switch to the fast cadence and pull the full snapshot now
    /// so it isn't blank.
    func panelDidOpen() {
        panelVisible = true
        scheduleTimer(every: activeInterval)
        Task { await refresh() }
    }

    /// Popover closed: drop back to the idle cadence; the next polls fetch only
    /// what the menu bar needs.
    func panelDidClose() {
        panelVisible = false
        scheduleTimer(every: idleInterval)
    }

    /// Token-usage window opened: start fetching the (heavy) token buckets and
    /// pull them once right away.
    func tokenWindowDidOpen() {
        tokenWindowVisible = true
        Task { await refresh() }
    }

    func tokenWindowDidClose() {
        tokenWindowVisible = false
    }

    func stop() {
        timer?.cancel()
        timer = nil
    }

    /// One poll's worth of data. The per-account detail (`limits`, `history`)
    /// and the heavy `tokens` scan are nil when their view isn't on screen, so
    /// an idle poll only carries the cheap menu-bar fields.
    private struct Snapshot {
        let status: DaemonStatus?
        let accounts: [AccountRow]
        let limits: [Int64: LimitsRow]?
        let history: [Int64: [UsageSamplePoint]]?
        let tokens: [Int64: [TokenBucketRow]]?
    }

    func refresh(forceDetail: Bool = false) async {
        let path = dbPath
        // Capture what's visible on the main actor before the background read,
        // so the poll fetches only what something is actually showing.
        // forceDetail overrides visibility to prime the caches once at launch
        // (see start()), so the first popover open isn't blank.
        let wantDetail = panelVisible || forceDetail
        let wantTokens = tokenWindowVisible
        // Tokens-tab granularity: daily looks back 14 days, hourly 48 hours —
        // enough history for the window without an unbounded scan.
        let hourly = self.tokensHourly
        let tokenSince = hourly
            ? Date().addingTimeInterval(-48 * 3600)
            : Date().addingTimeInterval(-14 * 24 * 3600)
        let result: Result<Snapshot, Error> = await withCheckedContinuation { cont in
            workQueue.async {
                do {
                    let r = try SQLiteReader(path: path)
                    // Always cheap — drives the menu-bar title.
                    let st = try r.daemonStatus()
                    let accs = try r.listAccounts()
                    // Per-account detail only feeds the popover; the 24h trend
                    // is bounded (~288 points/account at the 5-min cadence).
                    let lim = wantDetail ? try r.limits() : nil
                    let hist = wantDetail ? try r.usageHistory(since: Date().addingTimeInterval(-24 * 3600)) : nil
                    // The token scan is the heaviest query and only feeds the
                    // standalone Token-usage window.
                    let toks = wantTokens ? try r.tokenUsage(byHour: hourly, since: tokenSince) : nil
                    cont.resume(returning: .success(Snapshot(status: st, accounts: accs, limits: lim, history: hist, tokens: toks)))
                } catch {
                    cont.resume(returning: .failure(error))
                }
            }
        }
        switch result {
        case .success(let snap):
            self.status = snap.status
            self.accounts = snap.accounts
            // Leave the detail maps untouched when this poll skipped them, so a
            // reopened popover shows the last values instantly while the next
            // active poll refreshes them — no empty flash.
            if let lim = snap.limits { self.limitsByAccount = lim }
            if let hist = snap.history { self.historyByAccount = hist }
            if let toks = snap.tokens { self.tokenUsageByAccount = toks }
            self.lastError = nil
        case .failure(let err):
            self.lastError = "\(err)"
        }
    }

    /// Calls aimonitor CLI on a background queue so the UI doesn't freeze
    /// during the 50–200 ms switch dance.
    func switchTo(label: String) {
        guard switchingLabel == nil else { return }
        switchingLabel = label
        let q = workQueue
        q.async {
            do {
                try CLIBridge.switchTo(label: label)
                // The keychain swap is done, but the ✓ in the UI follows the
                // DAEMON\'s published status, which lags by its 2s publish
                // tick plus a 5s credential cache. Clearing the spinner here
                // made a successful switch look like "nothing happened" and
                // invited more clicks — keep "Switching…" until the daemon
                // confirms the new active account.
                Task { @MainActor in await self.confirmSwitch(to: label) }
            } catch {
                Task { @MainActor in
                    self.lastError = CLIBridge.userMessage(error)
                    self.switchingLabel = nil
                }
            }
        }
    }

    /// Polls the daemon-published status until it names `label` as active,
    /// then clears the in-flight spinner. Bounded: after ~12s we give up and
    /// surface a hint instead of spinning forever (daemon down, etc.).
    private func confirmSwitch(to label: String) async {
        let deadline = Date().addingTimeInterval(12)
        while Date() < deadline {
            await refresh()
            if status?.active_label == label {
                switchingLabel = nil
                return
            }
            try? await Task.sleep(nanoseconds: 700_000_000)
        }
        switchingLabel = nil
        lastError = "Switched, but the daemon hasn\'t confirmed \"\(label)\" yet — is `aimonitor daemon` running?"
    }

    /// Last time refreshInactiveOnOpen actually fired a fetch (throttle guard).
    private var lastInactiveRefresh: Date = .distantPast

    /// Called when the popover opens: fetch the INACTIVE accounts on demand,
    /// since they aren't polled in the background. The daemon keeps the active
    /// account fresh on its own cadence. Throttled so reopening the popover
    /// repeatedly doesn't hammer Anthropic for shared accounts.
    func refreshInactiveOnOpen() {
        guard Date().timeIntervalSince(lastInactiveRefresh) > 60 else { return }
        lastInactiveRefresh = Date()
        workQueue.async {
            try? CLIBridge.refreshInactive()
            Task { @MainActor in await self.refresh() }
        }
    }

    /// Fetches fresh usage for EVERY account via the CLI (including the active
    /// one, through the daemon's safe live path), then re-reads. Runs on a
    /// background queue since the CLI does several network calls; the
    /// refreshingUsage flag drives the button's disabled/progress state.
    func refreshUsage() {
        guard !refreshingUsage else { return }
        refreshingUsage = true
        workQueue.async {
            let failure: String? = {
                do { try CLIBridge.refreshUsage(); return nil } catch { return CLIBridge.userMessage(error) }
            }()
            Task { @MainActor in
                await self.refresh()
                self.refreshingUsage = false
                if let failure { self.lastError = failure }
            }
        }
    }

    /// Fetches fresh usage for a single account; on failure records the
    /// error against that account id so the row can show it.
    func refreshUsage(label: String, id: Int64) {
        guard !refreshingAccounts.contains(id) else { return }
        refreshingAccounts.insert(id)
        usageErrors[id] = nil
        workQueue.async {
            let failure: String? = {
                do { try CLIBridge.refreshUsage(label: label); return nil } catch { return CLIBridge.userMessage(error) }
            }()
            Task { @MainActor in
                await self.refresh()
                self.refreshingAccounts.remove(id)
                self.usageErrors[id] = failure
            }
        }
    }

    /// Renames an account on a background queue, then refreshes so the row
    /// (and the menu-bar title, if this was the active account) updates.
    func rename(label: String, to newLabel: String) {
        let q = workQueue
        q.async {
            do {
                try CLIBridge.rename(from: label, to: newLabel)
                Task { @MainActor in await self.refresh() }
            } catch {
                Task { @MainActor in self.lastError = CLIBridge.userMessage(error) }
            }
        }
    }

    /// Removes an account (its aimonitor keychain stash + registry row) on a
    /// background queue, then refreshes so the row disappears. The CLI refuses
    /// to remove the active account, so the UI only offers this on inactive
    /// rows; any error (e.g. that refusal) surfaces in lastError.
    func removeAccount(label: String) {
        let q = workQueue
        q.async {
            do {
                try CLIBridge.remove(label: label)
                Task { @MainActor in await self.refresh() }
            } catch {
                Task { @MainActor in self.lastError = CLIBridge.userMessage(error) }
            }
        }
    }

    func setAutoSwitch(_ enabled: Bool) {
        let q = workQueue
        q.async {
            do {
                try CLIBridge.setAutoSwitch(enabled)
                Task { @MainActor in await self.refresh() }
            } catch {
                Task { @MainActor in self.lastError = CLIBridge.userMessage(error) }
            }
        }
    }
}
