import Foundation
import ApplicationServices
import AppKit

// AXTree.swift — Accessibility tree reads (AXUIElement) with element indexes
// + a generation counter, and AX-based actions (press, set value, scroll,
// secondary actions, focus/selection). Ported from the design dissected in
// codex-computer-use-plugin.md §2/§4 — AX-first grounding, coordinates as the
// escape hatch.
//
// Threading: everything here runs on the main thread (AppKit/AX are
// main-affine in practice). Called via Dispatcher which hops to main.

public struct AXElementInfo: Codable {
    public let index: Int
    public let role: String
    public let subrole: String?
    public let title: String?
    public let value: String?
    public let desc: String?
    public let roleDescription: String?
    public let position: [Double]? // [x, y] in points
    public let size: [Double]?     // [w, h] in points
    public let actions: [String]
    public let focused: Bool
    public let enabled: Bool
}

public struct AXSnapshot {
    public let generation: Int
    public let elements: [AXUIElement] // index-aligned with `infos`
    public let infos: [AXElementInfo]
    public let pid: pid_t
    public let appName: String
}

public enum AXTreeError: Error {
    case appNotFound(String)
    case cannotCreateAppElement
    case attributeFailed(String, Int32)
}

public final class AXTree {
    private var generation = 0
    /// Cache of the last snapshot per pid for stale-index validation.
    public private(set) var last: AXSnapshot?

    public init() {}

    public var currentGeneration: Int { generation }

    // MARK: - App resolution

    /// Resolve an app by name ("Google Chrome"), bundle id, or full path.
    public func resolveApp(_ app: String) throws -> (NSRunningApplication, pid_t) {
        try syncOnMain { try self.resolveAppOnMain(app) }
    }

    private func resolveAppOnMain(_ app: String) throws -> (NSRunningApplication, pid_t) {
        let apps = NSWorkspace.shared.runningApplications
        let needle = app.lowercased()
        // Bundle id first (most stable), then exact name, then substring.
        if let a = apps.first(where: { $0.bundleIdentifier?.lowercased() == needle }) {
            return (a, a.processIdentifier)
        }
        if let a = apps.first(where: { $0.localizedName?.lowercased() == needle }) {
            return (a, a.processIdentifier)
        }
        if app.hasPrefix("/"),
           let a = apps.first(where: { $0.bundleURL?.path == app || $0.executableURL?.path == app }) {
            return (a, a.processIdentifier)
        }
        if let a = apps.first(where: { $0.localizedName?.lowercased().contains(needle) == true }) {
            return (a, a.processIdentifier)
        }
        throw RPCError(.unknownApp, "no running app matches \"\(app)\" — call apps() to list running apps")
    }

    // MARK: - Tree read

    /// Read the app's AX tree: frontmost window preferred, depth-capped,
    /// action-bearing and interesting nodes indexed. Returns the snapshot and
    /// installs it as `last` (bumps the generation counter).
    public func snapshot(app: String, maxElements: Int = 400) throws -> AXSnapshot {
        let (_, pid) = try resolveApp(app)
        guard TCC.accessibilityGranted else {
            throw RPCError(.noAXPermission, "Accessibility permission not granted to whip-computer")
        }
        let appEl = AXUIElementCreateApplication(pid)
        // Raise the app so key window is fresh.
        let windows = try copyAXArray(appEl, kAXWindowsAttribute)
        guard let window = windows.first else {
            throw RPCError(.internalError, "\(app) has no windows (hidden or minimized?)")
        }
        generation += 1
        var elements: [AXUIElement] = []
        var infos: [AXElementInfo] = []
        var index = 0
        walk(window, depth: 0, index: &index, elements: &elements, infos: &infos, max: maxElements)
        let snap = AXSnapshot(generation: generation, elements: elements, infos: infos,
                              pid: pid, appName: app)
        last = snap
        return snap
    }

    private let interestingRoles: Set<String> = [
        kAXButtonRole, kAXTextFieldRole, kAXTextAreaRole, kAXCheckBoxRole,
        kAXRadioButtonRole, kAXPopUpButtonRole, kAXMenuButtonRole, kAXComboBoxRole,
        kAXSliderRole, kAXTabGroupRole, "AXLink",
        kAXMenuItemRole, kAXRowRole, "AXCell",
        kAXStaticTextRole, kAXImageRole, kAXGroupRole, kAXListRole,
        kAXTableRole, kAXOutlineRole, kAXScrollAreaRole, "AXWebArea",
        kAXToolbarRole, kAXWindowRole, kAXSheetRole, "AXDrawer",
    ]

