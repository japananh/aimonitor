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
import UserNotifications

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
final class AppDelegate: NSObject, NSApplicationDelegate, UNUserNotificationCenterDelegate {
    private var statusItem: NSStatusItem!
    // A borderless NSPanel (not NSPopover) so there's no anchoring arrow.
    private var panel: NSPanel!
    private var clickMonitor: Any?
    private var preferencesWindow: NSWindow?
    // Resigns a focused text field in Preferences when the user clicks
    // elsewhere in that window (macOS keeps a field first-responder on empty
    // clicks otherwise, so "save on click outside" never fired).
    private var prefsClickMonitor: Any?
    // Repeating background update check (every 6h); retained so it isn't
    // invalidated when the launch scope exits.
    private var updateTimer: Timer?
    private let model = AppModel()
    private var cancellables = Set<AnyCancellable>()

    func applicationDidFinishLaunching(_ notification: Notification) {
        // Single-instance guard. macOS lets you launch the binary inside two
        // different bundle paths (e.g. the brew-installed /Applications copy
        // and a local build/AIMonitor.app) even though they share a bundle
        // identifier. Two instances each install a status item AND a global
        // click monitor, and the monitors swallow each other's clicks so the
        // panel never opens. If another instance of this bundle id is already
        // running, bow out before creating any UI — the first one wins.
        if let bundleID = Bundle.main.bundleIdentifier {
            let me = NSRunningApplication.current
            let others = NSRunningApplication
                .runningApplications(withBundleIdentifier: bundleID)
                .filter { $0.processIdentifier != me.processIdentifier }
            if !others.isEmpty {
                // Surface the existing instance, then quit this one cleanly.
                others.first?.activate(options: [])
                exit(0)
            }
        }

        // Apply the saved theme (light/dark/inherit-from-OS) before any UI shows.
        applyAppearance(UserDefaults.standard.string(forKey: appThemeKey) ?? defaultAppTheme)
        // Show .help() tooltips quickly. macOS's default initial delay is
        // ~1.5s, which reads as "no tooltip" for quick hovers; 300ms is
        // snappy without popping up on every transit. Per-app preference —
        // doesn't touch system-wide behavior.
        UserDefaults.standard.register(defaults: ["NSInitialToolTipDelay": 300])
        // Dock icon: user preference (Preferences → Appearance), OFF by
        // default — the app is a menu-bar accessory first. No env override:
        // the Dock icon must always match the toggle, so the preference is
        // the single source of truth. Clicking the Dock icon opens the
        // panel via applicationShouldHandleReopen below.
        if UserDefaults.standard.bool(forKey: showDockIconKey) {
            applyDockIconPolicy(true)
        }
        setupMainMenu()
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
        // Clickable update notifications go through UNUserNotificationCenter
        // (the osascript banner can't carry a click action). Authorization is
        // requested once; if the user/OS denies it, postUpdateNotification
        // falls back to the osascript banner.
        UNUserNotificationCenter.current().delegate = self
        UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound]) { _, _ in }
        // Background update check: shortly after launch, then every 6h, so a
        // long-running menu-bar app still notices releases without a relaunch.
        // auto_update.enabled decides the action (install vs notify) — see
        // backgroundUpdateCheck.
        DispatchQueue.main.asyncAfter(deadline: .now() + 4) { [weak self] in
            self?.backgroundUpdateCheck()
        }
        updateTimer = Timer.scheduledTimer(withTimeInterval: 6 * 3600, repeats: true) { [weak self] _ in
            Task { @MainActor in self?.backgroundUpdateCheck() }
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
        // Don't auto-focus the first control (the Theme segmented picker
        // got a focus ring on open) — same treatment as the panel: the
        // window stays key, no control is first responder.
        preferencesWindow?.makeFirstResponder(nil)
        NSApp.activate(ignoringOtherApps: true)
        installPrefsClickMonitor()
    }

    // installPrefsClickMonitor makes a click anywhere in the Preferences
    // window that ISN'T a text field end editing. macOS keeps a focused text
    // field as first responder when you click empty space, so a SwiftUI
    // @FocusState-based "save on blur" never fires on an outside click.
    // Resigning first responder here flips that @FocusState to false, which
    // runs the field's commit (validate + save + "Saved"). Installed once;
    // it's a no-op for any window other than Preferences.
    private func installPrefsClickMonitor() {
        guard prefsClickMonitor == nil else { return }
        prefsClickMonitor = NSEvent.addLocalMonitorForEvents(matching: .leftMouseDown) { [weak self] event in
            guard let self,
                  let win = self.preferencesWindow,
                  event.window == win,
                  let hit = win.contentView?.hitTest(event.locationInWindow)
            else { return event }
            // Don't resign when the click lands on a text field or its field
            // editor (the NSText the field uses while editing) — that's the
            // field itself, not an "outside" click.
            let onTextField = hit is NSText || hit is NSTextField
            if !onTextField {
                win.makeFirstResponder(nil)
            }
            return event
        }
    }

    // setupMainMenu installs a minimal main menu with an Edit submenu.
    // Without it, an LSUIElement/accessory app has no menu defining the
    // standard ⌘C/⌘X/⌘V/⌘A key equivalents, so those shortcuts never fire
    // even on selectable text (`.textSelection(.enabled)`). The items use
    // the standard responder-chain selectors (copy:, cut:, …) with nil
    // target, so they route to whatever text view is first responder in the
    // key panel. The menu itself is never shown (accessory app).
    private func setupMainMenu() {
        let mainMenu = NSMenu()
        let editItem = NSMenuItem()
        mainMenu.addItem(editItem)
        let editMenu = NSMenu(title: "Edit")
        editItem.submenu = editMenu
        editMenu.addItem(withTitle: "Cut", action: #selector(NSText.cut(_:)), keyEquivalent: "x")
        editMenu.addItem(withTitle: "Copy", action: #selector(NSText.copy(_:)), keyEquivalent: "c")
        editMenu.addItem(withTitle: "Paste", action: #selector(NSText.paste(_:)), keyEquivalent: "v")
        editMenu.addItem(withTitle: "Select All", action: #selector(NSText.selectAll(_:)), keyEquivalent: "a")
        NSApp.mainMenu = mainMenu
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

    // updateStatusTitle renders the status item as a compact two-line text:
    //   <account name>
    //   5h | <usage>%
    // replacing the chart icon whenever an active account is known. The
    // icon returns as the fallback when there's no active account / no
    // daemon data yet. A tooltip carries the full picture (name, email,
    // both windows with reset times).
    private func updateStatusTitle() {
        guard let button = statusItem.button else { return }
        let name = model.activeDisplayName
        guard !name.isEmpty else {
            button.attributedTitle = NSAttributedString(string: "")
            button.image = NSImage(systemSymbolName: "chart.bar.fill",
                                   accessibilityDescription: "AIMonitor")
            button.image?.isTemplate = true
            button.toolTip = "AIMonitor — no active account yet (is the daemon running?)"
            return
        }
        button.image = nil

        // "Has data" is gated on limits_fetched_at, NOT on the pct value:
        // a genuine 0% (fresh, unused account) is real data and must show
        // "0%", while a never-fetched account shows "–". (The daemon
        // publishes five_hour_pct without omitempty so 0 survives the JSON.)
        let bottom: String
        if model.status?.limits_fetched_at != nil, let pct5 = model.status?.five_hour_pct {
            bottom = "5h | " + String(format: "%.0f%%", pct5)
        } else {
            bottom = "5h | –"
        }

        // Two stacked lines inside the 22pt menu bar: 8pt name over 11pt
        // usage, 1px gap between them. Fixed
        // line heights keep the pair vertically centered, with a per-line
        // paragraph style so the bigger number line isn't clamped to the
        // name line's height.
        let paraName = NSMutableParagraphStyle()
        paraName.alignment = .center
        paraName.minimumLineHeight = 11
        paraName.maximumLineHeight = 11
        paraName.paragraphSpacing = 1 // the gap between line 1 and line 2
        let paraPct = NSMutableParagraphStyle()
        paraPct.alignment = .center
        paraPct.minimumLineHeight = 12
        paraPct.maximumLineHeight = 12
        // Usage line at full label color + semibold so it reads as the
        // brighter, highlighted line. The name keeps the default menu-bar
        // text color. Semantic colors (not literal white) stay correct in
        // both a light and a dark menu bar.
        let top = NSMutableAttributedString(
            string: name + "\n",
            attributes: [
                .font: NSFont.systemFont(ofSize: 10, weight: .semibold),
                .paragraphStyle: paraName,
            ])
        top.append(NSAttributedString(
            string: bottom,
            attributes: [
                .font: NSFont.monospacedDigitSystemFont(ofSize: 12, weight: .semibold),
                .foregroundColor: NSColor.labelColor,
                .paragraphStyle: paraPct,
            ]))
        // Nudge the block down so the two lines sit centered in the bar.
        top.addAttribute(.baselineOffset, value: -4,
                         range: NSRange(location: 0, length: top.length))
        button.attributedTitle = top
        button.toolTip = statusTooltip(name: name)
    }

    // statusTooltip builds the hover text:
    //   <name> — <email>
    //   5h: <pct>% · resets <time>
    //   7d: <pct>% · resets <time>
    private func statusTooltip(name: String) -> String {
        var lines: [String] = []
        let email = model.accounts.first(where: { $0.label == name })?.email ?? ""
        lines.append(email.isEmpty ? name : "\(name) — \(email)")
        if let st = model.status {
            lines.append(usageLine(window: "5h", pct: st.five_hour_pct, reset: st.five_hour_reset_at))
            lines.append(usageLine(window: "7d", pct: st.seven_day_pct, reset: st.seven_day_reset_at))
        }
        return lines.joined(separator: "\n")
    }

    private func usageLine(window: String, pct: Double?, reset: Date?) -> String {
        let p = pct.map { String(format: "%.0f%%", $0) } ?? "no data"
        guard let reset, reset.timeIntervalSinceNow > 0 else {
            return "\(window): \(p)"
        }
        let fmt = DateFormatter()
        // Same-day resets just need the time; later ones need the day too.
        fmt.dateFormat = Calendar.current.isDateInToday(reset) ? "HH:mm" : "E d MMM, HH:mm"
        return "\(window): \(p) · resets \(fmt.string(from: reset))"
    }

    private func setupPanel() {
        // A borderless panel has no popover arrow. Rounded material + shadow
        // give it the popover look without the caret pointing at the icon.
        let root = PopoverRootView(
            model: model,
            openPreferences: { [weak self] in self?.showPreferences() },
            quit: { NSApplication.shared.terminate(nil) },
            renameAccount: { [weak self] label in self?.promptRename(currentLabel: label) },
            removeAccount: { [weak self] label in self?.promptRemove(label: label) },
            importAccount: { [weak self] email in self?.promptImportCurrent(email: email) },
            addAccount: { [weak self] in self?.promptAddAccount() }
        )
        // Liquid Glass chrome on Tahoe, solid rounded background before it
        // (see PanelChrome.swift for why glass stays on the chrome only).
        .panelChrome()

        let hosting = NSHostingController(rootView: root)
        // Let the panel resize to the SwiftUI content (rows, banners, error
        // lines all change the height).
        hosting.sizingOptions = [.preferredContentSize]

        let p = KeyablePanel(contentViewController: hosting)
        p.styleMask = [.borderless, .nonactivatingPanel]
        p.isFloatingPanel = true
        p.level = .popUpMenu
        p.backgroundColor = .clear
        p.isOpaque = false
        // OS window shadow OFF: on this borderless transparent panel it renders
        // as a heavy dark rim, not a soft shadow. PanelChrome draws its own soft
        // shadow inside a transparent margin instead (controllable color/blur).
        p.hasShadow = false
        p.hidesOnDeactivate = false
        p.isReleasedWhenClosed = false
        p.isMovable = false
        // Tooltips (.help) are driven by NSToolTipManager, which only fires while
        // the window receives mouse-moved events. A borderless panel has this off
        // by default, so the header/row button tooltips never appeared — enable it.
        p.acceptsMouseMovedEvents = true
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
        // The panel floats at .popUpMenu level, above the normal-level
        // Preferences window. If Preferences is still open (e.g. left
        // lingering after a click outside the app), reopening the panel would
        // render on top of it and cover its close button. Keep the two
        // mutually exclusive: hide Preferences when the panel opens. (Opening
        // Preferences already closes the panel — see showPreferences.)
        if let prefs = preferencesWindow, prefs.isVisible {
            prefs.orderOut(nil)
        }
        // Fresh data the moment it opens, without waiting for the 2s tick.
        Task { @MainActor in await model.refresh() }
        // Inactive accounts aren't polled in the background — fetch them now
        // that the popover is open (throttled in the model). The daemon keeps
        // the active account fresh on its own cadence.
        model.refreshInactiveOnOpen()
        // Activate so the status item's window geometry is realised before we
        // read it for positioning (same reason the old popover needed it).
        NSApp.activate(ignoringOtherApps: true)
        positionPanel()
        panel.makeKeyAndOrderFront(nil)
        // The panel is key (so the first click reaches a control), but we
        // don't want a control auto-focused with a focus ring on open — drop
        // first responder to the window itself. Key state is unaffected.
        panel.makeFirstResponder(nil)
        installClickMonitor()
    }

    // positionPanel places the panel just below the menu-bar icon, its right
    // edge aligned to the icon's right edge, clamped to the visible screen.
    // Anchored by its TOP so height changes grow downward.
    private func positionPanel() {
        guard let button = statusItem.button, let bwin = button.window else { return }
        let b = bwin.convertToScreen(button.convert(button.bounds, to: nil))
        let size = panel.frame.size
        // The window carries a transparent panelShadowMargin around the visible
        // card (room for the soft shadow). Compensate by that margin so the
        // CARD — not the padded window — aligns flush under the icon: shift the
        // window right and up by the margin.
        let m = panelShadowMargin
        var x = b.maxX - size.width + m
        // Anchor the card's top edge to visibleFrame.maxY — the exact bottom of
        // the menu bar — so it sits flush. (b.minY fallback when no screen.)
        var y = b.minY - size.height + m
        if let screen = bwin.screen ?? NSScreen.main {
            let vf = screen.visibleFrame
            y = vf.maxY - size.height + m
            x = max(vf.minX + 4, min(x, vf.maxX - size.width - 4 + m))
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

    // promptAddAccount explains the add flow (option C): the actual sign-in
    // happens in the browser via `claude /login` (Google SSO and magic-link
    // accounts both work there); once the new login lands, the daemon's
    // unknown-account banner offers the import. The dialog exists because
    // the flow spans two apps — without it, users don't know the import
    // banner is coming.
    private func promptAddAccount() {
        closePanel()
        NSApp.activate(ignoringOtherApps: true)

        let alert = NSAlert()
        alert.messageText = "Add a Claude account"
        alert.informativeText = """
        1. In any terminal, run `claude /login` and sign in to the account \
        you want to add — the browser handles Google or email-code logins. \
        (If the browser auto-signs you in, choose “Use a different account”.)

        2. Come back to AIMonitor: a banner will offer to import the new \
        account. Click “Import this account…” and give it a name.

        3. Your previous account stays saved — use Switch to go back to it \
        any time.
        """
        alert.addButton(withTitle: "Copy `claude /login`")
        alert.addButton(withTitle: "Close")

        if alert.runModal() == .alertFirstButtonReturn {
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString("claude /login", forType: .string)
        }
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

    // promptRemove shows a destructive confirmation before deleting an account
    // (its aimonitor keychain stash + registry row). Only reached from inactive
    // rows — the CLI refuses to remove the active account. NSAlert (not a SwiftUI
    // alert) for the same reason as promptRename: the popover dismisses on focus.
    private func promptRemove(label: String) {
        closePanel()
        NSApp.activate(ignoringOtherApps: true)

        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = "Remove “\(label)”?"
        alert.informativeText = "Deletes AIMonitor's saved login for this account (its Keychain stash and list entry). Your active Claude login is untouched, and you can re-add this account later with `claude /login`."
        let removeButton = alert.addButton(withTitle: "Remove")
        alert.addButton(withTitle: "Cancel")
        removeButton.hasDestructiveAction = true

        if alert.runModal() == .alertFirstButtonReturn {
            model.removeAccount(label: label)
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
    // backgroundUpdateCheck runs on launch + every 6h. auto_update.enabled
    // decides the ACTION when a newer release is found (and isn't skipped):
    //   ON  → install it automatically (brew upgrade; the app quits + relaunches).
    //   OFF → post a clickable notification once per version; clicking it opens
    //         the Update available dialog (handled in didReceive below).
    // The manual "Check for updates" button still always shows the dialog.
    private func backgroundUpdateCheck() {
        DispatchQueue.global(qos: .utility).async {
            guard let info = try? CLIBridge.checkUpdate(), info.available else { return }
            DispatchQueue.main.async { [weak self] in
                guard let self else { return }
                if self.isSkipped(info.latest) { return }
                let autoInstall = (try? CLIBridge.configGet("auto_update.enabled")) != "false"
                if autoInstall {
                    self.startInstall(latest: info.latest, url: info.url)
                } else {
                    guard self.lastNotifiedVersion() != info.latest else { return }
                    self.setLastNotifiedVersion(info.latest)
                    self.postUpdateNotification(info)
                }
            }
        }
    }

    // postUpdateNotification posts a clickable "update available" banner via
    // UNUserNotificationCenter. Clicking it opens the Update available dialog
    // (didReceive). Falls back to the plain osascript banner if the system
    // rejects the request (e.g. notification permission denied).
    private func postUpdateNotification(_ info: CLIBridge.UpdateCheck) {
        let content = UNMutableNotificationContent()
        content.title = "AIMonitor \(info.latest) available"
        content.body = "You're on \(info.current). Click to review and install."
        content.userInfo = ["update_available": true]
        let req = UNNotificationRequest(identifier: "aimonitor-update-\(info.latest)", content: content, trigger: nil)
        UNUserNotificationCenter.current().add(req) { [weak self] err in
            guard err != nil else { return }
            DispatchQueue.main.async {
                self?.postNotification(title: "AIMonitor \(info.latest) available",
                                       body: "Open AIMonitor → Preferences → Check for updates to install.")
            }
        }
    }

    private func lastNotifiedVersion() -> String { UserDefaults.standard.string(forKey: "update.notifiedVersion") ?? "" }
    private func setLastNotifiedVersion(_ v: String) { UserDefaults.standard.set(v, forKey: "update.notifiedVersion") }

    // Show update banners even while the app is frontmost. nonisolated: the
    // system may call this off the main actor, and it touches no actor state.
    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification,
                                            withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        completionHandler([.banner, .sound])
    }

    // Clicking the update notification opens the Update available dialog (a
    // fresh re-check so it reflects the current latest + handles already-installed).
    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse,
                                            withCompletionHandler completionHandler: @escaping () -> Void) {
        let isUpdate = response.notification.request.content.userInfo["update_available"] != nil
        completionHandler()
        if isUpdate {
            Task { @MainActor [weak self] in self?.checkForUpdates(userInitiated: true) }
        }
    }

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

    // Dock-icon click handler. Only reachable when the Dock-icon test aid is
    // on (otherwise the app is an accessory with no Dock icon, so this never
    // fires). Opens/closes the panel so the widget is usable without the
    // menu-bar icon.
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        togglePopover(nil)
        return true
    }
}

// KeyablePanel is a borderless NSPanel that can become key. A plain
// borderless window returns canBecomeKey=false, so makeKeyAndOrderFront
// can't actually make it key — AppKit then spends the user's FIRST click
// just trying (and failing) to focus the window, so a button inside only
// fires on the SECOND click. Returning true lets the panel take key state
// when we show it, so the first click lands on the control as expected.
final class KeyablePanel: NSPanel {
    override var canBecomeKey: Bool { true }
}
