// Theme preference for the menu-bar app: follow the OS, or force light/dark.
// Persisted in UserDefaults under appThemeKey; applied by setting
// NSApp.appearance (nil = inherit from the OS).

import AppKit
import SwiftUI

/// severityColor maps utilization (0..100) to the bar/trend tint, with one set
/// of thresholds used everywhere: green <60, amber <85, red ≥85. Deliberately
/// MUTED tones — the full-saturation .systemGreen/.systemYellow/.systemRed read
/// as garish ("chói") on the small bars. These mid-tone RGBs stay legible in
/// both light and dark appearance.
func severityColor(for pct: Double) -> Color {
    switch pct {
    case ..<60: return Color(red: 0.40, green: 0.64, blue: 0.46) // sage green
    case ..<85: return Color(red: 0.84, green: 0.66, blue: 0.36) // muted amber
    default:    return Color(red: 0.80, green: 0.44, blue: 0.42) // soft red
    }
}

/// UserDefaults key for the appearance preference: "system", "light", "dark".
let appThemeKey = "appTheme"

/// Default when unset: inherit from the OS.
let defaultAppTheme = "system"

/// Applies the theme to the whole app. "system" (or anything unrecognized)
/// clears the override so the app follows the OS appearance.
@MainActor
func applyAppearance(_ theme: String) {
    switch theme {
    case "light":
        NSApp.appearance = NSAppearance(named: .aqua)
    case "dark":
        NSApp.appearance = NSAppearance(named: .darkAqua)
    default:
        NSApp.appearance = nil // inherit from the OS
    }
}

/// UserDefaults key for the Dock-icon preference. Off by default — the app
/// is a menu-bar accessory; the Dock icon is an opt-in convenience for
/// users whose menu bar is crowded (e.g. the icon hides behind the notch).
let showDockIconKey = "showDockIcon"

/// Shows or hides the Dock icon by flipping the activation policy.
/// Clicking the Dock icon opens the panel (applicationShouldHandleReopen).
@MainActor
func applyDockIconPolicy(_ show: Bool) {
    NSApp.setActivationPolicy(show ? .regular : .accessory)
    if show {
        // Re-activate so the policy flip takes effect without an app restart
        // (otherwise the Dock icon can appear only after the next focus).
        NSApp.activate(ignoringOtherApps: true)
    }
}
