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
