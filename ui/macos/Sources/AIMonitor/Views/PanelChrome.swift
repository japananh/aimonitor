// PanelChrome is the popover panel's window dressing: a solid window
// background clipped to a Tahoe-sized rounded rectangle.
//
// We deliberately do NOT apply Liquid Glass (.glassEffect) here. Tahoe's
// own guidance reserves glass for the navigation layer "above content" —
// and this panel is mostly content (the account cards, usage bars,
// status colors). Glass made the translucent panel (a) show the window's
// backing as a black rectangle in the corners and (b) diverge from the
// opaque Settings window in dark mode. A solid windowBackgroundColor
// matches the Settings window in both light and dark, and the Tahoe look
// comes from the SDK-26 controls and the rounded account cards instead.

import SwiftUI

/// Corner radius of the floating panel — Tahoe rounds popovers/menus more
/// generously than the pre-26 12pt.
let panelCornerRadius: CGFloat = 16

/// Transparent margin around the panel, giving the soft drop shadow room to
/// render (the window background is clear, so this shows the desktop).
/// positionPanel compensates by this much so the visible card — not the
/// padded window — stays anchored under the menu-bar icon.
let panelShadowMargin: CGFloat = 14

extension View {
    /// The popover panel's chrome: solid window background, rounded corners, a
    /// subtle system separator hairline (like the Preferences window), and a
    /// SOFT drop shadow drawn by us into a transparent margin — the OS window
    /// shadow is off because on a borderless transparent panel it rims dark.
    func panelChrome() -> some View {
        self
            .background(Color(nsColor: .windowBackgroundColor))
            .clipShape(RoundedRectangle(cornerRadius: panelCornerRadius, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: panelCornerRadius, style: .continuous)
                    .strokeBorder(Color(nsColor: .separatorColor), lineWidth: 1)
            )
            // Soft shadow + transparent margin so it has room to render. The
            // window background is clear, so the margin shows the desktop, not a
            // dark box.
            .shadow(color: .black.opacity(0.20), radius: 9, x: 0, y: 3)
            .padding(panelShadowMargin)
    }
}
