// Theme preference for the menu-bar app: follow the OS, or force light/dark.
// Persisted in UserDefaults under appThemeKey; applied by setting
// NSApp.appearance (nil = inherit from the OS).

import AppKit
import SwiftUI

/// severityColor maps utilization (0..100) to the bar/trend tint, one set of
/// thresholds everywhere: green <60, amber <85, red ≥85. Softer than the
/// full-saturation .systemGreen/.systemYellow/.systemRed (which read as garish),
/// and APPEARANCE-AWARE: a slightly deeper shade on light backgrounds, a
/// brighter one on dark — so it reads well whether the user is in light or dark.
func severityColor(for pct: Double) -> Color {
    switch pct {
    case ..<60:
        return adaptiveColor(
            light: NSColor(red: 0.13, green: 0.78, blue: 0.34, alpha: 1),
            dark: NSColor(red: 0.46, green: 0.95, blue: 0.58, alpha: 1))
    case ..<85:
        return adaptiveColor(
            light: NSColor(red: 0.98, green: 0.80, blue: 0.12, alpha: 1),
            dark: NSColor(red: 1.00, green: 0.92, blue: 0.40, alpha: 1))
    default:
        return adaptiveColor(
            light: NSColor(red: 0.90, green: 0.22, blue: 0.20, alpha: 1),
            dark: NSColor(red: 1.00, green: 0.46, blue: 0.42, alpha: 1))
    }
}

/// adaptiveColor returns a Color that resolves to `light` under the Aqua
/// appearance and `dark` under Dark Aqua, so fixed RGBs don't look wrong in one
/// of the two modes the user switches between.
func adaptiveColor(light: NSColor, dark: NSColor) -> Color {
    Color(nsColor: NSColor(name: nil) { appearance in
        appearance.bestMatch(from: [.aqua, .darkAqua]) == .darkAqua ? dark : light
    })
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
