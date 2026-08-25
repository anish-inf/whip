import Foundation
import ScreenCaptureKit
import CoreGraphics
import AppKit

// Capture.swift — ScreenCaptureKit capture → normalize to *point* resolution
// → JPEG (codex-computer-use-plugin.md §3). The model works in screenshot
// pixel space which equals point space after normalization; click injection
// is 1:1. Requires Screen Recording TCC.

public enum Capture {

    /// Screenshot the front window of `pid`, JPEG bytes normalized to point
    /// resolution (Retina 2x backing is downscaled to points).
    public static func screenshot(pid: pid_t, quality: Double = 0.7) async throws -> Data {
        guard TCC.screenRecordingGranted else {
            throw RPCError(.noScreenPermission, "Screen Recording permission not granted to loopy-computer")
        }
        guard CGPreflightScreenCaptureAccess() else {
            throw RPCError(.noScreenPermission, "Screen Recording permission not granted")
        }
        let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: true)
        let windows = content.windows.filter { $0.owningApplication?.processID == pid && $0.isOnScreen }
        guard let win = windows.first ?? content.windows.first(where: { $0.owningApplication?.processID == pid }) else {
            throw RPCError(.internalError, "no capturable window for pid \(pid)")
        }
        let filter = SCContentFilter(desktopIndependentWindow: win)
        let config = SCStreamConfiguration()
        // Capture at point resolution directly (SCK scales for us) — the
        // "normalize to point resolution" step from the dissection.
        let scale = win.frame.width > 0 ? win.frame.width : 1024
        let aspect = win.frame.width > 0 ? win.frame.height / win.frame.width : 0.75
        config.width = Int(scale)
        config.height = Int(scale * aspect)
        config.showsCursor = true
        config.captureResolution = .best
        if #available(macOS 14.0, *) {
            config.captureResolution = .automatic
        }
        let image = try await SCScreenshotManager.captureImage(contentFilter: filter, configuration: config)
        return try jpeg(image, pointWidth: scale, quality: quality)
    }

    /// Full-screen capture (fallback / debugging).
    public static func screenshotDisplay(quality: Double = 0.7) async throws -> Data {
        guard CGPreflightScreenCaptureAccess() else {
            throw RPCError(.noScreenPermission, "Screen Recording permission not granted")
        }
        let content = try await SCShareableContent.excludingDesktopWindows(false, onScreenWindowsOnly: true)
        guard let display = content.displays.first else {
            throw RPCError(.internalError, "no display found")
        }
        let filter = SCContentFilter(display: display, excludingWindows: [])
        let config = SCStreamConfiguration()
        // Normalize to points: display width in points.
        config.width = Int(display.frame.width)
        config.height = Int(display.frame.height)
        config.showsCursor = true
        let image = try await SCScreenshotManager.captureImage(contentFilter: filter, configuration: config)
        return try jpeg(image, pointWidth: display.frame.width, quality: quality)
    }

    private static func jpeg(_ image: CGImage, pointWidth: CGFloat, quality: Double) throws -> Data {
        let bitmap = NSBitmapImageRep(cgImage: image)
        // If the capture came back Retina-sized, downscale to points.
        if CGFloat(image.width) > pointWidth * 1.5 {
            let scale = pointWidth / CGFloat(image.width)
            let target = NSSize(width: pointWidth, height: CGFloat(image.height) * scale)
            let resized = NSImage(size: target)
            resized.lockFocus()
            NSImage(cgImage: image, size: target).draw(in: NSRect(origin: .zero, size: target))
            resized.unlockFocus()
            if let tiff = resized.tiffRepresentation,
               let rep = NSBitmapImageRep(data: tiff),
               let data = rep.representation(using: .jpeg, properties: [.compressionFactor: quality]) {
                return data
            }
        }
        guard let data = bitmap.representation(using: .jpeg, properties: [.compressionFactor: quality]) else {
            throw RPCError(.internalError, "JPEG encoding failed")
        }
        return data
    }
}
