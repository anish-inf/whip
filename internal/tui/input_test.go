package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// keyRunes builds the KeyMsg bubbletea would produce for an unknown sequence
// whose String() renders as s.
func keyRunes(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

func TestIsShiftEnterSeq(t *testing.T) {
	for in, want := range map[string]bool{
		// rendered forms of unknownCSISequenceMsg (see bubbletea key.go)
		"unknown csi sequence: 0x1b, '[', '1', '3', ';', '2', 'u'":                     true,  // CSI u
		"unknown csi sequence: 0x1b, '[', '2', '7', ';', '2', ';', '1', '3', '~'":      true,  // modifyOtherKeys
		"unknown csi sequence: 0x1b, '[', 'five', 'seven', 'four', 'four', 'one', 'u'": true,  // kitty 57441u
		"unknown csi sequence: 0x1b, '[', '1', ';', '2', 'A'":                          false, // shift+up
		"a":     false,
		"enter": false,
	} {
		if got := isShiftEnterSeq(keyRunes(in)); got != want {
			t.Errorf("isShiftEnterSeq(%q) = %v, want %v", in, got, want)
		}
	}
}
