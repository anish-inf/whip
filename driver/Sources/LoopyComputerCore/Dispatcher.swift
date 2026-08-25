import Foundation
import AppKit
import ApplicationServices

// Dispatcher.swift — the JSON-RPC method router. One method per driver
// capability (plan §"Helper/tool surface"); mutations return fresh state
// in-call (the plan's improvement over codex's mandatory re-query).
//
// Methods:
//   handshake {token}                     → {version, pid, tcc}
//   permissions.status                    → {accessibility, screenRecording}
//   permissions.request {}                → {accessibility, screenRecording} (waits in-turn, shows window)
//   apps {}                               → [{name, bundleId, pid, active}]
//   ax {app}                              → {generation, app, elements:[AXElementInfo]}
//   state {app}                           → {generation, app, elements, screenshot:{jpegBase64,w,h}}
//   click {app, index?, x?, y?, clicks?, button?, gen?}       → state
//   type {app, text, gen?}                → state
//   press {app, key, gen?}                → state
//   scroll {app, index?, x?, y?, dir, clicks?, gen?}          → state
//   set {app, index, value, gen?}         → state
//   select {app, index, target?, prefix?, suffix?, gen?}      → state
//   menu {app, index, action, gen?}       → state
//   screenshot {app}                      → {jpegBase64, w, h}
//   shutdown {}                           → {}

public final class Dispatcher {
    private let ax = AXTree()
    private let permWindow = PermissionsWindow()
    private let token: String?
    private var onShutdown: () -> Void = {}

    public init(token: String?, onShutdown: @escaping () -> Void) {
        self.token = token
        self.onShutdown = onShutdown
    }

    public func handle(_ req: Request) throws -> AnyCodable {
        // Token gate on every call except handshake (which carries it).
        if req.method != "handshake" {
            try checkToken(req.params)
        }
        switch req.method {
        case "handshake":
            try checkToken(req.params)
            let st = TCC.status()
            return .object([
                "version": .string(Protocol.version),
                "pid": .int(Int(ProcessInfo.processInfo.processIdentifier)),
                "tcc": .object([
                    "accessibility": .bool(st.accessibility),
                    "screenRecording": .bool(st.screenRecording),
                ]),
            ])
        case "shutdown":
            // Respond first, then exit (the response must flush).
            DispatchQueue.global().asyncAfter(deadline: .now() + 0.05) {
                self.onShutdown()
            }
            return .object(["ok": .bool(true)])
        case "permissions.status":
            let st = TCC.status()
            return .object([
                "accessibility": .bool(st.accessibility),
                "screenRecording": .bool(st.screenRecording),
            ])
        case "permissions.request":
            return try permissionsRequest()
        case "apps":
            return try listApps()
        case "ax":
            let snap = try snapshotFor(req)
            return snapshotResult(snap, screenshot: nil)
        case "state":
            let snap = try snapshotFor(req)
            let shot = try awaitCapture(pid: snap.pid)
            return snapshotResult(snap, screenshot: shot)
        case "screenshot":
            let app = try param(req, "app")
            let (_, pid) = try ax.resolveApp(app)
            guard let shot = try awaitCapture(pid: pid) else {
                throw RPCError(.noScreenPermission, "screenshot unavailable (Screen Recording not granted?)")
            }
            return shot
        case "click":
            return try click(req)
        case "type":
            let app = try param(req, "app")
            let text = try param(req, "text")
            try activate(app)
            Input.typeText(text)
            return try stateAfter(app, gen: genParam(req))
        case "press":
            let app = try param(req, "app")
            let key = try param(req, "key")
            try activate(app)
            try Input.press(key)
            return try stateAfter(app, gen: genParam(req))
        case "scroll":
            return try scroll(req)
        case "set":
            let app = try param(req, "app")
            let index = try requireInt(req, "index")
            let value = try param(req, "value")
            let (el, _) = try ax.element(app: app, index: index, gen: genParam(req))
            try ax.setValue(el, value)
            return try stateAfter(app, gen: nil)
        case "select":
            let app = try param(req, "app")
            let index = try requireInt(req, "index")
            let (el, _) = try ax.element(app: app, index: index, gen: genParam(req))
            try ax.selectText(el,
                              target: optionalParam(req, "target"),
                              prefix: optionalParam(req, "prefix"),
                              suffix: optionalParam(req, "suffix"))
            return try stateAfter(app, gen: nil)
        case "menu":
            let app = try param(req, "app")
            let index = try requireInt(req, "index")
            let action = try param(req, "action")
            let (el, _) = try ax.element(app: app, index: index, gen: genParam(req))
            try ax.perform(el, action: action)
            return try stateAfter(app, gen: nil)
        default:
            throw RPCError(.methodNotFound, "unknown method \(req.method)")
        }
    }

