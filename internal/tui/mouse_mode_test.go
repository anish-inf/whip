package tui

import (
	"os"
	"strings"
	"testing"
	"time"
)

// The mouse output filter must downgrade cell-motion reporting (?1002) to
// click+wheel-only (?1000) so the wheel reaches loopy but drag/motion bytes
// never do — that's what keeps native drag-to-copy working while wheel scroll
// works. SGR (?1006) and other output must pass through untouched.
func TestClickWheelMouseWriter(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	out := clickWheelMouseWriter(w)

	in := "before\x1b[?1002h mid \x1b[?1006h after \x1b[?1002l end"
	if _, err := out.Write([]byte(in)); err != nil {
		t.Fatal(err)
	}
	out.Close()

	buf := make([]byte, 4096)
	_ = r.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, _ := r.Read(buf)
	got := string(buf[:n])

	if strings.Contains(got, "1002") {
		t.Fatalf("cell-motion mode leaked through: %q", got)
	}
	if !strings.Contains(got, "\x1b[?1000h") {
		t.Errorf("click/wheel mode ?1000h missing: %q", got)
	}
	if !strings.Contains(got, "\x1b[?1006h") {
		t.Errorf("SGR mode ?1006h must pass through: %q", got)
	}
	for _, want := range []string{"before", "mid", "after", "end"} {
		if !strings.Contains(got, want) {
			t.Errorf("content %q lost: %q", want, got)
		}
	}
}
