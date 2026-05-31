// UsageBarsView renders the active account's OAuth-introspected rate-limit
// utilization as two horizontal bars (5-hour and 7-day) with reset-time
// countdowns. The data is fed by the daemon's UsageScheduler on a
// ~5-minute jittered cadence; the values arrive in DaemonStatus.
//
// Hidden entirely when the daemon hasn't published any limits yet — a
// blank panel beats stale or zero-defaulted bars.

import SwiftUI

struct UsageBarsView: View {
    @ObservedObject var model: AppModel

    var body: some View {
        if let status = model.status,
           status.five_hour_pct != nil || status.seven_day_pct != nil {
            VStack(alignment: .leading, spacing: 6) {
                HStack {
                    Text("Limits")
                        .font(.subheadline).bold()
                    Spacer()
                    if let fetched = status.limits_fetched_at {
                        Text(stalenessLabel(fetched: fetched))
                            .font(.caption2)
                            .foregroundStyle(.tertiary)
                    }
                }
                if let pct = status.five_hour_pct {
                    bar(label: "5h", pct: pct, resetAt: status.five_hour_reset_at)
                }
                if let pct = status.seven_day_pct {
                    bar(label: "7d", pct: pct, resetAt: status.seven_day_reset_at)
                }
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 6)
        }
    }

    // bar is the single 5h-or-7d row: label + percent + linear progress +
    // optional reset countdown.
    @ViewBuilder
    private func bar(label: String, pct: Double, resetAt: Date?) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 6) {
                Text(label)
                    .font(.caption.monospaced())
                    .frame(width: 22, alignment: .leading)
                ProgressView(value: min(max(pct, 0), 100) / 100.0)
                    .tint(color(for: pct))
                    .progressViewStyle(.linear)
                Text(String(format: "%.0f%%", pct))
                    .font(.caption.monospacedDigit())
                    .frame(width: 36, alignment: .trailing)
                    .foregroundStyle(.secondary)
            }
            if let resetAt {
                Text("resets in \(resetCountdown(resetAt))")
                    .font(.caption2)
                    .foregroundStyle(.tertiary)
                    .padding(.leading, 28)
            }
        }
    }

    // color thresholds mirror claude-bar's palette: green below 60%, amber
    // below 85%, red at or above 85%. The Swift `SessionBarView` uses a
    // different palette (40/60/100) because its semantics are different —
    // that bar is session-percent-of-budget, not absolute rate-limit
    // utilization. Don't unify; they're answering different questions.
    private func color(for pct: Double) -> Color {
        switch pct {
        case ..<60: return .green
        case ..<85: return .yellow
        default: return .red
        }
    }

    // resetCountdown formats the duration until the reset timestamp as a
    // human-friendly short string ("3h 12m", "47m", "2d 4h"). Never
    // shows seconds — at 5-minute polling cadence sub-minute precision
    // would be noise.
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

    // stalenessLabel shows when the bars were last fetched, and an
    // explicit "(stale)" suffix once the data is more than 10 minutes
    // old (2× the baseline 5-minute scheduler interval). A user looking
    // at a 30-minute-old number should know not to trust it for
    // decisions.
    private func stalenessLabel(fetched: Date) -> String {
        let age = Int(Date().timeIntervalSince(fetched))
        let stale = age > 600 ? " (stale)" : ""
        switch age {
        case ..<60: return "just now\(stale)"
        case ..<3600: return "\(age / 60)m ago\(stale)"
        default: return "\(age / 3600)h ago\(stale)"
        }
    }
}
