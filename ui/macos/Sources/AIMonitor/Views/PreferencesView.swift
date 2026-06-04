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
    // Auto-swap thresholds, one per rate-limit window. Loaded from the
    // settings table; edited via the ThresholdRow fields below.
    @State private var threshold5h = 80
    @State private var threshold7d = 80
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
                .pointerCursor()
            }
            Section("Auto-switch") {
                Toggle("Switch accounts automatically near the limit", isOn: Binding(
                    get: { autoSwapOn },
                    set: { newValue in autoSwapOn = newValue; setSetting("auto_swap.enabled", newValue) }
                ))
                .help("When on, AIMonitor switches the active account once it hits either threshold below")
                .pointerCursor()
                if autoSwapOn {
                    ThresholdRow(
                        label: "Switch when 5h usage reaches %:",
                        settingsKey: "auto_swap.threshold_pct",
                        value: $threshold5h
                    )
                    .help("Any whole number from 1 to 100. When the active account's 5-hour usage reaches it, AIMonitor switches to the account with the most remaining headroom.")
                    ThresholdRow(
                        label: "Switch when 7d usage reaches %:",
                        settingsKey: "auto_swap.threshold_7d_pct",
                        value: $threshold7d
                    )
                    .help("Any whole number from 1 to 100. When the active account's 7-day usage reaches it, AIMonitor switches — even if the alternatives are 5-hour-hot, since weekly caps last days while 5-hour windows recover in hours.")
                }
                Text("Each window has its own threshold; crossing either one triggers a switch to the account with the most remaining headroom — escaping a weekly-capped account even when the alternatives are only 5-hour-hot. If another credential manager is also running, AIMonitor recovers from token rotation automatically; to avoid both tools switching at once, turn this off and let the other tool drive.")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
            Section("Startup") {
                Toggle("Launch AIMonitor at login", isOn: $autostartOn)
                    .onChange(of: autostartOn) { _, newValue in
                        applyAutostart(newValue)
                    }
                    .pointerCursor()
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
                .pointerCursor()
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
        // No extra outer padding: the grouped form style already insets its
        // sections; doubling it wrapped everything in a thick margin (the
        // main popover gets by with 12px).
        .frame(width: 420, height: 560)
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
            let thr5 = Int((try? CLIBridge.configGet("auto_swap.threshold_pct")) ?? "80") ?? 80
            let thr7 = Int((try? CLIBridge.configGet("auto_swap.threshold_7d_pct")) ?? "80") ?? 80
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
                threshold5h = thr5
                threshold7d = thr7
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

// ThresholdRow is one "<label> <input> <stepper>" line with live validation,
// save-on-blur/Enter, and a transient green "Saved" note. Used for the 5h
// and 7d auto-swap thresholds; persists to the given settings key via the
// CLI. The field is edited as raw text so bad input (<=0, >100, non-numeric)
// shows an error while typing instead of being silently clamped.
private struct ThresholdRow: View {
    let label: String
    let settingsKey: String
    // The loaded/persisted value. loadState publishes into it after the
    // async settings read; commits write back through it.
    @Binding var value: Int

    @State private var text = ""
    @State private var error: String?
    @State private var saved = false
    // Generation token so overlapping 2s "Saved" timers don't hide a newer one.
    @State private var savedToken = 0
    @FocusState private var focused: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            // All three get the same 22pt height so .center makes their
            // vertical centers coincide exactly. The label is fixedSize +
            // single-line so it never wraps and pushes the field down.
            HStack(alignment: .center, spacing: 6) {
                Text(label)
                    .fixedSize(horizontal: true, vertical: false)
                    .frame(height: 22)
                // Bordered field so it visibly reads as editable. Empty
                // title: in a Form a non-empty title renders as a separate
                // label beside the field (a stray duplicate number).
                TextField("", text: $text)
                    .textFieldStyle(.roundedBorder)
                    .frame(width: 60)
                    .multilineTextAlignment(.trailing)
                    .focused($focused)
                    // Validate live as the user types so the error appears
                    // immediately — but only SAVE on Enter or blur.
                    .onChange(of: text) { _, _ in validate() }
                    .onSubmit { commit() }
                    .onChange(of: focused) { _, isFocused in
                        if isFocused {
                            // Editing resumed — drop a stale "Saved".
                            saved = false
                        } else {
                            commit()
                        }
                    }
                    // No pointerCursor() here: a forced pointing-hand
                    // suppresses AppKit's native I-beam over text fields,
                    // which is the cue that the field is typeable.
                Stepper("", value: stepper, in: 1...100)
                    .labelsHidden()
                    .frame(height: 22)
            }
            // Inline feedback: red validation error, or a green "Saved"
            // that auto-hides after 2 seconds.
            if let err = error {
                Text(err)
                    .font(.caption2)
                    .foregroundStyle(.red)
            } else if saved {
                Text("Saved")
                    .font(.caption2)
                    .foregroundStyle(.green)
            }
        }
        .onAppear { text = String(value) }
        .onChange(of: value) { _, v in
            // The async settings load publishes after onAppear; don't stomp
            // the user's in-progress edit.
            if !focused { text = String(v) }
        }
    }

    // validate updates the inline error as the user types, without saving.
    private func validate() {
        if parsed() != nil {
            error = nil
        } else {
            error = "Value must be > 0 and <= 100"
        }
    }

    // commit validates the raw text and, only when valid, persists it and
    // flashes "Saved". Invalid input shows the error and is NOT saved.
    private func commit() {
        guard let v = parsed() else {
            error = "Value must be > 0 and <= 100"
            return
        }
        error = nil
        // Normalise the displayed text (e.g. "080" -> "80").
        text = String(v)
        value = v
        let key = settingsKey
        DispatchQueue.global(qos: .utility).async {
            try? CLIBridge.configSet(key, String(v))
        }
        flashSaved()
    }

    private func parsed() -> Int? {
        let trimmed = text.trimmingCharacters(in: .whitespaces)
        guard let v = Int(trimmed), v > 0, v <= 100 else { return nil }
        return v
    }

    private func flashSaved() {
        saved = true
        savedToken += 1
        let token = savedToken
        DispatchQueue.main.asyncAfter(deadline: .now() + 2) {
            if token == savedToken { saved = false }
        }
    }

    // stepper drives the +/- control. Always within 1...100, so it routes
    // straight through commit (save + "Saved").
    private var stepper: Binding<Int> {
        Binding(
            get: { Int(text) ?? value },
            set: { newValue in
                text = String(min(max(newValue, 1), 100))
                commit()
            }
        )
    }
}
