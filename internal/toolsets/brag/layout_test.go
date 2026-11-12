package brag

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLayoutGate(t *testing.T) {
	_, a, _ := setup(t)
	for _, format := range []string{"vertical", "landscape", "square"} {
		m := testPlan("x")
		m["format"] = format
		b, _ := json.Marshal(m)
		var p planIn
		json.Unmarshal(b, &p)
		r := &run{ID: "x", Dir: t.TempDir()}
		if _, err := a.writeScaffold(r, &p); err != nil {
			t.Fatal(err)
		}
		if lp := layoutProblems(r); len(lp) != 0 {
			t.Fatalf("%s: fresh scaffold should pass: %v", format, lp)
		}
		// A hand-placed scene is flagged.
		f := filepath.Join(r.Dir, "composition", "compositions", "demo.html")
		os.WriteFile(f, []byte(`<template><div id="demo"><h1 style="position:absolute;top:7%">Hi</h1></div></template>`), 0o644)
		if lp := layoutProblems(r); len(lp) != 1 || !strings.Contains(lp[0], "demo.html") {
			t.Errorf("%s: want demo.html flagged, got %v", format, lp)
		}
	}
	// Runs from before the layout system aren't gated.
	old := &run{ID: "old", Dir: t.TempDir()}
	os.MkdirAll(filepath.Join(old.Dir, "composition", "compositions"), 0o755)
	os.WriteFile(filepath.Join(old.Dir, "composition", "index.html"), []byte(`<div data-composition-id="main"></div>`), 0o644)
	os.WriteFile(filepath.Join(old.Dir, "composition", "compositions", "a.html"), []byte(`<template></template>`), 0o644)
	if lp := layoutProblems(old); lp != nil {
		t.Errorf("old run gated: %v", lp)
	}
}
