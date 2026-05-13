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
        let probe = model.probes.first(where: { $0.accountID == acct.id })
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
                Text(probeCaption(probe)).font(.caption2).foregroundStyle(.secondary)
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

    private func probeCaption(_ probe: ProbeRow?) -> String {
        guard let probe else { return "no probe yet" }
        let age = Date().timeIntervalSince(probe.probedAt)
        let stale = age > 30 ? " (stale)" : ""
        let f = NumberFormatter()
        f.numberStyle = .decimal
        let remaining = f.string(from: NSNumber(value: probe.tokensRemaining)) ?? "\(probe.tokensRemaining)"
        return "\(remaining) tokens left · HTTP \(probe.httpStatus)\(stale)"
    }
}
