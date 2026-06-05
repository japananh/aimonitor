// The ONE text-button component, app-wide. Since the Tahoe (macOS 26)
// restyle it deliberately pins NOTHING: no font, no controlSize — the
// system renders buttons at the platform's default type scale (13pt on
// Tahoe) so they match System Settings and system menus exactly.
// Keeping the component (rather than bare Buttons) preserves the single
// place to restyle every text button at once, plus the pointer cursor.

import SwiftUI

struct AppTextButton: View {
    let title: String
    let action: () -> Void

    init(_ title: String, action: @escaping () -> Void) {
        self.title = title
        self.action = action
    }

    var body: some View {
        Button(title, action: action)
            .pointerCursor()
    }
}

extension View {
    /// Chrome for buttons with CUSTOM labels (spinner+text combos).
    /// Keep the label's Text at the default font so it matches
    /// AppTextButton.
    func appTextButtonChrome() -> some View {
        self.pointerCursor()
    }
}
