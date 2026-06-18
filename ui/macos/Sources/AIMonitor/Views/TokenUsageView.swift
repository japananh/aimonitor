// TokenUsageView renders the per-account token breakdown shown in the
// standalone Token-usage window (TokenUsageWindowView): consumption
// (input + output + cache) bucketed by local-time day or hour, read from the
// usage_samples table the daemon fills from Claude Code's JSONL. Distinct from
// the popover's Limits view, which shows the OAuth 5h/7d percentage — this is
// actual tokens processed, attributed to whichever account was active when each
// message was written.

import AppKit
import SwiftUI

// ThinScrollView wraps content in an AppKit NSScrollView forced to the thin
// "overlay" scroller style. SwiftUI's ScrollView inherits the system "Show
// scroll bars" setting, so on "Always" it renders the wide legacy scroller;
// overlay style is the slim bar that floats over the content and auto-hides,
// regardless of that setting. Vertical only — width is pinned to the clip view
// so the content lays out at the visible width and never scrolls sideways.
struct ThinScrollView<Content: View>: NSViewRepresentable {
    let content: Content
    init(@ViewBuilder content: () -> Content) { self.content = content() }

    func makeNSView(context: Context) -> NSScrollView {
        let scroll = NSScrollView()
        scroll.scrollerStyle = .overlay
        scroll.hasVerticalScroller = true
        scroll.hasHorizontalScroller = false
        scroll.autohidesScrollers = true
        scroll.drawsBackground = false
        let host = NSHostingView(rootView: content)
        host.translatesAutoresizingMaskIntoConstraints = false
        scroll.documentView = host
        NSLayoutConstraint.activate([
            host.leadingAnchor.constraint(equalTo: scroll.contentView.leadingAnchor),
            host.trailingAnchor.constraint(equalTo: scroll.contentView.trailingAnchor),
            host.topAnchor.constraint(equalTo: scroll.contentView.topAnchor),
        ])
        return scroll
    }

    func updateNSView(_ scroll: NSScrollView, context: Context) {
        (scroll.documentView as? NSHostingView<Content>)?.rootView = content
    }
}

// TokenUsageWindowView is the standalone "Token usage" window — the per-account
// token breakdown moved out of the menu-bar popover (which stays focused on the
// operational Limits view: "which account, am I near a limit"). Token counts
// are analytical (volume, attribution, cache efficiency) and don't map to the
// rate-limit windows or to cost on a subscription plan, so they're a lean-back
// review that belongs in its own window with room to grow (charts, per-model /
// per-project, export) rather than crowding the dropdown.
struct TokenUsageWindowView: View {
    @ObservedObject var model: AppModel

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 5) {
                Text("Token usage").font(.headline)
                // Catch-all explainer so nobody has to ask what the view means.
                Image(systemName: "info.circle")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .help("""
                        Tokens Claude Code actually processed — per account, by local-time day or hour.
                        This is VOLUME only: it is NOT your rate limit (that's in the menu bar) and NOT a bill (subscription plans aren't charged per token).
                        New = tokens sent + generated this turn, processed at full price.
                        Cached = earlier context reused from the prompt cache, billed ≈10% of the input price.
                        """)
                Spacer()
            }
            .padding(.horizontal, 16)
            .padding(.top, 14)
            .padding(.bottom, 4)

            // Plain-language explainer up front so the numbers below aren't
            // misread as a rate limit or a dollar cost.
            Text("Actual tokens processed — volume only, not your rate limit or a bill.")
                .font(.caption)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
                .padding(.horizontal, 16)
                .padding(.bottom, 8)

            // Legend: the two swatches match the segmented bars below so the
            // colors are self-explaining (hover each for the full definition).
            HStack(spacing: 14) {
                legendItem(color: tokenNewColor, label: "New",
                           help: "Tokens sent + generated this turn — newly processed at full price.")
                legendItem(color: tokenCachedColor, label: "Cached",
                           help: "Earlier context reused from the prompt cache — billed ≈10% of the input price.")
                Spacer()
            }
            .padding(.horizontal, 16)
            .padding(.bottom, 8)

            ThinScrollView {
                TokenUsageView(model: model)
                    .padding(.bottom, 12)
            }
        }
        .frame(width: 400, height: 500)
        // The model loads token data on its poll + on popover open; refresh on
        // appear so the window is populated even if opened without the popover.
        .task { await model.refresh() }
    }

    // One legend entry: a color swatch + label, with a hover definition.
    @ViewBuilder
    private func legendItem(color: Color, label: String, help: String) -> some View {
        HStack(spacing: 5) {
            RoundedRectangle(cornerRadius: 2, style: .continuous)
                .fill(color)
                .frame(width: 11, height: 11)
            Text(label).font(.caption).foregroundStyle(.secondary)
        }
        .help(help)
    }
}

