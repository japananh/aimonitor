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
        .padding(.horizontal, 16)
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
                // Compact 5h trend: "↗ +21% in 45m" — the change in 5-hour usage
                // over the span we have data for. Conveys "climbing fast?" in one
                // line without a chart. Aligned under the bars (28pt inset).
                let hist = model.historyByAccount[acct.id] ?? []
                if let trend = trendLabel(hist) {
                    Text(trend.text)
                        .font(.caption2)
                        .foregroundStyle(trend.color)
                        .padding(.leading, 28)
                        .help(sparkHelp(hist))
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
                Button("Rename") { rename(acct.label) }
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

    // trendLabel summarises the 5-hour usage change over the span we have
    // data for, as "↗ +21% in 45m" / "↘ −5% in 1h" / "→ steady · 45m". Returns
    // nil when there aren't enough points to say anything. Rising tints by the
    // current severity (so hot+climbing draws the eye); falling is green.
    private func trendLabel(_ hist: [UsageSamplePoint]) -> (text: String, color: Color)? {
        guard let firstP = hist.first, let lastP = hist.last, hist.count >= 2 else {
            return nil
        }
        let delta = lastP.fiveHourPct - firstP.fiveHourPct
        let span = humanSpan(lastP.ts.timeIntervalSince(firstP.ts))
        // RISING and FALLING both tint by the current 5h level — same
        // green/amber/red palette + thresholds as the 5h bar — so a trend at a
        // high level reads red/amber even while it's dropping (it's recovering,
        // but still hot); it only goes green once it's actually back in the safe
        // zone. STEADY is muted gray. ±2pp deadband so noise isn't a fake trend.
        let color = sparkColor(for: lastP.fiveHourPct)
        if delta >= 2 {
            return (String(format: "↗ +%.0f%% in %@", delta, span), color)
        }
        if delta <= -2 {
            return (String(format: "↘ %.0f%% in %@", delta, span), color)
        }
        return (String(format: "→ steady · %@", span), .secondary)
    }

    // sparkHelp describes the trend on hover: what it plots, the real time
    // span it actually covers (so a few-minutes line isn't mistaken for a day),
    // and the first→latest values plus the peak.
    private func sparkHelp(_ hist: [UsageSamplePoint]) -> String {
        guard let firstP = hist.first, let lastP = hist.last else {
            return "Recent 5-hour usage."
        }
        let vals = hist.map { $0.fiveHourPct }
        let peak = vals.max() ?? lastP.fiveHourPct
        let span = humanSpan(lastP.ts.timeIntervalSince(firstP.ts))
        return String(
            format: "This account's 5-hour usage over the last %@: went from %.0f%% to %.0f%% (highest %.0f%%). The line goes up as the 5-hour limit fills, and drops when the window resets.",
            span, firstP.fiveHourPct, lastP.fiveHourPct, peak
        )
    }

    // humanSpan renders an elapsed duration compactly: "45s", "12m", "3h 20m".
    private func humanSpan(_ secs: TimeInterval) -> String {
        let s = Int(secs)
        if s < 60 { return "\(s)s" }
        if s < 3600 { return "\(s / 60)m" }
        let h = s / 3600, m = (s % 3600) / 60
        return m > 0 ? "\(h)h \(m)m" : "\(h)h"
    }

    // Trend tint shares the bar's severityColor (Theme.swift) so they agree.
    private func sparkColor(for pct: Double) -> Color {
        severityColor(for: pct)
    }
}
