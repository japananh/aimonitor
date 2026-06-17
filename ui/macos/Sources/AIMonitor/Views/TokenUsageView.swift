// TokenUsageView is the "Tokens" tab of the popover: per-account token
// consumption (input + output + cache) bucketed by local-time day or hour,
// read from the usage_samples table the daemon fills from Claude Code's
// JSONL. Distinct from the "Limits" tab, which shows the OAuth 5h/7d
// percentage — this is actual tokens burned, attributed to whichever account
// was active when each message was written.

import SwiftUI

struct TokenUsageView: View {
    @ObservedObject var model: AppModel

    // How many most-recent buckets to show per account. Keeps the popover
    // compact; the CLI (`aimonitor tokens`) is the full view.
    private var maxBuckets: Int { model.tokensHourly ? 12 : 7 }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Picker("", selection: $model.tokensHourly) {
                Text("Daily").tag(false)
                Text("Hourly").tag(true)
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            // Reload at the new granularity the moment the user flips it
            // (otherwise the change shows only on the next 2s poll).
            .onChange(of: model.tokensHourly) { _, _ in
                Task { await model.refresh() }
            }

            if accountsWithData.isEmpty {
                Text("No token usage recorded yet. Use Claude Code with `aimonitor daemon` running and it'll show up here.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                    .padding(.vertical, 8)
            } else {
                ForEach(accountsWithData) { acct in
                    card(for: acct)
                }
            }
        }
        .padding(.horizontal, 16)
        .padding(.bottom, 4)
    }

    // Accounts that have any token data in the current window, in the same
    // order as the Limits tab (model.accounts is sorted by label).
    private var accountsWithData: [AccountRow] {
        model.accounts.filter { !(model.tokenUsageByAccount[$0.id]?.isEmpty ?? true) }
    }

    @ViewBuilder
    private func card(for acct: AccountRow) -> some View {
        let isActive = (model.status?.active_label == acct.label)
        // Newest buckets first, capped. Store returns oldest-first.
        let all = model.tokenUsageByAccount[acct.id] ?? []
        let recent = Array(all.suffix(maxBuckets).reversed())
        let maxTotal = max(1, recent.map(\.total).max() ?? 1)
        let windowTotal = all.reduce(Int64(0)) { $0 + $1.total }

        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 4) {
                if isActive {
                    Image(systemName: "checkmark.circle.fill")
                        .foregroundStyle(.green)
                        .font(.caption)
                }
                Text(acct.label).font(.headline)
                Spacer()
                Text("\(compactTokens(windowTotal)) tok")
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
                    .help("Total tokens in the shown window (input + output + cache)")
            }

            ForEach(recent, id: \.bucket) { b in
                bucketRow(b, maxTotal: maxTotal)
            }
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            RoundedRectangle(cornerRadius: 12, style: .continuous)
                .fill(Color(nsColor: .controlBackgroundColor).opacity(0.6))
        )
        .background(
            RoundedRectangle(cornerRadius: 12, style: .continuous)
                .fill(Color.accentColor.opacity(isActive ? 0.22 : 0))
        )
    }

    @ViewBuilder
    private func bucketRow(_ b: TokenBucketRow, maxTotal: Int64) -> some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack(spacing: 6) {
                Text(bucketLabel(b.bucket))
                    .font(.caption.monospaced())
                    .foregroundStyle(.primary)
                Spacer()
                Text(compactTokens(b.total))
                    .font(.caption.monospacedDigit())
                    .foregroundStyle(.secondary)
            }
            GeometryReader { geo in
                let frac = CGFloat(Double(b.total) / Double(maxTotal))
                ZStack(alignment: .leading) {
                    Capsule().fill(Color(nsColor: .quaternaryLabelColor))
                    Capsule()
                        .fill(tokenBarColor)
                        .frame(width: max(2, geo.size.width * frac))
                }
            }
            .frame(height: 5)
        }
        .help(bucketTooltip(b))
    }

    // Calm appearance-aware blue for token bars. Unlike the Limits bars,
    // token counts have no severity threshold, so a single accent reads
    // cleaner than green/amber/red here.
    private var tokenBarColor: Color {
        adaptiveColor(
            light: NSColor(red: 0.20, green: 0.50, blue: 0.95, alpha: 1),
            dark: NSColor(red: 0.45, green: 0.68, blue: 1.00, alpha: 1))
    }

    // For daily buckets ("2026-06-16") show "Jun 16"; for hourly
    // ("2026-06-16 14:00") show "16 14:00". Falls back to the raw string.
    private func bucketLabel(_ bucket: String) -> String {
        let parts = bucket.split(separator: " ")
        let date = parts.first.map(String.init) ?? bucket
        let dc = date.split(separator: "-")
        guard dc.count == 3 else { return bucket }
        let months = ["", "Jan", "Feb", "Mar", "Apr", "May", "Jun",
                      "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"]
        let mi = Int(dc[1]) ?? 0
        let mon = (mi >= 1 && mi <= 12) ? months[mi] : String(dc[1])
        if parts.count == 2 {
            return "\(dc[2]) \(parts[1])" // hourly: "16 14:00"
        }
        return "\(mon) \(dc[2])" // daily: "Jun 16"
    }

    private func bucketTooltip(_ b: TokenBucketRow) -> String {
        "in \(b.input)  ·  out \(b.output)  ·  cache r \(b.cacheRead)  ·  cache w \(b.cacheWrite)  ·  \(b.messages) msgs"
    }
}

// compactTokens renders a token count in a compact form: 742, 1.2K, 31.2K,
// 1.4M, 2.1B. Used wherever space is tight in the widget.
func compactTokens(_ n: Int64) -> String {
    let v = Double(n)
    switch abs(n) {
    case 0..<1_000:
        return "\(n)"
    case 1_000..<1_000_000:
        return String(format: "%.1fK", v / 1_000)
    case 1_000_000..<1_000_000_000:
        return String(format: "%.1fM", v / 1_000_000)
    default:
        return String(format: "%.1fB", v / 1_000_000_000)
    }
}