struct TokenUsageView: View {
    @ObservedObject var model: AppModel

    // How many most-recent buckets to show per account. Keeps the popover
    // compact; the CLI (`aimonitor tokens`) is the full view.
    private var maxBuckets: Int { model.tokensHourly ? 12 : 7 }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            // Granularity is a refinement *inside* the Tokens tab, not a peer
            // of the Limits/Tokens tab — so it's a light, right-aligned text
            // toggle (active = token-blue + semibold) rather than a second
            // full-width segmented control, which stacked under the tab read
            // as a cramped 4-button block.
            HStack(spacing: 2) {
                Spacer()
                GranularityButton(title: "Daily", active: !model.tokensHourly, activeColor: tokenNewColor) {
                    model.tokensHourly = false
                }
                Text("·").font(.system(size: 11)).foregroundStyle(.tertiary)
                GranularityButton(title: "Hourly", active: model.tokensHourly, activeColor: tokenNewColor) {
                    model.tokensHourly = true
                }
            }
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
                    AccountTokenCard(
                        acct: acct,
                        isActive: model.status?.active_label == acct.label,
                        buckets: model.tokenUsageByAccount[acct.id] ?? [],
                        maxBuckets: maxBuckets)
                }
            }
        }
        .padding(.horizontal, 16)
        .padding(.top, 6)
        .padding(.bottom, 4)
    }


    // Accounts that have any token data in the current window, in the same
    // order as the Limits tab (model.accounts is sorted by label).
    private var accountsWithData: [AccountRow] {
        model.accounts.filter { !(model.tokenUsageByAccount[$0.id]?.isEmpty ?? true) }
    }

}

// AccountTokenCard is one collapsible per-account section. Collapsed it's a
// single header line (name + window total) so the window stays short when there
// are many accounts; expanded it adds the new/cached composition line and the
// per-day/hour bars. The active account starts expanded (the one you most
// likely want to see); the rest start collapsed. The list is already capped to
// the most-recent N buckets — the CLI (`aimonitor tokens`) is the full history.
private struct AccountTokenCard: View {
    let acct: AccountRow
    let isActive: Bool
    let buckets: [TokenBucketRow] // oldest-first, as the store returns them
    let maxBuckets: Int
    @State private var expanded: Bool

    init(acct: AccountRow, isActive: Bool, buckets: [TokenBucketRow], maxBuckets: Int) {
        self.acct = acct
        self.isActive = isActive
        self.buckets = buckets
        self.maxBuckets = maxBuckets
        // Active account open by default; others collapsed to keep it short.
        _expanded = State(initialValue: isActive)
    }

