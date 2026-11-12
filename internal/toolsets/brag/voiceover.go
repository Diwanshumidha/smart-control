package brag

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Diwanshumidha/smart-control/internal/config"
	"github.com/Diwanshumidha/smart-control/internal/voice"
)

// Where a line sits inside its scene: it starts voiceLead after the scene
// starts and should stop voiceTail before the cut, so the picture settles
// before the voice and the cut doesn't clip a word.
const (
	voiceLead   = 0.25
	voiceTail   = 0.35
	voiceMinGap = 0.3 // silence the ear needs between two lines
	voiceTrack  = 11
	voiceDir    = "composition/assets/voice"
	voBegin     = "<!-- voiceover:begin (written by the voiceover tool: run it again instead of editing) -->"
	voEnd       = "<!-- voiceover:end -->"
)

type voLine struct {
	ID     string   `json:"id,omitempty" jsonschema:"slug for the file assets/voice/<id>.wav; defaults to the scene id"`
	Scene  string   `json:"scene" jsonschema:"scene id the line plays in (its window comes from composition/index.html)"`
	Text   string   `json:"text" jsonschema:"what is said. [pause 0.4] inserts exact silence; {cue-name} marks the word a visual action lands on"`
	Offset *float64 `json:"offset,omitempty" jsonschema:"seconds after the scene starts that the voice begins (default 0.25)"`
}

type voiceoverIn struct {
	RunID       string   `json:"run_id"`
	Lines       []voLine `json:"lines,omitempty" jsonschema:"omit to voice every scene's narration from the approved plan"`
	Voice       string   `json:"voice,omitempty" jsonschema:"voice id; default: the plan's voice, then the dashboard default"`
	Speed       float64  `json:"speed,omitempty" jsonschema:"speaking rate 0.7-1.3; default from the dashboard (1.0)"`
	ListVoices  bool     `json:"list_voices,omitempty" jsonschema:"only list the available voices"`
	JobID       string   `json:"job_id,omitempty" jsonschema:"job_id from an earlier voiceover call that was still running"`
	WaitSeconds int      `json:"wait_seconds,omitempty" jsonschema:"default 50"`
}

// voJob is an in-process voiceover run (several TTS calls plus mixing).
type voJob struct {
	id    string
	runID string
	done  chan struct{}

	mu       sync.Mutex
	progress string
	report   string
	err      error
}

type voJobs struct {
	mu  sync.Mutex
	seq int
	m   map[string]*voJob
}

func (j *voJob) set(p string) {
	j.mu.Lock()
	j.progress = p
	j.mu.Unlock()
}

func (a *app) voiceConfig() config.Voice {
	if a.cfg == nil {
		return config.Voice{Provider: "kokoro", Voice: "af_heart", Speed: 1}
	}
	return a.cfg.Voice
}

func (a *app) voiceSettings() voice.Settings {
	s := voice.Settings{Voice: a.voiceConfig()}
	if a.cfg != nil {
		s.DataDir = a.cfg.DataDir
	}
	return s
}

func voiceIDs(info voice.ProviderInfo) string {
	var ids []string
	for _, v := range info.Voices {
		ids = append(ids, v.ID)
	}
	return strings.Join(ids, ", ")
}

func voiceList(info voice.ProviderInfo, def string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Voices from %s (default %s):\n", info.Title, def)
	for _, v := range info.Voices {
		fmt.Fprintf(&b, "  %-12s %-9s %-6s %s\n", v.ID, v.Name, v.Lang, v.Note)
	}
	return b.String()
}

