package tui

import (
	"os"
	"strings"
	"testing"
	"time"
)

// enableClickWheelMouse must emit button-motion tracking (?1002) with SGR
// coords (?1006) so a held left-drag reports motion events — whip turns those
// into its own selection (select.go), because terminals suppress native
// drag-to-copy once any mouse mode is on. ?1002 is a superset of ?1000
// (press/release/wheel still report). ?1002, not ?1003: motion bytes only
// flow while a button is held.
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

	if !strings.Contains(got, "\x1b[?1006h") {
		t.Errorf("must enable SGR coords ?1006h, got %q", got)
	}
	if !strings.Contains(got, "\x1b[?1002h") {
		t.Errorf("must enable button-motion ?1002h so a drag reports motion (in-app selection), got %q", got)
	}
	// THE mac drag-to-copy regression: terminals keep a single mouse-tracking
	// mode, so writing ?1000h anywhere after ?1002h downgrades tracking to
	// click-only — drags stop reporting motion, and selection never starts.
	if strings.Contains(got, "\x1b[?1000h") {
		t.Errorf("must NOT enable ?1000h (it downgrades ?1002 button-motion tracking), got %q", got)
	}
	if strings.Contains(got, "1003") {
		t.Errorf("must NOT enable any-motion ?1003 (passive moves stay silent), got %q", got)
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
	if !strings.Contains(got, "\x1b[?1000l") || !strings.Contains(got, "\x1b[?1006l") || !strings.Contains(got, "\x1b[?1002l") {
		t.Errorf("must release ?1000, ?1002 and ?1006, got %q", got)
	}
}
