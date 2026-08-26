# ACP v1 Protocol Notes — Agent Side Implementation

Source: https://agentclientprotocol.com/protocol/v1/* (fetched 2025; the docs note the canonical
JSON schema can be downloaded from
https://github.com/agentclientprotocol/agent-client-protocol/releases/latest/download/schema.json).

We implement the **Agent** side: the harness runs as a subprocess of an editor (Client),
reading JSON-RPC 2.0 messages from stdin and writing to stdout.

Every request/response/notification params object (and most nested types) also accepts an
optional `_meta` field (`object | null`) for extensions. Implementations **MUST NOT** add
custom root-level fields to spec types; extension method names must start with `_`.

Go-struct guidance: `_meta` → `map[string]any`, `object`/`rawInput`/`rawOutput` →
`json.RawMessage` or `map[string]any`, `*T` for `| null` optional fields.

---

## 1. Transport (stdio)

From transports.md — exact rules:

* Client launches the agent as a **subprocess**.
* Agent reads JSON-RPC messages from **stdin**, writes messages to **stdout**.
* Messages are individual JSON-RPC requests, notifications, or responses.
* Messages are delimited by **newlines (`\n`)** and **MUST NOT contain embedded newlines**
  (i.e. newline-delimited JSON, one compact JSON object per line; **no** `Content-Length`
  headers — that is LSP, not ACP).
* All messages MUST be UTF-8 encoded.
* Agent MAY write UTF-8 strings to **stderr** for logging; clients may capture/forward/ignore.
* Agent MUST NOT write anything to stdout that is not a valid ACP message; client MUST NOT
  write anything to agent stdin that is not a valid ACP message.
* Shutdown: client closes stdin and terminates the subprocess.
* Streamable HTTP transport is a draft proposal; stdio is what both sides SHOULD support.

