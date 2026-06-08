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
        // Tahoe (Control Center) layout: each account is a rounded
        // "module" card floating on the glass panel — no full-width
        // dividers between rows.
        VStack(alignment: .leading, spacing: 8) {
            if model.accounts.isEmpty {
                Text("No accounts. Run `aimonitor add` in a terminal.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .padding(12)
            } else {
                ForEach(model.accounts) { acct in
                    rowView(acct)
                }
            }
        }
        .padding(.horizontal, 10)
        .padding(.bottom, 4)
    }

    @ViewBuilder
    private func rowView(_ acct: AccountRow) -> some View {
        let isActive = (model.status?.active_label == acct.label)

        VStack(alignment: .leading, spacing: 4) {
            // .top so the refresh/Switch buttons line up with the account
            // NAME (first line), not the vertical middle of the name/email/
            // org stack.
            HStack(alignment: .top) {
                VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 4) {
                        if isActive {
                            Image(systemName: "checkmark.circle.fill")
                                .foregroundStyle(.green)
                                .font(.caption)
                        }
                        // Account name — the largest text in the row.
                        Text(acct.label).font(.headline)
                        // Cooling badge: the account was 429'd and is parked
                        // (skipped by the poller + excluded from auto-swap)
                        // until the countdown elapses.
                        if let until = acct.cooldownUntil, until > Date() {
                            Text("⏳ \(cooldownLabel(until))")
                                .font(.caption2)
                                .foregroundStyle(Color(nsColor: .systemOrange))
                                .padding(.horizontal, 5)
                                .padding(.vertical, 1)
                                .background(
                                    Capsule().fill(Color(nsColor: .systemOrange).opacity(0.15))
                                )
                                .help(acct.cooldownReason ?? "Rate-limited — paused until the cooldown ends")
                        }
                        // Rename button right next to the name.
                        if let rename = renameAccount {
                            Button {
                                rename(acct.label)
                            } label: {
                                Image(systemName: "pencil")
                            }
                            .buttonStyle(.borderless)
                            .controlSize(.small)
                            .pointerCursor()
                            .help("Rename \(acct.label)")
                        }
                    }
                    // Email below the name — larger than the org, smaller
                    // than the account name.
                    if let email = acct.email, !email.isEmpty {
                        Text(email)
                            .font(.system(size: 11))
                            .foregroundStyle(.primary)
                            .textSelection(.enabled)
                    }
                    // Organization below the email — the smallest line.
                    if let org = acct.organizationName, !org.isEmpty {
                        Text(org)
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                            .textSelection(.enabled)
                    }
                    // Legacy rows added before identity capture: prompt a re-add.
                    if (acct.email?.isEmpty ?? true) && (acct.organizationName?.isEmpty ?? true) {
                        Text("identity not captured — re-run `aimonitor add`")
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                    }
                }
                Spacer()
                // Both buttons live in one fixed-height band matching the
                // name line: the row is top-aligned (so the band sits beside
                // the NAME, not the email), and the band centers the buttons
                // vertically on that line.
                HStack(spacing: 8) {
                    // Per-account usage refresh on every row. The CLI routes
                    // the active account through the daemon's safe
                    // live-refresh path and inactive accounts through their
                    // stash, so this is safe on all rows.
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

                    if !isActive {
                        // Spinner + disabled while a switch is in flight (it
                        // takes a few seconds: token refresh + keychain
                        // writes). ALL Switch buttons disable during it so a
                        // second switch can't queue behind the first.
                        Button {
                            model.switchTo(label: acct.label)
                        } label: {
                            if model.switchingLabel == acct.label {
                                HStack(spacing: 4) {
                                    ProgressView().controlSize(.small).scaleEffect(0.55).frame(width: 12, height: 12)
                                    Text("Switching…")
                                }
                            } else {
                                Text("Switch")
                            }
                        }
                        .appTextButtonChrome()
                        .disabled(model.switchingLabel != nil)
                        .help("Make \(acct.label) the active Claude account")
                    }
                }
                .frame(height: 18, alignment: .center)
            }
            // Per-account 5h / 7d utilization. Absent until the daemon has
            // fetched this account at least once (active every tick; inactive
            // round-robin when their token is valid).
            if let lim = model.limitsByAccount[acct.id] {
                UsageBars(limits: lim)
                // 24h trend of the 5h window, when we have enough points.
                // Aligns under the bars (28pt left inset matches the bar
                // labels) so it reads as part of the same block.
                let series = (model.historyByAccount[acct.id] ?? []).map { $0.fiveHourPct }
                if series.count >= 2 {
                    Sparkline(values: series, color: sparkColor(for: lim.fiveHourPct))
                        .padding(.leading, 28)
                        .padding(.top, 1)
                        .help(sparkHelp(series))
                }
            } else {
                Text("no usage data yet")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
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
        .padding(10)
        // Control-Center-style module card. The fill uses the semantic
        // control background (opaque, adapts per appearance) so a card
        // reads clearly against the translucent glass in BOTH light and
        // dark — quaternarySystemFill was too faint on the dark panel. A
        // hairline separator stroke gives every card a crisp edge; the
        // ACTIVE card is accent-tinted (on top of its green check).
        .background(
            RoundedRectangle(cornerRadius: 12, style: .continuous)
                .fill(Color(nsColor: .controlBackgroundColor).opacity(0.6))
        )
        .background(
            RoundedRectangle(cornerRadius: 12, style: .continuous)
                .fill(Color.accentColor.opacity(isActive ? 0.22 : 0))
        )
        .overlay(
            RoundedRectangle(cornerRadius: 12, style: .continuous)
                .strokeBorder(
                    isActive ? Color.accentColor.opacity(0.5) : Color(nsColor: .separatorColor),
                    lineWidth: 1
                )
        )
        .contextMenu {
            if let rename = renameAccount {
                Button("Rename…") { rename(acct.label) }
            }
        }
    }

    // "cooling 4m" / "cooling 1h 5m" — how long until the 429 park lifts.
    private func cooldownLabel(_ until: Date) -> String {
        let secs = Int(until.timeIntervalSinceNow)
        if secs <= 0 { return "cooling" }
        let h = secs / 3600
        let m = (secs % 3600) / 60
        if h > 0 { return "cooling \(h)h \(m)m" }
        if m > 0 { return "cooling \(m)m" }
        return "cooling <1m"
    }

    // sparkHelp describes the trend line on hover: what it plots, plus the
    // first→latest values and the peak so the shape has concrete numbers.
    private func sparkHelp(_ s: [Double]) -> String {
        guard let first = s.first, let last = s.last else {
            return "5-hour usage over recent history."
        }
        let peak = s.max() ?? last
        return String(
            format: "5-hour usage over recent history: %.0f%% → %.0f%% (peak %.0f%%). Rising means this account is burning through its 5-hour window; flat means steady.",
            first, last, peak
        )
    }

    // Severity tint for the sparkline, matching UsageBars' green/amber/red
    // thresholds so the trend line and the current-value bar agree at a
    // glance. AppKit system colors adapt to the active appearance.
    private func sparkColor(for pct: Double) -> Color {
        switch pct {
        case ..<60: return Color(nsColor: .systemGreen)
        case ..<85: return Color(nsColor: .systemYellow)
        default: return Color(nsColor: .systemRed)
        }
    }
}
