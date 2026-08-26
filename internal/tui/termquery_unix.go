package tui

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
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
func queryTerminalBackground(tty *os.File, inTmux bool) (light, ok bool) {
	fd := int(tty.Fd())
	if !isForegroundFd(fd) {
		return false, false
	}
	query := bgQuery(inTmux)

	// put the tty in raw-ish mode (no echo, non-canonical) for the query
	old, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	if err != nil {
		return false, false
	}
	raw := *old
	raw.Lflag &^= unix.ECHO | unix.ICANON
	// VMIN=0 + VTIME=1 (100ms): read returns 0 bytes when the terminal never
	// replies. os.File.SetReadDeadline does NOT work on /dev/tty (not in the
	// runtime poller on darwin), so without this the ReadByte below blocks
	// forever and whip hangs at startup (e.g. tmux with allow-passthrough off).
	raw.Cc[unix.VMIN] = 0
	raw.Cc[unix.VTIME] = 1
	if err := unix.IoctlSetTermios(fd, ioctlWriteTermios, &raw); err != nil {
		return false, false
	}
	defer unix.IoctlSetTermios(fd, ioctlWriteTermios, old) //nolint:errcheck // best-effort restore of the original termios on exit

	if _, err := tty.WriteString(query); err != nil {
		return false, false
	}

	// read replies with a deadline; the OSC 11 reply looks like
	// "\x1b]11;rgb:RRRR/GGGG/BBBB\x1b\\" (or BEL-terminated)
	_ = tty.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	defer tty.SetReadDeadline(time.Time{}) //nolint:errcheck // best-effort deadline clear before the fd is handed back
	r := bufio.NewReader(tty)
	var buf strings.Builder
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		b, err := r.ReadByte()
		if err != nil {
			break
		}
		buf.WriteByte(b)
		s := buf.String()
		if _, after, ok := strings.Cut(s, "\x1b]11;"); ok {
			// have the OSC reply start; find its terminator
			rest := after
			end := strings.Index(rest, "\x07")
			if j := strings.Index(rest, "\x1b\\"); j >= 0 && (end < 0 || j < end) {
				end = j
			}
			if end < 0 {
				continue // reply not complete yet
			}
			return parseOSCBg(rest[:end]), true
		}
		if len(s) > 128 { // no OSC 11 reply coming (e.g. passthrough off)
			break
		}
	}
	return false, false
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

// parseOSCBg parses an OSC 11 payload ("rgb:rrrr/gggg/bbbb" or "#rrggbb") and
// reports whether it is a light color, by relative luminance.
func parseOSCBg(payload string) bool {
	payload = strings.TrimSpace(payload)
	var r, g, b int
	if after, ok := strings.CutPrefix(payload, "rgb:"); ok {
		parts := strings.Split(after, "/")
		if len(parts) != 3 {
			return false
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
		r, g, b = comp(parts[0]), comp(parts[1]), comp(parts[2])
	} else if strings.HasPrefix(payload, "#") && len(payload) >= 7 {
		v, err := strconv.ParseUint(payload[1:7], 16, 32)
		if err != nil {
			return false
		}
		r, g, b = int(v>>16)&0xff, int(v>>8)&0xff, int(v)&0xff
	} else {
		return false
	}
	// ITU-R BT.601 luma; light backgrounds sit well above the midpoint
	return (299*r+587*g+114*b)/1000 > 128
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
