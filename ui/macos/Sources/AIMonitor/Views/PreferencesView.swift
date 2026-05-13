// PreferencesView is opened from the menu bar's "Preferences…" item.
// Surfaces the panel-toggle + auto-switch toggle + autostart toggle.
// Autostart in v1 lives behind a small shell-out helper (see Phase 4
// internal/install/autostart_darwin.go on the Go side).

import SwiftUI
import ServiceManagement

struct PreferencesView: View {
    @ObservedObject var model: AppModel
    @State private var autostartOn: Bool = false
    @State private var autostartError: String?

    var body: some View {
        Form {
            Section("Panels") {
                Toggle("Show per-account headroom panel", isOn: $model.showAccountPanel)
            }
            Section("Auto-switch") {
                let enabled = Binding<Bool>(
                    get: { model.status?.auto_switch_enabled ?? false },
                    set: { model.setAutoSwitch($0) }
                )
                Toggle("Enable auto-switch on tripwire crossings", isOn: enabled)
                Text("Auto-switch fires when the active session crosses a configured threshold (default 40%, 60%, 100%) AND a candidate account has strictly more server-side headroom.")
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
        }
        .formStyle(.grouped)
        .padding()
        .frame(width: 380, height: 320)
        .onAppear(perform: loadAutostartState)
    }

    private func loadAutostartState() {
        // SMAppService.mainApp reports current registration status. For
        // the v1 widget we register the menu bar binary itself (not a
        // separate helper bundle) — adequate since we have no helper to
        // launch separately.
        autostartOn = SMAppService.mainApp.status == .enabled
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
