// SessionBarView is the REQUIRED panel — per the v1 spec, this cannot
// be toggled off from Preferences. It shows the active account and
// session %, plus a horizontal progress bar coloured by tripwire band.

import SwiftUI

struct SessionBarView: View {
    @ObservedObject var model: AppModel

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text(activeLabel)
                    .font(.headline)
                Spacer()
                Text(percentLabel)
                    .font(.subheadline.monospacedDigit())
                    .foregroundStyle(.secondary)
            }
            // Claude identity of the active account: email (and the label
            // is the headline above). Hidden when identity isn't captured
            // so the header doesn't grow a blank line.
            if let email = model.activeEmail {
                Text(email)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)
            }
            ProgressView(value: clampedPercent / 100.0)
                .tint(barColor)
                .progressViewStyle(.linear)
            if let st = model.status {
                Text(footerCaption(st))
                    .font(.caption2)
                    .foregroundStyle(.tertiary)
            } else {
                Text("Daemon not running. Run `aimonitor daemon run` or enable autostart.")
                    .font(.caption2)
                    .foregroundStyle(.orange)
            }
        }
        .padding(12)
    }

    private var activeLabel: String {
        if let label = model.status?.active_label, !label.isEmpty {
            return label
        }
        return "—"
    }

    private var clampedPercent: Double {
        guard let pct = model.status?.session_percent else { return 0 }
        return min(max(pct, 0), 100)
    }

    private var percentLabel: String {
        guard model.status != nil else { return "—" }
        return String(format: "%.1f%%", clampedPercent)
    }

    // Tripwire-band colouring matches the defaults: green < 40, amber
    // 40–60, orange 60–100, red ≥ 100. Users with custom thresholds
    // still get a useful gradient; we deliberately don't read their
    // YAML config here (v1 keeps the widget thin).
    private var barColor: Color {
        switch clampedPercent {
        case ..<40: return .green
        case ..<60: return .yellow
        case ..<100: return .orange
        default: return .red
        }
    }

    private func footerCaption(_ st: DaemonStatus) -> String {
        let mode = st.auto_switch_enabled ? "auto-switch on" : "auto-switch off"
        return "\(formatTokens(st.usage_since_reset)) / \(formatTokens(st.observed_budget)) tokens · \(mode)"
    }

    private func formatTokens(_ n: Int64) -> String {
        let f = NumberFormatter()
        f.numberStyle = .decimal
        return f.string(from: NSNumber(value: n)) ?? "\(n)"
    }
}
