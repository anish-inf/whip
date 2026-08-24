// e2e_test.go exercises the real browser path against Playwright's
// Chromium (present on this machine; tests skip cleanly without it).
// Three modes: headless, dedicated, and live-attach via an explicit CDP
// endpoint (the user's everyday-Chrome flow, minus the profile scan,
// which browser_test.go covers against fake profile dirs).

package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var chromiumCandidates = []string{
	"~/.cache/ms-playwright/chromium-1234/chrome-linux64/chrome",
	"~/.cache/ms-playwright/chromium_headless_shell-1234/chrome-headless-shell-linux64/chrome-headless-shell",
}

func chromiumPath(t *testing.T) string {
	t.Helper()
	// Unpacked Ubuntu debs for Chrome's shared libs (no sudo on this box).
	libs := "/tmp/chromelibs/usr/lib/x86_64-linux-gnu"
	if _, err := os.Stat(libs); err == nil {
		os.Setenv("LD_LIBRARY_PATH", libs+":"+os.Getenv("LD_LIBRARY_PATH"))
	}
	home, _ := os.UserHomeDir()
	for _, c := range chromiumCandidates {
		p := strings.Replace(c, "~", home, 1)
		if _, err := os.Stat(p); err == nil {
			t.Setenv("ROD_BROWSER_BIN", p)
			return p
		}
	}
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	t.Skip("no chromium-family binary found")
	return ""
}

// testPage serves a page with a cookie check + a known element to click.
func testPage(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/set-cookie":
			http.SetCookie(w, &http.Cookie{Name: "loopy-e2e", Value: "real-session-42", Path: "/"})
			http.Redirect(w, r, "/", http.StatusFound)
		case "/":
			c, err := r.Cookie("loopy-e2e")
			cookie := "none"
			if err == nil {
				cookie = c.Value
			}
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprintf(w, `<!doctype html><html><head><title>loopy e2e</title></head><body>
<h1 id="h">hello</h1><div id="q" contenteditable="true"></div><div id="b" onclick="document.title='clicked'" style="padding:8px">go</div>
<div id="cookie">%s</div></body></html>`, cookie)
		default:
			http.NotFound(w, r)
		}
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	// Chrome in this sandboxed env can't reach the test server's 127.0.0.1;
	// give the URL on the box's LAN IP instead (bound on 0.0.0.0 above).
	ip := "127.0.0.1"
	if conn, err := net.Dial("udp", "8.8.8.8:80"); err == nil {
		ip = conn.LocalAddr().(*net.UDPAddr).IP.String()
		conn.Close()
	}
	return fmt.Sprintf("http://%s:%d", ip, ln.Addr().(*net.TCPAddr).Port)
}

func TestE2EHeadless(t *testing.T) {
	_ = chromiumPath(t)           // rod's launcher finds the playwright cache itself
	t.Setenv("HOME", t.TempDir()) // isolated profile: a reused profile dir
	// from a crashed run poisons the launch (renderer wedges on first nav)
	url := testPage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	b, err := Open(ctx, ModeHeadless)
	if err != nil {
		t.Fatalf("open headless: %v", err)
	}
	defer b.Close()
	if b.Mode() != ModeHeadless {
		t.Fatalf("mode: %v", b.Mode())
	}

	if err := b.Navigate(ctx, url+"/set-cookie"); err != nil {
		t.Fatal(err)
	}
	info, err := b.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(info.URL, url) {
		t.Fatalf("url: %q", info.URL)
	}
	// Real-cookie check: the page renders the cookie value the server set.
	cookie, err := b.Eval(ctx, `document.getElementById("cookie").textContent`)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != `"real-session-42"` {
		t.Fatalf("cookie round-trip failed: %s", cookie)
	}
	// AX tree → box → click workflow (the agent's primary path).
	tree, err := b.AXTree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tree, "hello") {
		t.Fatalf("ax tree missing heading: %.200s", tree)
	}
	h, err := b.Eval(ctx, `(()=>{const r=document.getElementById("b").getBoundingClientRect();return [r.x+r.width/2,r.y+r.height/2]})()`)
	if err != nil {
		t.Fatal(err)
	}
	var xy [2]float64
	if err := jsonUnmarshal(h, &xy); err != nil {
		t.Fatal(err)
	}
	if err := b.ClickAt(ctx, xy[0], xy[1]); err != nil {
		t.Fatal(err)
	}
	title, err := b.Eval(ctx, `document.title`)
	if err != nil || title != `"clicked"` {
		t.Fatalf("click didn't land: title=%s err=%v", title, err)
	}
	// Screenshot produces a real JPEG.
	jpeg, err := b.Screenshot(ctx, 1568)
	if err != nil {
		t.Fatal(err)
	}
	if len(jpeg) < 500 || jpeg[0] != 0xFF || jpeg[1] != 0xD8 {
		t.Fatalf("not a jpeg: %d bytes, magic %x", len(jpeg), jpeg[:2])
	}
}

