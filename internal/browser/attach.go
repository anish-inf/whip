// attach.go ports browser-harness daemon.py's live-browser discovery:
// scan well-known Chromium profile dirs for DevToolsActivePort, verify the
// browser process actually holds the profile (SingletonLock), and resolve
// the file's port+path (or an explicit endpoint) to a WebSocket debugger
// URL — including Chrome 147+'s disabled /json/* on the default profile
// and Chrome 144+'s per-connection permission popup as structured errors.

package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ProfileDir is a well-known Chromium-family user-data dir (daemon.py's
// _MAC_PROFILES/_LINUX_PROFILES/_WINDOWS_PROFILES).
func profileDirs() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var rel []string
	switch runtime.GOOS {
	case "darwin":
		rel = []string{
			"Library/Application Support/Google/Chrome",
			"Library/Application Support/Google/Chrome Canary",
			"Library/Application Support/Comet",
			"Library/Application Support/Arc/User Data",
			"Library/Application Support/Dia/User Data",
			"Library/Application Support/Microsoft Edge",
			"Library/Application Support/Microsoft Edge Beta",
			"Library/Application Support/Microsoft Edge Dev",
			"Library/Application Support/Microsoft Edge Canary",
			"Library/Application Support/BraveSoftware/Brave-Browser",
		}
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		rel = []string{
			"Google/Chrome/User Data",
			"Google/Chrome SxS/User Data",
			"Google/Chrome Beta/User Data",
			"Google/Chrome Dev/User Data",
			"Chromium/User Data",
			"Microsoft/Edge/User Data",
			"Microsoft/Edge Beta/User Data",
			"Microsoft/Edge Dev/User Data",
			"Microsoft/Edge SxS/User Data",
			"BraveSoftware/Brave-Browser/User Data",
		}
		var out []string
		for _, r := range rel {
			out = append(out, filepath.Join(local, filepath.FromSlash(r)))
		}
		return out
	default: // linux & friends
		rel = []string{
			".config/google-chrome",
			".config/chromium",
			".config/chromium-browser",
			".config/microsoft-edge",
			".config/microsoft-edge-beta",
			".config/microsoft-edge-dev",
			".var/app/org.chromium.Chromium/config/chromium",
			".var/app/com.google.Chrome/config/google-chrome",
			".var/app/com.brave.Browser/config/BraveSoftware/Brave-Browser",
			".var/app/com.microsoft.Edge/config/microsoft-edge",
		}
	}
	var out []string
	for _, r := range rel {
		out = append(out, filepath.Join(home, filepath.FromSlash(r)))
	}
	return out
}

// parseDevToolsActivePort reads the two-line file Chrome writes:
// line 1 = port, line 2 = WS path (daemon.py get_ws_url).
func parseDevToolsActivePort(data []byte) (port int, wsPath string, err error) {
	lines := strings.Split(string(data), "\n")
	if len(lines) < 2 {
		return 0, "", fmt.Errorf("DevToolsActivePort: want 2 lines, got %d", len(lines))
	}
	port, err = strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil {
		return 0, "", fmt.Errorf("DevToolsActivePort port: %w", err)
	}
	wsPath = strings.TrimSpace(lines[1])
	if wsPath == "" {
		return 0, "", fmt.Errorf("DevToolsActivePort: empty ws path")
	}
	return port, wsPath, nil
}

