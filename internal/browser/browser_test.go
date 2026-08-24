package browser

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// parseDevToolsActivePort: the two-line file Chrome writes.
func TestParseDevToolsActivePort(t *testing.T) {
	port, path, err := parseDevToolsActivePort([]byte("9222\n/devtools/browser/abc-123\n"))
	if err != nil || port != 9222 || path != "/devtools/browser/abc-123" {
		t.Fatalf("got %d %q %v", port, path, err)
	}
	if _, _, err := parseDevToolsActivePort([]byte("9222\n")); err == nil {
		t.Fatal("one line must fail")
	}
	if _, _, err := parseDevToolsActivePort([]byte("notaport\n/x")); err == nil {
		t.Fatal("non-numeric port must fail")
	}
}

// Profile scan finds a DevToolsActivePort in a fake profile dir.
func TestProfileScanFindsPortFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	prof := filepath.Join(home, ".config", "google-chrome")
	if err := os.MkdirAll(prof, 0o755); err != nil {
		t.Fatal(err)
	}
	// Closed browser: file exists but nothing listens → ErrNoLiveBrowser
	// with the stale-file hint, not a hang.
	if err := os.WriteFile(filepath.Join(prof, "DevToolsActivePort"), []byte("1\n/devtools/browser/dead\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := DiscoverLiveWS(context.Background())
	if err == nil || !strings.Contains(err.Error(), "stale DevToolsActivePort") {
		t.Fatalf("want stale-file error, got %v", err)
	}
}

// Live discovery resolves a running "browser" (httptest server) via
// /json/version → webSocketDebuggerUrl.
func TestDiscoverViaJSONVersion(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/version" {
			fmt.Fprintf(w, `{"webSocketDebuggerUrl":"ws://127.0.0.1:%d/devtools/browser/xyz"}`, port)
			return
		}
		http.NotFound(w, r)
	})}
	go srv.Serve(ln)
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	prof := filepath.Join(home, ".config", "chromium")
	if err := os.MkdirAll(prof, 0o755); err != nil {
		t.Fatal(err)
	}
	// SingletonLock must point at a live PID (ours) or discovery skips it.
	if err := os.Symlink(fmt.Sprintf("testhost-%d", os.Getpid()), filepath.Join(prof, "SingletonLock")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prof, "DevToolsActivePort"),
		[]byte(strconv.Itoa(port)+"\n/devtools/browser/xyz\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := DiscoverLiveWS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("ws://127.0.0.1:%d/devtools/browser/xyz", port); ws != want {
		t.Fatalf("got %q want %q", ws, want)
	}
}

// Chrome 147+ behavior: /json/version 404 → fall back to the file's ws path.
func TestDiscoverFallsBackToFileWSPath(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r) // /json/* disabled
	})}
	go srv.Serve(ln)
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	prof := filepath.Join(home, ".config", "google-chrome")
	if err := os.MkdirAll(prof, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fmt.Sprintf("h-%d", os.Getpid()), filepath.Join(prof, "SingletonLock")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(prof, "DevToolsActivePort"),
		[]byte(strconv.Itoa(port)+"\n/devtools/browser/fromfile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := DiscoverLiveWS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("ws://127.0.0.1:%d/devtools/browser/fromfile", port); ws != want {
		t.Fatalf("got %q want %q", ws, want)
	}
}

// 403 from /json/version = Chrome 144+ permission popup → ErrPermissionBlocked.
func TestDiscoverPermissionBlocked(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})}
	go srv.Serve(ln)
	defer srv.Close()

	home := t.TempDir()
	t.Setenv("HOME", home)
	prof := filepath.Join(home, ".config", "google-chrome")
	os.MkdirAll(prof, 0o755)
	os.Symlink(fmt.Sprintf("h-%d", os.Getpid()), filepath.Join(prof, "SingletonLock"))
	os.WriteFile(filepath.Join(prof, "DevToolsActivePort"), []byte(strconv.Itoa(port)+"\n/devtools/browser/x\n"), 0o644)

	_, err = DiscoverLiveWS(context.Background())
	if err == nil || !strings.Contains(err.Error(), "permission-blocked") {
		t.Fatalf("want permission-blocked, got %v", err)
	}
}

// Explicit endpoints beat the scan.
func TestDiscoverExplicitEndpoints(t *testing.T) {
	t.Setenv("LOOPY_CDP_WS", "ws://example:1234/devtools/browser/explicit")
	ws, err := DiscoverLiveWS(context.Background())
	if err != nil || ws != "ws://example:1234/devtools/browser/explicit" {
		t.Fatalf("LOOPY_CDP_WS: got %q %v", ws, err)
	}
}

// --- SSRF floor (url_safety.py port) ---

func TestCheckURLFloor(t *testing.T) {
	ctx := context.Background()
	for _, u := range []string{
		"http://169.254.169.254/latest/meta-data",
		"http://metadata.google.internal/",
		"http://169.254.170.2/v2/metadata",
	} {
		if err := CheckURL(ctx, u); err == nil {
			t.Errorf("%s must be blocked", u)
		}
	}
	if err := CheckURL(ctx, "https://example.com/"); err != nil {
		t.Errorf("example.com must pass: %v", err)
	}
	if err := CheckURL(ctx, "chrome://newtab"); err != nil {
		t.Errorf("non-http schemes pass: %v", err)
	}
}

func TestCheckPrivateURL(t *testing.T) {
	ctx := context.Background()
	for _, u := range []string{
		"http://127.0.0.1:8080/",
		"http://10.0.0.5/",
		"http://192.168.1.1/",
		"http://100.64.1.2/", // CGNAT
		"http://[::1]/",
	} {
		if err := CheckPrivateURL(ctx, u); err == nil {
			t.Errorf("%s must be blocked", u)
		}
	}
	if err := CheckPrivateURL(ctx, "https://example.com/"); err != nil {
		t.Errorf("example.com must pass: %v", err)
	}
}

// --- Session/mode selection ---

func TestSessionModeSelection(t *testing.T) {
	m := NewManager(ModeLive)
	s, err := m.Session("")
	if err != nil || s.name != "default" || s.mode != ModeLive {
		t.Fatalf("default session: %+v %v", s, err)
	}
	s, err = m.Session("headless:scrape")
	if err != nil || s.name != "scrape" || s.mode != ModeHeadless {
		t.Fatalf("mode-prefixed session: %+v %v", s, err)
	}
	if _, err := m.Session("bogus-mode:x"); err == nil {
		t.Fatal("unknown mode prefix must fail")
	}
	if _, err = m.Session("../evil"); err == nil {
		t.Fatal("path-traversal name must fail")
	}
	// Same key reuses the session; different modes don't collide.
	a, _ := m.Session("work")
	b, _ := m.Session("work")
	if a != b {
		t.Fatal("same name must return the same session")
	}
	c, _ := m.Session("dedicated:work")
	if c == a {
		t.Fatal("different modes must not share a session")
	}
}
