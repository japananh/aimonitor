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
    // removeAccount is invoked with a row's label to delete the account (the
    // app delegate shows a destructive confirmation NSAlert). Only offered on
    // inactive rows — the CLI refuses to remove the active account. nil hides it.
    var removeAccount: ((String) -> Void)? = nil

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
                            ProgressView().controlSize(.small).scaleEffect(0.6).frame(width: 22, height: 22)
                        } else {
                            Image(systemName: "arrow.clockwise").font(.system(size: 14)).iconHoverChrome()
                        }
                    }
                    .buttonStyle(.borderless)
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

                    // Overflow menu for the rarer per-account actions, so the
                    // row isn't a wall of icons. Rename on any row; Remove only
                    // on inactive ones (the CLI refuses to remove the active
                    // account). Mirrors the right-click context menu below.
                    if renameAccount != nil || (!isActive && removeAccount != nil) {
                        Menu {
                            if let rename = renameAccount {
                                Button("Rename") { rename(acct.label) }
                            }
                            if !isActive, let remove = removeAccount {
                                Button("Remove", role: .destructive) { remove(acct.label) }
                            }
                        } label: {
                            Image(systemName: "ellipsis").font(.system(size: 14)).iconHoverChrome()
                        }
                        .menuStyle(.borderlessButton)
                        .menuIndicator(.hidden)
                        .fixedSize()
                        .pointerCursor()
                        .help("More actions for \(acct.label)")
                    }
                }
                .frame(height: 22, alignment: .center)
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
                if let trend = trendLabel(hist, resetAt: lim.fiveHourResetAt) {
                    Text(trend.text)
                        .font(.caption2)
                        .foregroundStyle(trend.color)
                        .padding(.leading, 28)
                        .help(sparkHelp(hist, resetAt: lim.fiveHourResetAt))
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
            if !isActive, let remove = removeAccount {
                Button("Remove", role: .destructive) { remove(acct.label) }
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

    // trendLabel summarises how the CURRENT 5-hour window has filled, as
    // "↗ +21% in 45m" / "↘ −5% in 1h" / "→ steady · 45m". Returns nil when
    // there isn't enough data inside this window to say anything.
    //
    // The 5-hour usage % resets every 5 hours, so comparing points across a
    // reset is meaningless — an account can read 100% now and 100% a day ago
    // having reset to 0 several times between, which the old whole-series
    // delta reported as a fake "steady". We anchor to the current window: it
    // starts 5h before the API's reset timestamp, and only points inside it
    // are comparable. Rising tints by the current severity (so hot+climbing
    // draws the eye); falling is green.
    private func trendLabel(_ hist: [UsageSamplePoint], resetAt: Date?) -> (text: String, color: Color)? {
        guard let resetAt else { return nil }
        let windowStart = resetAt.addingTimeInterval(-5 * 3600)
        let run = hist.filter { $0.ts >= windowStart }
        // Fewer than two in-window samples → no delta to measure. Inactive
        // accounts are polled on demand, so right after a window reset they
        // often have 0–1 points; the account isn't being consumed, so a bare
        // "steady" is honest. Gating this on resetAt (loaded with the bars)
        // rather than the sample count makes the label render in lockstep with
        // the bars on every open, instead of popping in once a refresh lands
        // the second sample.
        guard let firstP = run.first, let lastP = run.last, run.count >= 2 else {
            return ("→ steady", .secondary)
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

    // sparkHelp describes the trend on hover: what it plots over the CURRENT
    // 5-hour window (same anchor as trendLabel — points before the last reset
    // aren't comparable), and the first→latest values plus the window peak.
    private func sparkHelp(_ hist: [UsageSamplePoint], resetAt: Date?) -> String {
        let windowStart = resetAt?.addingTimeInterval(-5 * 3600) ?? .distantPast
        let run = hist.filter { $0.ts >= windowStart }
        guard let firstP = run.first, let lastP = run.last else {
            return "5-hour usage for the current window."
        }
        let vals = run.map { $0.fiveHourPct }
        let peak = vals.max() ?? lastP.fiveHourPct
        let span = humanSpan(lastP.ts.timeIntervalSince(firstP.ts))
        return String(
            format: "This account's 5-hour usage so far in the current window (%@): went from %.0f%% to %.0f%% (highest %.0f%%). It climbs as the 5-hour limit fills and resets to 0 when the window rolls over.",
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
