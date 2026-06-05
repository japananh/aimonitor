// The ONE text-button component, app-wide. macOS's bordered button style
// ignores .font applied from OUTSIDE the button (it derives the label font
// from controlSize), so the font must sit on the Text INSIDE the label —
// which is why this is a component and not just a modifier. Every button
// whose label is text must be an AppTextButton; never hand-roll one.

import SwiftUI

/// Label size for all text buttons (and for custom button labels that
/// can't use AppTextButton directly, e.g. spinner+text combos).
let appButtonFontSize: CGFloat = 12

struct AppTextButton: View {
    let title: String
    let action: () -> Void

    init(_ title: String, action: @escaping () -> Void) {
        self.title = title
        self.action = action
    }

    var body: some View {
        Button(action: action) {
            Text(title).font(.system(size: appButtonFontSize))
        }
        .controlSize(.small)
        .pointerCursor()
    }
}

extension View {
    /// Chrome for buttons with CUSTOM labels (apply appButtonFontSize to
    /// the inner Text yourself).
    func appTextButtonChrome() -> some View {
        self
            .controlSize(.small)
            .pointerCursor()
    }
}
