package brag

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Diwanshumidha/smart-control/internal/jobs"
)

type reviewIn struct {
	RunID       string `json:"run_id"`
	JobID       string `json:"job_id,omitempty" jsonschema:"pass the job_id from an earlier review_sheet call that was still running"`
	WaitSeconds int    `json:"wait_seconds,omitempty" jsonschema:"default 50"`
}

// toolReviewSheet runs `hyperframes snapshot --review` into <run>/review and
// returns the contact sheets so looking at the film costs one call.
func (a *app) toolReviewSheet(_ context.Context, _ *mcp.CallToolRequest, in reviewIn) (*mcp.CallToolResult, any, error) {
	r, err := a.getRun(in.RunID)
	if err != nil {
		return nil, nil, err
	}
	if err := a.requireBuilding(r); err != nil {
		return nil, nil, err
	}
	var j *jobs.Job
	if in.JobID != "" {
		if j, err = a.jobs.Get(in.JobID); err != nil {
			return nil, nil, err
		}
	} else {
		comp := filepath.Join(r.Dir, "composition")
		_ = os.RemoveAll(filepath.Join(r.Dir, "review"))
		pkg := "hyperframes"
		if v := os.Getenv("HYPERFRAMES_VERSION"); v != "" {
			pkg += "@" + v
		}
		j, err = a.startJob(r.ID, comp, jobs.DefaultTimeout, "npx", "--yes", pkg, "snapshot", "--review", "--json", "--output", "../review")
		if err != nil {
			return nil, nil, err
		}
	}
	wait := in.WaitSeconds
	if wait <= 0 {
		wait = 50
	}
	jobs.Wait(j, waitDur(wait))
	if j.Running() {
		return text(fmt.Sprintf("review capture still running (job %s). Call review_sheet again with job_id=%q.", j.ID, j.ID)), nil, nil
	}
	if j.ExitCode != 0 || j.Err != "" {
		return nil, nil, fmt.Errorf("snapshot --review failed:\n%s", j.Report(r.Dir))
	}
	matches, _ := filepath.Glob(filepath.Join(r.Dir, "review", "*.jpg"))
	pngs, _ := filepath.Glob(filepath.Join(r.Dir, "review", "*.png"))
	matches = append(matches, pngs...)
	sort.Slice(matches, func(i, k int) bool { return reviewRank(matches[i]) < reviewRank(matches[k]) })
	var rels []string
	for _, m := range matches {
		rel, _ := filepath.Rel(r.Dir, m)
		rels = append(rels, filepath.ToSlash(rel))
	}
	if len(rels) == 0 {
		return text("snapshot finished but wrote no images:\n" + j.Report(r.Dir)), nil, nil
	}
	shown := rels
	if len(shown) > 6 {
		shown = shown[:6]
	}
	res, _, err := a.images(r, shown, 1280)
	if err != nil {
		return nil, nil, err
	}
	note := fmt.Sprintf("Overview only (%d images; showing %d). These sheets are too small to judge text, blur or cropping.\n  %s\n\nNext: call review_scene for EVERY scene (%s). It shows each scene at readable size at its settled frame, voice cues, exit and cut, and measures blank/frozen stretches. Write review.md with a \"## <scene id>\" section per scene, fix, re-check, and review_scene again for each scene you changed. The final render is blocked until every scene is reviewed after its last edit.", len(rels), len(shown), strings.Join(rels, "\n  "), strings.Join(a.sceneIDs(r), ", "))
	res.Content = append([]mcp.Content{&mcp.TextContent{Text: note}}, res.Content...)
	return res, nil, nil
}

func reviewRank(p string) string {
	b := filepath.Base(p)
	switch {
	case strings.HasPrefix(b, "review-beats"):
		return "0" + b
	case strings.HasPrefix(b, "review-seam"):
		return "1" + b
	case strings.HasPrefix(b, "review-phone"):
		return "2" + b
	}
	return "3" + b
}

func (a *app) sceneIDs(r *run) []string {
	plan, _ := a.readPlan(r)
	if plan == nil {
		return nil
	}
	var ids []string
	for _, s := range plan.Scenes {
		ids = append(ids, s.ID)
	}
	return ids
}
