package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/context-labs/whip/internal/config"
)

// queryTerminalBackground returns whether the terminal's background is light,
// by issuing an OSC 11 query and parsing the reply. It exists because termenv
// refuses to query inside tmux/screen (its termStatusReport short-circuits on
// TERM=screen*/tmux*), where it falls back to a hardcoded dark assumption —
// which is exactly wrong for a tmux user on a light terminal.
//
// Inside tmux the query is wrapped in a DCS passthrough sequence
// (\x1bPtmux;…\x1b\\) so it reaches the real terminal (ghostty, iTerm, …)
// instead of tmux's own (often unrelated) configured background. Requires
// `set -g allow-passthrough on` in tmux ≥3.3; when that's off the query gets
// no reply and we report !ok so the caller falls back to its default.
func queryTerminalBackground(tty *os.File, inTmux bool) bgResult {
	start := time.Now()
	fd := int(tty.Fd())
	if !isForegroundFd(fd) {
		config.LogEvent("theme.query", "skipped: not the foreground process group on the tty")
		return bgResult{}
	}
	query := bgQuery(inTmux)

	// put the tty in raw-ish mode (no echo, non-canonical) for the query
	old, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	if err != nil {
		return bgResult{}
	}
	raw := *old
	raw.Lflag &^= unix.ECHO | unix.ICANON
	// VMIN=0 + VTIME=1 (100ms): read returns 0 bytes when the terminal never
	// replies. os.File.SetReadDeadline does NOT work on /dev/tty (not in the
	// runtime poller on darwin), so without this the reads below block
	// forever and whip hangs at startup (e.g. tmux with allow-passthrough off).
	raw.Cc[unix.VMIN] = 0
	raw.Cc[unix.VTIME] = 1
	if err := unix.IoctlSetTermios(fd, ioctlWriteTermios, &raw); err != nil {
		return bgResult{}
	}
	defer unix.IoctlSetTermios(fd, ioctlWriteTermios, old) //nolint:errcheck // best-effort restore of the original termios on exit

	if _, err := tty.WriteString(query); err != nil {
		return bgResult{}
	}

	// Poll for the reply until the FULL deadline: over ssh the reply
	// round-trips to the user's local terminal, which can take several hundred
	// ms (a relayed tailscale hop easily beats 100ms) — a single quiet VTIME
	// window is NOT "no reply coming". Each read blocks ≤100ms (VMIN=0/VTIME=1),
	// so the loop stays bounded without SetReadDeadline.
	deadline := time.Now().Add(time.Second)
	var buf []byte
	chunk := make([]byte, 256)
	for time.Now().Before(deadline) {
		n, _ := tty.Read(chunk) // 0,err on a quiet window; keep polling
		if n == 0 {
			continue
		}
		buf = append(buf, chunk[:n]...)
		if _, after, ok := strings.Cut(string(buf), "\x1b]11;"); ok {
			// have the OSC reply start; find its terminator
			rest := after
			end := strings.Index(rest, "\x07")
			if j := strings.Index(rest, "\x1b\\"); j >= 0 && (end < 0 || j < end) {
				end = j
			}
			if end < 0 {
				continue // reply not complete yet
			}
			r, g, b, ok := parseOSCBgRGB(rest[:end])
			if !ok {
				config.LogEvent("theme.query", fmt.Sprintf("reply unparseable after %s: %q", time.Since(start).Round(time.Millisecond), rest[:min(end, 60)]))
				return bgResult{}
			}
			config.LogEvent("theme.query", fmt.Sprintf("ok in %s: #%02x%02x%02x (inTmux=%v)", time.Since(start).Round(time.Millisecond), r, g, b, inTmux))
			return bgResult{light: rgbIsLight(r, g, b), valid: true, r: r, g: g, b: b, hasRGB: true}
		}
	}
	config.LogEvent("theme.query", fmt.Sprintf("no OSC 11 reply in %s (inTmux=%v); received %d bytes: %q",
		time.Since(start).Round(time.Millisecond), inTmux, len(buf), truncLine(fmt.Sprintf("%q", buf), 120)))
	return bgResult{}
}

// bgQuery builds the background-color query bytes: an OSC 11 query, then a
// cursor-position report (CSI 6n) as a guaranteed terminator so a terminal
// that ignores OSC 11 still unblocks the read. Inside tmux the OSC 11 is sent
// TWICE: bare — tmux ≥3.4 answers it itself with the client terminal's real
// background, no config needed — and DCS-passthrough-wrapped (every ESC in
// the payload doubled) so the outer terminal answers directly where tmux
// doesn't but `allow-passthrough on` is set. Whichever reply arrives first
// wins; both describe the real terminal. The 6n stays OUTSIDE the wrapper:
// tmux always answers CPR for the pane, so the terminator never depends on
// passthrough.
func bgQuery(inTmux bool) string {
	const osc11 = "\x1b]11;?\x1b\\"
	q := osc11
	if inTmux {
		q += "\x1bPtmux;" + strings.ReplaceAll(osc11, "\x1b", "\x1b\x1b") + "\x1b\\"
	}
	return q + "\x1b[6n"
}

// parseOSCBgRGB parses an OSC 11 payload ("rgb:rrrr/gggg/bbbb" or "#rrggbb")
// into 8-bit RGB components.
func parseOSCBgRGB(payload string) (r, g, b int, ok bool) {
	payload = strings.TrimSpace(payload)
	if after, found := strings.CutPrefix(payload, "rgb:"); found {
		parts := strings.Split(after, "/")
		if len(parts) != 3 {
			return 0, 0, 0, false
		}
		// components are 1–4 hex digits; normalize to 8-bit
		comp := func(s string) int {
			s = strings.TrimRight(s, "\x07")
			// 4 hex digits max => v <= 0xffff, so the int conversion can't
			// overflow; the ParseUint bitSize stays 16 to prove it.
			v, err := strconv.ParseUint(s, 16, 16)
			if err != nil {
				return 0
			}
			maxVal := (1 << (4 * uint(len(s)))) - 1
			if maxVal <= 0 {
				return 0
			}
			return int(v) * 255 / maxVal
		}
		return comp(parts[0]), comp(parts[1]), comp(parts[2]), true
	}
	if strings.HasPrefix(payload, "#") && len(payload) >= 7 {
		v, err := strconv.ParseUint(payload[1:7], 16, 32)
		if err != nil {
			return 0, 0, 0, false
		}
		return int(v>>16) & 0xff, int(v>>8) & 0xff, int(v) & 0xff, true
	}
	return 0, 0, 0, false
}

// rgbIsLight reports whether a color reads as a light background
// (ITU-R BT.601 luma; light backgrounds sit well above the midpoint).
func rgbIsLight(r, g, b int) bool {
	return (299*r+587*g+114*b)/1000 > 128
}

// parseOSCBg parses an OSC 11 payload and reports whether it is a light color.
func parseOSCBg(payload string) bool {
	r, g, b, ok := parseOSCBgRGB(payload)
	return ok && rgbIsLight(r, g, b)
}

// isForegroundFd reports whether the fd is the controlling terminal in the
// foreground (mirrors termenv's isForeground).
func isForegroundFd(fd int) bool {
	pgrp, err := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
	if err != nil {
		return false
	}
	return pgrp == unix.Getpgrp()
}