    // MARK: - Methods

    private func checkToken(_ params: [String: AnyCodable]?) throws {
        guard let expected = token, !expected.isEmpty else { return } // no token configured (dev)
        guard let got = params?["token"]?.stringValue, got == expected else {
            throw RPCError(.badToken, "missing or invalid handshake token")
        }
    }

    /// Busy-wait in-turn for grants (codex's trick, kept): show the window,
    /// poll grant state, return when granted or after ~120s with the pending
    /// status so the caller can re-call.
    private func permissionsRequest() throws -> AnyCodable {
        var missing: [String: String] = [:]
        if !TCC.accessibilityGranted { missing["Accessibility"] = TCC.accessibilityPane }
        if !TCC.screenRecordingGranted { missing["Screen Recording"] = TCC.screenRecordingPane }
        if missing.isEmpty {
            let st = TCC.status()
            return .object(["accessibility": .bool(st.accessibility),
                            "screenRecording": .bool(st.screenRecording),
                            "pending": .bool(false)])
        }
        // Nudge the system prompts too (they co-exist with our window).
        _ = TCC.requestAccessibility()
        _ = TCC.requestScreenRecording()
        try syncOnMain { self.permWindow.show(missing: missing) }
        let deadline = Date().addingTimeInterval(120)
        while Date() < deadline {
            Thread.sleep(forTimeInterval: 0.5)
            if TCC.accessibilityGranted && TCC.screenRecordingGranted {
                try syncOnMain { self.permWindow.close() }
                return .object(["accessibility": .bool(true),
                                "screenRecording": .bool(true),
                                "pending": .bool(false)])
            }
        }
        let st = TCC.status()
        return .object([
            "accessibility": .bool(st.accessibility),
            "screenRecording": .bool(st.screenRecording),
            "pending": .bool(true),
            "hint": .string("permissions still pending — the user hasn't finished granting them in the loopy-computer window; call permissions.request again, do not give up yet"),
        ])
    }

    private func listApps() throws -> AnyCodable {
        try syncOnMain {
            let apps = NSWorkspace.shared.runningApplications
                .filter { $0.activationPolicy == .regular }
                .sorted { ($0.localizedName ?? "") < ($1.localizedName ?? "") }
            let arr: [AnyCodable] = apps.map { a in
                .object([
                    "name": .string(a.localizedName ?? "?"),
                    "bundleId": .string(a.bundleIdentifier ?? ""),
                    "pid": .int(Int(a.processIdentifier)),
                    "active": .bool(a.isActive),
                ])
            }
            return .array(arr)
        }
    }

    private func snapshotFor(_ req: Request) throws -> AXSnapshot {
        let app = try param(req, "app")
        return try ax.snapshot(app: app)
    }

    private func click(_ req: Request) throws -> AnyCodable {
        let app = try param(req, "app")
        let clicks = intParam(req, "clicks") ?? 1
        let button = optionalParam(req, "button") ?? "left"
        try activate(app)
        if let idx = intParam(req, "index") {
            let (el, snap) = try ax.element(app: app, index: idx, gen: genParam(req))
            // AX press preferred; fall back to pixel-clicking the element
            // center (codex's coordinate escape hatch).
            do {
                try ax.press(el)
            } catch {
                let info = snap.infos[idx]
                guard let pos = info.position, let size = info.size else {
                    throw error
                }
                try Input.click(x: pos[0] + size[0] / 2, y: pos[1] + size[1] / 2,
                                clicks: clicks, button: button)
            }
        } else if let x = req.params?["x"]?.doubleValue, let y = req.params?["y"]?.doubleValue {
            try Input.click(x: x, y: y, clicks: clicks, button: button)
        } else {
            throw RPCError(.invalidParams, "click needs index or x,y")
        }
        return try stateAfter(app, gen: nil)
    }

