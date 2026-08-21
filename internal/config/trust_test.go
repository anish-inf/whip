package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTrustRoundTrip(t *testing.T) {
	t.Setenv("LOOPY_HOME", t.TempDir())
	dir := "/home/abe/code/loopy"
	if Trusted(dir) {
		t.Fatal("should not be trusted initially")
	}
	if err := Trust(dir); err != nil {
		t.Fatal(err)
	}
	if !Trusted(dir) {
		t.Fatal("should be trusted after Trust()")
	}
	// persists across reads
	if !Trusted(dir) {
		t.Fatal("trust should persist")
	}
	// a different path is not trusted
	if Trusted("/other/path") {
		t.Fatal("unrelated path must not be trusted")
	}
	// file exists in LOOPY_HOME
	home, _ := Dir()
	if _, err := os.Stat(filepath.Join(home, "trusted.json")); err != nil {
		t.Fatalf("trusted.json missing: %v", err)
	}
}
