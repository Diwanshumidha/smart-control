package brag

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Diwanshumidha/smart-control/internal/jobs"
)

// review_scene shows one scene at the moments that matter, at a size where
// text and blur are visible, next to what the plan and the voice say should be
// on screen, plus measurements the eye misses (blank and frozen stretches).

type reviewSceneIn struct {
	RunID       string  `json:"run_id"`
	Scene       string  `json:"scene" jsonschema:"scene id (from the plan / index.html)"`
	Zoom        string  `json:"zoom,omitempty" jsonschema:"optional: CSS selector or 'x,y,w,h' region to also capture at 3x density, to read small UI text"`
	ZoomAt      float64 `json:"zoom_at,omitempty" jsonschema:"film second for the zoom (default: the scene's settled frame)"`
	JobID       string  `json:"job_id,omitempty" jsonschema:"job_id from an earlier review_scene call that was still running"`
	WaitSeconds int     `json:"wait_seconds,omitempty" jsonschema:"default 60"`
}

const (
	sampleStep     = 0.5    // analysis sampling inside a scene
	blankStdDev    = 4.0    // luma std-dev (0-255) under which a frame is empty
	stillDiff      = 0.0025 // mean abs luma difference (0-1) under which two samples are the same picture
	frozenFor      = 2.5    // report a frozen stretch at least this long
	maxShownFrames = 6
)

type keyFrame struct {
	T     float64
	Label string
}

type sampleStat struct {
	T      float64
	StdDev float64
	luma   []float64
}

