# whip-computer (the native macOS driver)

The Swift helper behind whip's `computer_exec` native tier — AX-tree reads,
CGEvent input, ScreenCaptureKit capture, TCC preflight — talking JSON-RPC
over stdio to the Go `internal/computer.Helper` client. Design:
`.ai-docs/plans/computer-use-native/README.md`; source of the ported design:
`docs/learnings/other-harnesses/codex-computer-use-plugin.md`.

## Build

```bash
task driver          # swift build -c release → internal/computer/bin/whip-computer
task build           # go build (embeds the helper into whip)
```

Needs macOS + Xcode Command Line Tools (`xcode-select --install`). Full Xcode
only for the XCTest suite (`task driver-test`) — CLT can't run XCTest.

## Permissions (TCC)

The helper needs **Accessibility** (AX + input) and **Screen Recording**
(SCK). On first `computer_exec` native call it presents one window with deep
links into the two System Settings panes and waits in-turn; grant once and it
sticks (TCC binds to the stable path + signature at `~/.whip/bin/whip-computer`).

Dev builds are ad-hoc signed. If Gatekeeper quarantines a copied binary:

```bash
xattr -d com.apple.quarantine ~/.whip/bin/whip-computer
```

## Wire protocol

Newline-delimited JSON-RPC 2.0 on stdin/stdout. First line is the plain-text
version announcement `whip-computer/1` (must match Go's
`internal/computer.ProtocolVersion`). Every call carries `params.token`
matching the `WHIP_COMPUTER_TOKEN` env var the parent sets — the helper
refuses token-less requests. stdout is protocol-only; logs go to stderr.

Smoke it by hand:

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"handshake","params":{"token":""}}' \
  '{"jsonrpc":"2.0","id":2,"method":"apps","params":{"token":""}}' \
  | driver/.build/release/whip-computer
```
