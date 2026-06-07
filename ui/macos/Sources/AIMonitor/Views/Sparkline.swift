// Sparkline draws a compact, axis-free utilization trend — a glance cue
// for "is this account climbing fast?" that the point-in-time bars can't
// convey. Values are percentages (0..100), oldest-first. Y is fixed to the
// 0..100 domain (not auto-scaled) so the slope reads as real utilization,
// not relative wiggle.

import SwiftUI

struct Sparkline: View {
    /// Utilization values, oldest-first, each 0..100.
    let values: [Double]
    /// Line + fill tint (caller picks it from the latest value's severity).
    let color: Color
    var height: CGFloat = 22

    var body: some View {
        // Need at least two points to draw a segment; below that the trend
        // is meaningless, so the caller hides it.
        Canvas { ctx, size in
            guard values.count >= 2 else { return }
            let maxX = max(1, CGFloat(values.count - 1))

            func point(_ i: Int) -> CGPoint {
                let x = size.width * CGFloat(i) / maxX
                let frac = CGFloat(min(max(values[i], 0), 100) / 100.0)
                // Invert: 0% at the bottom, 100% at the top. Inset 1px top
                // and bottom so the stroke isn't clipped.
                let y = (size.height - 2) * (1 - frac) + 1
                return CGPoint(x: x, y: y)
            }

            var line = Path()
            line.move(to: point(0))
            for i in 1..<values.count { line.addLine(to: point(i)) }

            // Soft fill under the line for body, then the crisp stroke.
            var fill = line
            fill.addLine(to: CGPoint(x: size.width, y: size.height))
            fill.addLine(to: CGPoint(x: 0, y: size.height))
            fill.closeSubpath()
            ctx.fill(fill, with: .color(color.opacity(0.12)))
            ctx.stroke(line, with: .color(color.opacity(0.9)),
                       style: StrokeStyle(lineWidth: 1.5, lineCap: .round, lineJoin: .round))
        }
        .frame(height: height)
        .accessibilityHidden(true)
    }
}