func (a *app) toolReviewScene(_ context.Context, _ *mcp.CallToolRequest, in reviewSceneIn) (*mcp.CallToolResult, any, error) {
	r, err := a.getRun(in.RunID)
	if err != nil {
		return nil, nil, err
	}
	if err := a.requireBuilding(r); err != nil {
		return nil, nil, err
	}
	plan, _ := a.readPlan(r)
	windows, _, err := a.sceneWindows(r, plan)
	if err != nil {
		return nil, nil, err
	}
	w, ok := windows[in.Scene]
	if !ok {
		return nil, nil, fmt.Errorf("no scene %q (have %s)", in.Scene, strings.Join(sceneNames(windows), ", "))
	}
	keys := a.keyFrames(r, plan, windows, in.Scene, w)
	times := sampleTimes(w, keys)
	outRel := filepath.ToSlash(filepath.Join("review", "scenes", in.Scene))
	out := filepath.Join(r.Dir, filepath.FromSlash(outRel))

	var j *jobs.Job
	if in.JobID != "" {
		if j, err = a.jobs.Get(in.JobID); err != nil {
			return nil, nil, err
		}
	} else {
		_ = os.RemoveAll(out)
		args := []string{"--yes", hfPkg(), "snapshot", "--at", joinTimes(times), "--no-end", "--json", "--output", out}
		j, err = a.startJob(r.ID, filepath.Join(r.Dir, "composition"), jobs.DefaultTimeout, "npx", args...)
		if err != nil {
			return nil, nil, err
		}
	}
	wait := in.WaitSeconds
	if wait <= 0 {
		wait = 60
	}
	jobs.Wait(j, waitDur(wait))
	if j.Running() {
		return text(fmt.Sprintf("capturing scene %q (job %s). Call review_scene again with scene=%q and job_id=%q.", in.Scene, j.ID, in.Scene, j.ID)), nil, nil
	}
	if j.ExitCode != 0 || j.Err != "" {
		return nil, nil, fmt.Errorf("snapshot failed:\n%s", j.Report(r.Dir))
	}
	frames := snapshotFiles(out)
	if len(frames) == 0 {
		return nil, nil, fmt.Errorf("snapshot wrote no frames:\n%s", j.Report(r.Dir))
	}

	// Measure every sample.
	var stats []sampleStat
	for _, t := range times {
		if p, ok := nearestFrame(frames, t); ok {
			if st, err := measure(p); err == nil {
				st.T = t
				stats = append(stats, st)
			}
		}
	}
	findings := measureFindings(stats, w)

	// Optional zoom crop.
	var zoomRel string
	if in.Zoom != "" {
		at := in.ZoomAt
		if at == 0 && len(keys) > 0 {
			at = keys[0].T
		}
		zoomDir := filepath.Join(out, "zoom")
		zj, err := a.startJob(r.ID, filepath.Join(r.Dir, "composition"), jobs.DefaultTimeout, "npx",
			"--yes", hfPkg(), "snapshot", "--at", num(at), "--no-end", "--zoom", in.Zoom, "--output", zoomDir)
		if err == nil {
			waitDone(zj)
			if zf := snapshotFiles(zoomDir); len(zf) > 0 {
				for _, p := range zf {
					zoomRel, _ = filepath.Rel(r.Dir, p)
				}
			} else {
				findings = append(findings, "zoom capture failed: "+zj.Report(r.Dir))
			}
		}
	}

	// Build the result: context, then each key frame with what should be there.
	var b strings.Builder
	fmt.Fprintf(&b, "REVIEW scene %q, %.2f–%.2fs (%.2fs)\n", in.Scene, w.Start, w.End, w.End-w.Start)
	if s := planScene0(plan, in.Scene); s != nil {
		if s.Script != "" {
			fmt.Fprintf(&b, "Planned caption: %q\n", s.Script)
		}
		fmt.Fprintf(&b, "Planned visual: %s\nPlanned motion: %s\n", s.Visual, s.Motion)
	}
	if lp := layoutProblems(r); len(lp) > 0 {
		b.WriteString("Layout: " + strings.Join(lp, "; ") + "\n")
	}
	if cw := a.checkFindingsIn(r, w); len(cw) > 0 {
		b.WriteString("Check findings in this scene:\n  - " + strings.Join(cw, "\n  - ") + "\n")
	}
	if len(findings) > 0 {
		b.WriteString("Measured:\n  - " + strings.Join(findings, "\n  - ") + "\n")
	} else {
		b.WriteString("Measured: no blank or frozen stretches.\n")
	}
	b.WriteString(`
For EACH frame below, answer in review.md under "## ` + in.Scene + `":
  1. What is the one thing to look at? If you can't say, the frame needs focus.
  2. Does it match what the voice is saying at that moment (shown above each frame)?
  3. Is every word the viewer must read legible at phone size (about a third of this image)?
  4. Anything cropped, blurry, stretched, overlapping, off the layout grid, empty, or a dev/error overlay?
Then fix what fails. Use zoom (a CSS selector such as ".lay-stage") to read small UI text.
`)
	content := []mcp.Content{&mcp.TextContent{Text: b.String()}}
	width := reviewWidth(plan)
	shown := 0
	for _, k := range keys {
		if shown == maxShownFrames {
			break
		}
		p, ok := nearestFrame(frames, k.T)
		if !ok {
			continue
		}
		data, mime, err := toPreviewJPEG(p, width)
		if err != nil {
			continue
		}
		label := fmt.Sprintf("%.2fs · %s · voice: %s", k.T, k.Label, a.voiceAt(r, k.T))
		content = append(content, &mcp.TextContent{Text: label}, &mcp.ImageContent{Data: data, MIMEType: mime})
		shown++
	}
	if zoomRel != "" {
		if data, mime, err := toPreviewJPEG(filepath.Join(r.Dir, zoomRel), 1400); err == nil {
			content = append(content, &mcp.TextContent{Text: fmt.Sprintf("zoom %s (3x density)", in.Zoom)}, &mcp.ImageContent{Data: data, MIMEType: mime})
		}
	}
	a.updateRun(r, func(r *run) {
		if r.Reviewed == nil {
			r.Reviewed = map[string]time.Time{}
		}
		r.Reviewed[in.Scene] = time.Now()
	})
	return &mcp.CallToolResult{Content: content}, nil, nil
}

