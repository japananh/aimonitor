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
    @Published var lastError: String? = nil

    // showAccountPanel is the user preference for whether the per-account
    // headroom panel is visible. Default true; persisted via UserDefaults.
    @Published var showAccountPanel: Bool {
        didSet {
            UserDefaults.standard.set(showAccountPanel, forKey: "showAccountPanel")
        }
    }

    private let dbPath: String
    private var timer: AnyCancellable?
    private let workQueue = DispatchQueue(label: "dev.aimonitor.dbpoll", qos: .utility)

    init(dbPath: String = SQLiteReader.defaultPath()) {
        self.dbPath = dbPath
        // UserDefaults returns false for unset bool keys, so default to true
        // when the key has never been written.
        if UserDefaults.standard.object(forKey: "showAccountPanel") == nil {
            self.showAccountPanel = true
        } else {
            self.showAccountPanel = UserDefaults.standard.bool(forKey: "showAccountPanel")
        }
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
        let result: Result<(DaemonStatus?, [AccountRow], [ProbeRow]), Error> = await withCheckedContinuation { cont in
            workQueue.async {
                do {
                    let r = try SQLiteReader(path: path)
                    let st = try r.daemonStatus()
                    let accs = try r.listAccounts()
                    let pr = try r.listProbes()
                    cont.resume(returning: .success((st, accs, pr)))
                } catch {
                    cont.resume(returning: .failure(error))
                }
            }
        }
        switch result {
        case .success(let (st, accs, pr)):
            self.status = st
            self.accounts = accs
            self.probes = pr
            self.lastError = nil
        case .failure(let err):
            self.lastError = "\(err)"
        }
    }

    /// Calls aimonitor CLI on a background queue so the UI doesn't freeze
    /// during the 50–200 ms switch dance.
    func switchTo(label: String) {
        let q = workQueue
        q.async {
            do {
                try CLIBridge.switchTo(label: label)
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
