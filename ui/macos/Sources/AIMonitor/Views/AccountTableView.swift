// AccountTableView is the TOGGLEABLE per-account headroom panel.
// Hidden when model.showAccountPanel == false. Each row shows label,
// probed remaining tokens (server-side truth), last-used date, and a
// "Switch" button.

import SwiftUI

struct AccountTableView: View {
    @ObservedObject var model: AppModel
    // renameAccount is invoked with a row's current label; the app delegate
    // shows a modal text prompt (an NSAlert is reliable even though showing
    // it dismisses the transient popover). nil disables the affordance.
    var renameAccount: ((String) -> Void)? = nil

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("Accounts")
                .font(.subheadline).bold()
                .padding(.horizontal, 12)
                .padding(.top, 4)

            if model.accounts.isEmpty {
                Text("No accounts. Run `aimonitor add` in a terminal.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .padding(12)
            } else {
                Divider()
                ForEach(model.accounts) { acct in
                    rowView(acct)
                    Divider()
                }
            }
        }
        .padding(.bottom, 6)
    }

    @ViewBuilder
    private func rowView(_ acct: AccountRow) -> some View {
        let isActive = (model.status?.active_label == acct.label)

        VStack(alignment: .leading, spacing: 4) {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 4) {
                        if isActive {
                            Image(systemName: "checkmark.circle.fill")
                                .foregroundStyle(.green)
                                .font(.caption)
                        }
                        Text(acct.label).font(.subheadline)
                    }
                    Text(identityCaption(acct))
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                }
                Spacer()
                if !isActive {
                    // Per-account usage refresh. The active account is kept
                    // fresh by the daemon and must not be refreshed via the
                    // stash path, so this is inactive-only.
                    Button {
                        model.refreshUsage(label: acct.label, id: acct.id)
                    } label: {
                        if model.refreshingAccounts.contains(acct.id) {
                            ProgressView().controlSize(.small).scaleEffect(0.6).frame(width: 14, height: 14)
                        } else {
                            Image(systemName: "arrow.clockwise")
                        }
                    }
                    .buttonStyle(.borderless)
                    .controlSize(.small)
                    .disabled(model.refreshingAccounts.contains(acct.id))
                    .pointerCursor()
                    .help("Fetch \(acct.label)'s latest usage now")

                    Button("Switch") {
                        model.switchTo(label: acct.label)
                    }
                    .controlSize(.small)
                    .pointerCursor()
                    .help("Make \(acct.label) the active Claude account")
                }
            }
            // Per-account 5h / 7d utilization. Absent until the daemon has
            // fetched this account at least once (active every tick; inactive
            // round-robin when their token is valid).
            if let lim = model.limitsByAccount[acct.id] {
                UsageBars(limits: lim)
            } else {
                Text("no usage data yet")
                    .font(.caption2)
                    .foregroundStyle(.tertiary)
                    .padding(.leading, 28)
            }
            // Per-account refresh error (e.g. expired refresh token →
            // "re-add the account"). Cleared on the next successful refresh.
            if let err = model.usageErrors[acct.id] {
                Text(err)
                    .font(.caption2)
                    .foregroundStyle(.red)
                    .textSelection(.enabled)
                    .padding(.leading, 28)
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 6)
        .contextMenu {
            if let rename = renameAccount {
                Button("Rename…") { rename(acct.label) }
            }
        }
    }

    // identityCaption is the account's Claude identity line: email and,
    // when known, the organization. Empty/absent identity (legacy rows
    // added before identity capture) prompts a re-add rather than showing
    // a blank line.
    private func identityCaption(_ acct: AccountRow) -> String {
        switch (acct.email, acct.organizationName) {
        case let (email?, org?):
            return "\(email) · \(org)"
        case let (email?, nil):
            return email
        case let (nil, org?):
            return org
        default:
            return "identity not captured — re-run `aimonitor add`"
        }
    }
}
