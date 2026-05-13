// swift-tools-version: 5.10
// Swift Package Manager manifest for the AIMonitor menu bar widget.
//
// What this builds: a single executable Mach-O at .build/release/AIMonitor.
// Phase 5 (install pipeline) wraps the binary into a .app bundle via
// goreleaser hooks before publishing to Homebrew.
//
// Why SPM and not an Xcode project: a Package.swift is text, diff-able,
// and builds headlessly in CI. The trade-off is we have to wire the
// Info.plist via linker flags ourselves (see linkerSettings below) rather
// than having Xcode synthesize it.
//
// Why macOS 14: SMAppService (autostart helper for v1) and Swift Charts
// (deferred to v1.1) both require Sonoma+. Locking the floor here means
// we don't accumulate runtime availability checks.
import PackageDescription

let package = Package(
    name: "AIMonitor",
    platforms: [
        .macOS(.v14),
    ],
    targets: [
        .executableTarget(
            name: "AIMonitor",
            path: "Sources/AIMonitor",
            resources: [],
            linkerSettings: [
                // libsqlite3 is part of the macOS SDK; this links the
                // system copy. No package dependency needed.
                .linkedLibrary("sqlite3"),
                // Embed Info.plist into the Mach-O __TEXT __info_plist
                // section so LSUIElement=true takes effect on launch
                // (menu bar accessory; no Dock icon).
                .unsafeFlags([
                    "-Xlinker", "-sectcreate",
                    "-Xlinker", "__TEXT",
                    "-Xlinker", "__info_plist",
                    "-Xlinker", "Resources/Info.plist",
                ]),
            ]
        ),
    ]
)
