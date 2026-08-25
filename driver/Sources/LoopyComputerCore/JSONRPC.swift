import Foundation

// JSONRPC.swift — line-delimited JSON-RPC 2.0 over stdin/stdout. One request
// per line (newline-terminated UTF-8 JSON), one response per line. The Go
// side (internal/computer) owns framing on the other end — keep this byte
// compatible with helper.go.
//
// Protocol constants live here; both sides pin ProtocolVersion and refuse
// mismatches (codex's CodexComputerUseIPC-4 lesson).

public enum Protocol {
    public static let version = "loopy-computer/1"
    public static let tokenEnvVar = "LOOPY_COMPUTER_TOKEN"
}

public enum RPCErrorCode: Int {
    case parseError = -32700
    case invalidRequest = -32600
    case methodNotFound = -32601
    case invalidParams = -32602
    case internalError = -32603
    // Application codes (positive, ours):
    case unknownApp = 1
    case noAXPermission = 2
    case noScreenPermission = 3
    case staleGeneration = 4
    case indexOutOfRange = 5
    case elementNotActionable = 6
    case screenLocked = 7
    case badToken = 8
}

public struct RPCError: Error, Codable {
    public let code: Int
    public let message: String
    public var data: AnyCodable?

    public init(_ code: RPCErrorCode, _ message: String, data: AnyCodable? = nil) {
        self.code = code.rawValue
        self.message = message
        self.data = data
    }
}

/// Minimal type-erased JSON value for heterogeneous params/results.
public enum AnyCodable: Codable {
    case string(String)
    case number(Double)
    case int(Int)
    case bool(Bool)
    case array([AnyCodable])
    case object([String: AnyCodable])
    case null

    public init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() { self = .null; return }
        if let b = try? c.decode(Bool.self) { self = .bool(b); return }
        if let i = try? c.decode(Int.self) { self = .int(i); return }
        if let d = try? c.decode(Double.self) { self = .number(d); return }
        if let s = try? c.decode(String.self) { self = .string(s); return }
        if let a = try? c.decode([AnyCodable].self) { self = .array(a); return }
        if let o = try? c.decode([String: AnyCodable].self) { self = .object(o); return }
        throw DecodingError.dataCorruptedError(in: c, debugDescription: "unsupported JSON value")
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .string(let s): try c.encode(s)
        case .number(let d): try c.encode(d)
        case .int(let i): try c.encode(i)
        case .bool(let b): try c.encode(b)
        case .array(let a): try c.encode(a)
        case .object(let o): try c.encode(o)
        case .null: try c.encodeNil()
        }
    }

    public var stringValue: String? {
        if case .string(let s) = self { return s }
        return nil
    }
    public var intValue: Int? {
        switch self {
        case .int(let i): return i
        case .number(let d): return Int(d)
        default: return nil
        }
    }
    public var doubleValue: Double? {
        switch self {
        case .number(let d): return d
        case .int(let i): return Double(i)
        default: return nil
        }
    }
    public var boolValue: Bool? {
        if case .bool(let b) = self { return b }
        return nil
    }
    public var objectValue: [String: AnyCodable]? {
        if case .object(let o) = self { return o }
        return nil
    }
}

public struct Request: Codable {
    public let jsonrpc: String
    public let id: AnyCodable
    public let method: String
    public var params: [String: AnyCodable]?
}

public struct Response: Codable {
    public var jsonrpc = "2.0"
    public let id: AnyCodable
    public var result: AnyCodable?
    public var error: RPCError?

    public static func ok(_ id: AnyCodable, _ result: AnyCodable) -> Response {
        Response(id: id, result: result)
    }
    public static func err(_ id: AnyCodable, _ e: RPCError) -> Response {
        Response(id: id, error: e)
    }
}

/// Server reads newline-delimited requests from stdin, dispatches, writes
/// newline-delimited responses to stdout. Stdout is for protocol only — logs
/// go to stderr.
public final class StdioServer {
    public typealias Handler = (Request) throws -> AnyCodable
    private let handler: Handler
    private let encoder = JSONEncoder()
    private let decoder = JSONDecoder()
    private let outLock = NSLock()

    public init(handler: @escaping Handler) {
        self.handler = handler
        encoder.outputFormatting = [] // compact, single-line
    }

    /// Emits a non-JSON line so the parent can distinguish "alive" from a
    /// crashed binary before the first request.
    public func announce() {
        write(rawLine: Protocol.version + "\n")
    }

    public func run() {
        announce()
        let stdin = FileHandle.standardInput
        while let line = readLine(stdin) {
            autoreleasepool {
                handle(line: line)
            }
        }
    }

    private func readLine(_ fh: FileHandle) -> String? {
        var data = Data()
        while true {
            let chunk = fh.availableData
            if chunk.isEmpty { return data.isEmpty ? nil : String(data: data, encoding: .utf8) }
            if let idx = chunk.firstIndex(of: 0x0A) {
                data.append(chunk[..<idx])
                // Push back the remainder isn't possible with FileHandle;
                // requests are strictly one-per-line from the Go side.
                return String(data: data, encoding: .utf8)
            }
            data.append(chunk)
        }
    }

    private func handle(line: String) {
        guard let data = line.data(using: .utf8) else { return }
        let req: Request
        do {
            req = try decoder.decode(Request.self, from: data)
        } catch {
            send(.err(.null, RPCError(.parseError, "invalid JSON: \(error.localizedDescription)")))
            return
        }
        do {
            let result = try handler(req)
            send(.ok(req.id, result))
        } catch let e as RPCError {
            send(.err(req.id, e))
        } catch {
            send(.err(req.id, RPCError(.internalError, String(describing: error))))
        }
    }

    private func send(_ resp: Response) {
        do {
            var data = try encoder.encode(resp)
            data.append(0x0A)
            outLock.lock()
            FileHandle.standardOutput.write(data)
            outLock.unlock()
        } catch {
            FileHandle.standardError.write("encode response failed: \(error)\n".data(using: .utf8)!)
        }
    }

    private func write(rawLine: String) {
        FileHandle.standardOutput.write(rawLine.data(using: .utf8)!)
    }
}
