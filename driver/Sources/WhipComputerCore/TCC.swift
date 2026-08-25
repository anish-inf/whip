import Foundation
import ApplicationServices
import AppKit
import CoreGraphics

// TCC.swift — permission preflight: Accessibility → Screen Recording →
// Automation, one window with x-apple.systempreferences: deep links (ported
// from codex's SystemSettingsCoordinator design, docs/learnings/
// other-harnesses/codex-computer-use-plugin.md §5).

public enum TCC {
    /// Accessibility (AX reads + CGEvent input). Does not prompt.
    public static var accessibilityGranted: Bool {
        AXIsProcessTrusted()
    }

    /// Prompt once (the system alert) if not granted.
    @discardableResult
    public static func requestAccessibility() -> Bool {
        let opts = [kAXTrustedCheckOptionPrompt.takeUnretainedValue(): true] as CFDictionary
        return AXIsProcessTrustedWithOptions(opts)
    }

    /// Screen Recording (ScreenCaptureKit). Preflight is free; request shows
    /// the system prompt once. NOTE: preflight can stale-report true after a
    /// TCC db reset for some processes; capture itself validates at use time.
    public static var screenRecordingGranted: Bool {
        CGPreflightScreenCaptureAccess()
    }

    @discardableResult
    public static func requestScreenRecording() -> Bool {
        CGRequestScreenCaptureAccess()
    }

    /// Deep links into System Settings panes (codex's anchors).
    public static let accessibilityPane = "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"
    public static let screenRecordingPane = "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture"
    public static let automationPane = "x-apple.systempreferences:com.apple.preference.security?Privacy_Automation"

    public static func openPane(_ url: String) {
        if let u = URL(string: url) {
            NSWorkspace.shared.open(u)
        }
    }

    public struct Status: Codable {
        public var accessibility: Bool
        public var screenRecording: Bool
    }

    public static func status() -> Status {
        Status(accessibility: accessibilityGranted, screenRecording: screenRecordingGranted)
    }
}

/// PermissionsWindow — the single first-run window (plan §"Permissions UX"):
/// one panel listing the missing grants with buttons that deep-link into the
/// right System Settings panes. Shown while a request waits on grants.
public final class PermissionsWindow {
    private var window: NSWindow?

    public init() {}

    public func show(missing: [String: String]) {
        if window != nil { return }
        let panel = NSPanel(
            contentRect: NSRect(x: 0, y: 0, width: 460, height: 220),
            styleMask: [.titled, .closable],
            backing: .buffered, defer: false)
        panel.title = "whip-computer permissions"
        panel.isFloatingPanel = true
        panel.level = .floating

        let stack = NSStackView()
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 12
        stack.edgeInsets = NSEdgeInsets(top: 16, left: 16, bottom: 16, right: 16)

        let header = NSTextField(labelWithString: "whip needs these permissions to drive your Mac:")
        header.font = .boldSystemFont(ofSize: 13)
        stack.addArrangedSubview(header)

        for (name, pane) in missing.sorted(by: { $0.key < $1.key }) {
            let btn = NSButton(title: "Grant \(name)…", target: nil, action: nil)
            btn.bezelStyle = .rounded
            btn.setAction { _ in TCC.openPane(pane) }
            stack.addArrangedSubview(btn)
        }

        let hint = NSTextField(wrappingLabelWithString: "Grant each permission, then return here — whip continues automatically once everything is granted.")
        hint.font = .systemFont(ofSize: 11)
        hint.textColor = .secondaryLabelColor
        stack.addArrangedSubview(hint)

        panel.contentView = stack
        panel.center()
        panel.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        window = panel
    }

    public func close() {
        window?.close()
        window = nil
    }
}

private extension NSButton {
    func setAction(_ block: @escaping (NSButton) -> Void) {
        let t = ButtonTarget(block)
        target = t
        action = #selector(ButtonTarget.fire(_:))
        objc_setAssociatedObject(self, &targetKey, t, .OBJC_ASSOCIATION_RETAIN)
    }
}

private var targetKey: UInt8 = 0
private final class ButtonTarget: NSObject {
    let block: (NSButton) -> Void
    init(_ b: @escaping (NSButton) -> Void) { block = b }
    @objc func fire(_ sender: NSButton) { block(sender) }
}


/// Run `body` on the main thread, synchronously. AppKit (NSWorkspace,
/// NSRunningApplication.activate, panels) is main-only; AX/CGEvent/TCC are
/// not. Safe to call from the main thread (runs inline).
@discardableResult
public func syncOnMain<T>(_ body: () throws -> T) throws -> T {
    if Thread.isMainThread { return try body() }
    var result: Result<T, Error>!
    // Swift 6 wants Sendable closures here; the box pattern is the
    // classic escape. Errors from `body` are rethrown verbatim.
    DispatchQueue.main.sync {
        result = Result { try body() }
    }
    return try result!.get()
}
