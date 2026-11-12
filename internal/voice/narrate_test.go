package voice

import (
	"math"
	"testing"
)

func TestParse(t *testing.T) {
	p, err := Parse("Twelve products are low. [pause 0.6] One message, {draft} and the order is ready.{done}")
	if err != nil {
		t.Fatal(err)
	}
	want := []Piece{
		{Kind: "text", Text: "Twelve products are low."},
		{Kind: "pause", Pause: 0.6},
		{Kind: "text", Text: "One message,"},
		{Kind: "cue", Cue: "draft"},
		{Kind: "text", Text: "and the order is ready."},
		{Kind: "cue", Cue: "done"},
	}
	if len(p) != len(want) {
		t.Fatalf("got %+v", p)
	}
	for i := range want {
		if p[i] != want[i] {
			t.Errorf("piece %d = %+v, want %+v", i, p[i], want[i])
		}
	}
	if s := Spoken("Low stock. [pause] {go} Order it."); s != "Low stock. Order it." {
		t.Errorf("Spoken = %q", s)
	}
}

func TestParseRejects(t *testing.T) {
	for _, line := range []string{"[pause 9]", "Hello [wait 2] there", "{only-a-cue}", "Hi {bad cue}"} {
		if _, err := Parse(line); err == nil {
			t.Errorf("Parse(%q) should fail", line)
		}
	}
	if p, _ := Parse("Hold on [pause] now"); p[1].Pause != 0.5 {
		t.Errorf("bare [pause] = %v, want 0.5", p[1].Pause)
	}
}

func TestNaturalGap(t *testing.T) {
	cases := map[string]float64{"Done.": gapSentence, "Really?": gapSentence, "First,": gapClause, "and then": gapPhrase}
	for in, want := range cases {
		if got := naturalGap(in); got != want {
			t.Errorf("naturalGap(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestTrim(t *testing.T) {
	s := make([]int16, SampleRate) // 1 s: silence, 0.4 s of tone in the middle, silence
	for i := SampleRate * 3 / 10; i < SampleRate*7/10; i++ {
		s[i] = int16(8000 * math.Sin(float64(i)/8))
	}
	got := float64(len(trim(s))) / SampleRate
	if got < 0.4 || got > 0.52 {
		t.Errorf("trimmed to %.3fs, want ≈0.4s plus a little air", got)
	}
	if trim(make([]int16, 1000)) != nil {
		t.Error("pure silence should trim to nothing")
	}
}

func TestEstimateSeconds(t *testing.T) {
	plain := EstimateSeconds("one two three four five six seven eight nine ten eleven twelve thirteen", 1)
	if plain < 4.5 || plain > 5.5 {
		t.Errorf("13 words ≈ %.2fs, want ≈5s", plain)
	}
	if paused := EstimateSeconds("one two. [pause 1] three", 1); paused < 1.9 {
		t.Errorf("explicit pause not counted: %.2f", paused)
	}
}
