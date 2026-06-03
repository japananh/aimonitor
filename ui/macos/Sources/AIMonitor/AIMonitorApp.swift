// AIMonitorApp is the @main entry point. It bootstraps an
// NSApplication, installs an NSStatusItem in the menu bar, and shows a
// SwiftUI panel under the icon when clicked.
//
// Why a borderless NSPanel rather than NSPopover: NSPopover always draws
// an anchoring arrow (caret) pointing at the status item, with no public
// way to hide it. A borderless, key-capable NSPanel gives the same
// dropdown without the arrow — we position it under the icon ourselves
// and dismiss it on an outside click.

import AppKit
import Combine
import SwiftUI

@main
struct AIMonitorAppEntry {
    static func main() {
        // Force NSApplicationDelegate-style lifecycle so we control
        // termination + status item ourselves.
        let app = NSApplication.shared
        let delegate = AppDelegate()
        app.delegate = delegate
        // .accessory means no Dock icon (mirrors LSUIElement=true in
        // the Info.plist; setting it twice is harmless).
        app.setActivationPolicy(.accessory)
        app.run()
    }
}

@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    private var statusItem: NSStatusItem!
    // A borderless NSPanel (not NSPopover) so there's no anchoring arrow.
    private var panel: NSPanel!
    private var clickMonitor: Any?
    private var preferencesWindow: NSWindow?
    private let model = AppModel()
    private var cancellables = Set<AnyCancellable>()

    func applicationDidFinishLaunching(_ notification: Notification) {
        setupStatusItem()
        setupPanel()
        // Keep the menu-bar title in sync with the active account. status
        // carries active_label and accounts carries the identity, so the
        // title (icon + account name) recomputes whenever either changes.
        model.$status.combineLatest(model.$accounts)
            .receive(on: RunLoop.main)
            .sink { [weak self] _, _ in self?.updateStatusTitle() }
            .store(in: &cancellables)
        Task { @MainActor in self.model.start() }
        // Auto-check for updates shortly after launch when enabled. Silent
        // (no alert) unless an update is available and not skipped. The delay
        // keeps startup snappy and lets the daemon settle first.
        DispatchQueue.main.asyncAfter(deadline: .now() + 4) { [weak self] in
            self?.checkForUpdates(userInitiated: false)
        }
    }

    private func showPreferences() {
        closePanel()
        if preferencesWindow == nil {
            let view = PreferencesView(
                model: model,
                checkForUpdates: { [weak self] in self?.checkForUpdates(userInitiated: true) }
            )
            let host = NSHostingController(rootView: view)
            let win = NSWindow(contentViewController: host)
            win.title = "AIMonitor Preferences"
            win.styleMask = [.titled, .closable]
            win.isReleasedWhenClosed = false
            preferencesWindow = win
        }
        preferencesWindow?.center()
        preferencesWindow?.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    private func setupStatusItem() {
        statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
        if let button = statusItem.button {
            // SF Symbol "chart.bar.fill" looks like a usage bar; the menu
            // bar template treatment keeps it monochromatic so the icon
            // adapts to dark/light menu bar appearance automatically.
            button.image = NSImage(systemSymbolName: "chart.bar.fill",
                                   accessibilityDescription: "AIMonitor")
            button.image?.isTemplate = true
            // Keep the icon to the left of the title text (account name).
            button.imagePosition = .imageLeading
            button.action = #selector(togglePopover(_:))
            button.target = self
        }
    }

    // updateStatusTitle shows the active account name to the right of the
    // menu-bar icon. Empty title → icon only (no active account, or the
    // daemon hasn't published yet). A leading space separates the glyph
    // from the text since imagePosition gives no built-in padding.
    private func updateStatusTitle() {
        guard let button = statusItem.button else { return }
        let name = model.activeDisplayName
        button.title = name.isEmpty ? "" : " \(name)"
    }

    private func setupPanel() {
        // A borderless panel has no popover arrow. Rounded material + shadow
        // give it the popover look without the caret pointing at the icon.
        let root = PopoverRootView(
            model: model,
            openPreferences: { [weak self] in self?.showPreferences() },
            quit: { NSApplication.shared.terminate(nil) },
            renameAccount: { [weak self] label in self?.promptRename(currentLabel: label) },
            importAccount: { [weak self] email in self?.promptImportCurrent(email: email) }
        )
        // Solid window background — NOT .regularMaterial, whose vibrancy
        // desaturates the foreground (the colored 5h/7d bars, the green
        // active check, red errors). Rounded + shadowed via clip + panel.
        .background(Color(nsColor: .windowBackgroundColor))
        .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))

        let hosting = NSHostingController(rootView: root)
        // Let the panel resize to the SwiftUI content (rows, banners, error
        // lines all change the height).
        hosting.sizingOptions = [.preferredContentSize]

        let p = NSPanel(contentViewController: hosting)
        p.styleMask = [.borderless, .nonactivatingPanel]
        p.isFloatingPanel = true
        p.level = .popUpMenu
        p.backgroundColor = .clear
        p.isOpaque = false
        p.hasShadow = true
        p.hidesOnDeactivate = false
        p.isReleasedWhenClosed = false
        p.isMovable = false
        p.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary, .transient]
        panel = p

        // Keep the panel anchored under the icon when its height changes
        // (e.g. an error row appears) — reposition keeps the TOP edge fixed.
        NotificationCenter.default.addObserver(
            self, selector: #selector(panelDidResize),
            name: NSWindow.didResizeNotification, object: p)
    }

    @objc private func panelDidResize(_ note: Notification) {
        if panel.isVisible { positionPanel() }
    }

    @objc private func togglePopover(_ sender: Any?) {
        if panel.isVisible {
            closePanel()
            return
        }
        // Fresh data the moment it opens, without waiting for the 2s tick.
        Task { @MainActor in await model.refresh() }
        // Activate so the status item's window geometry is realised before we
        // read it for positioning (same reason the old popover needed it).
        NSApp.activate(ignoringOtherApps: true)
        positionPanel()
        panel.makeKeyAndOrderFront(nil)
        installClickMonitor()
    }

    // positionPanel places the panel just below the menu-bar icon, its right
    // edge aligned to the icon's right edge, clamped to the visible screen.
    // Anchored by its TOP so height changes grow downward.
    private func positionPanel() {
        guard let button = statusItem.button, let bwin = button.window else { return }
        let b = bwin.convertToScreen(button.convert(button.bounds, to: nil))
        let size = panel.frame.size
        let gap: CGFloat = 6
        var x = b.maxX - size.width
        var y = b.minY - gap - size.height // origin is bottom-left; top sits gap below the icon
        if let screen = bwin.screen ?? NSScreen.main {
            let vf = screen.visibleFrame
            x = max(vf.minX + 4, min(x, vf.maxX - size.width - 4))
            y = max(vf.minY + 4, y)
        }
        panel.setFrameOrigin(NSPoint(x: x, y: y))
    }

    // installClickMonitor dismisses the panel when the user clicks anywhere
    // outside it (the global monitor only sees events outside our app's
    // windows; clicks inside the panel don't fire it).
    private func installClickMonitor() {
        guard clickMonitor == nil else { return }
        clickMonitor = NSEvent.addGlobalMonitorForEvents(matching: [.leftMouseDown, .rightMouseDown]) { [weak self] _ in
            guard let self else { return }
            // A click on our own status item must be left for togglePopover to
            // handle (it closes the panel); closing here too would let the
            // button action then re-open it. Only outside clicks dismiss.
            if let button = self.statusItem.button, let bwin = button.window {
                let btnScreen = bwin.convertToScreen(button.convert(button.bounds, to: nil))
                if btnScreen.contains(NSEvent.mouseLocation) { return }
            }
            self.closePanel()
        }
    }

    private func closePanel() {
        if let m = clickMonitor {
            NSEvent.removeMonitor(m)
            clickMonitor = nil
        }
        panel?.orderOut(nil)
    }

    // promptRename shows a modal text field pre-filled with the current
    // label and renames on confirm. NSAlert is used (rather than a SwiftUI
    // alert inside the popover) because the transient popover dismisses as
    // soon as the modal takes focus, which would tear down a SwiftUI alert.
    private func promptRename(currentLabel: String) {
        closePanel()
        NSApp.activate(ignoringOtherApps: true)

        let alert = NSAlert()
        alert.messageText = "Rename account"
        alert.informativeText = "New name for “\(currentLabel)”:"
        alert.addButton(withTitle: "Rename")
        alert.addButton(withTitle: "Cancel")

        let field = NSTextField(frame: NSRect(x: 0, y: 0, width: 220, height: 24))
        field.stringValue = currentLabel
        alert.accessoryView = field
        alert.window.initialFirstResponder = field

        if alert.runModal() == .alertFirstButtonReturn {
            let newLabel = field.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)
            if !newLabel.isEmpty, newLabel != currentLabel {
                model.rename(label: currentLabel, to: newLabel)
            }
        }
    }

    // promptImportCurrent offers to import the account currently signed into
    // the live slot (one another app/`claude /login` created). The label
    // defaults to the email's local part. `add --adopt-current` captures the
    // live blob without changing the active account; on failure (e.g. label
    // already taken) the CLI error is surfaced so the user can retry.
    private func promptImportCurrent(email: String) {
        closePanel()
        NSApp.activate(ignoringOtherApps: true)

        let alert = NSAlert()
        alert.messageText = "Import account"
        alert.informativeText = "Claude is signed into \(email), which AIMonitor doesn’t manage yet. Give it a label to import it:"
        alert.addButton(withTitle: "Import")
        alert.addButton(withTitle: "Cancel")

        let field = NSTextField(frame: NSRect(x: 0, y: 0, width: 220, height: 24))
        field.stringValue = String(email.split(separator: "@").first ?? Substring(email))
        alert.accessoryView = field
        alert.window.initialFirstResponder = field

        guard alert.runModal() == .alertFirstButtonReturn else { return }
        let label = field.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !label.isEmpty else { return }

        // The background closure captures no self (only `label`); the weak
        // capture lives on the Task — the actual concurrent hop — so we never
        // reference a captured `var self` across the concurrency boundary.
        DispatchQueue.global(qos: .userInitiated).async {
            let failure: String?
            do {
                try CLIBridge.adoptCurrent(label: label)
                failure = nil
            } catch {
                failure = error.localizedDescription
            }
            Task { @MainActor [weak self] in
                guard let self else { return }
                if let failure {
                    self.showError("Import failed", failure)
                } else {
                    await self.model.refresh()
                }
            }
        }
    }

    private func showError(_ title: String, _ body: String) {
        let a = NSAlert()
        a.messageText = title
        a.informativeText = body
        a.addButton(withTitle: "OK")
        a.runModal()
    }

    // checkForUpdates queries GitHub via the CLI on a background queue, then
    // prompts on the main thread. userInitiated=true also reports "up to
    // date" and errors; the automatic (launch) check is silent unless a new,
    // non-skipped version is available.
    private func checkForUpdates(userInitiated: Bool) {
        DispatchQueue.global(qos: .utility).async {
            let result = Result { try CLIBridge.checkUpdate() }
            DispatchQueue.main.async { [weak self] in
                guard let self else { return }
                switch result {
                case .failure(let err):
                    if userInitiated { self.presentUpdateError(err) }
                case .success(let info):
                    if !info.available {
                        if userInitiated { self.presentUpToDate(info.current) }
                        return
                    }
                    if !userInitiated, self.isSkipped(info.latest) { return }
                    self.presentUpdate(info)
                }
            }
        }
    }

    private func presentUpdate(_ info: CLIBridge.UpdateCheck) {
        NSApp.activate(ignoringOtherApps: true)
        let alert = NSAlert()
        alert.messageText = "Update available"
        var body = "A new version of AIMonitor is available.\n\nInstalled: \(info.current)\nLatest: \(info.latest)"
        if let notes = info.notes, !notes.isEmpty {
            body += "\n\n\(notes.prefix(400))"
        }
        alert.informativeText = body
        alert.addButton(withTitle: "Install")          // .alertFirstButtonReturn
        alert.addButton(withTitle: "Later")             // .alertSecondButtonReturn
        alert.addButton(withTitle: "Skip This Version") // .alertThirdButtonReturn

        switch alert.runModal() {
        case .alertFirstButtonReturn:
            self.startInstall(latest: info.latest, url: info.url)
        case .alertThirdButtonReturn:
            self.setSkipped(info.latest)
        default:
            break // Later: do nothing
        }
    }

    private func startInstall(latest: String, url: String) {
        do {
            try CLIBridge.installUpdate()
            // Deliberately NON-blocking: the detached `brew upgrade` quits
            // this app within seconds, and sitting in a modal run loop while
            // something else terminates us is a deadlock risk. The quit +
            // relaunch is itself the feedback; a banner just sets expectation.
            postNotification(title: "Updating to \(latest)",
                             body: "Downloading in the background. AIMonitor will quit and relaunch when it finishes.")
        } catch {
            // Homebrew missing or spawn failed — fall back to the releases page.
            let a = NSAlert()
            a.messageText = "Couldn’t start the update"
            a.informativeText = "\(error.localizedDescription)\n\nOpening the releases page so you can update manually."
            a.addButton(withTitle: "Open Releases")
            a.addButton(withTitle: "Cancel")
            if a.runModal() == .alertFirstButtonReturn, let u = URL(string: url) {
                NSWorkspace.shared.open(u)
            }
        }
    }

    private func presentUpToDate(_ current: String) {
        let a = NSAlert()
        a.messageText = "You’re up to date"
        a.informativeText = "AIMonitor \(current) is the latest version."
        a.addButton(withTitle: "OK")
        a.runModal()
    }

    private func presentUpdateError(_ err: Error) {
        let a = NSAlert()
        a.messageText = "Couldn’t check for updates"
        a.informativeText = err.localizedDescription
        a.addButton(withTitle: "OK")
        a.runModal()
    }

    // postNotification shows a non-blocking Notification Center banner via
    // osascript (no authorization prompt, works for an accessory app).
    // Best-effort; used where a modal would be unsafe (e.g. right before the
    // updater quits us). Fields are escaped for the AppleScript string.
    private func postNotification(title: String, body: String) {
        func esc(_ s: String) -> String {
            s.replacingOccurrences(of: "\\", with: "\\\\")
                .replacingOccurrences(of: "\"", with: "\\\"")
        }
        let script = "display notification \"\(esc(body))\" with title \"\(esc(title))\""
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/usr/bin/osascript")
        p.arguments = ["-e", script]
        try? p.run()
    }

    private func isSkipped(_ version: String) -> Bool {
        (try? CLIBridge.configGet("update.skipped_version")) == version
    }

    private func setSkipped(_ version: String) {
        try? CLIBridge.configSet("update.skipped_version", version)
    }

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        model.stop()
        return .terminateNow
    }
}
