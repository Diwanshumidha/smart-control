package brag

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScreenshotGate(t *testing.T) {
	_, a, _ := setup(t)
	r := &run{ID: "shots", Dir: t.TempDir(), Stage: stageBuilding}
	writePlan := func(requested string) {
		m := testPlan(r.ID)
		m["screenshots_requested"] = requested
		b, _ := json.Marshal(m)
		os.WriteFile(filepath.Join(r.Dir, "plan.json"), b, 0o644)
	}
	png := []byte("\x89PNG\r\n\x1a\nfake capture bytes")
	os.MkdirAll(filepath.Join(r.Dir, "capture"), 0o755)
	os.WriteFile(filepath.Join(r.Dir, "capture", "app-430x932@2x.png"), png, 0o644)
	comp := filepath.Join(r.Dir, "composition")
	os.MkdirAll(filepath.Join(comp, "compositions"), 0o755)
	os.MkdirAll(filepath.Join(comp, "assets"), 0o755)
	os.WriteFile(filepath.Join(comp, "index.html"), []byte(`<div data-composition-id="main"></div>`), 0o644)
	scene := filepath.Join(comp, "compositions", "demo.html")

	writePlan("")
	os.WriteFile(scene, []byte(`<template><div class="lay"><h1 class="lay-caption">Rebuilt</h1></div></template>`), 0o644)
	if uses := a.screenshotUses(r); uses != nil {
		t.Fatalf("rebuilt scene flagged: %v", uses)
	}

	// A renamed copy, a direct link and a base64 paste are all caught.
	os.WriteFile(filepath.Join(comp, "assets", "screen.png"), png, 0o644)
	os.WriteFile(scene, []byte(`<template><img src="assets/screen.png"><img src="../capture/app-430x932@2x.png"><img src="data:image/png;base64,`+base64.StdEncoding.EncodeToString(png)+`"></template>`), 0o644)
	uses := a.screenshotUses(r)
	all := strings.Join(uses, "\n")
	for _, want := range []string{"assets/screen.png (a copy of capture app-430x932@2x.png)", "../capture/app-430x932@2x.png", "pasted into the HTML as base64"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in %v", want, uses)
		}
	}
	if err := a.requireReviewed(r); err == nil || !strings.Contains(err.Error(), "didn't ask for them") {
		t.Errorf("final render should be blocked: %v", err)
	}

	// Allowed once the user asked.
	writePlan("Use the real app screenshots, please.")
	if uses := a.screenshotUses(r); uses != nil {
		t.Errorf("requested screenshots flagged: %v", uses)
	}
}

func TestPlanRejectsUnrequestedScreenshots(t *testing.T) {
	_, a, _ := setup(t)
	validate := func(requested string) []string {
		m := testPlan("x")
		m["scenes"].([]map[string]any)[1]["visual"] = "The real Ledgers screenshot inside a phone frame"
		m["screenshots_requested"] = requested
		b, _ := json.Marshal(m)
		var p planIn
		json.Unmarshal(b, &p)
		errs, _ := a.validatePlan(context.Background(), &p)
		return errs
	}
	if errs := validate(""); !strings.Contains(strings.Join(errs, "\n"), "didn't ask for screenshots") {
		t.Errorf("unrequested screenshot accepted: %v", errs)
	}
	if errs := validate("show screenshots"); strings.Contains(strings.Join(errs, "\n"), "screenshot") {
		t.Errorf("requested screenshot rejected: %v", errs)
	}
}
