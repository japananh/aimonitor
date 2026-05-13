// AIMonitorApp is the @main entry point. It bootstraps an
// NSApplication, installs an NSStatusItem in the menu bar, and shows a
// SwiftUI popover when the user clicks the icon.
//
// Why AppKit + SwiftUI instead of pure SwiftUI MenuBarExtra: MenuBarExtra
// from .menuBarExtraStyle(.window) gives us a menu-bar attached panel
// but its size/positioning quirks are worse than vanilla NSPopover for a
// content panel with a progress bar + table. NSPopover is well-trodden.

import AppKit
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

    func applicationDidFinishLaunching(_ notification: Notification) {
        setupStatusItem()
        setupPopover()
        Task { @MainActor in self.model.start() }
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
            button.action = #selector(togglePopover(_:))
            button.target = self
        }
    }

    private func setupPopover() {
        popover = NSPopover()
        popover.behavior = .transient
        popover.contentSize = NSSize(width: 340, height: 320)
        let root = PopoverRootView(
            model: model,
            openPreferences: { [weak self] in self?.showPreferences() },
            quit: { NSApplication.shared.terminate(nil) }
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
            popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)
        }
    }

    private func showPreferences() {
        popover.performClose(nil)
        if preferencesWindow == nil {
            let view = PreferencesView(model: model)
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

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        model.stop()
        return .terminateNow
    }
}
