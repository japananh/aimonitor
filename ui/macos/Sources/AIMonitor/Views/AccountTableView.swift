// AccountTableView is the TOGGLEABLE per-account headroom panel.
// Hidden when model.showAccountPanel == false. Each row shows label,
// probed remaining tokens (server-side truth), last-used date, and a
// "Switch" button.

import SwiftUI

struct AccountTableView: View {
    @ObservedObject var model: AppModel

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
                Button("Switch") {
                    model.switchTo(label: acct.label)
                }
                .controlSize(.small)
            }
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 4)
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
