// The ONE text-button component, app-wide. Since the Tahoe (macOS 26)
// restyle it pins no font/controlSize — the system default type scale (13pt)
// matches System Settings. It carries a rounded chrome with a hover highlight
// (a subtle base fill that brightens on mouse-over), applied through
// TextButtonChrome so every text button — plain or custom-label — behaves the
// same.

import SwiftUI

struct AppTextButton: View {
    let title: String
    let action: () -> Void

    init(_ title: String, action: @escaping () -> Void) {
        self.title = title
        self.action = action
    }

    var body: some View {
        Button(action: action) { Text(title) }
            .buttonStyle(.plain)
            .modifier(TextButtonChrome())
            .pointerCursor()
    }
}

extension View {
    /// Chrome for buttons with CUSTOM labels (e.g. spinner + text combos).
    /// Gives them the same rounded fill + hover highlight as AppTextButton.
    func appTextButtonChrome() -> some View {
        self.buttonStyle(.plain)
            .modifier(TextButtonChrome())
            .pointerCursor()
    }
}

extension View {
    /// Hover chrome for small icon-only buttons in account rows (refresh, edit,
    /// delete) — a square rounded background that appears on mouse-over, matching
    /// the header action icons. Apply to the button's label image; pair with
    /// `.buttonStyle(.borderless)`. Unlike the text chrome there's no resting
    /// fill, so a row of icons stays quiet until hovered.
    func iconHoverChrome(size: CGFloat = 20) -> some View {
        modifier(IconHoverChrome(size: size))
    }
}

/// See `iconHoverChrome`. Squares the glyph into a uniform frame so a row of
/// icons aligns, and fills a rounded background only while hovered.
private struct IconHoverChrome: ViewModifier {
    let size: CGFloat
    @State private var hovering = false

    func body(content: Content) -> some View {
        content
            .frame(width: size, height: size)
            .background(
                RoundedRectangle(cornerRadius: 6, style: .continuous)
                    .fill(Color.primary.opacity(hovering ? 0.12 : 0))
            )
            .contentShape(Rectangle())
            .onHover { hovering = $0 }
    }
}

/// Rounded chrome with a hover highlight. Base fill is a faint neutral so the
/// button reads as tappable; hovering deepens it. Color.primary adapts to
/// light/dark (dark text on light, light on dark).
private struct TextButtonChrome: ViewModifier {
    @State private var hovering = false

    func body(content: Content) -> some View {
        content
            .padding(.horizontal, 8)
            .padding(.vertical, 3)
            .background(
                RoundedRectangle(cornerRadius: 6, style: .continuous)
                    .fill(Color.primary.opacity(hovering ? 0.16 : 0.07))
            )
            .contentShape(RoundedRectangle(cornerRadius: 6, style: .continuous))
            .onHover { hovering = $0 }
    }
}
