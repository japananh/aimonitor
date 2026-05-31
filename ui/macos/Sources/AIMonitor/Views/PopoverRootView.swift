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
            // Rate-limit utilization bars (5h / 7d). UsageBarsView hides
            // itself when the daemon hasn't published any limits yet, so
            // no Divider here would orphan if the bars are absent.
            if let st = model.status,
               st.five_hour_pct != nil || st.seven_day_pct != nil {
                Divider()
                UsageBarsView(model: model)
            }
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
