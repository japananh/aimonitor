// PopoverRootView is the SwiftUI root for the menu bar popover. It shows a
// daemon-health banner (when the daemon isn't publishing), the per-account
// list with 5h/7d usage bars, and the action footer.

import SwiftUI

struct PopoverRootView: View {
    @ObservedObject var model: AppModel
    let openPreferences: () -> Void
    let quit: () -> Void
    // Invoked with an account's label to start a rename (app delegate shows
    // the modal prompt). nil hides the rename affordance.
    var renameAccount: ((String) -> Void)? = nil
    // Invoked with an account's label to delete it (app delegate shows a
    // destructive confirmation). nil hides the remove affordance.
    var removeAccount: ((String) -> Void)? = nil
    // Invoked with an account's label when its session expired, to show
    // re-login instructions. nil hides the Re-login button.
    var reloginAccount: ((String) -> Void)? = nil
    // Invoked with the signed-in email when the live account isn't managed
    // by aimonitor, to offer importing it. nil hides the import prompt.
    var importAccount: ((String) -> Void)? = nil
    // Shows the add-account instructions (claude /login → import banner).
    // nil hides the affordance.
    var addAccount: (() -> Void)? = nil

    // Start-daemon banner state: in-flight flag + surfaced error.
    @State private var daemonStarting = false
    @State private var daemonStartError: String?

    // Which panel is showing: the OAuth 5h/7d limit bars, or the token
    // breakdown. Limits stays the default — it's the at-a-glance "can I keep
    // working" view; tokens is the "how much have I burned" drill-down.
    private enum Tab: Hashable { case limits, tokens }
    @State private var tab: Tab = .limits

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            // Top bar: the "Accounts" title and the settings gear on one
            // line (gear right-aligned). Replaces the old footer
            // "Preferences…" text button and the account table's own header.
            HStack(alignment: .center, spacing: 0) {
                // Panel title — bigger than the account-row headlines so the
                // hierarchy reads title > account name > email > org.
                Text("Accounts")
                    .font(.headline)
                Spacer()
                // Action icons. Each is a uniform square so their glyph
                // centers line up with each other and with the title — SF
                // Symbols have differing intrinsic heights, so without an equal
                // frame they'd sit at slightly different vertical positions.
                HStack(alignment: .center, spacing: 4) {
                    if let addAccount {
                        IconActionButton(systemName: "plus", help: "Add a Claude account to AIMonitor", action: addAccount)
                    }
                    // ladybug's glyph sits ~1px low vs plus/gearshape even when
                    // framed — nudge it up so the three line up optically.
                    IconActionButton(systemName: "ladybug", help: "Report a bug — opens a new GitHub issue", yNudge: -1) {
                        NSWorkspace.shared.open(URL(string: "https://github.com/japananh/aimonitor/issues/new?template=bug_report.yml")!)
                    }
                    IconActionButton(systemName: "gearshape", help: "Preferences — auto-switch, updates, and startup settings", action: openPreferences)
                }
            }
            .padding(.horizontal, 16)
            .padding(.top, 14)
            .padding(.bottom, 8)

            // When the daemon hasn't published recently the rows below are
            // stale; surface that explicitly. (Dropping the old session bar
            // removed the previous "daemon not running" hint — keep one.)
            if daemonDown {
                bannerCard {
                    Label(
                        "Daemon not running — usage may be stale.",
                        systemImage: "exclamationmark.triangle.fill"
                    )
                    .font(.caption2)
                    .foregroundStyle(.orange)
                    HStack(spacing: 8) {
                        // Registers the daemon's LaunchAgent and starts it
                        // immediately (`aimonitor config set autostart true`).
                        // The Preferences "Launch at login" toggle only
                        // registers the widget app — it never starts the
                        // daemon, which is why this lives on the banner.
                        AppTextButton(daemonStarting ? "Starting…" : "Start daemon") {
                            startDaemon()
                        }
                        .disabled(daemonStarting)
                        .help("Start the background daemon now and keep it running at login")
                        if let err = daemonStartError {
                            Text(err)
                                .font(.caption2)
                                .foregroundStyle(.red)
                                .textSelection(.enabled)
                        }
                    }
                }
            } else if let email = model.status?.unknown_active_email, !email.isEmpty {
                // Another app (or `claude /login`) is signed into an account
                // aimonitor doesn't manage — offer to import it.
                bannerCard {
                    Label("Claude is signed into \(email), which AIMonitor doesn’t manage.",
                          systemImage: "person.crop.circle.badge.questionmark")
                        .font(.caption2)
                        .foregroundStyle(.orange)
                    if let importAccount {
                        AppTextButton("Import this account") { importAccount(email) }
                            .help("Register the currently signed-in account with AIMonitor")
                    }
                }
            }

