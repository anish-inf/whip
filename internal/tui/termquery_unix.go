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
func queryTerminalBackground(tty *os.File, inTmux bool) (light bool, ok bool) {
	fd := int(tty.Fd())
	if !isForegroundFd(fd) {
		return false, false
	}
	// query the background color (OSC 11), then a cursor-position report (CSI
	// 6n) as a guaranteed terminator so a terminal that ignores OSC 11 still
	// unblocks the read.
	query := "\x1b]11;?\x1b\\" + "\x1b[6n"
	if inTmux {
		// DCS passthrough: wrap the query, doubling every ESC in the payload.
		query = "\x1bPtmux;" + strings.ReplaceAll(query, "\x1b", "\x1b\x1b") + "\x1b\\"
	}

	// put the tty in raw-ish mode (no echo, non-canonical) for the query
	old, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	if err != nil {
		return false, false
	}
	raw := *old
	raw.Lflag &^= unix.ECHO | unix.ICANON
	if err := unix.IoctlSetTermios(fd, ioctlWriteTermios, &raw); err != nil {
		return false, false
	}
	defer unix.IoctlSetTermios(fd, ioctlWriteTermios, old) //nolint:errcheck

	if _, err := tty.WriteString(query); err != nil {
		return false, false
	}

	// read replies with a deadline; the OSC 11 reply looks like
	// "\x1b]11;rgb:RRRR/GGGG/BBBB\x1b\\" (or BEL-terminated)
	_ = tty.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	defer tty.SetReadDeadline(time.Time{}) //nolint:errcheck
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
		if i := strings.Index(s, "\x1b]11;"); i >= 0 {
			// have the OSC reply start; find its terminator
			rest := s[i+len("\x1b]11;"):]
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

// parseOSCBg parses an OSC 11 payload ("rgb:rrrr/gggg/bbbb" or "#rrggbb") and
// reports whether it is a light color, by relative luminance.
func parseOSCBg(payload string) bool {
	payload = strings.TrimSpace(payload)
	var r, g, b int
	if strings.HasPrefix(payload, "rgb:") {
		parts := strings.Split(strings.TrimPrefix(payload, "rgb:"), "/")
		if len(parts) != 3 {
			return false
		}
		// components are 1–4 hex digits; normalize to 8-bit
		comp := func(s string) int {
			v, err := strconv.ParseUint(strings.TrimRight(s, "\x07"), 16, 32)
			if err != nil {
				return 0
			}
			max := (1 << (4 * uint(len(strings.TrimRight(s, "\x07"))))) - 1
			if max <= 0 {
				return 0
			}
			return int(v) * 255 / max
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
