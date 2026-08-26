// fakehelper_test.go holds the source of the Go fake whip-computer helper
// (built into a temp binary by helper_test.go). It mirrors the Swift
// driver's stdio protocol: announce version line, then newline-delimited
// JSON-RPC. Tests inject behavior via the `handle` hook (see // SCRIPT).

package computer

import (
	"bytes"
	"context"
	"os"
	"os/exec"
)

// runCmd runs a command in dir, returning combined output.
func runCmd(dir, name string, args ...string) (string, error) {
	c := exec.CommandContext(context.Background(), name, args...)
	c.Dir = dir
	var b bytes.Buffer
	c.Stdout, c.Stderr = &b, &b
	return b.String(), c.Run()
}

// fakeHelperSource is a self-contained main.go for the fake. It must stay
// dependency-free (stdlib only) and protocol-compatible with the Swift
// driver: version line first, then JSON-RPC frames. The token check accepts
// whatever the client sends (the client sets it; the fake just echoes).
const fakeHelperSource = `

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

var versionLine = "whip-computer/1"

type rpcErr struct {
	Code    int
	Message string
}

// handle is replaced by tests via the SCRIPT marker below.
var handle = func(req map[string]any) (any, *rpcErr) {
	return map[string]any{"ok": true}, nil
}

// SCRIPT

func main() {
	fmt.Println(versionLine)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 4<<20), 4<<20)
	enc := json.NewEncoder(os.Stdout)
	for sc.Scan() {
		var req map[string]any
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": map[string]any{"code": -32700, "message": err.Error()}})
			continue
		}
		if req["method"] == "handshake" {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": map[string]any{"version": versionLine}})
			continue
		}
		res, rerr := handle(req)
		if rerr != nil {
			_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req["id"], "error": map[string]any{"code": rerr.Code, "message": rerr.Message}})
			continue
		}
		_ = enc.Encode(map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": res})
	}
}
`

// Ensure os is referenced even when scripts don't (keeps builds green).
var _ = os.Getenv
