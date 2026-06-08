// UsageBars renders one account's OAuth-introspected rate-limit utilization
// as two compact horizontal bars (5-hour and 7-day) with a freshness label.
// It's value-driven (a LimitsRow) so it can be embedded in every account
// row, not just the active account.
//
// The freshness label is load-bearing, not decoration: inactive accounts
// are polled only while their token is valid, so a row can be hours stale.
// "Gem 1 at 8%" is only actionable if you know whether that's 8% now or
// this morning — the "(stale)" marker tells you.

import SwiftUI
import AppKit

struct UsageBars: View {
    let limits: LimitsRow

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            bar(label: "5h", pct: limits.fiveHourPct, resetAt: limits.fiveHourResetAt)
            bar(label: "7d", pct: limits.sevenDayPct, resetAt: limits.sevenDayResetAt)
            Text(stalenessLabel(fetched: limits.fetchedAt))
                .font(.caption2)
                .foregroundStyle(.secondary)
                .padding(.leading, 28)
        }
    }

    @ViewBuilder
    private func bar(label: String, pct: Double, resetAt: Date?) -> some View {
        HStack(spacing: 6) {
            Text(label)
                .font(.caption.monospaced())
                .frame(width: 22, alignment: .leading)
                .foregroundStyle(.primary)
            // Custom-drawn bar instead of ProgressView: macOS's linear
            // ProgressView ignores .tint for the fill, so the bars rendered
            // colorless. Drawing a track + colored fill guarantees the
            // green/amber/red severity shows. Both colors are AppKit system
            // colors, so they adapt to the active appearance — including the
            // "inherit from OS" theme, where they match the OS light/dark shade.
            GeometryReader { geo in
                let frac = CGFloat(min(max(pct, 0), 100) / 100.0)
                ZStack(alignment: .leading) {
                    Capsule().fill(Color(nsColor: .quaternaryLabelColor))
                    Capsule()
                        .fill(color(for: pct))
                        .frame(width: max(0, geo.size.width * frac))
                }
            }
            .frame(height: 6)
            Text(String(format: "%.0f%%", pct))
                .font(.caption.monospacedDigit())
                .frame(width: 34, alignment: .trailing)
                .foregroundStyle(.primary)
            Text(resetAt.map { "· \(resetCountdown($0))" } ?? "")
                .font(.caption2)
                .foregroundStyle(.secondary)
                .frame(width: 64, alignment: .leading)
        }
    }

    // Muted green/amber/red by utilization — shared severityColor (Theme.swift)
    // so the bar matches the trend label and isn't garish.
    private func color(for pct: Double) -> Color {
        severityColor(for: pct)
    }

    private func resetCountdown(_ resetAt: Date) -> String {
        let secs = Int(resetAt.timeIntervalSinceNow)
        if secs <= 0 { return "now" }
        let days = secs / 86400
        let hours = (secs % 86400) / 3600
        let mins = (secs % 3600) / 60
        switch (days, hours) {
        case let (d, h) where d > 0:
            return "\(d)d \(h)h"
        case let (_, h) where h > 0:
            return "\(h)h \(mins)m"
        default:
            return "\(mins)m"
        }
    }

    // "(stale)" once data is older than ~12 minutes (2× the 5-min active
    // baseline, with slack for the slower inactive round-robin cadence).
    private func stalenessLabel(fetched: Date) -> String {
        let age = Int(Date().timeIntervalSince(fetched))
        let stale = age > 720 ? " (stale)" : ""
        switch age {
        case ..<60: return "Updated just now\(stale)"
        case ..<3600: return "Updated \(age / 60)m ago\(stale)"
        default: return "Updated \(age / 3600)h ago\(stale)"
        }
    }
}
