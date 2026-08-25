import Foundation
import AppKit
import WhipComputerCore

// main.swift — whip-computer: the embedded desktop driver. JSON-RPC over
// stdio, spawned by whip with WHIP_COMPUTER_TOKEN set; refuses requests
// without the token (the plan's stdio+token boundary in place of codex's XPC
// ceremony).
//
// Threading: the stdio loop runs ON the main thread. NSApplication.run()
// doesn't service DispatchQueue.main from a headless launch context, so
// instead we keep main free-running (CFRunLoop via the blocking stdin read)
// and hop OFF-main for the heavy work. AX/CGEvent/TCC-status APIs are all
// callable off-main; AppKit touchpoints (activate, the permissions window)
// hop back onto main via syncOnMain.

let app = NSApplication.shared
app.setActivationPolicy(.accessory) // LSUIElement-style: no Dock icon

let token = ProcessInfo.processInfo.environment[Protocol.tokenEnvVar]
let dispatcher = Dispatcher(token: token) {
    exit(0)
}

let server = StdioServer { req in try dispatcher.handle(req) }
server.run()
exit(0) // stdin closed — parent died