    var body: some View {
        // Newest buckets first, capped. Totals span the whole window (not just
        // the shown buckets) so the header figure matches the CLI.
        let recent = Array(buckets.suffix(maxBuckets).reversed())
        let maxTotal = max(1, recent.map(\.total).max() ?? 1)
        let windowTotal = buckets.reduce(Int64(0)) { $0 + $1.total }
        let newTotal = buckets.reduce(Int64(0)) { $0 + $1.input + $1.output }
        let cacheTotal = buckets.reduce(Int64(0)) { $0 + $1.cacheRead + $1.cacheWrite }
        let cachedPct = windowTotal > 0 ? Int((Double(cacheTotal) / Double(windowTotal) * 100).rounded()) : 0

        VStack(alignment: .leading, spacing: 6) {
            // Clickable header — the whole row toggles expand/collapse.
            Button {
                withAnimation(.easeInOut(duration: 0.15)) { expanded.toggle() }
            } label: {
                HStack(spacing: 4) {
                    Image(systemName: "chevron.right")
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(.secondary)
                        .rotationEffect(.degrees(expanded ? 90 : 0))
                    if isActive {
                        Image(systemName: "checkmark.circle.fill")
                            .foregroundStyle(.green)
                            .font(.caption)
                    }
                    Text(acct.label).font(.headline)
                    Spacer()
                    Text("\(compactTokens(windowTotal)) total")
                        .font(.caption.monospacedDigit())
                        .foregroundStyle(.secondary)
                        .help("All tokens processed in the shown window — new (sent + generated) plus cached (reused context).")
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .pointerCursor()
            .help(expanded ? "Collapse" : "Expand the daily/hourly breakdown")

            if expanded {
                Text("\(compactTokens(newTotal)) new · \(cachedPct)% reused from cache")
                    .font(.caption2.monospacedDigit())
                    .foregroundStyle(.secondary)
                    .help("Of \(compactTokens(windowTotal)) tokens here, \(compactTokens(newTotal)) were newly processed (your prompts + the replies, full price) and \(compactTokens(cacheTotal)) were reused from cache (earlier context, ≈10% of input price) — \(cachedPct)% of the total.")

                ForEach(recent, id: \.bucket) { b in
                    bucketRow(b, maxTotal: maxTotal)
                }
            }
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        // Control-Center-style module card, matching the Limits account cards:
        // a semantic control-background fill plus an accent tint when active,
        // finished with a hairline separator stroke. On the plain window
        // backdrop (not the translucent glass panel) the fill alone is too
        // faint, so the stroke is what makes each section read as a card.
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
    }

    @ViewBuilder
    private func bucketRow(_ b: TokenBucketRow, maxTotal: Int64) -> some View {
        // Bar length encodes total volume (vs the biggest bucket); the split
        // shows composition — solid = new (real work), light = cached (reuse) —
        // so a long bar that's mostly light reads as "lots of cache, little
        // new work" at a glance, which a single-color total bar hid.
        let newTok = b.input + b.output
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
                let barW = max(2, geo.size.width * frac)
                let newFrac = b.total > 0 ? CGFloat(Double(newTok) / Double(b.total)) : 0
                ZStack(alignment: .leading) {
                    Capsule().fill(Color(nsColor: .quaternaryLabelColor))
                    HStack(spacing: 0) {
                        Rectangle().fill(tokenNewColor).frame(width: barW * newFrac)
                        Rectangle().fill(tokenCachedColor)
                    }
                    .frame(width: barW)
                    .clipShape(Capsule())
                }
            }
            .frame(height: 6)
        }
        .help(bucketTooltip(b))
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
        let newTok = b.input + b.output
        let cacheTok = b.cacheRead + b.cacheWrite
        return "\(bucketLabel(b.bucket)) — \(compactTokens(b.total)) total: "
            + "\(compactTokens(newTok)) new (sent + generated) + "
            + "\(compactTokens(cacheTok)) cached (reused context) · \(b.messages) messages"
    }
}

// Token-bar colors, shared by the window legend and the segmented bars so the
// legend swatches always match the bars. "New" is a calm appearance-aware blue
// (token counts have no severity threshold, so a single hue reads cleaner than
// the Limits bars' green/amber/red); "Cached" is the same hue, lighter, to read
// as the cheap/secondary portion of the same metric.
private var tokenNewColor: Color {
    adaptiveColor(
        light: NSColor(red: 0.20, green: 0.50, blue: 0.95, alpha: 1),
        dark: NSColor(red: 0.45, green: 0.68, blue: 1.00, alpha: 1))
}
private var tokenCachedColor: Color { tokenNewColor.opacity(0.30) }

// GranularityButton is one side of the Daily/Hourly toggle: a borderless text
// button that tints + bolds when active and shows a subtle rounded background
// on hover, so the pair reads as a quiet refinement of the Tokens tab rather
// than a button row competing with the Limits/Tokens segmented control above.
private struct GranularityButton: View {
    let title: String
    let active: Bool
    let activeColor: Color
    let action: () -> Void
    @State private var hovering = false

    var body: some View {
        Button(action: action) {
            Text(title)
                // Font.caption is 10pt on macOS; bumped +1px for legibility.
                .font(.system(size: 11, weight: active ? .semibold : .regular))
                .foregroundStyle(active ? activeColor : Color.secondary)
                .padding(.horizontal, 6)
                .padding(.vertical, 2)
                .background(
                    RoundedRectangle(cornerRadius: 5, style: .continuous)
                        .fill(Color.primary.opacity(hovering ? 0.10 : 0))
                )
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .pointerCursor()
        .onHover { hovering = $0 }
        .help(title == "Daily" ? "Group token usage by calendar day" : "Group token usage by hour")
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
