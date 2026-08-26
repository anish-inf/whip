package extrelay

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/gobwas/ws/wsutil"
)

// TestRodThroughRelay drives a real rod.Browser through the relay's /cdp
// endpoint, with a fake extension answering CDP — the closest we get to the
// real path without loading Chrome. Proves the synthesized Target.* answers
// satisfy rod's attach (Pages → attachToTarget) and that page-level commands
// tunnel through.
func TestRodThroughRelay(t *testing.T) {
	r, err := NewRelay()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	ext := dialWS(t, fmt.Sprintf("ws://%s/ext?token=%s", r.Addr(), r.Token()))
	defer ext.Close()
	// Extension reports the pinned tab (a real page URL so attachPage picks it).
	writeCli(t, ext, `{"method":"whip.attached","params":{"tabId":7,"title":"Example","url":"https://example.com/"}}`)
	time.Sleep(100 * time.Millisecond)

	// Answer CDP requests the way chrome.debugger would: a tiny fake page.
	go func() {
		for {
			msg, err := wsutil.ReadServerText(ext)
			if err != nil {
				return
			}
			var f struct {
				ID     int64           `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if json.Unmarshal(msg, &f) != nil || f.ID == 0 {
				continue
			}
			answer(ext, f.ID, f.Method)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	b := rod.New().ControlURL(r.CDPURL()).Context(ctx) // rod.Context shallow-copies;
	// Connect+use the SAME object — Connect on a copy leaves the original's client nil.
	if err := b.Connect(); err != nil {
		t.Fatalf("rod connect through relay: %v", err)
	}
	defer b.Close()

	pages, err := b.Pages() // Target.getTargets → synth, then attachToTarget → synth
	if err != nil {
		t.Fatalf("rod Pages through relay: %v", err)
	}
	if len(pages) != 1 {
		t.Fatalf("want exactly one page target, got %d", len(pages))
	}
	// A page-level command tunnels to the extension and back.
	val, err := pages[0].Context(ctx).Eval(`() => document.title`)
	if err != nil {
		t.Fatalf("rod Eval through relay: %v", err)
	}
	if val.Value.Str() != "Example" {
		t.Fatalf("eval round-trip: got %q", val.Value.Str())
	}
}

// answer fakes chrome.debugger responses for the commands rod issues.
// Errors writing are ignored — this runs in a background goroutine where a
// t.Fatalf is illegal; a failed write surfaces as the rod-side call failing.
func answer(ext *client, id int64, method string) {
	var result string
	switch method {
	case "Runtime.evaluate":
		result = `{"result":{"type":"string","value":"Example"}}`
	case "Runtime.callFunctionOn":
		// rod's Page.Eval wraps the expression in callFunctionOn; answer with
		// the document.title the test asserts on.
		result = `{"result":{"type":"string","value":"Example"}}`
	case "Page.getFrameTree":
		result = `{"frameTree":{"frame":{"id":"tab-7","url":"https://example.com/"},"childFrames":[]}}`
	default:
		// Permissive default: chrome.debugger answers most domain enables and
		// queries (Emulation.*, Page.enable, etc.) with an empty object.
		result = `{}`
	}
	_ = wsutil.WriteClientText(ext.nc, []byte(fmt.Sprintf(`{"id":%d,"result":%s}`, id, result)))
}
