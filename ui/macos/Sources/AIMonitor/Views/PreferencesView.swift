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
    @State private var autoUpdateOn = true
    @State private var versionText = "—"

    private let repoURL = URL(string: "https://github.com/japananh/aimonitor")!

    var body: some View {
        Form {
            Section("Panels") {
                Toggle("Show per-account headroom panel", isOn: $model.showAccountPanel)
            }
            Section("Auto-switch") {
                Toggle("Switch accounts automatically near the limit", isOn: Binding(
                    get: { autoSwapOn },
                    set: { newValue in autoSwapOn = newValue; setSetting("auto_swap.enabled", newValue) }
                ))
                Text("When on, AIMonitor switches to the least-used account once the active one passes the threshold (default 80%). Leave this off if you also run claude-bar, so the two don't both switch the active account.")
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
                Text("Checks GitHub for new releases and notifies you. Updates are never installed without your confirmation.")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                Button("Check for Updates…", action: checkForUpdates)
            }
            Section("About") {
                Text(versionText)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
                Link("View on GitHub", destination: repoURL)
            }
        }
        .formStyle(.grouped)
        .padding()
        .frame(width: 400, height: 480)
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
            let ver = (try? CLIBridge.run(["version"]))?
                .trimmingCharacters(in: .whitespacesAndNewlines) ?? "version unavailable"
            DispatchQueue.main.async {
                autoSwapOn = swap
                autoUpdateOn = upd
                versionText = ver
            }
        }
    }

    private func setSetting(_ key: String, _ on: Bool) {
        DispatchQueue.global(qos: .utility).async {
            try? CLIBridge.configSet(key, on ? "true" : "false")
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
