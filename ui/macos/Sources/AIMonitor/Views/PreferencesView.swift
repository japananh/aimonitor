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
    // The threshold field is edited as text so we can validate the raw input
    // (reject <=0 / >100 / non-numeric) instead of silently clamping.
    @State private var thresholdText = "80"
    @State private var thresholdError: String?
    @State private var showSaved = false
    // Generation token so overlapping 2s "Saved" timers don't hide a newer one.
    @State private var savedToken = 0
    @FocusState private var thresholdFocused: Bool
    @State private var autoUpdateOn = true
    @State private var versionText = "—"
    // Appearance preference, persisted in UserDefaults; applied via NSApp.
    @AppStorage(appThemeKey) private var appTheme = defaultAppTheme

    private let repoURL = URL(string: "https://github.com/japananh/aimonitor")!

    var body: some View {
        Form {
            Section("Appearance") {
                Picker("Theme", selection: $appTheme) {
                    Text("System").tag("system")
                        .help("Follows your macOS appearance")
                    Text("Light").tag("light")
                    Text("Dark").tag("dark")
                }
                .pickerStyle(.segmented)
                .onChange(of: appTheme) { _, newValue in applyAppearance(newValue) }
            }
            Section("Auto-switch") {
                Toggle("Switch accounts automatically near the limit", isOn: Binding(
                    get: { autoSwapOn },
                    set: { newValue in autoSwapOn = newValue; setSetting("auto_swap.enabled", newValue) }
                ))
                .help("When on, AIMonitor switches the active account once it hits the threshold below")
                if autoSwapOn {
                    // Description on its own line; the compact controls on the
                    // next line. Keeping them apart avoids the row overflowing
                    // and wrapping the field onto a second line (which looked
                    // like the field "dropped below" the others).
                    VStack(alignment: .leading, spacing: 4) {
                        // One line: label, input, stepper. .firstTextBaseline
                        // aligns the label's text with the field's text; the
                        // label is fixedSize + single-line so it never wraps and
                        // pushes the field onto a second line. A concise label
                        // keeps the whole row inside the window width.
                        // All three get the same 22pt height so .center makes
                        // their vertical centers coincide exactly (baseline
                        // alignment left the stepper sitting off-center).
                        HStack(alignment: .center, spacing: 6) {
                            Text("Switch when 5h or 7d usage reaches %:")
                                .fixedSize(horizontal: true, vertical: false)
                                .frame(height: 22)
                            // Bordered field so it visibly reads as editable.
                            // Empty title: in a Form a non-empty title renders
                            // as a separate label beside the field (a stray
                            // duplicate number).
                            TextField("", text: $thresholdText)
                                .textFieldStyle(.roundedBorder)
                                .frame(width: 60)
                                .multilineTextAlignment(.trailing)
                                .focused($thresholdFocused)
                                // Validate live as the user types so the error
                                // appears immediately — but only SAVE on Enter
                                // or when focus leaves the field.
                                .onChange(of: thresholdText) { _, _ in validateThreshold() }
                                .onSubmit { commitThreshold() }
                                .onChange(of: thresholdFocused) { _, focused in
                                    if focused {
                                        // Editing resumed — drop a stale "Saved".
                                        showSaved = false
                                    } else {
                                        commitThreshold()
                                    }
                                }
                                .pointerCursor()
                            Stepper("", value: thresholdStepper, in: 1...100)
                                .labelsHidden()
                                .frame(height: 22)
                        }
                        .help("Any whole number from 1 to 100. When the active account's 5-hour OR 7-day usage reaches it, AIMonitor switches to the account with the most remaining headroom.")
                        // Inline feedback: red validation error, or a green
                        // "Saved" that auto-hides after 2 seconds.
                        if let err = thresholdError {
                            Text(err)
                                .font(.caption2)
                                .foregroundStyle(.red)
                        } else if showSaved {
                            Text("Saved")
                                .font(.caption2)
                                .foregroundStyle(.green)
                        }
                    }
                }
                Text("Triggers on either the 5-hour or the 7-day limit and picks the account with the most remaining headroom — escaping a weekly-capped account even when the alternatives are only 5-hour-hot (5-hour windows recover in hours; weekly caps last days). If another credential manager is also running, AIMonitor recovers from token rotation automatically; to avoid both tools switching at once, turn this off and let the other tool drive.")
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
            // Just the version number for About — no commit/build date.
            // (The `aimonitor version` CLI still prints those for diagnostics.)
            var ver = "version unavailable"
            if let out = try? CLIBridge.run(["version", "--json"]),
               let data = out.data(using: .utf8),
               let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
               let v = obj["version"] as? String {
                ver = "aimonitor \(v)"
            }
            DispatchQueue.main.async {
                autoSwapOn = swap
                autoUpdateOn = upd
                threshold = thr
                thresholdText = String(thr)
                versionText = ver
            }
        }
    }

    private func setSetting(_ key: String, _ on: Bool) {
        DispatchQueue.global(qos: .utility).async {
            try? CLIBridge.configSet(key, on ? "true" : "false")
        }
    }

    // validateThreshold updates the inline error as the user types, without
    // saving. It's the live-feedback counterpart to commitThreshold (which
    // persists on Enter / blur).
    private func validateThreshold() {
        let trimmed = thresholdText.trimmingCharacters(in: .whitespaces)
        if let v = Int(trimmed), v > 0, v <= 100 {
            thresholdError = nil
        } else {
            thresholdError = "Value must be > 0 and <= 100"
        }
    }

    // commitThreshold validates the raw text and, only when valid, persists it
    // and flashes "Saved". Invalid input (non-numeric, <=0, >100) shows an
    // error and is NOT saved. Called on Enter and when the field loses focus.
    private func commitThreshold() {
        let trimmed = thresholdText.trimmingCharacters(in: .whitespaces)
        guard let v = Int(trimmed), v > 0, v <= 100 else {
            thresholdError = "Value must be > 0 and <= 100"
            return
        }
        thresholdError = nil
        // Normalise the displayed text (e.g. "080" -> "80").
        thresholdText = String(v)
        threshold = v
        DispatchQueue.global(qos: .utility).async {
            try? CLIBridge.configSet("auto_swap.threshold_pct", String(v))
        }
        flashSaved()
    }

    // flashSaved shows the green "Saved" note for 2 seconds. The generation
    // token ensures a rapid second save doesn't get hidden early by the first
    // save's timer.
    private func flashSaved() {
        showSaved = true
        savedToken += 1
        let token = savedToken
        DispatchQueue.main.asyncAfter(deadline: .now() + 2) {
            if token == savedToken { showSaved = false }
        }
    }

    // thresholdStepper drives the +/- control. It's always within 1...100, so
    // it routes through commitThreshold (which then saves + flashes "Saved").
    private var thresholdStepper: Binding<Int> {
        Binding(
            get: { Int(thresholdText) ?? threshold },
            set: { newValue in
                thresholdText = String(min(max(newValue, 1), 100))
                commitThreshold()
            }
        )
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
