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
        // onContinuousHover (not plain onHover) re-asserts the cursor on every
        // mouse move inside the view. onHover fires only on enter/exit, so an
        // AppKit control hosted underneath — e.g. a segmented Picker — can
        // reset the cursor back to the arrow as the pointer moves across it,
        // and the pointing hand never appears. Re-setting on each move keeps it
        // stable. set() (vs push/pop) can't get unbalanced if the popover
        // closes mid-hover; worst case the cursor self-corrects on next move.
        content.onContinuousHover { phase in
            switch phase {
            case .active:
                NSCursor.pointingHand.set()
            case .ended:
                NSCursor.arrow.set()
            }
        }
    }
}