func (a *app) toolVoiceover(_ context.Context, _ *mcp.CallToolRequest, in voiceoverIn) (*mcp.CallToolResult, any, error) {
	settings := a.voiceSettings()
	info, infoErr := voice.Info(settings.Voice.Provider)
	if in.ListVoices {
		if infoErr != nil {
			return nil, nil, infoErr
		}
		return text(voiceList(info, settings.Voice.Voice)), nil, nil
	}
	if in.JobID != "" {
		return a.waitVoiceover(in.JobID, in.WaitSeconds)
	}
	r, err := a.getRun(in.RunID)
	if err != nil {
		return nil, nil, err
	}
	if err := a.requireBuilding(r); err != nil {
		return nil, nil, err
	}
	if j := a.runningVoiceover(r.ID); j != nil {
		return nil, nil, fmt.Errorf("a voiceover is already running for this run (job %s); call voiceover with job_id=%q", j.id, j.id)
	}
	plan, _ := a.readPlan(r)
	lines := in.Lines
	if len(lines) == 0 {
		if plan == nil {
			return nil, nil, fmt.Errorf("no lines given and no plan.json to read narration from")
		}
		for _, s := range plan.Scenes {
			if n := strings.TrimSpace(s.Narration); n != "" {
				lines = append(lines, voLine{ID: s.ID, Scene: s.ID, Text: n})
			}
		}
		if len(lines) == 0 {
			return nil, nil, fmt.Errorf("the approved plan has no narration; pass lines (scene, text) or add narration with the user's OK")
		}
	}
	seen := map[string]bool{}
	for i := range lines {
		l := &lines[i]
		if l.ID == "" {
			l.ID = l.Scene
		}
		if !slugRe.MatchString(l.ID) {
			return nil, nil, fmt.Errorf("line %d id %q must be a lowercase slug", i+1, l.ID)
		}
		if seen[l.ID] {
			return nil, nil, fmt.Errorf("two lines have id %q; give each its own id", l.ID)
		}
		seen[l.ID] = true
		if _, err := voice.Parse(l.Text); err != nil {
			return nil, nil, fmt.Errorf("line %q: %w", l.ID, err)
		}
	}
	if infoErr != nil {
		return nil, nil, infoErr
	}
	voiceID := in.Voice
	if voiceID == "" && plan != nil {
		voiceID = plan.Voice
	}
	if voiceID == "" {
		voiceID = settings.Voice.Voice
	}
	if voiceID == "" {
		voiceID = info.DefaultVoice
	}
	if !info.HasVoice(voiceID) {
		return nil, nil, fmt.Errorf("voice %q isn't offered by %s; pick one of: %s", voiceID, info.Title, voiceIDs(info))
	}
	speed := in.Speed
	if speed == 0 {
		speed = settings.Voice.Speed
	}
	if speed < 0.7 || speed > 1.3 {
		return nil, nil, fmt.Errorf("speed must be 0.7-1.3 (got %.2f): above that speech blurs, below it drags", speed)
	}
	prov, _, err := voice.New(settings)
	if err != nil {
		return nil, nil, err
	}
	windows, film, err := a.sceneWindows(r, plan)
	if err != nil {
		return nil, nil, err
	}
	for _, l := range lines {
		if _, ok := windows[l.Scene]; !ok {
			return nil, nil, fmt.Errorf("line %q: no scene %q in composition/index.html or the plan (have %s)", l.ID, l.Scene, strings.Join(sceneNames(windows), ", "))
		}
	}

	j := a.newVoiceJob(r.ID)
	go func() {
		defer close(j.done)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		report, err := a.buildVoiceover(ctx, r, j, prov, info, voiceID, speed, lines, windows, film)
		j.mu.Lock()
		j.report, j.err = report, err
		j.mu.Unlock()
	}()
	return a.waitVoiceover(j.id, in.WaitSeconds)
}

func (a *app) newVoiceJob(runID string) *voJob {
	a.voice.mu.Lock()
	defer a.voice.mu.Unlock()
	if a.voice.m == nil {
		a.voice.m = map[string]*voJob{}
	}
	a.voice.seq++
	j := &voJob{id: fmt.Sprintf("voice-%d", a.voice.seq), runID: runID, done: make(chan struct{}), progress: "starting"}
	a.voice.m[j.id] = j
	return j
}

func (a *app) runningVoiceover(runID string) *voJob {
	a.voice.mu.Lock()
	defer a.voice.mu.Unlock()
	for _, j := range a.voice.m {
		select {
		case <-j.done:
		default:
			if j.runID == runID {
				return j
			}
		}
	}
	return nil
}

