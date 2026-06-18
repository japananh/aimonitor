// The ONE text-button component, app-wide. It pins no font/controlSize — the
// system default type scale (13pt) matches System Settings. It carries a
// relogin-style pill chrome (faint accent tint → solid accent + white text on
// hover, capsule, pointer cursor, no border/shadow), applied through
// TextButtonChrome so every text button — plain or custom-label — matches.

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

/// The ONE icon-button component, app-wide — header actions (＋/bug/gear),
/// per-row refresh, and the row overflow (⋯) menu all use it, so they share one
/// look: a uniform square with a rounded background that appears on hover, a
/// centered glyph, pointer cursor, and a tooltip. Adding a new icon control =
/// instantiate this; you can't drift the style per-button.
///
/// Three flavors via the initializers: a tap action, a loading state (spinner
/// in place of the glyph, auto-disabled), and a menu (same chrome; the hover
/// highlight is tracked on the outer view, so it works even though a Menu would
/// otherwise swallow the label's own onHover). `EmptyView` is the menu type for
/// the non-menu flavors.
struct IconActionButton<MenuContent: View>: View {
    let systemName: String
    let help: String
    var size: CGFloat = 22
    var glyphSize: CGFloat = 14
    /// Nudges an optically-off glyph (e.g. the ladybug sits ~1px low) without
    /// shifting the hover background.
    var yNudge: CGFloat = 0
    var isLoading: Bool = false

    private let action: (() -> Void)?
    private let menu: (() -> MenuContent)?

    @State private var hovering = false

    /// Tap-action button (no menu).
    init(
        systemName: String,
        help: String,
        size: CGFloat = 22,
        glyphSize: CGFloat = 14,
        yNudge: CGFloat = 0,
        isLoading: Bool = false,
        action: @escaping () -> Void
    ) where MenuContent == EmptyView {
        self.systemName = systemName
        self.help = help
        self.size = size
        self.glyphSize = glyphSize
        self.yNudge = yNudge
        self.isLoading = isLoading
        self.action = action
        self.menu = nil
    }

    /// Menu button — the label is the same glyph chrome; `menu` builds the items.
    init(
        systemName: String,
        help: String,
        size: CGFloat = 22,
        glyphSize: CGFloat = 14,
        yNudge: CGFloat = 0,
        @ViewBuilder menu: @escaping () -> MenuContent
    ) {
        self.systemName = systemName
        self.help = help
        self.size = size
        self.glyphSize = glyphSize
        self.yNudge = yNudge
        self.action = nil
        self.menu = menu
    }

    // The sized rounded rect is the base; the glyph (or spinner) is overlaid
    // centered, so SF Symbol baseline metrics can't push it off-center.
    private var chrome: some View {
        RoundedRectangle(cornerRadius: 6, style: .continuous)
            .fill(Color.primary.opacity(hovering ? 0.12 : 0))
            .frame(width: size, height: size)
            .overlay {
                if isLoading {
                    ProgressView().controlSize(.small).scaleEffect(0.6)
                } else {
                    Image(systemName: systemName).font(.system(size: glyphSize)).offset(y: yNudge)
                }
            }
            .contentShape(Rectangle())
    }

    var body: some View {
        Group {
            if let menu {
                Menu { menu() } label: { chrome }
                    .menuStyle(.borderlessButton)
                    .menuIndicator(.hidden)
                    .fixedSize()
            } else {
                Button(action: { action?() }) { chrome }
                    .buttonStyle(.plain)
                    .disabled(isLoading)
            }
        }
        .onHover { hovering = $0 }
        .pointerCursor()
        .help(help)
    }
}

/// The ONE text-button look, app-wide — a relogin-style pill: a faint accent
/// tint with accent text at rest, filling to a solid accent capsule with white
/// text on hover. No border, no shadow. Shared by AppTextButton and
/// appTextButtonChrome so every text button (plain or custom-label) matches the
/// Re-login button's style.
private struct TextButtonChrome: ViewModifier {
    @State private var hovering = false

    func body(content: Content) -> some View {
        content
            .foregroundStyle(hovering ? Color.white : Color.accentColor)
            .padding(.horizontal, 10)
            .padding(.vertical, 3)
            .background(Capsule().fill(hovering ? Color.accentColor : Color.accentColor.opacity(0.15)))
            .contentShape(Capsule())
            .onHover { hovering = $0 }
    }
}