// keyFrames picks the moments worth looking at in one scene.
func (a *app) keyFrames(r *run, plan *planIn, windows map[string]window, scene string, w window) []keyFrame {
	d := w.End - w.Start
	settle := w.Start + math.Min(1.2, d*0.35)
	keys := []keyFrame{{settle, "settled (entrance done)"}}
	for _, c := range a.cuesIn(r, w) {
		keys = append(keys, keyFrame{c.At + 0.05, "cue {" + c.Name + "}: the action should be happening"})
	}
	mid := w.Start + d*0.65
	if mid-settle > 0.8 {
		keys = append(keys, keyFrame{mid, "later in the beat"})
	}
	keys = append(keys, keyFrame{math.Max(w.End-0.4, settle+0.1), "just before the exit"})
	if last := lastSceneEnd(windows); w.End < last-0.01 {
		keys = append(keys, keyFrame{w.End, "the cut into the next scene"})
	}
	sort.SliceStable(keys, func(i, k int) bool { return keys[i].T < keys[k].T })
	return keys
}

func sampleTimes(w window, keys []keyFrame) []float64 {
	set := map[string]float64{}
	add := func(t float64) { set[num(math.Round(t*100)/100)] = math.Round(t*100) / 100 }
	for t := w.Start + 0.1; t < w.End-0.05; t += sampleStep {
		add(t)
	}
	for _, k := range keys {
		add(k.T)
	}
	var out []float64
	for _, t := range set {
		out = append(out, t)
	}
	sort.Float64s(out)
	return out
}

func joinTimes(ts []float64) string {
	var s []string
	for _, t := range ts {
		s = append(s, num(t))
	}
	return strings.Join(s, ",")
}

func lastSceneEnd(w map[string]window) float64 {
	m := 0.0
	for _, v := range w {
		m = math.Max(m, v.End)
	}
	return m
}

func hfPkg() string {
	if v := os.Getenv("HYPERFRAMES_VERSION"); v != "" {
		return "hyperframes@" + v
	}
	return "hyperframes"
}

// snapshotFiles maps each captured time to its PNG (frame-NN-at-<t>s.png).
func snapshotFiles(dir string) map[float64]string {
	out := map[float64]string{}
	files, _ := filepath.Glob(filepath.Join(dir, "*.png"))
	for _, f := range files {
		b := filepath.Base(f)
		i := strings.LastIndex(b, "-at-")
		if i < 0 || !strings.HasSuffix(b, "s.png") {
			continue
		}
		if t, err := strconv.ParseFloat(strings.TrimSuffix(b[i+4:], "s.png"), 64); err == nil {
			out[t] = f
		}
	}
	return out
}

func nearestFrame(frames map[float64]string, t float64) (string, bool) {
	best, bestD := "", math.Inf(1)
	for ft, p := range frames {
		if d := math.Abs(ft - t); d < bestD {
			best, bestD = p, d
		}
	}
	return best, bestD < 0.06
}

// measure downsamples a frame to a 48×48 luma grid.
func measure(path string) (sampleStat, error) {
	f, err := os.Open(path)
	if err != nil {
		return sampleStat{}, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return sampleStat{}, err
	}
	const n = 48
	bnd := img.Bounds()
	luma := make([]float64, 0, n*n)
	var sum, sq float64
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			px := bnd.Min.X + (x*2+1)*bnd.Dx()/(2*n)
			py := bnd.Min.Y + (y*2+1)*bnd.Dy()/(2*n)
			r, g, b, _ := img.At(px, py).RGBA()
			l := (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(b)) / 257
			luma = append(luma, l)
			sum += l
			sq += l * l
		}
	}
	mean := sum / float64(len(luma))
	return sampleStat{StdDev: math.Sqrt(math.Max(sq/float64(len(luma))-mean*mean, 0)), luma: luma}, nil
}

func diff(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 1
	}
	var s float64
	for i := range a {
		s += math.Abs(a[i] - b[i])
	}
	return s / float64(len(a)) / 255
}

func measureFindings(stats []sampleStat, w window) []string {
	var out []string
	var blank []string
	for _, s := range stats {
		if s.StdDev < blankStdDev {
			blank = append(blank, num(s.T)+"s")
		}
	}
	if len(blank) > 0 {
		out = append(out, "EMPTY frame (nothing on screen) at "+strings.Join(blank, ", ")+": something must be visible at every moment, including across cuts")
	}
	start := -1
	for i := 1; i <= len(stats); i++ {
		same := i < len(stats) && diff(stats[i-1].luma, stats[i].luma) < stillDiff && stats[i].StdDev >= blankStdDev
		if same && start < 0 {
			start = i - 1
		}
		if !same && start >= 0 {
			if d := stats[i-1].T - stats[start].T; d >= frozenFor {
				out = append(out, fmt.Sprintf("FROZEN %.2f–%.2fs (%.1fs with no visible change): add the next element, a camera move or a focus shift, or shorten the scene", stats[start].T, stats[i-1].T, d))
			}
			start = -1
		}
	}
	if len(stats) >= 2 {
		if d := diff(stats[0].luma, stats[len(stats)-1].luma); d < stillDiff {
			out = append(out, "the scene ends looking exactly as it starts: nothing builds up")
		}
	}
	return out
}

func reviewWidth(plan *planIn) int {
	if plan != nil && plan.Format == "vertical" {
		return 720 // 1080×1920 → 720×1280: phone UI text stays readable
	}
	if plan != nil && plan.Format == "square" {
		return 960
	}
	return 1280
}

func planScene0(p *planIn, id string) *planScene {
	if p == nil {
		return nil
	}
	for i := range p.Scenes {
		if p.Scenes[i].ID == id {
			return &p.Scenes[i]
		}
	}
	return nil
}

// voiceAt says what the voiceover is saying at film second t.
func (a *app) voiceAt(r *run, t float64) string {
	lines := readVoiceLines(r)
	if lines == nil {
		return "(no voiceover)"
	}
	for _, l := range lines {
		for _, s := range l.Spans {
			if t >= s.Start-0.05 && t <= s.End+0.05 {
				return fmt.Sprintf("%q", s.Text)
			}
		}
	}
	return "(silence)"
}

func (a *app) cuesIn(r *run, w window) []struct {
	Name string
	At   float64
} {
	var out []struct {
		Name string
		At   float64
	}
	for _, l := range readVoiceLines(r) {
		for _, c := range l.Cues {
			if c.At >= w.Start && c.At < w.End {
				out = append(out, struct {
					Name string
					At   float64
				}{c.Name, c.At})
			}
		}
	}
	return out
}

func readVoiceLines(r *run) []placedLine {
	b, err := os.ReadFile(filepath.Join(r.Dir, "voiceover.json"))
	if err != nil {
		return nil
	}
	var meta struct {
		Lines []placedLine `json:"lines"`
	}
	if json.Unmarshal(b, &meta) != nil {
		return nil
	}
	return meta.Lines
}

// checkFindingsIn lists the newest check report's findings inside a window.
func (a *app) checkFindingsIn(r *run, w window) []string {
	rep, ok := newestCheckReport(filepath.Join(r.Dir, "composition", ".hyperframes", "artifacts", "checks"), time.Time{})
	if !ok {
		return nil
	}
	var out []string
	for _, f := range rep.findings() {
		if f.Time >= w.Start && f.Time < w.End {
			out = append(out, f.line())
		}
	}
	return out
}

// unreviewedScenes lists scenes not reviewed with review_scene since their
// files (or the host) last changed.
func (a *app) unreviewedScenes(r *run) []string {
	plan, _ := a.readPlan(r)
	if plan == nil {
		return nil
	}
	comp := filepath.Join(r.Dir, "composition")
	mtime := func(p string) time.Time {
		if st, err := os.Stat(p); err == nil {
			return st.ModTime()
		}
		return time.Time{}
	}
	shared := mtime(filepath.Join(comp, "index.html"))
	comps, _ := filepath.Glob(filepath.Join(comp, "compositions", "components", "*.html"))
	for _, c := range comps {
		if t := mtime(c); t.After(shared) {
			shared = t
		}
	}
	var out []string
	for _, s := range plan.Scenes {
		changed := shared
		if t := mtime(filepath.Join(comp, "compositions", s.ID+".html")); t.After(changed) {
			changed = t
		}
		if seen := r.Reviewed[s.ID]; seen.IsZero() || seen.Before(changed) {
			out = append(out, s.ID)
		}
	}
	return out
}