func (a *app) waitVoiceover(id string, wait int) (*mcp.CallToolResult, any, error) {
	a.voice.mu.Lock()
	j := a.voice.m[id]
	a.voice.mu.Unlock()
	if j == nil {
		return nil, nil, fmt.Errorf("unknown voiceover job %q", id)
	}
	if wait <= 0 {
		wait = 50
	}
	select {
	case <-j.done:
	case <-time.After(waitDur(wait)):
		j.mu.Lock()
		p := j.progress
		j.mu.Unlock()
		return text(fmt.Sprintf("voiceover still running (%s; the first line downloads the voice model). Call voiceover again with job_id=%q.", p, j.id)), nil, nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.err != nil {
		return nil, nil, j.err
	}
	return text(j.report), nil, nil
}

// readPlan loads the run's plan.json, or nil if there is none.
func (a *app) readPlan(r *run) (*planIn, error) {
	b, err := os.ReadFile(filepath.Join(r.Dir, "plan.json"))
	if err != nil {
		return nil, err
	}
	var p planIn
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

type window struct{ Start, End float64 }

var (
	compTagRe = regexp.MustCompile(`<[a-zA-Z][^>]*\bdata-composition-id="([^"]+)"[^>]*>`)
	attrRe    = func(name string) *regexp.Regexp { return regexp.MustCompile(`\b` + name + `="([^"]*)"`) }
	startRe   = attrRe("data-start")
	durRe     = attrRe("data-duration")
	idRe      = regexp.MustCompile(`\sid="([^"]*)"`)
	mainTagRe = regexp.MustCompile(`<[a-zA-Z][^>]*\bdata-composition-id="main"[^>]*>`)
	voBlockRe = regexp.MustCompile(`(?s)\n?[ \t]*<!-- voiceover:begin.*?<!-- voiceover:end -->`)
	musicRe   = regexp.MustCompile(`<audio[^>]*\bid="music"[^>]*>`)
	volumeRe  = attrRe("data-volume")
)

// sceneWindows reads each scene's start and end from composition/index.html
// (what will actually render), falling back to the plan's timings.
func (a *app) sceneWindows(r *run, plan *planIn) (map[string]window, float64, error) {
	out := map[string]window{}
	film := 0.0
	if plan != nil {
		t := 0.0
		for _, s := range plan.Scenes {
			out[s.ID] = window{t, t + s.Duration}
			t += s.Duration
		}
		film = t
	}
	b, err := os.ReadFile(filepath.Join(r.Dir, "composition", "index.html"))
	if err != nil {
		if plan == nil {
			return nil, 0, fmt.Errorf("no composition/index.html and no plan to time the lines against")
		}
		return out, film, nil
	}
	for _, m := range compTagRe.FindAllStringSubmatch(string(b), -1) {
		tag, comp := m[0], m[1]
		sm, dm := startRe.FindStringSubmatch(tag), durRe.FindStringSubmatch(tag)
		if sm == nil || dm == nil {
			continue
		}
		st, err1 := strconv.ParseFloat(sm[1], 64)
		du, err2 := strconv.ParseFloat(dm[1], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		if comp == "main" {
			film = st + du
			continue
		}
		w := window{st, st + du}
		out[comp] = w
		if im := idRe.FindStringSubmatch(tag); im != nil {
			out[im[1]] = w
		}
	}
	return out, film, nil
}

type placedLine struct {
	ID       string       `json:"id"`
	Scene    string       `json:"scene"`
	Text     string       `json:"text"`
	Spoken   string       `json:"spoken"`
	File     string       `json:"file"`
	Start    float64      `json:"start"`
	End      float64      `json:"end"`
	Duration float64      `json:"duration"`
	Window   [2]float64   `json:"scene_window"`
	Cues     []voice.Cue  `json:"cues,omitempty"` // film time
	Spans    []voice.Span `json:"spans"`          // film time
	Track    int          `json:"track"`
	take     *voice.Take
}

func (a *app) buildVoiceover(ctx context.Context, r *run, j *voJob, prov voice.Provider, info voice.ProviderInfo, voiceID string, speed float64, lines []voLine, windows map[string]window, film float64) (string, error) {
	var placed []placedLine
	for i, l := range lines {
		j.set(fmt.Sprintf("line %d of %d: %s", i+1, len(lines), l.ID))
		rel := voiceDir + "/" + l.ID + ".wav"
		take, err := voice.Narrate(ctx, prov, info.Name, l.Text, voiceID, speed, filepath.Join(r.Dir, filepath.FromSlash(rel)), filepath.Join(r.Dir, "voice-cache"))
		if err != nil {
			return "", fmt.Errorf("line %q: %w", l.ID, err)
		}
		w := windows[l.Scene]
		off := voiceLead
		if l.Offset != nil {
			off = *l.Offset
		}
		p := placedLine{ID: l.ID, Scene: l.Scene, Text: l.Text, Spoken: voice.Spoken(l.Text), File: rel,
			Start: round3(w.Start + off), Duration: take.Duration, Window: [2]float64{w.Start, w.End}, take: take}
		p.End = round3(p.Start + take.Duration)
		for _, c := range take.Cues {
			p.Cues = append(p.Cues, voice.Cue{Name: c.Name, At: round3(p.Start + c.At)})
		}
		for _, s := range take.Spans {
			p.Spans = append(p.Spans, voice.Span{Text: s.Text, Start: round3(p.Start + s.Start), End: round3(p.Start + s.End)})
		}
		placed = append(placed, p)
	}
	sort.SliceStable(placed, func(i, k int) bool { return placed[i].Start < placed[k].Start })

	var problems []string
	prevEnd := math.Inf(-1)
	for i := range placed {
		p := &placed[i]
		p.Track = voiceTrack
		if p.Start < prevEnd {
			p.Track = voiceTrack + 1
		}
		fix := fmt.Sprintf("Lengthen scene %q by %.1fs (shift the later scenes), cut words, or start the line earlier with offset", p.Scene, math.Ceil((voiceTail-(p.Window[1]-p.End))*10-1e-6)/10)
		switch room := p.Window[1] - p.End; {
		case room < 0:
			problems = append(problems, fmt.Sprintf("%q runs %.2fs past the cut at %.2fs: its last words play over the next scene. %s", p.ID, -room, p.Window[1], fix))
		case room < 0.15:
			problems = append(problems, fmt.Sprintf("%q ends only %.2fs before the cut at %.2fs, so the cut lands on the last word. Leave about %.2fs. %s", p.ID, room, p.Window[1], voiceTail, fix))
		}
		if i > 0 {
			gap := p.Start - placed[i-1].End
			switch {
			case gap < 0:
				problems = append(problems, fmt.Sprintf("%q starts %.2fs before %q has finished: the two voices overlap", p.ID, -gap, placed[i-1].ID))
			case gap < voiceMinGap:
				problems = append(problems, fmt.Sprintf("only %.2fs between %q and %q; leave at least %.1fs so the lines don't run together", gap, placed[i-1].ID, p.ID, voiceMinGap))
			}
		}
		if film > 0 && p.End > film {
			problems = append(problems, fmt.Sprintf("%q ends at %.2fs, after the film ends (%.2fs)", p.ID, p.End, film))
		}
		prevEnd = math.Max(prevEnd, p.End)
	}

	if err := a.writeVoiceBlock(r, placed); err != nil {
		return "", err
	}
	a.pruneVoiceFiles(r, placed)
	meta := map[string]any{"provider": info.Name, "voice": voiceID, "speed": speed, "lines": placed, "problems": problems}
	if b, err := json.MarshalIndent(meta, "", "  "); err == nil {
		_ = os.WriteFile(filepath.Join(r.Dir, "voiceover.json"), b, 0o644)
	}
	a.markEdited(r, "composition/")

	var b strings.Builder
	fmt.Fprintf(&b, "Voiceover: %d line(s), voice %s (%s), speed %.2f. Files in %s/, wired into composition/index.html on track %d. Timings saved to voiceover.json.\n\n", len(placed), voiceID, info.Title, speed, voiceDir, voiceTrack)
	b.WriteString("line          scene window    voice                    room before cut\n")
	for _, p := range placed {
		fmt.Fprintf(&b, "%-13s %5.2f–%5.2fs   %5.2f–%5.2fs (%.2fs)   %+.2fs\n", p.ID, p.Window[0], p.Window[1], p.Start, p.End, p.Duration, p.Window[1]-p.End)
	}
	b.WriteString("\nWhat is said when (film seconds):\n")
	for _, p := range placed {
		for _, s := range p.Spans {
			fmt.Fprintf(&b, "  %6.2f–%-6.2f %s\n", s.Start, s.End, s.Text)
		}
	}
	var cues []string
	for _, p := range placed {
		for _, c := range p.Cues {
			cues = append(cues, fmt.Sprintf("  {%s} @ %.2fs  (line %s)", c.Name, c.At, p.ID))
		}
	}
	if len(cues) > 0 {
		b.WriteString("\nCues: land the matching visual here. The action (click, result, reveal) may lead the word by up to 0.15s, never trail it:\n")
		b.WriteString(strings.Join(cues, "\n") + "\n")
	}
	if idx, err := os.ReadFile(filepath.Join(r.Dir, "composition", "index.html")); err == nil {
		if m := musicRe.FindString(string(idx)); m != "" {
			if vm := volumeRe.FindStringSubmatch(m); vm != nil {
				if v, err := strconv.ParseFloat(vm[1], 64); err == nil && v > 0.15 {
					fmt.Fprintf(&b, "\nMusic is at data-volume=%s: lower it to about 0.12 so it sits under the voice.\n", vm[1])
				}
			}
		}
	}
	if len(problems) > 0 {
		b.WriteString("\nPROBLEMS (fix these, then run voiceover again):\n- " + strings.Join(problems, "\n- ") + "\n")
	} else {
		b.WriteString("\nEvery line fits its scene with room to breathe.\n")
	}
	b.WriteString("\nNext: time each scene's actions to the spans and cues above, re-run `check`, then review_sheet. If you change a scene's timing in index.html, run voiceover again so the lines move with it (unchanged text is reused, not re-spoken).")
	return b.String(), nil
}

// writeVoiceBlock replaces the voiceover <audio> block inside the root
// composition of index.html.
func (a *app) writeVoiceBlock(r *run, placed []placedLine) error {
	path := filepath.Join(r.Dir, "composition", "index.html")
	b, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("composition/index.html: %w", err)
	}
	html := voBlockRe.ReplaceAllString(string(b), "")
	loc := mainTagRe.FindStringIndex(html)
	if loc == nil {
		return fmt.Errorf(`composition/index.html has no root element with data-composition-id="main"`)
	}
	var blk strings.Builder
	blk.WriteString("\n    " + voBegin + "\n")
	for _, p := range placed {
		fmt.Fprintf(&blk, `    <audio id="vo-%s" class="clip" data-start="%s" data-duration="%s" data-track-index="%d" data-volume="1" src="assets/voice/%s.wav"></audio>`+"\n",
			p.ID, num(p.Start), num(p.Duration), p.Track, p.ID)
	}
	blk.WriteString("    " + voEnd)
	html = html[:loc[1]] + blk.String() + html[loc[1]:]
	return os.WriteFile(path, []byte(html), 0o644)
}

// pruneVoiceFiles removes voice files no current line uses.
func (a *app) pruneVoiceFiles(r *run, placed []placedLine) {
	keep := map[string]bool{}
	for _, p := range placed {
		keep[p.ID+".wav"] = true
	}
	dir := filepath.Join(r.Dir, filepath.FromSlash(voiceDir))
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".wav") && !keep[e.Name()] {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

func sceneNames(w map[string]window) []string {
	var out []string
	for k := range w {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }
