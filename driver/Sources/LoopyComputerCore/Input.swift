import Foundation
import CoreGraphics
import AppKit

// Input.swift — CGEvent mouse/keyboard injection (codex-computer-use-plugin.md
// §4). Coordinates are in *points* (the driver's normalized space — SCK
// capture normalizes to point resolution, so model coordinates map 1:1).
// Key syntax is xdotool-style: "a", "Return", "super+c", "KP_0", "Up".

public enum Input {

    // MARK: - Mouse

    public static func click(x: Double, y: Double, clicks: Int = 1, button: String = "left") throws {
        let pt = CGPoint(x: x, y: y)
        let (downType, upType, btn): (CGEventType, CGEventType, CGMouseButton)
        switch button.lowercased() {
        case "right": (downType, upType, btn) = (.rightMouseDown, .rightMouseUp, .right)
        case "middle", "center": (downType, upType, btn) = (.otherMouseDown, .otherMouseUp, .center)
        default: (downType, upType, btn) = (.leftMouseDown, .leftMouseUp, .left)
        }
        let src = CGEventSource(stateID: .hidSystemState)
        for i in 1...max(1, clicks) {
            guard let down = CGEvent(mouseEventSource: src, mouseType: downType, mouseCursorPosition: pt, mouseButton: btn),
                  let up = CGEvent(mouseEventSource: src, mouseType: upType, mouseCursorPosition: pt, mouseButton: btn) else {
                throw RPCError(.internalError, "CGEvent creation failed")
            }
            down.setIntegerValueField(.mouseEventClickState, value: Int64(i))
            up.setIntegerValueField(.mouseEventClickState, value: Int64(i))
            down.post(tap: .cghidEventTap)
            usleep(15000)
            up.post(tap: .cghidEventTap)
            if i < clicks { usleep(60000) }
        }
    }

    public static func move(x: Double, y: Double) {
        let pt = CGPoint(x: x, y: y)
        CGEvent(mouseEventSource: nil, mouseType: .mouseMoved, mouseCursorPosition: pt, mouseButton: .left)?
            .post(tap: .cghidEventTap)
    }

    public static func drag(x1: Double, y1: Double, x2: Double, y2: Double) throws {
        let src = CGEventSource(stateID: .hidSystemState)
        let from = CGPoint(x: x1, y: y1)
        let to = CGPoint(x: x2, y: y2)
        guard let down = CGEvent(mouseEventSource: src, mouseType: .leftMouseDown, mouseCursorPosition: from, mouseButton: .left) else {
            throw RPCError(.internalError, "CGEvent creation failed")
        }
        down.post(tap: .cghidEventTap)
        usleep(30000)
        // Interpolated drag so apps that track the drag see motion.
        let steps = 12
        for i in 1...steps {
            let t = Double(i) / Double(steps)
            let pt = CGPoint(x: x1 + (x2 - x1) * t, y: y1 + (y2 - y1) * t)
            CGEvent(mouseEventSource: src, mouseType: .leftMouseDragged, mouseCursorPosition: pt, mouseButton: .left)?
                .post(tap: .cghidEventTap)
            usleep(8000)
        }
        CGEvent(mouseEventSource: src, mouseType: .leftMouseUp, mouseCursorPosition: to, mouseButton: .left)?
            .post(tap: .cghidEventTap)
    }

    /// Pixel-scroll fallback (AX scroll is preferred; this targets x,y).
    public static func scroll(x: Double, y: Double, dir: String, clicks: Int) {
        move(x: x, y: y)
        usleep(20000)
        let (dy, dx): (Int32, Int32)
        switch dir.lowercased() {
        case "up": (dy, dx) = (Int32(abs(clicks)), 0)
        case "left": (dy, dx) = (0, Int32(abs(clicks)))
        case "right": (dy, dx) = (0, -Int32(abs(clicks)))
        default: (dy, dx) = (-Int32(abs(clicks)), 0) // down
        }
        let src = CGEventSource(stateID: .hidSystemState)
        let ev = CGEvent(scrollWheelEvent2Source: src, units: .line, wheelCount: 2,
                         wheel1: dy, wheel2: dx, wheel3: 0)
        ev?.post(tap: .cghidEventTap)
    }

    // MARK: - Keyboard

    /// Type literal text via CGEventKeyboardSetUnicodeString (handles any
    /// Unicode, unlike keycode synthesis).
    public static func typeText(_ text: String) {
        let src = CGEventSource(stateID: .hidSystemState)
        for scalarChunk in text.chunkedUTF16() {
            guard let down = CGEvent(keyboardEventSource: src, virtualKey: 0, keyDown: true),
                  let up = CGEvent(keyboardEventSource: src, virtualKey: 0, keyDown: false) else { continue }
            var units = scalarChunk
            down.keyboardSetUnicodeString(stringLength: units.count, unicodeString: &units)
            up.keyboardSetUnicodeString(stringLength: units.count, unicodeString: &units)
            down.post(tap: .cghidEventTap)
            usleep(2000)
            up.post(tap: .cghidEventTap)
        }
    }

    /// xdotool-style key press: "a", "Return", "Tab", "super+c", "shift+alt+Left", "KP_0".
    public static func press(_ spec: String) throws {
        let combo = try KeyMap.parse(spec)
        let src = CGEventSource(stateID: .hidSystemState)
        guard let down = CGEvent(keyboardEventSource: src, virtualKey: combo.keyCode, keyDown: true),
              let up = CGEvent(keyboardEventSource: src, virtualKey: combo.keyCode, keyDown: false) else {
            throw RPCError(.internalError, "CGEvent keyboard creation failed")
        }
        down.flags = combo.flags
        up.flags = combo.flags
        down.post(tap: .cghidEventTap)
        usleep(2000)
        up.post(tap: .cghidEventTap)
    }
}

public struct KeyCombo {
    public let keyCode: CGKeyCode
    public let flags: CGEventFlags
}

/// xdotool key syntax → CG key codes (codex borrowed the same; portable to
/// the Linux backend).
public enum KeyMap {
    public static func parse(_ spec: String) throws -> KeyCombo {
        let parts = spec.split(separator: "+").map { $0.trimmingCharacters(in: .whitespaces) }
        guard let keyName = parts.last, !keyName.isEmpty else {
            throw RPCError(.invalidParams, "empty key spec")
        }
        var flags = CGEventFlags()
        for mod in parts.dropLast() {
            switch mod.lowercased() {
            case "super", "cmd", "command", "meta": flags.insert(.maskCommand)
            case "shift": flags.insert(.maskShift)
            case "alt", "option", "opt": flags.insert(.maskAlternate)
            case "ctrl", "control": flags.insert(.maskControl)
            case "fn": flags.insert(.maskSecondaryFn)
            default: throw RPCError(.invalidParams, "unknown modifier \"\(mod)\" (use super/shift/alt/ctrl)")
            }
        }
        guard let code = keyCode(for: keyName) else {
            throw RPCError(.invalidParams, "unknown key \"\(keyName)\"")
        }
        // Uppercase letters imply shift.
        if keyName.count == 1, keyName.first!.isLetter, keyName.first!.isUppercase {
            flags.insert(.maskShift)
        }
        return KeyCombo(keyCode: code, flags: flags)
    }

    public static func keyCode(for name: String) -> CGKeyCode? {
        if name.count == 1, let c = name.lowercased().first, let code = charKeys[c] {
            return code
        }
        return namedKeys[name.lowercased()]
    }

    // macOS virtual key codes (Carbon HIToolbox Events.h).
    private static let charKeys: [Character: CGKeyCode] = [
        "a": 0x00, "s": 0x01, "d": 0x02, "f": 0x03, "h": 0x04, "g": 0x05,
        "z": 0x06, "x": 0x07, "c": 0x08, "v": 0x09, "b": 0x0B, "q": 0x0C,
        "w": 0x0D, "e": 0x0E, "r": 0x0F, "y": 0x10, "t": 0x11,
        "1": 0x12, "2": 0x13, "3": 0x14, "4": 0x15, "6": 0x16, "5": 0x17,
        "=": 0x18, "9": 0x19, "7": 0x1A, "-": 0x1B, "8": 0x1C, "0": 0x1D,
        "]": 0x1E, "o": 0x1F, "u": 0x20, "[": 0x21, "i": 0x22, "p": 0x23,
        "l": 0x25, "j": 0x26, "'": 0x27, "k": 0x28, ";": 0x29, "\\": 0x2A,
        ",": 0x2B, "/": 0x2C, "n": 0x2D, "m": 0x2E, ".": 0x2F, "`": 0x32,
    ]

    private static let namedKeys: [String: CGKeyCode] = [
        "return": 0x24, "enter": 0x24, "tab": 0x30, "space": 0x31,
        "delete": 0x33, "backspace": 0x33, "escape": 0x35, "esc": 0x35,
        "forwarddelete": 0x75, "home": 0x73, "end": 0x77,
        "pageup": 0x74, "pagedown": 0x79,
        "left": 0x7B, "right": 0x7C, "down": 0x7D, "up": 0x7E,
        "f1": 0x7A, "f2": 0x78, "f3": 0x63, "f4": 0x76, "f5": 0x60,
        "f6": 0x61, "f7": 0x62, "f8": 0x64, "f9": 0x65, "f10": 0x6D,
        "f11": 0x67, "f12": 0x6F,
        "kp_0": 0x52, "kp_1": 0x53, "kp_2": 0x54, "kp_3": 0x55, "kp_4": 0x56,
        "kp_5": 0x57, "kp_6": 0x58, "kp_7": 0x59, "kp_8": 0x5B, "kp_9": 0x5C,
        "kp_decimal": 0x41, "kp_multiply": 0x43, "kp_plus": 0x45,
        "kp_divide": 0x4B, "kp_enter": 0x4C, "kp_minus": 0x4E, "kp_equal": 0x51,
    ]
}

private extension String {
    /// Split into UTF-16 chunks (pairs max) for keyboardSetUnicodeString.
    func chunkedUTF16() -> [[unichar]] {
        var out: [[unichar]] = []
        var units = Array(utf16)
        while !units.isEmpty {
            let take = min(units.count, 20)
            out.append(Array(units.prefix(take)))
            units.removeFirst(take)
        }
        return out
    }
}