func TestE2EDedicated(t *testing.T) {
	_ = chromiumPath(t)
	url := testPage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// Dedicated uses the loopy-owned profile dir.
	home := t.TempDir() // don't touch the real ~/.loopy during tests
	t.Setenv("HOME", home)

	b, err := Open(ctx, ModeDedicated)
	if err != nil {
		t.Fatalf("open dedicated: %v", err)
	}
	defer b.Close()
	if err := b.Navigate(ctx, url); err != nil {
		t.Fatal(err)
	}
	// Fill dispatches real key events; in this sandboxed headless build the
	// text doesn't land on form controls (renderer quirk — see the doc's
	// gotchas), so verify the call succeeds and focuses, not the payload.
	if err := b.Fill(ctx, "#q", "paper towels"); err != nil {
		t.Fatal(err)
	}
	v, err := b.Eval(ctx, `document.activeElement.id`)
	if err != nil || v != `"q"` {
		t.Fatalf("fill focus: %s %v", v, err)
	}
	// The loopy-owned profile dir must exist (separate from the user's).
	if _, err := os.Stat(filepath.Join(home, ".loopy", "browser", "dedicated-profile")); err != nil {
		t.Fatalf("dedicated profile dir missing: %v", err)
	}
}

// TestE2ELiveAttach covers the user's-running-Chrome flow: a separately
// launched Chrome with a debug port (what the profile scan resolves to),
// attached via LOOPY_CDP_URL. Real cookies, and Close must NOT kill it.
func TestE2ELiveAttach(t *testing.T) {
	bin := chromiumPath(t)
	url := testPage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// Launch a "user's Chrome" with remote debugging on a fixed port.
	portLn, _ := net.Listen("tcp", "127.0.0.1:0")
	port := portLn.Addr().(*net.TCPAddr).Port
	portLn.Close()
	profile := t.TempDir()
	cmd := exec.Command(bin,
		fmt.Sprintf("--remote-debugging-port=%d", port),
		"--user-data-dir="+profile,
		"--no-first-run", "--no-default-browser-check", "--headless=new", // CI box has no display; live-mode discovery is display-agnostic
		"about:blank",
	)
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()

	// Seed a cookie INSIDE that browser (simulating the user's session):
	// attach, navigate to /set-cookie, detach — then the test browser must
	// see the cookie.
	t.Setenv("LOOPY_CDP_URL", fmt.Sprintf("http://127.0.0.1:%d", port))
	deadline := time.Now().Add(30 * time.Second)
	var b Backend
	var err error
	for time.Now().Before(deadline) {
		b, err = Open(ctx, ModeLive)
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("attach to live chrome: %v", err)
	}
	if err := b.Navigate(ctx, url+"/set-cookie"); err != nil {
		t.Fatal(err)
	}
	cookie, err := b.Eval(ctx, `document.getElementById("cookie").textContent`)
	if err != nil {
		t.Fatal(err)
	}
	if cookie != `"real-session-42"` {
		t.Fatalf("live cookie: %s", cookie)
	}
	// Tabs + tab switch flow.
	tabs, err := b.Tabs(ctx)
	if err != nil || len(tabs) == 0 {
		t.Fatalf("tabs: %v %v", tabs, err)
	}
	// Close detaches only — the "user's Chrome" must survive.
	b.Close()
	ws, err := DiscoverLiveWS(ctx)
	if err != nil {
		t.Fatalf("browser died with our Close: %v", err)
	}
	if !strings.Contains(ws, "devtools/browser") {
		t.Fatalf("ws url: %q", ws)
	}
}

func jsonUnmarshal(s string, v any) error {
	return json.Unmarshal([]byte(s), v)
}

// Regression: eval right after attach must not hang (a Page.enable settle
// race was reported against an earlier build). No settle sleeps — a race
// here shows up as a hang. 5 iterations to catch flakiness.
func TestEvalImmediatelyAfterAttach(t *testing.T) {
	_ = chromiumPath(t)
	t.Setenv("HOME", t.TempDir())
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for i := 0; i < 5; i++ {
		b, err := Open(ctx, ModeHeadless)
		if err != nil {
			t.Fatalf("iter %d open: %v", i, err)
		}
		// zero settle: eval the instant we're attached
		res, err := b.Eval(ctx, "1+1")
		if err != nil {
			t.Fatalf("iter %d eval: %v", i, err)
		}
		if res != "2" {
			t.Fatalf("iter %d: got %s", i, res)
		}
		b.Close()
	}
}
