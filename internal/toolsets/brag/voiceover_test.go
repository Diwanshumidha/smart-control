package brag

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Diwanshumidha/smart-control/internal/config"
	"github.com/Diwanshumidha/smart-control/internal/voice"
)

// fakeVoice "speaks" 0.3 s of tone per word, so timings are predictable.
type fakeVoice struct{}

func (fakeVoice) Synthesize(ctx context.Context, req voice.Request) error {
	d := 0.3 * float64(len(strings.Fields(req.Text)))
	return exec.CommandContext(ctx, "ffmpeg", "-v", "error", "-y", "-f", "lavfi", "-i",
		fmt.Sprintf("sine=frequency=300:duration=%.2f", d), "-ar", "24000", "-ac", "1", req.Out).Run()
}

func init() {
	voice.Register(voice.ProviderInfo{Name: "fake-test", Title: "Fake", DefaultVoice: "v1", Voices: []voice.Voice{{ID: "v1"}}},
		func(voice.Settings) (voice.Provider, error) { return fakeVoice{}, nil }, nil)
}

const voTestIndex = `<!doctype html>
<html><body>
<div id="root" data-composition-id="main" data-start="0" data-duration="12" data-width="1920" data-height="1080">
  <div id="hook" data-composition-id="hook" data-composition-src="compositions/hook.html" data-start="0" data-duration="3" data-track-index="1"></div>
  <div id="demo" data-composition-id="demo" data-composition-src="compositions/demo.html" data-start="3" data-duration="5" data-track-index="1"></div>
  <div id="outro" data-composition-id="outro" data-composition-src="compositions/outro.html" data-start="8" data-duration="4" data-track-index="1"></div>
  <audio id="music" class="clip" data-start="0" data-duration="12" data-track-index="10" data-volume="0.3" src="assets/music.mp3"></audio>
</div>
</body></html>`

func TestVoiceover(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	_, a, _ := setup(t)
	a.cfg = &config.Config{DataDir: t.TempDir(), Voice: config.Voice{Provider: "fake-test", Voice: "v1", Speed: 1}}
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "composition"), 0o755)
	os.WriteFile(filepath.Join(dir, "composition", "index.html"), []byte(voTestIndex), 0o644)
	r := &run{ID: "run-vo", Dir: dir, Stage: stageBuilding, Created: time.Now()}
	a.addRun(r)

	in := voiceoverIn{RunID: r.ID, WaitSeconds: 60, Lines: []voLine{
		{Scene: "hook", Text: "Twelve products are low."},                                                                       // 4 words: 1.2 s
		{Scene: "demo", Text: "One message [pause 0.5] {draft} drafts the whole order for you, ready to send right now today."}, // 2 + 12 words
	}}
	res, _, err := a.toolVoiceover(context.Background(), nil, in)
	if err != nil {
		t.Fatal(err)
	}
	report := res.Content[0].(*mcp.TextContent).Text
	t.Log(report)

	for _, want := range []string{"hook", "{draft} @ ", "PROBLEMS", `"demo" ends only 0.05s`, "lower it to about 0.12"} {
		if !strings.Contains(report, want) {
			t.Errorf("report missing %q", want)
		}
	}
	html, _ := os.ReadFile(filepath.Join(dir, "composition", "index.html"))
	if strings.Count(string(html), "<audio id=\"vo-") != 2 || !strings.Contains(string(html), `id="vo-hook" class="clip" data-start="0.25"`) {
		t.Errorf("voice block not wired:\n%s", html)
	}
	var meta struct {
		Lines []placedLine `json:"lines"`
	}
	b, _ := os.ReadFile(filepath.Join(dir, "voiceover.json"))
	if err := json.Unmarshal(b, &meta); err != nil || len(meta.Lines) != 2 {
		t.Fatalf("voiceover.json: %v %s", err, b)
	}
	demo := meta.Lines[1]
	if demo.Start != 3.25 || len(demo.Cues) != 1 || demo.Cues[0].At != demo.Spans[1].Start {
		t.Errorf("demo line placement: %+v", demo)
	}
	if gap := demo.Spans[1].Start - demo.Spans[0].End; gap < 0.49 || gap > 0.51 {
		t.Errorf("explicit pause is %.3fs", gap)
	}

	// Re-running replaces the block instead of adding a second one.
	in.Lines = in.Lines[:1]
	if _, _, err := a.toolVoiceover(context.Background(), nil, in); err != nil {
		t.Fatal(err)
	}
	html, _ = os.ReadFile(filepath.Join(dir, "composition", "index.html"))
	if strings.Count(string(html), "voiceover:begin") != 1 || strings.Contains(string(html), "vo-demo") {
		t.Errorf("re-run should replace the block:\n%s", html)
	}
	if _, err := os.Stat(filepath.Join(dir, "composition", "assets", "voice", "demo.wav")); err == nil {
		t.Error("demo.wav should be pruned once no line uses it")
	}
}

func TestPlanNarrationMustFit(t *testing.T) {
	_, a, _ := setup(t)
	a.cfg = &config.Config{Voice: config.Voice{Provider: "fake-test", Voice: "v1", Speed: 1}}
	plan := func(hook string) []string {
		m := testPlan("x")
		m["scenes"].([]map[string]any)[0]["narration"] = hook
		b, _ := json.Marshal(m)
		var p planIn
		json.Unmarshal(b, &p)
		errs, _ := a.validatePlan(context.Background(), &p)
		return errs
	}
	has := func(errs []string, sub string) bool {
		for _, e := range errs {
			if strings.Contains(e, sub) {
				return true
			}
		}
		return false
	}
	// The hook is 3 s: 2.4 s of room after the lead and tail.
	if errs := plan("Horses deserve love too."); has(errs, "narration") || has(errs, "scene \"hook\"") {
		t.Errorf("short line rejected: %v", errs)
	}
	if errs := plan("Every single horse in the whole wide world deserves somebody who will swipe right on them."); !has(errs, "takes about") {
		t.Errorf("long line accepted: %v", errs)
	}
	if errs := plan("Horses [wait] too."); !has(errs, "narration") {
		t.Errorf("bad marker accepted: %v", errs)
	}
}
