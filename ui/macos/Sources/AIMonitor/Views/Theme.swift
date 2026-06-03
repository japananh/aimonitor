// Theme preference for the menu-bar app: follow the OS, or force light/dark.
// Persisted in UserDefaults under appThemeKey; applied by setting
// NSApp.appearance (nil = inherit from the OS).

import AppKit

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
