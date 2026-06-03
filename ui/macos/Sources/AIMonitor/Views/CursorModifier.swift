// pointerCursor() shows the pointing-hand cursor while hovering a control,
// which SwiftUI does not do by default for Buttons on macOS.
//
// We use NSCursor.set() (not push()/pop()): in a transient popover the
// hover-exit callback may never fire if the popover closes mid-hover, and an
// unbalanced push() would leave the pointing-hand cursor stuck system-wide.
// set() is idempotent and can't get unbalanced — worst case the cursor stays
// pointing-hand until the next mouse move, which AppKit corrects immediately.

import SwiftUI
import AppKit

extension View {
    func pointerCursor() -> some View {
        modifier(PointerCursorModifier())
    }
}

private struct PointerCursorModifier: ViewModifier {
    func body(content: Content) -> some View {
        content.onHover { inside in
            if inside {
                NSCursor.pointingHand.set()
            } else {
                NSCursor.arrow.set()
            }
        }
    }
}
