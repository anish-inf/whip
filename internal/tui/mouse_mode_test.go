package tui

import (
	"os"
	"strings"
	"testing"
	"time"
)

// enableClickWheelMouse must emit click+wheel reporting (?1000) with SGR coords
// (?1006) and crucially NOT motion reporting (?1002/?1003) — motion makes the
// terminal/tmux forward drags to the app, killing native drag-to-copy. And it
// must write to the real TTY (not a pipe) so bubbletea's terminal-size detection
// keeps working (piping output collapses width/height to 0 — the regression).
func TestClickWheelMouseEscapes(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	enableClickWheelMouse(w)
	w.Close()
	buf := make([]byte, 256)
	_ = r.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _ := r.Read(buf)
	got := string(buf[:n])

	if !strings.Contains(got, "\x1b[?1000h") {
		t.Errorf("must enable click/wheel ?1000h, got %q", got)
	}
	if !strings.Contains(got, "\x1b[?1006h") {
		t.Errorf("must enable SGR coords ?1006h, got %q", got)
	}
	if strings.Contains(got, "1002") || strings.Contains(got, "1003") {
		t.Errorf("must NOT enable motion reporting (?1002/?1003), got %q", got)
	}
}

// disableClickWheelMouse must release exactly the modes enableClickWheelMouse set.
func TestDisableClickWheelMouse(t *testing.T) {
	r, w, _ := os.Pipe()
	disableClickWheelMouse(w)
	w.Close()
	buf := make([]byte, 256)
	_ = r.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _ := r.Read(buf)
	got := string(buf[:n])
	if !strings.Contains(got, "\x1b[?1000l") || !strings.Contains(got, "\x1b[?1006l") {
		t.Errorf("must release ?1000 and ?1006, got %q", got)
	}
}
