package brag

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Diwanshumidha/smart-control/internal/jobs"
)

// Polish budget: after this many passing checks without review.md the model
// is told to stop polishing warnings and move on.
const (
	polishSoftLimit = 3
	polishHardLimit = 5
)

type checkFinding struct {
	Code     string  `json:"code"`
	Severity string  `json:"severity"`
	Message  string  `json:"message"`
	Selector string  `json:"selector"`
	Time     float64 `json:"time"`
}

// checkVerdict turns the newest `hyperframes check` report into a short,
// unambiguous verdict for the model. The raw CLI log only lists findings, so
// without this the model treats every warning as a failure and keeps
// patching (observed: 10 passing checks in a row, never moving to review.md).
func (a *app) checkVerdict(r *run, j *jobs.Job) string {
	if j.Running() || !strings.Contains(j.Command, " check") {
		return ""
	}
	report, ok := newestCheckReport(filepath.Join(r.Dir, "composition", ".hyperframes", "artifacts", "checks"), j.Started)
	if !ok {
		return ""
	}
	var errs, warns []checkFinding
	for _, f := range report.findings() {
		if f.Severity == "error" {
			errs = append(errs, f)
		} else {
			warns = append(warns, f)
		}
	}
	expected := a.expectedWarnings(r)
	if report.OK && len(errs) == 0 {
		a.countPass(r, j.ID)
	}

	var b strings.Builder
	b.WriteString("\n=== CHECK VERDICT ===\n")
	if !report.OK || len(errs) > 0 {
		fmt.Fprintf(&b, "FAILED: %d error(s). Fix these (errors block the render):\n", len(errs))
		for _, f := range errs {
			b.WriteString("  - " + f.line() + "\n")
		}
	} else {
		b.WriteString("PASSED: 0 errors. The composition is valid and can be rendered.\n")
	}
	if lp := layoutProblems(r); len(lp) > 0 {
		b.WriteString("LAYOUT (blocks the final render):\n  - " + strings.Join(lp, "\n  - ") + "\n")
	}
	if uses := a.screenshotUses(r); len(uses) > 0 {
		b.WriteString("SCREENSHOTS (blocks the final render):\n  - " + screenshotProblem(uses) + "\n")
	}
	if len(warns) > 0 {
		fmt.Fprintf(&b, "%d warning(s), advisory only. They do NOT block rendering:\n", len(warns))
		for _, f := range warns {
			note := ""
			if why, ok := expected[f.Code]; ok {
				note = "  [EXPECTED: " + why + ". Ignore.]"
			}
			b.WriteString("  - " + f.line() + note + "\n")
		}
		b.WriteString("Fix a warning only if it breaks a quality-bar item (unreadable text, overlapping copy). Do not chase warnings to zero.\n")
	}
	if report.OK && len(errs) == 0 {
		passes := r.PassedChecks
		_, reviewErr := os.Stat(filepath.Join(r.Dir, "review.md"))
		switch {
		case reviewErr == nil:
			b.WriteString("NEXT: review.md exists. Do the draft render, then the delivery render (brag_guide step-4).\n")
		case passes >= polishHardLimit:
			fmt.Fprintf(&b, "STOP POLISHING: check has passed %d times. Do not patch again. Write review.md now (score the 10 quality-bar items, list what you changed), then render.\n", passes)
		case passes >= polishSoftLimit:
			fmt.Fprintf(&b, "NEXT: check has passed %d times. One last review_sheet at most, then write review.md and render. Further polishing is not needed.\n", passes)
		default:
			b.WriteString("NEXT: review_sheet → write review.md → render. At most 2 fix rounds for real quality-bar problems.\n")
		}
	}
	return b.String()
}

// countPass counts a passing check once, whichever tool reports it first.
func (a *app) countPass(r *run, jobID string) {
	a.mu.Lock()
	if a.counted == nil {
		a.counted = map[string]bool{}
	}
	seen := a.counted[jobID]
	a.counted[jobID] = true
	a.mu.Unlock()
	if !seen {
		a.updateRun(r, func(r *run) { r.PassedChecks++ })
	}
}

// expectedWarnings lists warning codes that are inherent to this run's
// approved plan or this build, so the model doesn't try to fix them.
func (a *app) expectedWarnings(r *run) map[string]string {
	out := map[string]string{}
	var plan struct {
		Music  string `json:"music"`
		Scenes []struct {
			Narration string `json:"narration"`
		} `json:"scenes"`
	}
	if b, err := os.ReadFile(filepath.Join(r.Dir, "plan.json")); err == nil && json.Unmarshal(b, &plan) == nil {
		narrated := false
		for _, s := range plan.Scenes {
			narrated = narrated || strings.TrimSpace(s.Narration) != ""
		}
		if (plan.Music == "" || plan.Music == "none") && !narrated {
			why := `the approved plan has music "none" (no audio is bundled with this build)`
			out["film_silent"], out["audio_none"] = why, why
		}
	}
	return out
}

type checkReport struct {
	OK       bool                       `json:"ok"`
	Sections map[string]json.RawMessage `json:"-"`
}

func (c *checkReport) findings() []checkFinding {
	var out []checkFinding
	keys := make([]string, 0, len(c.Sections))
	for k := range c.Sections {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var sec map[string]json.RawMessage
		if json.Unmarshal(c.Sections[k], &sec) != nil {
			continue
		}
		for _, field := range []string{"errors", "warnings", "issues", "findings", "failures", "problems"} {
			var list []checkFinding
			if json.Unmarshal(sec[field], &list) != nil {
				continue
			}
			for _, f := range list {
				if f.Severity == "" {
					f.Severity = map[bool]string{true: "error", false: "warning"}[field == "errors" || field == "failures"]
				}
				if f.Code == "" {
					f.Code = k
				}
				out = append(out, f)
			}
		}
	}
	return out
}

func (f checkFinding) line() string {
	s := f.Code
	if f.Time > 0 {
		s += fmt.Sprintf(" @%.2fs", f.Time)
	}
	if f.Selector != "" {
		s += " (" + f.Selector + ")"
	}
	msg := f.Message
	if len(msg) > 220 {
		msg = msg[:220] + "…"
	}
	return s + ": " + msg
}

// newestCheckReport reads the newest report written after since.
func newestCheckReport(dir string, since time.Time) (*checkReport, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, false
	}
	var newest string
	var newestT time.Time
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || info.ModTime().Before(since.Add(-time.Second)) {
			continue
		}
		if info.ModTime().After(newestT) {
			newest, newestT = e.Name(), info.ModTime()
		}
	}
	if newest == "" {
		return nil, false
	}
	b, err := os.ReadFile(filepath.Join(dir, newest))
	if err != nil {
		return nil, false
	}
	var rep checkReport
	if json.Unmarshal(b, &rep) != nil {
		return nil, false
	}
	_ = json.Unmarshal(b, &rep.Sections)
	delete(rep.Sections, "ok")
	return &rep, true
}
