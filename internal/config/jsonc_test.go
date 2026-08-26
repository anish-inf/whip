package config

import (
	"testing"
)

func TestReadWriteJSONRoundTrip(t *testing.T) {
	t.Setenv("WHIP_HOME", t.TempDir())
	type state struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	if err := WriteJSON("state.json", state{Name: "abe", Count: 3}); err != nil {
		t.Fatal(err)
	}
	var got state
	if err := ReadJSON("state.json", &got); err != nil {
		t.Fatal(err)
	}
	if got.Name != "abe" || got.Count != 3 {
		t.Fatalf("round-trip = %+v", got)
	}
}

func TestReadJSONMissingFileErrors(t *testing.T) {
	t.Setenv("WHIP_HOME", t.TempDir())
	var v map[string]any
	if err := ReadJSON("nope.json", &v); err == nil {
		t.Fatal("missing file should be an error the caller treats as empty")
	}
}