// browserRunningForProfile reports whether a live process holds this
// user-data-dir, via the SingletonLock symlink's embedded PID (POSIX;
// daemon.py browser_running_for_profile). Unknown/Windows answers false —
// discovery then relies on the port being reachable.
func browserRunningForProfile(base string) bool {
	if runtime.GOOS == "windows" {
		return true // Chromium on Windows uses a named mutex; assume running.
	}
	target, err := os.Readlink(filepath.Join(base, "SingletonLock"))
	if err != nil {
		return false
	}
	pidStr := target[strings.LastIndex(target, "-")+1:]
	pid, err := strconv.Atoi(pidStr)
	if err != nil {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 = existence probe (os.kill(pid, 0) in the Python).
	return proc.Signal(os.Signal(sigzero())) == nil
}

// portLive is daemon.py's _devtools_port_live: a stale DevToolsActivePort
// file left by a closed browser must not count.
func portLive(port int) bool {
	c, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 500*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// resolveWSURL turns an HTTP DevTools endpoint into the WebSocket debugger
// URL, per daemon.py get_ws_url: /json/version normally; on 404 (Chrome
// 147+ default profile) fall back to the DevToolsActivePort file contents.
func resolveWSURL(ctx context.Context, base, host string, port int, wsPath string) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, base+"/json/version", nil)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return "", fmt.Errorf("%w: Chrome is reachable, but the per-session 'Allow remote debugging' popup has not been accepted — click Allow in Chrome, then retry", ErrPermissionBlocked)
	}
	if resp.StatusCode == http.StatusOK {
		var v struct {
			WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&v); err == nil && v.WebSocketDebuggerURL != "" {
			return v.WebSocketDebuggerURL, nil
		}
	}
	// 404 (or undecodable): Chrome 147+ disables /json/* on the default
	// user-data-dir; the ws path in DevToolsActivePort still works.
	if wsPath != "" {
		return "ws://" + net.JoinHostPort(host, strconv.Itoa(port)) + wsPath, nil
	}
	return "", fmt.Errorf("%s/json/version: HTTP %d and no DevToolsActivePort ws path", base, resp.StatusCode)
}

// DiscoverLiveWS finds a running Chromium-family browser with remote
// debugging enabled and returns its browser-level WebSocket URL. Explicit
// endpoints win: LOOPY_CDP_WS (ws:// URL, used verbatim) then LOOPY_CDP_URL
// (http:// endpoint, resolved). Otherwise the profile scan runs.
func DiscoverLiveWS(ctx context.Context) (string, error) {
	if ws := os.Getenv("LOOPY_CDP_WS"); ws != "" {
		return ws, nil
	}
	if httpURL := os.Getenv("LOOPY_CDP_URL"); httpURL != "" {
		host, port := splitHostPort(httpURL)
		return resolveWSURL(ctx, strings.TrimRight(httpURL, "/"), host, port, "")
	}
	var sawStaleFile bool
	for _, base := range profileDirs() {
		data, err := os.ReadFile(filepath.Join(base, "DevToolsActivePort"))
		if err != nil {
			continue
		}
		port, wsPath, err := parseDevToolsActivePort(data)
		if err != nil {
			continue
		}
		if !portLive(port) {
			sawStaleFile = true // closed browser leaves the file behind
			continue
		}
		if !browserRunningForProfile(base) {
			continue
		}
		ws, err := resolveWSURL(ctx, fmt.Sprintf("http://127.0.0.1:%d", port), "127.0.0.1", port, wsPath)
		if err == nil {
			return ws, nil
		}
		if strings.Contains(err.Error(), ErrPermissionBlocked.Error()) {
			return "", err // permission beats continuing the scan
		}
	}
	// Fallback probe of the conventional ports (daemon.py's last resort).
	for _, port := range []int{9222, 9223} {
		if !portLive(port) {
			continue
		}
		ws, err := resolveWSURL(ctx, fmt.Sprintf("http://127.0.0.1:%d", port), "127.0.0.1", port, "")
		if err == nil {
			return ws, nil
		}
		if strings.Contains(err.Error(), ErrPermissionBlocked.Error()) {
			return "", err
		}
	}
	if sawStaleFile {
		return "", fmt.Errorf("%w: a closed browser left a stale DevToolsActivePort file — reopen Chrome with remote debugging enabled (chrome://inspect/#remote-debugging), or run in dedicated/headless mode", ErrNoLiveBrowser)
	}
	return "", fmt.Errorf("%w: no supported Chromium-family browser with remote debugging is running — enable chrome://inspect/#remote-debugging in Chrome, start Chrome with --remote-debugging-port=9222, or use dedicated/headless mode", ErrNoLiveBrowser)
}

func splitHostPort(httpURL string) (string, int) {
	u := strings.TrimPrefix(strings.TrimPrefix(httpURL, "http://"), "https://")
	host, portStr, err := net.SplitHostPort(strings.TrimSuffix(u, "/"))
	if err != nil {
		return u, 80
	}
	port, _ := strconv.Atoi(portStr)
	return host, port
}
