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
    @Published var probes: [ProbeRow] = []
    // Per-account rate-limit snapshots keyed by account id, for the
    // per-account 5h/7d bars. A row may be stale (see LimitsRow.fetchedAt).
    @Published var limitsByAccount: [Int64: LimitsRow] = [:]
    // Per-account utilization time series (last ~24h) for the sparkline
    // trend. Keyed by account id; empty until the daemon has logged a couple
    // of points.
    @Published var historyByAccount: [Int64: [UsageSamplePoint]] = [:]
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

    init(dbPath: String = SQLiteReader.defaultPath()) {
        self.dbPath = dbPath
    }

    func start() {
        // Immediate refresh on launch so the popover doesn't open blank.
        Task { await refresh() }
        timer = Timer.publish(every: 2.0, on: .main, in: .common)
            .autoconnect()
            .sink { [weak self] _ in
                Task { [weak self] in await self?.refresh() }
            }
    }

    func stop() {
        timer?.cancel()
        timer = nil
    }

    func refresh() async {
        let path = dbPath
        let result: Result<(DaemonStatus?, [AccountRow], [ProbeRow], [Int64: LimitsRow], [Int64: [UsageSamplePoint]]), Error> = await withCheckedContinuation { cont in
            workQueue.async {
                do {
                    let r = try SQLiteReader(path: path)
                    let st = try r.daemonStatus()
                    let accs = try r.listAccounts()
                    let pr = try r.listProbes()
                    let lim = try r.limits()
                    // Last 24h of trend for the sparkline. One query, grouped
                    // by account; bounded (~288 points/account at the 5-min
                    // cadence) so it's cheap on the 2s poll.
                    let hist = try r.usageHistory(since: Date().addingTimeInterval(-24 * 3600))
                    cont.resume(returning: .success((st, accs, pr, lim, hist)))
                } catch {
                    cont.resume(returning: .failure(error))
                }
            }
        }
        switch result {
        case .success(let (st, accs, pr, lim, hist)):
            self.status = st
            self.accounts = accs
            self.probes = pr
            self.limitsByAccount = lim
            self.historyByAccount = hist
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
                    self.lastError = "\(error)"
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
                do { try CLIBridge.refreshUsage(); return nil } catch { return "\(error)" }
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
                do { try CLIBridge.refreshUsage(label: label); return nil } catch { return "\(error)" }
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
                Task { @MainActor in self.lastError = "\(error)" }
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
                Task { @MainActor in self.lastError = "\(error)" }
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
                Task { @MainActor in self.lastError = "\(error)" }
            }
        }
    }
}
