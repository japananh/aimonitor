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
private let panelCornerRadius: CGFloat = 16

extension View {
    /// The popover panel's chrome: solid window background, rounded corners.
    func panelChrome() -> some View {
        self
            .background(Color(nsColor: .windowBackgroundColor))
            .clipShape(RoundedRectangle(cornerRadius: panelCornerRadius, style: .continuous))
    }
}