            // Tab switch: Limits (OAuth 5h/7d bars) vs Tokens (token
            // breakdown). Sits above the content so it reads as a control
            // for the panel below it.
            Picker("", selection: $tab) {
                Text("Limits").tag(Tab.limits)
                Text("Tokens").tag(Tab.tokens)
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .padding(.horizontal, 16)
            .padding(.bottom, 8)

            if tab == .limits {
                AccountTableView(model: model, renameAccount: renameAccount, removeAccount: removeAccount, reloginAccount: reloginAccount)
            } else {
                TokenUsageView(model: model)
            }

            // Footer actions float directly on the glass — no separator;
            // the account cards above provide the visual grouping. "Refresh
            // usage" only fetches OAuth limits, so scope it to the Limits tab.
            HStack {
                if tab == .limits {
                    AppTextButton(model.refreshingUsage ? "Refreshing…" : "Refresh usage") {
                        model.refreshUsage()
                    }
                    .disabled(model.refreshingUsage)
                    .help("Fetch the latest 5h/7d usage for every account now")
                }
                Spacer()
                AppTextButton("Quit", action: quit)
                    .help("Quit the menu-bar app (the background daemon keeps running)")
            }
            .padding(.horizontal, 16)
            .padding(.top, 8)
            .padding(.bottom, 14)

            if let err = model.lastError {
                Text(err)
                    .font(.caption2)
                    .foregroundStyle(.red)
                    .textSelection(.enabled)
                    .padding(10)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(
                        RoundedRectangle(cornerRadius: 12, style: .continuous)
                            .fill(Color.red.opacity(0.12))
                    )
                    .padding(.horizontal, 16)
                    .padding(.bottom, 10)
            }
        }
        .frame(width: 360)
    }

    // bannerCard wraps banner content in a Control-Center-style module
    // card (amber tint = attention), matching the account cards below.
    @ViewBuilder
    private func bannerCard<Content: View>(@ViewBuilder _ content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            content()
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            RoundedRectangle(cornerRadius: 12, style: .continuous)
                .fill(Color.orange.opacity(0.12))
        )
        .padding(.horizontal, 16)
        .padding(.bottom, 8)
    }

    // daemonDown is true when no status has been published, or the last
    // publish is older than ~15 publish intervals (the daemon publishes
    // every ~2s). A short window avoids false alarms from a single missed tick.
    private var daemonDown: Bool {
        guard let pub = model.status?.published_at else { return true }
        return Date().timeIntervalSince(pub) > 30
    }

    // startDaemon registers + starts the daemon LaunchAgent via the CLI
    // (`config set autostart true` bootstraps it immediately; RunAtLoad
    // keeps it across logins). Runs off-main; the banner clears by itself
    // once the daemon publishes its first status tick.
    private func startDaemon() {
        daemonStarting = true
        daemonStartError = nil
        DispatchQueue.global(qos: .userInitiated).async {
            do {
                try CLIBridge.configSet("autostart", "true")
                DispatchQueue.main.async { daemonStarting = false }
            } catch {
                DispatchQueue.main.async {
                    daemonStarting = false
                    daemonStartError = "\(error)"
                }
            }
        }
    }
}

// Header action icons now use the shared IconActionButton (Buttons.swift).
