// PreferencesView is opened from the menu bar's "Preferences…" item.
// Surfaces panel/auto-switch/startup toggles plus Updates (auto-check +
// manual check) and an About section (version + GitHub link). Settings
// that the daemon/widget read live in the SQLite settings table and are
// read/written via the aimonitor CLI (CLIBridge); autostart uses
// SMAppService directly.

import SwiftUI
import ServiceManagement
import AppKit

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
    // Threshold notifications (active only when auto-switch is off).
    @State private var notifyOn = true
    @State private var notifyWarn = 80
    @State private var notifyCrit = 95
    @State private var versionText = "—"
    // Last export/import result or error, shown under the Backup buttons.
    @State private var backupMessage: String?
    // Integrations (MCP) state, loaded via `mcp status --json`.
    @State private var mcpServices: [MCPServiceStatus] = []
    // Whether the first status load has finished. Drives "Loading…" vs the
    // loaded UI — without it, an empty list (load failed, or a never-connected
    // state that failed to decode) was indistinguishable from "still loading".
    @State private var mcpLoaded = false
    @State private var mcpToolCount = 0
    @State private var mcpBusy: String? = nil // service with an op in flight
    @State private var mcpError: [String: String] = [:]
    @State private var mcpTokenPrompt: String? = nil // service awaiting a pasted token
    @State private var mcpTokenInput = ""
    // Appearance preference, persisted in UserDefaults; applied via NSApp.
    @AppStorage(appThemeKey) private var appTheme = defaultAppTheme
    // Dock-icon preference, persisted in UserDefaults; applied live.
    @AppStorage(showDockIconKey) private var showDockIcon = false

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
                miniToggle("Show Dock icon", isOn: $showDockIcon)
                    .onChange(of: showDockIcon) { _, show in applyDockIconPolicy(show) }
                    .help("Also show AIMonitor in the Dock — clicking the Dock icon opens the panel. Handy when the menu-bar icon is hidden behind the notch.")
            }
            Section("Auto-switch") {
                miniToggle("Switch accounts automatically near the limit", isOn: Binding(
                    get: { autoSwapOn },
                    set: { newValue in autoSwapOn = newValue; setSetting("auto_swap.enabled", newValue) }
                ))
                .help("When on, AIMonitor switches the active account once it hits either threshold below")
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
                Text("Crossing either threshold switches to the account with the most remaining headroom.")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
            Section("Notifications") {
                miniToggle("Warn me as the active account nears its limit", isOn: Binding(
                    get: { notifyOn },
                    set: { newValue in notifyOn = newValue; setSetting("notify.enabled", newValue) }
                ))
                .help("Posts a macOS notification when the active account crosses the levels below. Active only when auto-switch is off — with it on, auto-switch's own notifications cover the same moment.")
                if notifyOn {
                    ThresholdRow(
                        label: "Warn at %:",
                        settingsKey: "notify.warn_pct",
                        value: $notifyWarn
                    )
                    .help("Whole number 1–100. A notification fires the first time the active account reaches this on either window.")
                    ThresholdRow(
                        label: "Critical at %:",
                        settingsKey: "notify.crit_pct",
                        value: $notifyCrit
                    )
                    .help("Whole number 1–100. A stronger notification fires when usage reaches this level.")
                }
                Text("Heads-up only when auto-switch is off — otherwise auto-switch's own notifications cover it.")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
            Section("MCP") {
                if !mcpLoaded {
                    Text("Loading…").font(.caption).foregroundStyle(.secondary)
                } else if mcpServices.isEmpty {
                    // Loaded but nothing came back (e.g. the CLI isn't installed
                    // or errored) — offer a retry instead of an endless spinner.
                    Text("Integrations unavailable.")
                        .font(.caption).foregroundStyle(.secondary)
                    AppTextButton("Retry") {
                        mcpLoaded = false
                        DispatchQueue.global(qos: .userInitiated).async { reloadMCP() }
                    }
                    .help("Try loading the Slack and ClickUp integration status again")
                } else {
                    ForEach(mcpServices) { svc in
                        integrationRow(svc)
                    }
                }
            }
            Section("Startup") {
                miniToggle("Launch AIMonitor at login", isOn: $autostartOn)
                    .onChange(of: autostartOn) { _, newValue in
                        applyAutostart(newValue)
                    }
                if let msg = autostartError {
                    Text(msg).font(.caption2).foregroundStyle(.red)
                }
            }
            Section("Updates") {
                miniToggle("Install updates automatically", isOn: Binding(
                    get: { autoUpdateOn },
                    set: { newValue in autoUpdateOn = newValue; setSetting("auto_update.enabled", newValue) }
                ))
                .help("On: install new releases automatically. Off: just notify you when one is available.")
                Text("AIMonitor checks GitHub for new releases (on launch and every few hours). When on, it installs them automatically and relaunches. When off, it sends a notification — click it to review and install.")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                AppTextButton("Check for updates", action: checkForUpdates)
                    .help("Check for a newer version now")
            }
            Section("Backup") {
                AppTextButton("Export", action: exportFlow)
                    .help("Save your settings to a file — optionally including account logins, encrypted with a passphrase.")
                AppTextButton("Import", action: importBundle)
                    .help("Restore settings (and credentials, if the file has them) from an export file.")
                if let msg = backupMessage {
                    Text(msg)
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                }
            }
            Section("About") {
                // Match the row labels (e.g. "Launch AIMonitor at login"): the
                // default body font + primary color, not the smaller secondary
                // caption it used to be.
                Text(versionText)
                    .textSelection(.enabled)
                Link("View on GitHub", destination: repoURL)
                    .pointerCursor()
                    .help("Open the AIMonitor repository in your browser")
                AppTextButton("Show Logs in Finder", action: revealLogs)
                    .help("Open the daemon log folder (~/Library/Logs/aimonitor) in Finder — handy when filing a bug report")
            }
        }
        .formStyle(.grouped)
        // Make every label selectable (the modifier propagates to all
        // descendant Text), so users can drag-select and ⌘C any value —
        // version, MCP identity, error strings — for a bug report. Pairs
        // with the app's Edit menu, which defines the ⌘C key equivalent.
        .textSelection(.enabled)
        // Tahoe restyle: NO pinned font, controlSize, or row-height
        // overrides — the grouped Form renders at the system's default
        // type scale (13pt body on macOS 26) so the window matches
        // System Settings exactly. The old 12pt/.small/compact-row pins
        // made everything read smaller than the OS.
        .frame(width: 460, height: 640)
        .onAppear(perform: loadState)
    }

    // miniToggle renders "label … switch". The switch sits at .small —
    // the size System Settings uses on Tahoe — with the label at the
    // row's default font (only the control is sized, never the text).
    private func miniToggle(_ label: String, isOn: Binding<Bool>) -> some View {
        HStack {
            Text(label)
            Spacer()
            Toggle("", isOn: isOn)
                .labelsHidden()
                .toggleStyle(.switch)
                .controlSize(.small)
                .pointerCursor()
        }
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
            let notif = (try? CLIBridge.configGet("notify.enabled")) != "false"
            let warn = Int((try? CLIBridge.configGet("notify.warn_pct")) ?? "80") ?? 80
            let crit = Int((try? CLIBridge.configGet("notify.crit_pct")) ?? "95") ?? 95
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
                notifyOn = notif
                notifyWarn = warn
                notifyCrit = crit
                versionText = ver
            }
            reloadMCP()
        }
    }

    private func setSetting(_ key: String, _ on: Bool) {
        DispatchQueue.global(qos: .utility).async {
            try? CLIBridge.configSet(key, on ? "true" : "false")
        }
    }

    // revealLogs opens Finder at the daemon log folder
    // (~/Library/Logs/aimonitor), selecting the main err.log when it
    // exists so the user lands right on the file to attach to a bug
    // report. Falls back to the folder, then the Logs root, so the button
    // always opens *somewhere* sensible even before the daemon has logged.
    private func revealLogs() {
        let fm = FileManager.default
        guard let lib = fm.urls(for: .libraryDirectory, in: .userDomainMask).first else { return }
        let logsRoot = lib.appendingPathComponent("Logs")
        let dir = logsRoot.appendingPathComponent("aimonitor")
        let daemonLog = dir.appendingPathComponent("aimonitor.daemon.log")
        let target: URL
        if fm.fileExists(atPath: daemonLog.path) {
            target = daemonLog
        } else if fm.fileExists(atPath: dir.path) {
            target = dir
        } else {
            target = logsRoot
        }
        NSWorkspace.shared.activateFileViewerSelecting([target])
    }

    // MARK: - Backup (export / import)

    // exportFlow is the single entry point: pick what to export (Bitwarden-style
    // — one Export action, choose mode), then run the matching handler.
    private func exportFlow() {
        let alert = NSAlert()
        alert.messageText = "Export configuration"
        alert.informativeText = "“Settings only” is safe to share. “With credentials” also bundles your account logins, encrypted with a passphrase — that file is a live login, keep it safe."
        alert.addButton(withTitle: "Settings Only")
        alert.addButton(withTitle: "With Credentials")
        alert.addButton(withTitle: "Cancel")
        switch alert.runModal() {
        case .alertFirstButtonReturn:
            exportSettings()
        case .alertSecondButtonReturn:
            exportWithTokens()
        default:
            break
        }
    }

    private func exportSettings() {
        guard let url = backupSavePanel(defaultName: defaultBackupName()) else { return }
        runBackup(reload: false) {
            try CLIBridge.configExport(to: url.path, includeTokens: false, passphrase: nil)
            return "Exported settings to \(url.lastPathComponent)."
        }
    }

    private func exportWithTokens() {
        guard let pass = passphrasePrompt(
            title: "Choose a passphrase",
            info: "The export will include your account credentials, encrypted with this passphrase. You'll need the same passphrase to import — it can't be recovered."
        ) else { return }
        if pass.isEmpty { backupMessage = "Export cancelled: passphrase was empty."; return }
        guard let url = backupSavePanel(defaultName: defaultBackupName()) else { return }
        runBackup(reload: false) {
            try CLIBridge.configExport(to: url.path, includeTokens: true, passphrase: pass)
            return "Exported settings + encrypted credentials to \(url.lastPathComponent)."
        }
    }

    private func importBundle() {
        guard let url = backupOpenPanel() else { return }
        var passphrase: String?
        if bundleHasTokens(url) {
            guard let p = passphrasePrompt(
                title: "Enter the passphrase",
                info: "This file contains encrypted credentials. Enter the passphrase used when it was exported."
            ) else { return }
            passphrase = p
        }
        runBackup(reload: true) {
            let out = try CLIBridge.configImport(from: url.path, passphrase: passphrase)
            let summary = out.trimmingCharacters(in: .whitespacesAndNewlines)
            return summary.isEmpty ? "Imported \(url.lastPathComponent)." : summary
        }
    }

    // runBackup runs a CLI backup op off the main thread, then publishes the
    // result (or error) to backupMessage. reload=true re-reads settings after
    // an import so the toggles reflect the restored values.
    private func runBackup(reload: Bool, _ work: @escaping () throws -> String) {
        backupMessage = "Working…"
        DispatchQueue.global(qos: .userInitiated).async {
            let message: String
            do { message = try work() } catch {
                DispatchQueue.main.async { backupMessage = "Failed: \(error.localizedDescription)" }
                return
            }
            DispatchQueue.main.async {
                backupMessage = message
                if reload { loadState() }
            }
        }
    }

    // Default export filename: aimonitor-<unix epoch millis>.json — unique per
    // export so successive backups don't silently overwrite.
    private func defaultBackupName() -> String {
        let ms = Int(Date().timeIntervalSince1970 * 1000)
        return "aimonitor-\(ms).json"
    }

    private func backupSavePanel(defaultName: String) -> URL? {
        let panel = NSSavePanel()
        panel.nameFieldStringValue = defaultName
        panel.canCreateDirectories = true
        panel.title = "Export aimonitor configuration"
        return panel.runModal() == .OK ? panel.url : nil
    }

    private func backupOpenPanel() -> URL? {
        let panel = NSOpenPanel()
        panel.canChooseFiles = true
        panel.canChooseDirectories = false
        panel.allowsMultipleSelection = false
        panel.title = "Import aimonitor configuration"
        return panel.runModal() == .OK ? panel.url : nil
    }

    // passphrasePrompt shows a modal with a masked field; nil on Cancel.
    private func passphrasePrompt(title: String, info: String) -> String? {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = info
        alert.addButton(withTitle: "OK")
        alert.addButton(withTitle: "Cancel")
        let field = NSSecureTextField(frame: NSRect(x: 0, y: 0, width: 260, height: 24))
        alert.accessoryView = field
        alert.window.initialFirstResponder = field
        return alert.runModal() == .alertFirstButtonReturn ? field.stringValue : nil
    }

    // bundleHasTokens peeks at the JSON for an encrypted-credentials block, so
    // import only asks for a passphrase when one is actually needed.
    private func bundleHasTokens(_ url: URL) -> Bool {
        guard let data = try? Data(contentsOf: url),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            return false
        }
        return (obj["encrypted"] as? Bool) == true
    }

    // integrationRow renders one service: status line, Connect/Disconnect,
    // Enabled + Read-only toggles, inline token paste when migration fails.
    // integrationRow emits SIBLING form rows (no wrapping VStack: a nested
    // container loses the grouped Form's compact-switch row treatment).
    // Inside the single "MCP" section, each service gets a bold sub-header
    // row; its Enabled/Read-only rows are indented underneath it.
    @ViewBuilder
    private func integrationRow(_ svc: MCPServiceStatus) -> some View {
        HStack {
            Text(svc.service == "slack" ? "Slack" : "ClickUp").bold()
            Spacer()
            if svc.connected, let ident = svc.identity {
                Text(ident).font(.caption).foregroundStyle(.secondary)
            } else if !svc.connected {
                Text("Not connected").font(.caption).foregroundStyle(.secondary)
            }
            if svc.connected {
                AppTextButton("Disconnect") { mcpDisconnect(svc.service) }
                    .disabled(mcpBusy != nil)
            } else {
                AppTextButton(mcpBusy == svc.service ? "Connecting…" : "Connect") { mcpConnect(svc.service) }
                    .disabled(mcpBusy != nil)
            }
        }
        if let err = mcpError[svc.service] {
            Text(err).font(.caption2).foregroundStyle(.red).textSelection(.enabled)
        }
        if mcpTokenPrompt == svc.service {
            HStack(spacing: 6) {
                SecureField(svc.service == "slack" ? "xoxp-… user token" : "pk_… personal token", text: $mcpTokenInput)
                    .textFieldStyle(.roundedBorder)
                AppTextButton("Verify & Save") { mcpConnectWithToken(svc.service) }
                    .disabled(mcpTokenInput.trimmingCharacters(in: .whitespaces).isEmpty || mcpBusy != nil)
                    .pointerCursor()
            }
        }
        if svc.connected {
            miniToggle("Enabled", isOn: mcpBinding(svc, keyPath: \.enabled, settingSuffix: "enabled"))
                .padding(.leading, 16)
                .help("Off hides every \(svc.service) tool from Claude")
            miniToggle("Read-only", isOn: mcpBinding(svc, keyPath: \.read_only, settingSuffix: "read_only"))
                .padding(.leading, 16)
                .help("On hides write tools (post/create/update/comment) from Claude entirely")
        }
    }

    // mcpBinding maps a service flag to its mcp.<svc>.<suffix> setting and
    // refreshes the local snapshot after writing.
    private func mcpBinding(_ svc: MCPServiceStatus, keyPath: KeyPath<MCPServiceStatus, Bool>, settingSuffix: String) -> Binding<Bool> {
        Binding(
            // Read from the LIVE array, not the captured row: the captured
            // snapshot goes stale after the async reload, and a quick second
            // click against a stale get() wrote inverted values to the WRONG
            // state (observed: disabling ClickUp also flipped Slack off).
            get: {
                mcpServices.first(where: { $0.service == svc.service })?[keyPath: keyPath] ?? svc[keyPath: keyPath]
            },
            set: { newValue in
                // Optimistic local update so the toggle reflects the click
                // immediately; the settings write + reload reconcile after.
                if let i = mcpServices.firstIndex(where: { $0.service == svc.service }) {
                    var updated = mcpServices[i]
                    updated = MCPServiceStatus(
                        service: updated.service,
                        connected: updated.connected,
                        identity: updated.identity,
                        error: updated.error,
                        enabled: settingSuffix == "enabled" ? newValue : updated.enabled,
                        read_only: settingSuffix == "read_only" ? newValue : updated.read_only
                    )
                    mcpServices[i] = updated
                }
                let key = "mcp.\(svc.service).\(settingSuffix)"
                DispatchQueue.global(qos: .userInitiated).async {
                    try? CLIBridge.configSet(key, newValue ? "true" : "false")
                    reloadMCP()
                }
            }
        )
    }

    // reloadMCP fetches `mcp status --json` (a blocking shell-out — callers run
    // it off the main thread). It always flips mcpLoaded true, even on failure,
    // so the section leaves "Loading…" and shows either the services or the
    // unavailable/retry state rather than spinning forever.
    private func reloadMCP() {
        let result = try? CLIBridge.mcpStatus()
        DispatchQueue.main.async {
            if let st = result {
                mcpServices = st.services
                mcpToolCount = st.tools.count
            }
            mcpLoaded = true
        }
    }

    private func mcpConnect(_ service: String) {
        mcpBusy = service
        mcpError[service] = nil
        DispatchQueue.global(qos: .userInitiated).async {
            defer { DispatchQueue.main.async { mcpBusy = nil } }
            do {
                _ = try CLIBridge.mcpConnect(service: service)
                reloadMCP()
            } catch {
                // No migratable claude-bar token → ask for a pasted one.
                DispatchQueue.main.async {
                    mcpTokenPrompt = service
                    mcpTokenInput = ""
                    mcpError[service] = "No claude-bar token to migrate — paste a token below."
                }
            }
        }
    }

    private func mcpConnectWithToken(_ service: String) {
        let token = mcpTokenInput.trimmingCharacters(in: .whitespaces)
        mcpBusy = service
        mcpError[service] = nil
        DispatchQueue.global(qos: .userInitiated).async {
            defer { DispatchQueue.main.async { mcpBusy = nil } }
            do {
                _ = try CLIBridge.mcpConnect(service: service, token: token)
                DispatchQueue.main.async {
                    mcpTokenPrompt = nil
                    mcpTokenInput = ""
                }
                reloadMCP()
            } catch {
                DispatchQueue.main.async { mcpError[service] = "\(error)" }
            }
        }
    }

    private func mcpDisconnect(_ service: String) {
        mcpBusy = service
        DispatchQueue.global(qos: .userInitiated).async {
            defer { DispatchQueue.main.async { mcpBusy = nil } }
            try? CLIBridge.mcpDisconnect(service: service)
            reloadMCP()
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
