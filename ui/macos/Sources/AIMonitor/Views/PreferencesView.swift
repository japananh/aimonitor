// PreferencesView is opened from the menu bar's "Preferences…" item.
// Surfaces panel/auto-switch/startup toggles plus Updates (auto-check +
// manual check) and an About section (version + GitHub link). Settings
// that the daemon/widget read live in the SQLite settings table and are
// read/written via the aimonitor CLI (CLIBridge); autostart uses
// SMAppService directly.

import SwiftUI
import ServiceManagement

struct PreferencesView: View {
    @ObservedObject var model: AppModel
    // Invoked by the "Check for Updates…" button; the app delegate runs the
    // check and shows the install/skip/later prompt.
    let checkForUpdates: () -> Void

    @State private var autostartOn = false
    @State private var autostartError: String?
    @State private var autoSwapOn = false
    @State private var threshold = 80
    @State private var autoUpdateOn = true
    @State private var versionText = "—"

    private let repoURL = URL(string: "https://github.com/japananh/aimonitor")!

    var body: some View {
        Form {
            Section("Panels") {
                Toggle("Show the account list", isOn: $model.showAccountPanel)
            }
            Section("Auto-switch") {
                Toggle("Switch accounts automatically near the limit", isOn: Binding(
                    get: { autoSwapOn },
                    set: { newValue in autoSwapOn = newValue; setSetting("auto_swap.enabled", newValue) }
                ))
                .help("When on, AIMonitor switches the active account once it hits the threshold below")
                if autoSwapOn {
                    Stepper("Switch when 5-hour usage reaches \(threshold)%", value: Binding(
                        get: { threshold },
                        set: { newValue in threshold = newValue; setThreshold(newValue) }
                    ), in: 10...99, step: 5)
                    .help("If the active account's 5-hour usage reaches this, switch to an account below it (lowest wins)")
                }
                Text("Switches to the least-used account whose 5-hour usage is still below the threshold. If another credential manager is also running, AIMonitor recovers from token rotation automatically; to avoid both tools switching at once, turn this off and let the other tool drive.")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
            Section("Startup") {
                Toggle("Launch AIMonitor at login", isOn: $autostartOn)
                    .onChange(of: autostartOn) { _, newValue in
                        applyAutostart(newValue)
                    }
                if let msg = autostartError {
                    Text(msg).font(.caption2).foregroundStyle(.red)
                }
            }
            Section("Updates") {
                Toggle("Automatically check for updates", isOn: Binding(
                    get: { autoUpdateOn },
                    set: { newValue in autoUpdateOn = newValue; setSetting("auto_update.enabled", newValue) }
                ))
                .help("Check GitHub for new releases on launch and notify you")
                Text("Checks GitHub for new releases and notifies you. Updates are never installed without your confirmation.")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                Button("Check for Updates…", action: checkForUpdates)
                    .pointerCursor()
                    .help("Check for a newer version now")
            }
            Section("About") {
                Text(versionText)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
                Link("View on GitHub", destination: repoURL)
                    .pointerCursor()
                    .help("Open the AIMonitor repository in your browser")
            }
        }
        .formStyle(.grouped)
        .padding()
        .frame(width: 420, height: 520)
        .onAppear(perform: loadState)
    }

    private func loadState() {
        autostartOn = SMAppService.mainApp.status == .enabled
        // Read CLI-backed settings off the main thread (each is a short
        // shell-out); publish back on main.
        DispatchQueue.global(qos: .userInitiated).async {
            let swap = (try? CLIBridge.configGet("auto_swap.enabled")) == "true"
            // Default on: only an explicit "false" disables.
            let upd = (try? CLIBridge.configGet("auto_update.enabled")) != "false"
            let thr = Int((try? CLIBridge.configGet("auto_swap.threshold_pct")) ?? "80") ?? 80
            let ver = (try? CLIBridge.run(["version"]))?
                .trimmingCharacters(in: .whitespacesAndNewlines) ?? "version unavailable"
            DispatchQueue.main.async {
                autoSwapOn = swap
                autoUpdateOn = upd
                threshold = thr
                versionText = ver
            }
        }
    }

    private func setSetting(_ key: String, _ on: Bool) {
        DispatchQueue.global(qos: .utility).async {
            try? CLIBridge.configSet(key, on ? "true" : "false")
        }
    }

    private func setThreshold(_ pct: Int) {
        DispatchQueue.global(qos: .utility).async {
            try? CLIBridge.configSet("auto_swap.threshold_pct", String(pct))
        }
    }

    private func applyAutostart(_ enable: Bool) {
        autostartError = nil
        do {
            if enable {
                try SMAppService.mainApp.register()
            } else {
                try SMAppService.mainApp.unregister()
            }
        } catch {
            autostartError = "\(error)"
            // Revert the toggle so the UI matches reality.
            autostartOn = !enable
        }
    }
}
