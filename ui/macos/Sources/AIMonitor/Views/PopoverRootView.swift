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
    // Invoked with the signed-in email when the live account isn't managed
    // by aimonitor, to offer importing it. nil hides the import prompt.
    var importAccount: ((String) -> Void)? = nil

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            // When the daemon hasn't published recently the rows below are
            // stale; surface that explicitly. (Dropping the old session bar
            // removed the previous "daemon not running" hint — keep one.)
            if daemonDown {
                Label(
                    "Daemon not running — usage may be stale. Enable “Launch at login” in Preferences, or run `aimonitor daemon start`.",
                    systemImage: "exclamationmark.triangle.fill"
                )
                .font(.caption2)
                .foregroundStyle(.orange)
                .padding(.horizontal, 12)
                .padding(.vertical, 6)
                Divider()
            } else if let email = model.status?.unknown_active_email, !email.isEmpty {
                // Another app (or `claude /login`) is signed into an account
                // aimonitor doesn't manage — offer to import it.
                VStack(alignment: .leading, spacing: 4) {
                    Label("Claude is signed into \(email), which AIMonitor doesn’t manage.",
                          systemImage: "person.crop.circle.badge.questionmark")
                        .font(.caption2)
                        .foregroundStyle(.orange)
                    if let importAccount {
                        Button("Import this account…") { importAccount(email) }
                            .controlSize(.small)
                            .pointerCursor()
                            .help("Register the currently signed-in account with AIMonitor")
                    }
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 6)
                Divider()
            }

            if model.showAccountPanel {
                AccountTableView(model: model, renameAccount: renameAccount)
            }

            Divider()
            HStack {
                Button(model.refreshingUsage ? "Refreshing…" : "Refresh usage") {
                    model.refreshUsage()
                }
                .disabled(model.refreshingUsage)
                .pointerCursor()
                .help("Fetch the latest 5h/7d usage for every account now")
                Spacer()
                Button("Preferences…", action: openPreferences)
                    .pointerCursor()
                    .help("Auto-switch, updates, and startup settings")
                Button("Quit", action: quit)
                    .pointerCursor()
                    .help("Quit the menu-bar app (the background daemon keeps running)")
            }
            .controlSize(.small)
            .padding(.horizontal, 12)
            .padding(.vertical, 6)

            if let err = model.lastError {
                Divider()
                Text(err)
                    .font(.caption2)
                    .foregroundStyle(.red)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 4)
            }
        }
        .frame(width: 360)
    }

    // daemonDown is true when no status has been published, or the last
    // publish is older than ~15 publish intervals (the daemon publishes
    // every ~2s). A short window avoids false alarms from a single missed tick.
    private var daemonDown: Bool {
        guard let pub = model.status?.published_at else { return true }
        return Date().timeIntervalSince(pub) > 30
    }
}
