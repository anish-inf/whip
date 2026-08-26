package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/context-labs/whip/internal/browser/extrelay"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// fakeExtClient is a test double for the browser extension, dialing the
// relay's /ext socket with its token and answering CDP the way
// chrome.debugger would. Mirrors extrelay's test client but lives here so
// browser-package tests can drive a real *Browser through the relay.
type fakeExtClient struct {
	nc net.Conn
	br *bufio.Reader
}

func dialRelayExt(t *testing.T, r *extrelay.Relay) *fakeExtClient {
	t.Helper()
	nc, br, _, err := ws.Dial(context.Background(),
		fmt.Sprintf("ws://%s/ext?token=%s", r.Addr(), r.Token()))
	if err != nil {
		t.Fatalf("extension dial: %v", err)
	}
	if br == nil {
		br = bufio.NewReader(nc)
	}
	return &fakeExtClient{nc: nc, br: br}
}

func (f *fakeExtClient) close() { _ = f.nc.Close() }

// rw adapts the conn + buffered reader into the io.ReadWriter wsutil wants.
func (f *fakeExtClient) Read(p []byte) (int, error)  { return f.br.Read(p) }
func (f *fakeExtClient) Write(p []byte) (int, error) { return f.nc.Write(p) }

func (f *fakeExtClient) send(t *testing.T, s string) {
	t.Helper()
	if err := wsutil.WriteClientText(f.nc, []byte(s)); err != nil {
		t.Fatalf("ext write: %v", err)
	}
}

// answerLoop answers every CDP request the relay forwards, faking a page at
// https://example.com/. Runs in a goroutine until the socket closes.
func (f *fakeExtClient) answerLoop(t *testing.T) {
	t.Helper()
	for {
		msg, err := wsutil.ReadServerText(f)
		if err != nil {
			return
		}
		var fr struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
			Params struct {
				Expression string `json:"expression"`
			} `json:"params"`
		}
		if json.Unmarshal(msg, &fr) != nil || fr.ID == 0 {
			continue
		}
		_ = wsutil.WriteClientText(f.nc, []byte(fmt.Sprintf(`{"id":%d,"result":%s}`, fr.ID, fakeCDPResult(fr.Method, fr.Params.Expression))))
	}
}

// fakeCDPResult returns a method-appropriate response for the CDP commands
// whip's Backend methods issue (page-level) plus rod's attach machinery.
// Runtime.evaluate dispatches on the expression: WaitLoad polls
// document.readyState expecting the string "complete", Info() wants the
// page's URL/title JSON payload.
func fakeCDPResult(method, expr string) string {
	switch method {
	case "Runtime.evaluate":
		if expr == "document.readyState" {
			return `{"result":{"type":"string","value":"complete"}}`
		}
		if strings.Contains(expr, "JSON.stringify") {
			// Info(): JSON.stringify yields a STRING of JSON — the value must
			// be a string, not an object, or whip's unmarshal fails.
			return `{"result":{"type":"string","value":"{\"url\":\"https://example.com/\",\"title\":\"Example\",\"w\":1280,\"h\":800,\"sx\":0,\"sy\":0,\"pw\":1280,\"ph\":800}"}}`
		}
		return `{"result":{"type":"string","value":"Example"}}`
	case "Runtime.callFunctionOn":
		return `{"result":{"type":"string","value":"Example"}}`
	case "Page.getFrameTree":
		return `{"frameTree":{"frame":{"id":"tab-7","url":"https://example.com/"}}}`
	case "Page.getLayoutMetrics":
		return `{"layoutViewport":{"clientWidth":1280,"clientHeight":800},"cssContentSize":{"width":1280,"height":800},"contentSize":{"width":1280,"height":800}}`
	case "Page.captureScreenshot":
		return `{"data":"/9j/4AAQSkZJRg=="}` // minimal jpeg bytes
	case "Accessibility.getFullAXTree":
		return `{"nodes":[{"nodeId":"1","role":{"value":"RootWebArea"},"name":{"value":"Example"}}]}`
	case "DOM.getBoxModel":
		return `{"model":{"content":[0,0,10,0,10,10,0,10],"width":10,"height":10}}`
	default:
		// Page.enable, Input.*, Emulation.*, Network.*, Page.navigate, etc.
		return `{}`
	}
}