    private func walk(_ el: AXUIElement, depth: Int, index: inout Int,
                      elements: inout [AXUIElement], infos: inout [AXElementInfo], max: Int) {
        guard index < max, depth < 24 else { return }
        let info = describe(el, index: index)
        let include = !info.actions.isEmpty || interestingRoles.contains(info.role)
            || info.role == kAXWindowRole
        if include {
            elements.append(el)
            infos.append(info)
            index += 1
        }
        let kids = (try? copyAXArray(el, kAXChildrenAttribute)) ?? []
        for kid in kids {
            walk(kid, depth: depth + 1, index: &index, elements: &elements, infos: &infos, max: max)
        }
    }

    private func describe(_ el: AXUIElement, index: Int) -> AXElementInfo {
        let role = axString(el, kAXRoleAttribute) ?? "AXUnknown"
        let actions = axActionNames(el)
        var pos: [Double]?
        if let p = axValue(el, kAXPositionAttribute, AXValueType.cgPoint) as? CGPoint {
            pos = [Double(p.x), Double(p.y)]
        }
        var size: [Double]?
        if let s = axValue(el, kAXSizeAttribute, AXValueType.cgSize) as? CGSize {
            size = [Double(s.width), Double(s.height)]
        }
        return AXElementInfo(
            index: index,
            role: role,
            subrole: axString(el, kAXSubroleAttribute),
            title: axString(el, kAXTitleAttribute),
            value: valueString(el),
            desc: axString(el, kAXDescriptionAttribute),
            roleDescription: axString(el, kAXRoleDescriptionAttribute),
            position: pos,
            size: size,
            actions: actions,
            focused: axBool(el, kAXFocusedAttribute) ?? false,
            enabled: axBool(el, kAXEnabledAttribute) ?? true
        )
    }

    private func valueString(_ el: AXUIElement) -> String? {
        var v: AnyObject?
        guard AXUIElementCopyAttributeValue(el, kAXValueAttribute as CFString, &v) == .success,
              let val = v else { return nil }
        if let s = val as? String { return String(s.prefix(500)) }
        if let n = val as? NSNumber { return n.stringValue }
        return nil
    }

    // MARK: - Stale-index validation + element lookup

    /// Fetch element `index` from the last snapshot for `app`, enforcing the
    /// generation guard: an action carrying `gen` older than the last read
    /// fails with "state changed — re-read" (the plan's per-action state gate).
    public func element(app: String, index: Int, gen: Int?) throws -> (AXUIElement, AXSnapshot) {
        guard let snap = last else {
            throw RPCError(.staleGeneration, "no state read yet — call state(app) first")
        }
        if snap.appName != app && (try? resolveApp(app).1) != snap.pid {
            // Different app than the snapshot: force a fresh read.
            _ = try resolveApp(app) // validate it exists for a good error
            throw RPCError(.staleGeneration, "last state read was for \(snap.appName), not \(app) — call state(\"\(app)\") first")
        }
        if let g = gen, g != snap.generation {
            throw RPCError(.staleGeneration,
                "state changed since generation \(g) (now \(snap.generation)) — call state(app) to re-read before acting")
        }
        guard index >= 0, index < snap.elements.count else {
            throw RPCError(.indexOutOfRange,
                "index \(index) out of range (0..\(snap.elements.count - 1)) — re-read state(app)")
        }
        return (snap.elements[index], snap)
    }

    // MARK: - AX actions

    public func press(_ el: AXUIElement) throws {
        let err = AXUIElementPerformAction(el, kAXPressAction as CFString)
        guard err == .success else {
            throw RPCError(.elementNotActionable, "AXPress failed (\(axErr(err))) — try a pixel click instead")
        }
    }

    public func perform(_ el: AXUIElement, action: String) throws {
        let err = AXUIElementPerformAction(el, action as CFString)
        guard err == .success else {
            throw RPCError(.elementNotActionable, "AX action \(action) failed (\(axErr(err)))")
        }
    }

    public func setValue(_ el: AXUIElement, _ value: String) throws {
        var settable = DarwinBoolean(false)
        guard AXUIElementIsAttributeSettable(el, kAXValueAttribute as CFString, &settable) == .success, settable.boolValue else {
            throw RPCError(.elementNotActionable, "element's value is not settable")
        }
        let err = AXUIElementSetAttributeValue(el, kAXValueAttribute as CFString, value as CFTypeRef)
        guard err == .success else {
            throw RPCError(.internalError, "AXSetValue failed (\(axErr(err)))")
        }
    }

    public func setFocused(_ el: AXUIElement) {
        AXUIElementSetAttributeValue(el, kAXFocusedAttribute as CFString, kCFBooleanTrue)
    }

