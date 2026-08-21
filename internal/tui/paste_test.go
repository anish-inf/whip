package tui

import "testing"

func TestHasImageType(t *testing.T) {
	cases := []struct {
		in      string
		wantExt string
		wantOK  bool
	}{
		{"TARGETS\nimage/png\nUTF8_STRING\n", "png", true},
		{"image/jpeg\n", "jpeg", true},
		{"image/jpg\n", "jpg", true},
		{"image/webp\n", "webp", true},
		{"image/x-custom\n", "x-custom", true}, // unknown subtype kept as ext
		{"TARGETS\nUTF8_STRING\ntext/plain\n", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		ext, ok := hasImageType([]byte(c.in))
		if ok != c.wantOK || (ok && ext != c.wantExt) {
			t.Errorf("hasImageType(%q) = %q,%v want %q,%v", c.in, ext, ok, c.wantExt, c.wantOK)
		}
	}
}