JSON-RPC framing: request = `{jsonrpc:"2.0", id:<string|number>, method, params?}`;
notification = same without `id`; response = `{jsonrpc:"2.0", id, result}` or
`{jsonrpc:"2.0", id, error:{code, message, data?}}`. RequestId may be string, int64, or
null (null discouraged). The agent will both receive requests (initialize, session/*) and
**send** its own requests to the client (session/request_permission, fs/*, terminal/*) while
a prompt turn is in flight — the connection is fully bidirectional and concurrent.

---

## 2. Version negotiation

* `protocolVersion` is a single **integer (uint16, 0–65535)** identifying a MAJOR version;
  bumped only on breaking changes. Current version: **1**.
* Client sends the latest version it supports in `initialize`.
* If the Agent supports the requested version, it MUST respond with the same version;
  otherwise it MUST respond with the latest version it supports.
* If the Client doesn't support the version in the response, it SHOULD close the connection.
* Non-breaking features are introduced via **capabilities**, never version bumps.
* Capabilities omitted from `initialize` MUST be treated as **UNSUPPORTED** by both sides.

---

## 3. Method schemas (exact wire shapes)

All shapes below are copied from schema.md. `[req]` = required, otherwise optional/nullable.
`_meta?: object | null` is omitted from the listings for brevity but is legal on **every**
params/result object and nested type.

### 3.1 `initialize` (client → agent request)

**InitializeRequest params:**
```json
{
  "protocolVersion": 1,                     // [req] uint16 — latest version client supports
  "clientCapabilities": {                    // optional; default {"fs":{"readTextFile":false,"writeTextFile":false},"terminal":false,"auth":{"terminal":false}}
    "fs": {                                  // optional; default {"readTextFile":false,"writeTextFile":false}
      "readTextFile": true,                  // bool, default false — fs/read_text_file available
      "writeTextFile": true                  // bool, default false — fs/write_text_file available
    },
    "terminal": true,                        // bool, default false — all terminal/* methods available
    "auth": {                                // optional; default {"terminal":false}
      "terminal": true                       // bool, default false — client can run agent in interactive terminal
    },
    "elicitation": null,                     // ElicitationCapabilities | null — omitted/null = no elicitation modes
    "session": null                          // ClientSessionCapabilities | null — {"configOptions": {"boolean": {}}} etc.
  },
  "clientInfo": {                            // Implementation | null (SHOULD be sent)
    "name": "my-client",                     // [req] string
    "title": "My Client",                    // string | null — human-readable
    "version": "1.0.0"                       // [req] string
  }
}
```

**InitializeResponse result:**
```json
{
  "protocolVersion": 1,                      // [req] uint16 — client's version if supported, else agent's latest
  "agentCapabilities": {                     // optional; default {"loadSession":false,"promptCapabilities":{"image":false,"audio":false,"embeddedContext":false},"mcpCapabilities":{"http":false,"sse":false},"sessionCapabilities":{},"auth":{}}
    "loadSession": true,                     // bool, default false — session/load available
    "promptCapabilities": {                  // optional; default all false
      "image": true,                         // bool, default false — ContentBlock::Image in prompts
      "audio": true,                         // bool, default false — ContentBlock::Audio
      "embeddedContext": true                // bool, default false — ContentBlock::Resource
    },
    "mcpCapabilities": {                     // optional; default {"http":false,"sse":false}
      "http": true,                          // bool, default false — McpServer::Http supported
      "sse": true                            // bool, default false — McpServer::Sse (deprecated by MCP)
    },
    "auth": {                                // optional; default {}
      "logout": {}                           // LogoutCapabilities | null — omitted/null = unsupported; {} = logout available
    },
    "sessionCapabilities": {                 // optional; default {}
      "list": {},                            // {} | null — session/list
      "resume": {},                          // {} | null — session/resume
      "close": {},                           // {} | null — session/close
      "delete": {},                          // {} | null — session/delete
      "additionalDirectories": {}            // {} | null — additionalDirectories field honored
    }
  },
  "agentInfo": {"name": "my-agent", "title": "My Agent", "version": "1.0.0"},  // Implementation | null (SHOULD)
  "authMethods": []                          // AuthMethod[], default [] — see §9
}
```

Note: `session/load` support is advertised by the **top-level `loadSession` bool**, NOT inside
`sessionCapabilities` (the schema notes this inconsistency will be unified in a future version).

### 3.2 `authenticate` (client → agent request) — likely not implemented

**AuthenticateRequest params:** `{ "methodId": "agent-login" }` — `methodId` [req] AuthMethodId (string);
must be one of the advertised auth methods whose type defines the authenticate flow (never a `terminal` method).

**AuthenticateResponse result:** empty object `{}` (only `_meta` allowed).

### 3.3 `session/new` (client → agent request)

**NewSessionRequest params:**
```json
{
  "cwd": "/home/user/project",               // [req] string — absolute path; base for relative paths
  "mcpServers": [ /* McpServer[], [req] — may be empty; see §5 */ ],
  "additionalDirectories": ["/abs/path"]     // string[] — only if agent advertised sessionCapabilities.additionalDirectories
}
```

**NewSessionResponse result:**
```json
{
  "sessionId": "sess_abc123def456",          // [req] SessionId (string) — unique, used in all subsequent requests
  "modes": {                                 // SessionModeState | null — initial mode state if supported
    "currentModeId": "ask",                  // [req] SessionModeId (string)
    "availableModes": [                      // [req] SessionMode[]
      {"id": "ask", "name": "Ask", "description": "Request permission before making any changes"}
    ]                                        // SessionMode: id [req], name [req], description (string|null)
  },
  "configOptions": null                      // SessionConfigOption[] | null — initial config options if supported
}
```
May return an `auth_required` error (-32000) if authentication is required.

### 3.4 `session/load` (client → agent request; only if `loadSession: true`)

**LoadSessionRequest params:**
```json
{
  "sessionId": "sess_789xyz",                // [req] SessionId
  "cwd": "/home/user/project",               // [req] string — absolute; must match session's cwd
  "mcpServers": [ /* McpServer[] [req] */ ],
  "additionalDirectories": ["..."]           // string[] — complete replacement list
}
```

**LoadSessionResponse result:** object; all fields optional:
```json
{
  "modes": null,                             // SessionModeState | null
  "configOptions": null                      // SessionConfigOption[] | null
}
```
IMPORTANT ordering: the agent MUST replay the **entire** conversation as `session/update`
notifications (user_message_chunk / agent_message_chunk, with optional opaque `messageId`s)
**before** responding to `session/load`. (The session-setup.md example shows `"result": null`;
the schema defines an object with optional modes/configOptions — sending `null` matches older
behavior, `{}` is schema-conformant.)

### 3.5 `session/resume` / `session/close` / `session/list` / `session/delete` (capability-gated, FYI)

* `session/resume` params: `{sessionId [req], cwd [req], mcpServers (optional here!), additionalDirectories?}`;
  result `{modes?, configOptions?}`; MUST NOT replay history. Gated by `sessionCapabilities.resume`.
* `session/close` params: `{sessionId [req]}`; result `{}`. Agent MUST cancel ongoing work as if
  `session/cancel` was called, then free resources. Gated by `sessionCapabilities.close`.
* `session/list` params: `{cursor?: string|null, cwd?: string|null}`; result
  `{sessions: SessionInfo[] [req], nextCursor?: string|null}` where
  SessionInfo = `{sessionId [req], cwd [req], title?: string|null, updatedAt?: string|null (ISO 8601), additionalDirectories?: string[]}`.
* `session/delete` params: `{sessionId [req]}`; result `{}`.

### 3.6 `session/prompt` (client → agent request)

**PromptRequest params:**
```json
{
  "sessionId": "sess_abc123def456",          // [req] SessionId
  "prompt": [ /* ContentBlock[] [req] — see §4 */ ]
}
```

**PromptResponse result:**
```json
{ "stopReason": "end_turn" }                 // [req] StopReason
```

**StopReason** (exact wire values):
* `"end_turn"` — model finished without requesting more tools
* `"max_tokens"` — maximum token limit reached
* `"max_turn_requests"` — max model requests within a single turn exceeded
* `"refusal"` — agent refused to continue (prompt and everything after won't be in next context)
* `"cancelled"` — client cancelled via `session/cancel`; MUST be returned on cancellation even
  if underlying ops throw (catch abort errors and return this, not an error response)

### 3.7 `session/cancel` (client → agent **notification**, no response)

**CancelNotification params:** `{ "sessionId": "sess_abc123def456" }` — `sessionId` [req].

On receipt the agent SHOULD stop all LLM requests and tool invocations ASAP, MAY send pending
`session/update` notifications first, then MUST respond to the in-flight `session/prompt` with
`stopReason: "cancelled"`. Client SHOULD preemptively mark unfinished tool calls cancelled and
MUST answer pending `session/request_permission` with the `"cancelled"` outcome.

### 3.8 `session/set_mode` (client → agent request; only if agent advertised modes)

**SetSessionModeRequest params:** `{ "sessionId": "...", "modeId": "code" }` — both [req];
`modeId` must be one of `availableModes`. **SetSessionModeResponse result:** `{}` (only `_meta`).

### 3.9 `session/update` (agent → client **notification**)

**SessionNotification params:**
```json
{
  "sessionId": "sess_abc123def456",          // [req]
  "update": { /* SessionUpdate union [req], discriminated by "sessionUpdate" field */ }
}
```

**SessionUpdate variants** (discriminator field: `sessionUpdate`, required on every variant):

1. `"user_message_chunk"` — used when replaying history in session/load:
   `{sessionUpdate:"user_message_chunk" [req], content: ContentBlock [req], messageId?: string|null}`
2. `"agent_message_chunk"` — streamed model output:
   `{sessionUpdate:"agent_message_chunk" [req], content: ContentBlock [req], messageId?: string|null}`
3. `"agent_thought_chunk"` — streamed internal reasoning:
   `{sessionUpdate:"agent_thought_chunk" [req], content: ContentBlock [req], messageId?: string|null}`

   (Chunks sharing a `messageId` belong to one message; a changed `messageId` starts a new message.
   `messageId` is optional, opaque, unique per message.)

4. `"tool_call"` — new tool call initiated (== ToolCall struct):
   ```json
   {
     "sessionUpdate": "tool_call",            // [req]
     "toolCallId": "call_001",                // [req] string, unique within session
     "title": "Reading configuration file",   // [req] string
     "kind": "read",                          // ToolKind, optional (default "other")
     "status": "pending",                     // ToolCallStatus, optional (default "pending")
     "content": [ /* ToolCallContent[] */ ],  // optional
     "locations": [ {"path": "/abs", "line": 42} ],  // ToolCallLocation[]: path [req] string, line int|null
     "rawInput": {},                          // object, optional
     "rawOutput": {}                          // object, optional
   }
   ```
5. `"tool_call_update"` — partial update; **all fields except `sessionUpdate` and `toolCallId`
   are optional**; only changed fields included; `content`/`locations` are wholesale replacements:
   `{sessionUpdate:"tool_call_update" [req], toolCallId [req], title?: string|null,
   kind?: ToolKind|null, status?: ToolCallStatus|null, content?: ToolCallContent[]|null,
   locations?: ToolCallLocation[]|null, rawInput?: object, rawOutput?: object}`
6. `"plan"` — `{sessionUpdate:"plan" [req], entries: PlanEntry[] [req]}`. MUST send the complete
   entry list each time; client replaces the whole plan.
   PlanEntry: `{content: string [req], priority: PlanEntryPriority [req], status: PlanEntryStatus [req]}`
7. `"available_commands_update"` — `{sessionUpdate:"available_commands_update" [req],
   availableCommands: AvailableCommand[] [req]}`.
   AvailableCommand: `{name: string [req], description: string [req], input?: {hint: string [req]} | null}`
   (input is AvailableCommandInput, currently only unstructured text with a `hint`).
8. `"current_mode_update"` — `{sessionUpdate:"current_mode_update" [req], currentModeId: SessionModeId [req]}`.
   ⚠️ The session-modes.md doc example shows `"modeId": "code"` but the **schema** (both the
   SessionUpdate variant and the CurrentModeUpdate type) says `currentModeId` — trust the schema.
9. `"config_option_update"` — `{sessionUpdate:"config_option_update" [req], configOptions: SessionConfigOption[] [req]}`
10. `"session_info_update"` — `{sessionUpdate:"session_info_update" [req], title?: string|null, updatedAt?: string|null (ISO 8601)}`
11. `"usage_update"` — `{sessionUpdate:"usage_update" [req], used: uint64 [req], size: uint64 [req],
    cost?: {amount: number [req], currency: string [req] /* ISO 4217, e.g. "USD" */} | null}`
    (`used` = tokens currently in context; `size` = total context window tokens.)

### 3.10 `session/request_permission` (**agent → client request**)

**RequestPermissionRequest params:**
```json
{
  "sessionId": "sess_abc123def456",          // [req]
  "toolCall": { /* ToolCallUpdate [req] — same shape as tool_call_update above; toolCallId [req], rest optional */ },
  "options": [                               // [req] PermissionOption[]
    {"optionId": "allow-once", "name": "Allow once", "kind": "allow_once"}
  ]                                          // PermissionOption: optionId [req] string, name [req] string, kind [req] PermissionOptionKind
}
```

**RequestPermissionResponse result:**
```json
{ "outcome": { "outcome": "selected", "optionId": "allow-once" } }   // selected variant
{ "outcome": { "outcome": "cancelled" } }                            // cancelled variant (no optionId)
```
`outcome` [req] RequestPermissionOutcome — union discriminated by its own `"outcome"` field:
`"selected"` (adds `optionId: PermissionOptionId [req]`) or `"cancelled"`. Client MUST answer
`cancelled` for pending permission requests when the turn is cancelled.

**PermissionOptionKind** (exact wire values): `"allow_once"`, `"allow_always"`, `"reject_once"`, `"reject_always"`.

### 3.11 `fs/read_text_file` (agent → client request; only if `clientCapabilities.fs.readTextFile`)

**Params:** `{sessionId [req], path [req] string (absolute), line?: int|null (1-based start, min 0), limit?: int|null (max lines, min 0)}`
**Result:** `{content: string [req]}`

### 3.12 `fs/write_text_file` (agent → client request; only if `clientCapabilities.fs.writeTextFile`)

**Params:** `{sessionId [req], path [req] string (absolute; client MUST create if missing), content [req] string}`
**Result:** empty (`{}` / `null`).

### 3.13 `terminal/create` (agent → client request; only if `clientCapabilities.terminal`)

**Params:**
```json
{
  "sessionId": "...",                        // [req]
  "command": "npm",                          // [req] string
  "args": ["test", "--coverage"],            // string[], optional
  "env": [{"name": "NODE_ENV", "value": "test"}],  // EnvVariable[] {name [req], value [req]}, optional
  "cwd": "/home/user/project",               // string|null, absolute
  "outputByteLimit": 1048576                 // int|null, min 0 — client truncates from the beginning at a char boundary
}
```
**Result:** `{terminalId: string [req]}` — returned immediately; command runs in background.
Agent MUST eventually call `terminal/release`.

### 3.14 `terminal/output` (agent → client)

**Params:** `{sessionId [req], terminalId [req]}`
**Result:** `{output: string [req], truncated: bool [req], exitStatus?: {exitCode: int|null, signal: string|null} | null}` —
`exitStatus` present only if the command has exited.

### 3.15 `terminal/wait_for_exit` (agent → client)

**Params:** `{sessionId [req], terminalId [req]}`
**Result:** `{exitCode?: int|null (min 0; null if killed by signal), signal?: string|null (null if exited normally)}`
— resolves once the command exits.

### 3.16 `terminal/kill` (agent → client)

**Params:** `{sessionId [req], terminalId [req]}` — kills the command but keeps the TerminalId
valid for `terminal/output` / `terminal/wait_for_exit`. **Result:** empty. Still MUST release later.

### 3.17 `terminal/release` (agent → client)

**Params:** `{sessionId [req], terminalId [req]}` — kills if still running, frees resources.
**Result:** empty. After release the TerminalId is invalid for all `terminal/*` methods, but a
tool call already containing it SHOULD keep displaying its output.

---

## 4. Content blocks (ContentBlock union)

Same structure as MCP's ContentBlock (2025-06-18). Discriminated by `"type"`. All variants also
allow `annotations?: Annotations|null` and `_meta`. Annotations: `{audience?: ("assistant"|"user")[]|null, lastModified?: string|null, priority?: number|null}`.

| type | fields | prompt gating |
|---|---|---|
| `"text"` | `text: string [req]` | **MUST be supported by all agents** (baseline) |
| `"image"` | `data: string [req] (base64), mimeType: string [req], uri?: string\|null` | requires `promptCapabilities.image` |
| `"audio"` | `data: string [req] (base64), mimeType: string [req]` | requires `promptCapabilities.audio` |
| `"resource_link"` | `uri: string [req], name: string [req], mimeType?: string\|null, title?: string\|null, description?: string\|null, size?: int\|null` | **MUST be supported by all agents** (baseline) |
| `"resource"` | `resource: EmbeddedResourceResource [req]` | requires `promptCapabilities.embeddedContext` |

`EmbeddedResourceResource` union (no discriminator; presence of `text` vs `blob` distinguishes):
* Text: `{uri: string [req], text: string [req], mimeType?: string|null}`
* Blob: `{uri: string [req], blob: string [req] (base64), mimeType?: string|null}`

Clients MUST restrict prompt content types to the advertised prompt capabilities.
`resource` is preferred over `resource_link` for context (avoids round-trips; works for sources
the agent can't access). Clients SHOULD render text blocks as Markdown.

## 5. Tool call content & enums

**ToolCallContent** union (discriminated by `"type"`):
* `"content"` — `{type:"content" [req], content: ContentBlock [req]}`
* `"diff"` — `{type:"diff" [req], path: string [req] (absolute), newText: string [req], oldText: string|null (null = new file)}`
* `"terminal"` — `{type:"terminal" [req], terminalId: string [req]}` (terminal must be embedded before `terminal/release`)

**ToolCallStatus** (wire values): `"pending"` (input streaming / awaiting approval), `"in_progress"`,
`"completed"`, `"failed"`. Default when omitted on `tool_call`: `pending`.

**ToolKind** (wire values): `"read"`, `"edit"`, `"delete"`, `"move"`, `"search"`, `"execute"`,
`"think"`, `"fetch"`, `"switch_mode"`, `"other"` (default).

**PlanEntryPriority**: `"high"`, `"medium"`, `"low"`. **PlanEntryStatus**: `"pending"`,
`"in_progress"`, `"completed"`.

**McpServer** union in `session/new`/`session/load`/`session/resume`:
* stdio (no `type` field; all agents MUST support): `{name [req], command [req] (absolute path), args [req] string[], env [req] EnvVariable[]}`
* http: `{type:"http" [req], name [req], url [req], headers [req] HttpHeader[]}` — needs `mcpCapabilities.http`
* sse: `{type:"sse" [req], name [req], url [req], headers [req] HttpHeader[]}` — needs `mcpCapabilities.sse` (deprecated transport)

EnvVariable / HttpHeader: `{name: string [req], value: string [req]}`.

---

## 6. Lifecycle / state machine

1. **Connection**: client spawns agent subprocess; newline-delimited JSON-RPC over stdio.
2. **Initialize (MUST be first)**: client → `initialize`; agent replies with version +
   `agentCapabilities` + `authMethods`. All capability omissions = unsupported.
3. **Authenticate (optional)**: if `session/new` would fail, it returns error `-32000`
   (`auth_required`); client calls `authenticate {methodId}` for an `agent`-type method, or runs
   a `terminal`-type method out-of-band, then retries.
4. **Session setup**: client → `session/new` (or `session/load` / `session/resume` when
   capability-advertised). Agent returns `sessionId` (+ optional `modes`, `configOptions`).
   Agent SHOULD connect to the listed MCP servers. On `session/load`, replay full history via
   `session/update` before responding. After session creation the agent MAY send
   `available_commands_update` (and re-send it any time).
5. **Prompt turn** (repeatable):
   * Client → `session/prompt`.
   * Agent processes (possibly many LLM round-trips), streaming `session/update` notifications:
     `plan`, `agent_thought_chunk`, `agent_message_chunk`, `tool_call`, `tool_call_update`,
     `usage_update`, `session_info_update`, `current_mode_update`.
   * Before executing a sensitive tool, agent MAY send `session/request_permission` (a request
     agent→client with its own id) and MUST wait for the outcome.
   * During tool execution the agent MAY call client-capability methods (`fs/*`, `terminal/*`)
     — these are agent→client requests that run **concurrently** with the in-flight prompt.
   * Turn ends when the agent responds to `session/prompt` with a `stopReason`. The agent MAY
     stop the turn at any point with any stop reason.
   * Cancellation: client sends `session/cancel` notification → agent aborts, MAY flush pending
     updates first, then MUST answer `session/prompt` with `stopReason:"cancelled"` (never an
     error). Client SHOULD still accept tool call updates arriving after it sent cancel.
6. **Mode changes** any time (idle or mid-turn): client → `session/set_mode`; agent →
   `current_mode_update` notification when it changes modes itself.
7. **Session teardown**: `session/close` (if advertised) = cancel work + free resources.
8. **Connection teardown**: client closes stdin, terminates subprocess.

## 7. Error codes (ErrorCode)

Standard JSON-RPC plus ACP-specific (reserved range -32000..-32099):

| code | meaning |
|---|---|
| `-32700` | Parse error |
| `-32600` | Invalid request |
| `-32601` | Method not found (also used for unknown `_` extension methods) |
| `-32602` | Invalid params |
| `-32603` | Internal error |
| `-32800` | **Request cancelled** — response to a request cancelled via `$/cancel_request` or internal abort |
| `-32000` | **Authentication required** (`auth_required`) — returned e.g. by `session/new` when auth needed |
| `-32002` | **Resource not found** — e.g. a file not found |
| other int32 | implementation-defined |

Error object: `{code: int [req], message: string [req], data?: object}`.

**`$/cancel_request`** (protocol-level notification, either direction): params
`{requestId: RequestId [req]}`. Receiver MAY cancel the activity, MAY flush pending
notifications, and MUST then respond to the original request with either a valid (partial)
result or error `-32800`. `$/`-prefixed notifications may be ignored by implementations that
can't support them. Note this is distinct from `session/cancel` (which cancels a prompt turn).

## 8. Capability negotiation — what gates what

Agent-advertised (InitializeResponse.agentCapabilities):
* `loadSession` (bool) → client may call `session/load`.
* `promptCapabilities.image/audio/embeddedContext` → client may include those ContentBlock types
  in `session/prompt`. Text + resource_link are baseline MUSTs.
* `mcpCapabilities.http/sse` → client may include http/sse McpServer entries. Stdio MCP servers
  are baseline MUST for agents.
* `auth.logout: {}` → client may call `logout`.
* `sessionCapabilities.resume/close/delete/list/additionalDirectories` (each `{}` = supported,
  omitted/null = unsupported) → gates `session/resume`, `session/close`, `session/delete`,
  `session/list`, and the `additionalDirectories` field on new/load/resume.
* Advertising `modes` in session/new|load|resume result enables `session/set_mode` from client.

Client-advertised (InitializeRequest.clientCapabilities):
* `fs.readTextFile` / `fs.writeTextFile` → agent may call `fs/read_text_file` / `fs/write_text_file`.
* `terminal` (bool) → agent may call all `terminal/*` methods.
* `auth.terminal` (bool) → agent may advertise `type:"terminal"` auth methods.
* `elicitation` → gates `elicitation/create` modes (form/url). We likely skip.
* `session.configOptions.boolean: {}` → agent may include `type:"boolean"` config options.

If a capability is false/absent, the peer **MUST NOT** call the gated methods. New capabilities
are non-breaking; treat unknown capabilities as ignorable and unknown `_meta`/extension
notifications as ignorable (unknown extension *requests* get `-32601`).

## 9. Authentication (skim — we likely skip)

* Agent advertises `authMethods: AuthMethod[]` in initialize response (default `[]`).
* AuthMethod union: default `agent` type `{id [req], name [req], description?}` (no `type` field);
  or `{type:"terminal" [req], id [req], name [req], description?, args? string[], env? object}` —
  terminal methods may only be advertised if client set `clientCapabilities.auth.terminal`, and
  MUST NOT be passed to `authenticate` (client re-runs the agent binary interactively with
  those args/env, then reconnects).
* `authenticate {methodId}` → `{}` on success. `logout {}` → `{}` (only if `auth.logout: {}`).
* Auth-gated methods return error code `-32000`.

## 10. Extensibility

* `_meta` (`{[key: string]: unknown} | null`) on every type. Reserved root keys for W3C trace
  context: `traceparent`, `tracestate`, `baggage`.
* Custom methods MUST start with `_` (e.g. `_zed.dev/workspace/buffers`); unknown custom requests
  → `-32601`; unknown custom notifications SHOULD be ignored.
* Custom capabilities advertised inside capability objects' `_meta` (e.g.
  `agentCapabilities._meta["zed.dev"]`).

## 11. Implementation notes / gotchas for the Go agent

* Framing is NDJSON — one compact JSON object per line on stdin/stdout; nothing else on stdout.
* The agent is both server (initialize, session/new, session/load, session/prompt, session/cancel,
  session/set_mode, authenticate, $/cancel_request) and client (session/request_permission,
  fs/*, terminal/*, elicitation/*). Requests in both directions may be in flight simultaneously —
  the JSON-RPC demultiplexer must correlate ids across directions and dispatch while a prompt
  turn is running.
* Ids can be number or string — use `json.RawMessage` and echo back verbatim.
* Cancellation: never let an aborted LLM call surface as an error response to `session/prompt`;
  map it to `stopReason:"cancelled"`.
* Schema/docs discrepancy: `current_mode_update` field is `currentModeId` per schema (docs page
  shows `modeId` — treat schema as authoritative).
* `session/load` response: docs example shows `result: null`, schema says object with optional
  `modes`/`configOptions` — send `{}` (or null) after replaying history.
* `tool_call_update` semantics: `content` and `locations` replace the whole collection; other
  fields are patched.
