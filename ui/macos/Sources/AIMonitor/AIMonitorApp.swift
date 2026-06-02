// AIMonitorApp is the @main entry point. It bootstraps an
// NSApplication, installs an NSStatusItem in the menu bar, and shows a
// SwiftUI popover when the user clicks the icon.
//
// Why AppKit + SwiftUI instead of pure SwiftUI MenuBarExtra: MenuBarExtra
// from .menuBarExtraStyle(.window) gives us a menu-bar attached panel
// but its size/positioning quirks are worse than vanilla NSPopover for a
// content panel with a progress bar + table. NSPopover is well-trodden.

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
    private var popover: NSPopover!
    private var preferencesWindow: NSWindow?
    private let model = AppModel()
    private var cancellables = Set<AnyCancellable>()

    func applicationDidFinishLaunching(_ notification: Notification) {
        setupStatusItem()
        setupPopover()
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
        popover.performClose(nil)
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

    private func setupPopover() {
        popover = NSPopover()
        popover.behavior = .transient
        popover.contentSize = NSSize(width: 340, height: 320)
        let root = PopoverRootView(
            model: model,
            openPreferences: { [weak self] in self?.showPreferences() },
            quit: { NSApplication.shared.terminate(nil) },
            renameAccount: { [weak self] label in self?.promptRename(currentLabel: label) }
        )
        popover.contentViewController = NSHostingController(rootView: root)
    }

    @objc private func togglePopover(_ sender: Any?) {
        guard let button = statusItem.button else { return }
        if popover.isShown {
            popover.performClose(sender)
        } else {
            // Trigger a fresh poll right when the user opens the popover
            // so they see up-to-date numbers without waiting for the
            // 2-second timer tick.
            Task { @MainActor in await model.refresh() }
            // Activate first. For an .accessory (LSUIElement) app that is
            // not the frontmost app, the status item's window frame isn't
            // finalized at click time, so NSPopover falls back to a default
            // position (screen centre / top-left) instead of anchoring to
            // the button. Activating realises the window geometry so
            // show(relativeTo:) lands directly beneath the menu-bar icon.
            NSApp.activate(ignoringOtherApps: true)
            popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)
            // Keep the popover key so click-outside dismiss + keyboard work
            // even though the app has no regular window.
            popover.contentViewController?.view.window?.makeKey()
        }
    }

    // promptRename shows a modal text field pre-filled with the current
    // label and renames on confirm. NSAlert is used (rather than a SwiftUI
    // alert inside the popover) because the transient popover dismisses as
    // soon as the modal takes focus, which would tear down a SwiftUI alert.
    private func promptRename(currentLabel: String) {
        popover.performClose(nil)
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
            let done = NSAlert()
            done.messageText = "Updating to \(latest)"
            done.informativeText = "The update is downloading in the background. AIMonitor will quit and relaunch automatically when it finishes."
            done.addButton(withTitle: "OK")
            done.runModal()
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