    /// Select `target` text inside a text element; prefix/suffix disambiguate
    /// repeats (codex's select_text semantics). Falls back to selecting all.
    public func selectText(_ el: AXUIElement, target: String?, prefix: String?, suffix: String?) throws {
        var v: AnyObject?
        guard AXUIElementCopyAttributeValue(el, kAXValueAttribute as CFString, &v) == .success,
              let text = v as? String else {
            throw RPCError(.elementNotActionable, "element has no text value")
        }
        let ns = text as NSString
        var range = NSRange(location: 0, length: ns.length)
        if let t = target {
            let needle = (prefix ?? "") + t + (suffix ?? "")
            var found = ns.range(of: needle)
            if found.location != NSNotFound, prefix != nil || suffix != nil {
                found = NSRange(location: found.location + (prefix ?? "").count, length: (t as NSString).length)
            } else if found.location == NSNotFound {
                found = ns.range(of: t)
            }
            guard found.location != NSNotFound else {
                throw RPCError(.invalidParams, "text \"\(t)\" not found in element value")
            }
            range = found
        }
        let axRange = CFRange(location: range.location, length: range.length)
        var r = axRange
        guard let val = AXValueCreate(.cfRange, &r) else {
            throw RPCError(.internalError, "AXValueCreate(cfRange) failed")
        }
        let err = AXUIElementSetAttributeValue(el, kAXSelectedTextRangeAttribute as CFString, val)
        guard err == .success else {
            throw RPCError(.elementNotActionable, "setting selection failed (\(axErr(err)))")
        }
    }

    // MARK: - AX helpers

    private func copyAXArray(_ el: AXUIElement, _ attr: String) throws -> [AXUIElement] {
        var v: AnyObject?
        let err = AXUIElementCopyAttributeValue(el, attr as CFString, &v)
        guard err == .success, let arr = v as? [AXUIElement] else {
            if err == .noValue || err == .attributeUnsupported { return [] }
            throw AXTreeError.attributeFailed(attr, err.rawValue)
        }
        return arr
    }

    private func axString(_ el: AXUIElement, _ attr: String) -> String? {
        var v: AnyObject?
        guard AXUIElementCopyAttributeValue(el, attr as CFString, &v) == .success else { return nil }
        return v as? String
    }

    private func axBool(_ el: AXUIElement, _ attr: String) -> Bool? {
        var v: AnyObject?
        guard AXUIElementCopyAttributeValue(el, attr as CFString, &v) == .success else { return nil }
        return (v as? NSNumber)?.boolValue
    }

    private func axValue(_ el: AXUIElement, _ attr: String, _ type: AXValueType) -> Any? {
        var v: AnyObject?
        guard AXUIElementCopyAttributeValue(el, attr as CFString, &v) == .success,
              let av = v, CFGetTypeID(av) == AXValueGetTypeID() else { return nil }
        let axv = unsafeBitCast(av, to: AXValue.self)
        switch type {
        case .cgPoint:
            var p = CGPoint.zero
            if AXValueGetValue(axv, .cgPoint, &p) { return p }
        case .cgSize:
            var s = CGSize.zero
            if AXValueGetValue(axv, .cgSize, &s) { return s }
        default: break
        }
        return nil
    }

    private func axActionNames(_ el: AXUIElement) -> [String] {
        var names: CFArray?
        guard AXUIElementCopyActionNames(el, &names) == .success,
              let arr = names as? [String] else { return [] }
        return arr
    }
}

private func axErr(_ e: ApplicationServices.AXError) -> String {
    switch e {
    case .success: return "success"
    case .failure: return "failure"
    case .illegalArgument: return "illegalArgument"
    case .invalidUIElement: return "invalidUIElement"
    case .invalidUIElementObserver: return "invalidUIElementObserver"
    case .cannotComplete: return "cannotComplete (app unresponsive?)"
    case .attributeUnsupported: return "attributeUnsupported"
    case .actionUnsupported: return "actionUnsupported"
    case .notificationUnsupported: return "notificationUnsupported"
    case .notImplemented: return "notImplemented"
    case .notificationAlreadyRegistered: return "notificationAlreadyRegistered"
    case .notificationNotRegistered: return "notificationNotRegistered"
    case .apiDisabled: return "apiDisabled (Accessibility permission missing)"
    case .noValue: return "noValue"
    case .parameterizedAttributeUnsupported: return "parameterizedAttributeUnsupported"
    case .notEnoughPrecision: return "notEnoughPrecision"
    @unknown default: return "unknown(\(e.rawValue))"
    }
}
