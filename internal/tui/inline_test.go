package tui

import (
	"strings"
	"testing"
)

// The transcript must render inline (no alt-screen) so native drag-to-copy
// works everywhere: no mouse capture by default, no alt-screen escape in the
// program options, and the view still assembles header + transcript + input.
func TestInlineRendering(t *testing.T) {
	m := compactCmdModel()
	m.Update(mkWinSize(80, 30))
	m.appendAssistant("hello **world**")
	v := m.View()
	if strings.Contains(v, "\x1b[?1049h") || strings.Contains(v, "\x1b[?47h") {
		t.Fatal("view must not enter the alternate screen")
	}
	for _, want := range []string{"loopy ·", "hello", "world"} {
		if !strings.Contains(stripAll(v), want) {
			t.Errorf("inline view missing %q", want)
		}
	}
	// and mouse stays off by default
	if m.mouseOn {
		t.Fatal("mouse capture must default off for native selection")
	}
}

func stripAll(s string) string {
	out := strings.Builder{}
	i := 0
	for i < len(s) {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' && s[i] != 'h' && s[i] != 'l' {
				i++
			}
			i++
			continue
		}
		out.WriteByte(s[i])
		i++
	}
	return out.String()
}
