// PanelChrome is the popover panel's window dressing. On macOS 26+
// (Tahoe) it renders the Liquid Glass material system menus use — the
// panel floats as translucent glass under the menu bar. On older
// systems it keeps the pre-Tahoe look: solid window background.
//
// Glass is applied to the panel CHROME only, per the Tahoe guidance
// ("Liquid Glass is for the navigation layer, not content"): the
// content above it — colored usage bars, the green active check, red
// error text — draws normally on top and is never vibrancy-blended,
// which is what desaturated colors when we tried .regularMaterial.

import SwiftUI

/// Corner radius of the floating panel. Tahoe menus/popovers use a
/// noticeably larger radius than the pre-26 12pt.
private let panelCornerRadius: CGFloat = 16

struct PanelChrome: ViewModifier {
    func body(content: Content) -> some View {
        if #available(macOS 26.0, *) {
            content
                // Clip the content too: dividers and the active-row tint
                // span the panel's full width, so without the clip their
                // square corners would poke past the glass shape.
                .clipShape(RoundedRectangle(cornerRadius: panelCornerRadius, style: .continuous))
                .glassEffect(
                    .regular,
                    in: RoundedRectangle(cornerRadius: panelCornerRadius, style: .continuous)
                )
        } else {
            content
                .background(Color(nsColor: .windowBackgroundColor))
                .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
        }
    }
}

extension View {
    /// The popover panel's floating chrome: Liquid Glass on Tahoe,
    /// solid rounded background before it.
    func panelChrome() -> some View {
        modifier(PanelChrome())
    }
}
