package kokoro

import (
	"context"
	"math"
	"os"
	"testing"

	"github.com/Diwanshumidha/smart-control/internal/config"
	"github.com/Diwanshumidha/smart-control/internal/voice"
)

// TestLive installs the voice into a temp data dir and speaks a line with a
// pause and a cue. It downloads ~250 MB, so it only runs with
// SMART_CONTROL_VOICE_LIVE=1 (set SMART_CONTROL_VOICE_DATA to reuse a dir).
func TestLive(t *testing.T) {
	if os.Getenv("SMART_CONTROL_VOICE_LIVE") == "" {
		t.Skip("set SMART_CONTROL_VOICE_LIVE=1 to run")
	}
	ctx := context.Background()
	data := os.Getenv("SMART_CONTROL_VOICE_DATA")
	if data == "" {
		data = t.TempDir()
	}
	s := voice.Settings{DataDir: data, Voice: config.Voice{Provider: name, Voice: "af_heart", Speed: 1}}
	if ok, _ := probe(s); !ok {
		if err := Install(ctx, data, os.Stdout); err != nil {
			t.Fatal(err)
		}
	}
	p, _, err := voice.New(s)
	if err != nil {
		t.Fatal(err)
	}
	out := t.TempDir() + "/line.wav"
	take, err := voice.Narrate(ctx, p, name, "Twelve products are running low. [pause 0.8] One message, {draft} and the order is drafted.", "af_heart", 1, out, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("take: %+v", take)
	if len(take.Spans) != 3 || len(take.Cues) != 1 {
		t.Fatalf("want 3 spans and 1 cue, got %+v", take)
	}
	if gap := take.Spans[1].Start - take.Spans[0].End; math.Abs(gap-0.8) > 0.01 {
		t.Errorf("pause is %.3fs, want 0.8s", gap)
	}
	if take.Cues[0].At != take.Spans[2].Start {
		t.Errorf("cue at %.3f, want the start of %q at %.3f", take.Cues[0].At, take.Spans[2].Text, take.Spans[2].Start)
	}
	if take.Spans[0].Start != 0 || take.Duration < 3 {
		t.Errorf("take should start on the first word and run a few seconds: %+v", take)
	}
}
