package brag

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Diwanshumidha/smart-control/internal/jobs"
)

// testdata/check holds a real passing report from a run that looped on
// warnings (film_silent, text_below_floor, content_overlap) and its plan.
func TestCheckVerdictStopsWarningLoop(t *testing.T) {
	_, a, _ := setup(t)
	dir := t.TempDir()
	checks := filepath.Join(dir, "composition", ".hyperframes", "artifacts", "checks")
	if err := os.MkdirAll(checks, 0o755); err != nil {
		t.Fatal(err)
	}
	copyTestFile(t, "testdata/check/plan.json", filepath.Join(dir, "plan.json"))
	r := &run{ID: "run-test", Dir: dir, Stage: "building", Created: time.Now()}
	a.addRun(r)

	m := jobs.NewManager()
	verdict := func() string {
		j, err := m.Start(r.ID, dir, time.Minute, "true", "check")
		if err != nil {
			t.Fatal(err)
		}
		// Report written while the job "runs".
		copyTestFile(t, "testdata/check/report.json", filepath.Join(checks, j.ID+".json"))
		jobs.Wait(j, 5*time.Second)
		j.Command = "npx --yes hyperframes check --json"
		return a.checkVerdict(r, j)
	}

	v := verdict()
	for _, want := range []string{"PASSED: 0 errors", "advisory only", "film_silent", "[EXPECTED", "text_below_floor", "NEXT: review_sheet"} {
		if !strings.Contains(v, want) {
			t.Errorf("verdict missing %q:\n%s", want, v)
		}
	}
	for _, line := range strings.Split(v, "\n") {
		if strings.Contains(line, "text_below_floor") && strings.Contains(line, "[EXPECTED") {
			t.Errorf("text_below_floor is a real problem, not expected: %s", line)
		}
	}

	for i := 0; i < polishHardLimit; i++ {
		v = verdict()
	}
	if !strings.Contains(v, "STOP POLISHING") {
		t.Fatalf("after %d passes the verdict should stop the loop:\n%s", r.PassedChecks, v)
	}

	// Once review.md exists, it points to rendering.
	if err := os.WriteFile(filepath.Join(dir, "review.md"), []byte(strings.Repeat("x", 300)), 0o644); err != nil {
		t.Fatal(err)
	}
	if v = verdict(); !strings.Contains(v, "draft render") {
		t.Fatalf("with review.md the verdict should point to rendering:\n%s", v)
	}
}

func copyTestFile(t *testing.T, src, dst string) {
	t.Helper()
	b, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dst, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
