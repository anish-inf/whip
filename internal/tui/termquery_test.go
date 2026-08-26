package tui

import (
	"strings"
	"testing"
)

// bgQuery outside tmux: one bare OSC 11 plus the CSI 6n terminator.
func TestBgQueryPlain(t *testing.T) {
	if got := bgQuery(false); got != "\x1b]11;?\x1b\\\x1b[6n" {
		t.Fatalf("plain query wrong: %q", got)
	}
}

// bgQuery inside tmux: the OSC 11 goes out twice — bare (tmux ≥3.4 answers it
// itself) and DCS-passthrough-wrapped with doubled ESCs (for allow-passthrough
// setups) — and the CSI 6n terminator stays OUTSIDE the wrapper so tmux always
// answers it and the read never blocks on passthrough being off.
func TestBgQueryTmux(t *testing.T) {
	got := bgQuery(true)
	if !strings.HasPrefix(got, "\x1b]11;?\x1b\\") {
		t.Fatalf("must start with a bare OSC 11 for tmux itself to answer: %q", got)
	}
	if !strings.Contains(got, "\x1bPtmux;\x1b\x1b]11;?\x1b\x1b\\\x1b\\") {
		t.Fatalf("must include the passthrough-wrapped OSC 11 (ESCs doubled): %q", got)
	}
	if !strings.HasSuffix(got, "\x1b\\\x1b[6n") {
		t.Fatalf("CSI 6n terminator must be last and unwrapped: %q", got)
	}
}

// When no terminal query succeeded, the scheme comes from COLORFGBG or stays
// neutral. Inside tmux the ONLY acceptable outcomes are COLORFGBG or neutral —
// never an assumed dark. (The regression: termenv's fallback can't query
// through tmux and silently assumes dark, which painted the dark palette on
// light terminals.)
func TestFallbackScheme(t *testing.T) {
	// COLORFGBG wins when parseable ("fg;bg", bg 7+ = light)
	if light, ok, _ := fallbackScheme(true, "0;15"); !ok || !light {
		t.Fatal("COLORFGBG 0;15 must resolve light")
	}
	if light, ok, _ := fallbackScheme(false, "15;0"); !ok || light {
		t.Fatal("COLORFGBG 15;0 must resolve dark")
	}
	// unparseable or missing → not-ok (caller goes neutral), with a hint
	// naming the actual remedy per environment
	if _, ok, how := fallbackScheme(true, ""); ok || !strings.Contains(how, "tmux") {
		t.Fatalf("tmux, no signal: must be neutral with a tmux hint, got ok=%v how=%q", ok, how)
	}
	if _, ok, how := fallbackScheme(false, "junk"); ok || !strings.Contains(how, "undetermined") {
		t.Fatalf("no signal: must be neutral, got ok=%v how=%q", ok, how)
	}
}
