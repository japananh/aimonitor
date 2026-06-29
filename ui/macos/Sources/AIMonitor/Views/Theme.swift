// Theme preference for the menu-bar app: follow the OS, or force light/dark.
// Persisted in UserDefaults under appThemeKey; applied by setting
// NSApp.appearance (nil = inherit from the OS).

import AppKit
import SwiftUI

/// sevenDayAtLimitPct is the 7-day utilization at which the menu bar stops
/// showing the 5h window and surfaces the 7d one instead. The 5h window is the
/// default (it moves fast within a session); the weekly window only takes over
/// the single menu-bar line once it's effectively maxed, so a 7d-exhausted
/// account can't hide behind a recovered 5h number.
let sevenDayAtLimitPct: Double = 95

/// severityNSColor maps utilization (0..100) to the bar/trend tint, one set of
/// thresholds everywhere: green <60, amber <85, red ≥85. Softer than the
/// full-saturation .systemGreen/.systemYellow/.systemRed (which read as garish),
/// and APPEARANCE-AWARE: a slightly deeper shade on light backgrounds, a
/// brighter one on dark — so it reads well whether the user is in light or dark.
/// Returned as an NSColor so callers building NSAttributedStrings (the menu-bar
/// title) get the same dynamic light/dark color the SwiftUI bars use.
func severityNSColor(for pct: Double) -> NSColor {
    switch pct {
    case ..<60:
        return dynamicNSColor(
            light: NSColor(red: 0.13, green: 0.78, blue: 0.34, alpha: 1),
            dark: NSColor(red: 0.46, green: 0.95, blue: 0.58, alpha: 1))
    case ..<85:
        return dynamicNSColor(
            light: NSColor(red: 0.98, green: 0.80, blue: 0.12, alpha: 1),
            dark: NSColor(red: 1.00, green: 0.92, blue: 0.40, alpha: 1))
    default:
        return dynamicNSColor(
            light: NSColor(red: 0.90, green: 0.22, blue: 0.20, alpha: 1),
            dark: NSColor(red: 1.00, green: 0.46, blue: 0.42, alpha: 1))
    }
}

/// severityColor is the SwiftUI wrapper over severityNSColor — same thresholds,
/// same dynamic color, for views that want a `Color`.
func severityColor(for pct: Double) -> Color {
    Color(nsColor: severityNSColor(for: pct))
}

/// dynamicNSColor returns an NSColor that resolves to `light` under the Aqua
/// appearance and `dark` under Dark Aqua, so fixed RGBs don't look wrong in one
/// of the two modes the user switches between.
func dynamicNSColor(light: NSColor, dark: NSColor) -> NSColor {
    NSColor(name: nil) { appearance in
        appearance.bestMatch(from: [.aqua, .darkAqua]) == .darkAqua ? dark : light
    }
}

/// adaptiveColor is the SwiftUI wrapper over dynamicNSColor.
func adaptiveColor(light: NSColor, dark: NSColor) -> Color {
    Color(nsColor: dynamicNSColor(light: light, dark: dark))
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
