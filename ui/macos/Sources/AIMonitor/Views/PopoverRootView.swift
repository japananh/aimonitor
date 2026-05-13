// PopoverRootView is the SwiftUI root for the menu bar popover. It
// composes the required SessionBarView with the optional
// AccountTableView and surfaces the menu items below.

import SwiftUI

struct PopoverRootView: View {
    @ObservedObject var model: AppModel
    let openPreferences: () -> Void
    let quit: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            SessionBarView(model: model)
            if model.showAccountPanel {
                Divider()
                AccountTableView(model: model)
            }
            Divider()
            HStack {
                Button("Refresh") {
                    Task { await model.refresh() }
                }
                Spacer()
                Button("Preferences…", action: openPreferences)
                Button("Quit", action: quit)
            }
            .controlSize(.small)
            .padding(.horizontal, 12)
            .padding(.vertical, 6)

            if let err = model.lastError {
                Divider()
                Text(err)
                    .font(.caption2)
                    .foregroundStyle(.red)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 4)
            }
        }
        .frame(width: 340)
    }
}