    private func scroll(_ req: Request) throws -> AnyCodable {
        let app = try param(req, "app")
        let dir = try param(req, "dir")
        let clicks = intParam(req, "clicks") ?? 3
        try activate(app)
        if let idx = intParam(req, "index") {
            let (_, snap) = try ax.element(app: app, index: idx, gen: genParam(req))
            // Pixel-scroll over the element center (AX scroll APIs are
            // unreliable; codex effectively did the same via trackpad deltas).
            if let pos = snap.infos[idx].position, let size = snap.infos[idx].size {
                Input.scroll(x: pos[0] + size[0] / 2, y: pos[1] + size[1] / 2, dir: dir, clicks: clicks)
            } else {
                throw RPCError(.elementNotActionable, "element has no frame — use x,y scroll")
            }
        } else if let x = req.params?["x"]?.doubleValue, let y = req.params?["y"]?.doubleValue {
            Input.scroll(x: x, y: y, dir: dir, clicks: clicks)
        } else {
            throw RPCError(.invalidParams, "scroll needs index or x,y")
        }
        return try stateAfter(app, gen: nil)
    }

    /// Mutations return fresh state in-call: re-read the AX tree + a fresh
    /// screenshot after a beat so the model doesn't pay a second round trip.
    private func stateAfter(_ app: String, gen: Int?) throws -> AnyCodable {
        // Let the UI settle (codex's SerialExecutor waited on observers; a
        // short settle covers the common cases for v1).
        Thread.sleep(forTimeInterval: 0.25)
        let snap = try ax.snapshot(app: app)
        let shot = try awaitCapture(pid: snap.pid)
        return snapshotResult(snap, screenshot: shot)
    }

    /// SCK is async; bridge it synchronously (we're in a sync stdio loop).
    private func awaitCapture(pid: pid_t) throws -> AnyCodable? {
        guard TCC.screenRecordingGranted else { return nil }
        var result: Result<Data, Error>?
        let sem = DispatchSemaphore(value: 0)
        Task {
            do { result = .success(try await Capture.screenshot(pid: pid)) }
            catch { result = .failure(error) }
            sem.signal()
        }
        _ = sem.wait(timeout: .now() + 10)
        switch result {
        case .success(let data):
            return .object([
                "jpegBase64": .string(data.base64EncodedString()),
                "bytes": .int(data.count),
            ])
        case .failure(let e):
            // Screenshot is best-effort on mutations — surface as data, not a
            // hard failure of the action.
            return .object(["error": .string(String(describing: e))])
        case .none:
            return nil
        }
    }

    private func snapshotResult(_ snap: AXSnapshot, screenshot: AnyCodable?) -> AnyCodable {
        let enc = JSONEncoder()
        let infos: AnyCodable
        if let data = try? enc.encode(snap.infos),
           let arr = try? JSONDecoder().decode(AnyCodable.self, from: data) {
            infos = arr
        } else {
            infos = .array([])
        }
        var obj: [String: AnyCodable] = [
            "generation": .int(snap.generation),
            "app": .string(snap.appName),
            "elements": infos,
        ]
        if let shot = screenshot { obj["screenshot"] = shot }
        return .object(obj)
    }

    private func activate(_ app: String) throws {
        try syncOnMain {
            let (running, _) = try self.ax.resolveApp(app)
            if !running.isActive {
                running.activate()
            }
        }
        Thread.sleep(forTimeInterval: 0.15)
    }

    // MARK: - Params

    private func param(_ req: Request, _ name: String) throws -> String {
        guard let v = req.params?[name]?.stringValue else {
            throw RPCError(.invalidParams, "missing string param \"\(name)\"")
        }
        return v
    }

    private func optionalParam(_ req: Request, _ name: String) -> String? {
        req.params?[name]?.stringValue
    }

    private func intParam(_ req: Request, _ name: String) -> Int? {
        req.params?[name]?.intValue
    }

    private func requireInt(_ req: Request, _ name: String) throws -> Int {
        guard let v = req.params?[name]?.intValue else {
            throw RPCError(.invalidParams, "missing int param \"\(name)\"")
        }
        return v
    }

    private func genParam(_ req: Request) -> Int? {
        req.params?["gen"]?.intValue
    }
}
