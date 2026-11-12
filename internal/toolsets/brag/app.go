// Package brag is the /brag launch-video toolset (formerly brag-bridge). The
// AI does the creative work; this package reads the project, writes the
// composition, and runs hyperframes/ffmpeg locally.
package brag

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Diwanshumidha/smart-control/internal/browser"
	"github.com/Diwanshumidha/smart-control/internal/config"
	"github.com/Diwanshumidha/smart-control/internal/events"
	"github.com/Diwanshumidha/smart-control/internal/jobs"
	"github.com/Diwanshumidha/smart-control/internal/mcpx"
	"github.com/Diwanshumidha/smart-control/internal/osutil"
	"github.com/Diwanshumidha/smart-control/internal/resources"
	"github.com/Diwanshumidha/smart-control/internal/security/pathguard"
	"github.com/Diwanshumidha/smart-control/internal/toolsets/kit"
)

//go:embed all:embedded
var embedded embed.FS

type run struct {
	ID      string    `json:"id"`
	Dir     string    `json:"dir"`
	Project string    `json:"project,omitempty"`
	URL     string    `json:"url,omitempty"`
	Created time.Time `json:"created"`

	// Two-step flow: "planning" → (submit_plan) "awaiting_approval" → (approve_plan) "building".
	Stage         string    `json:"stage,omitempty"`
	PlanHash      string    `json:"plan_hash,omitempty"`
	ApprovedQuote string    `json:"approved_quote,omitempty"`
	Templates     []string  `json:"templates,omitempty"`     // registry names in the approved plan
	EditedAt      time.Time `json:"edited_at,omitempty"`     // last composition change
	CheckedAt     time.Time `json:"checked_at,omitempty"`    // last passing `hyperframes check`
	PassedChecks  int       `json:"passed_checks,omitempty"` // passing checks since approval (polish budget)
	// Reviewed is when review_scene last showed each scene.
	Reviewed map[string]time.Time `json:"reviewed,omitempty"`
}

type app struct {
	version   string
	workdir   string
	guard     *pathguard.Guard
	browser   *browser.Service
	bus       *events.Bus
	jobs      *jobs.Manager
	fileURLFn func(toolset, rel string) string
	assetsDir string
	res       *resources.Service
	cfg       *config.Config

	mu      sync.Mutex
	runs    map[string]*run
	jobIDs  []string        // jobs this toolset started, cancelled on Close
	counted map[string]bool // check jobs already counted toward PassedChecks
	voice   voJobs          // in-process voiceover jobs
}

// New is the kit.Factory for the brag toolset.
func New(d *kit.Deps) (mcpx.Toolset, error) {
	a := &app{
		version:   d.Version,
		workdir:   d.Config.Sub("brag"),
		guard:     d.Guard,
		browser:   d.Browser,
		bus:       d.Bus,
		jobs:      d.Jobs,
		fileURLFn: d.FileURL,
		res:       d.Resources,
		cfg:       d.Config,
		runs:      map[string]*run{},
	}
	if err := os.MkdirAll(filepath.Join(a.workdir, "runs"), 0o700); err != nil {
		return nil, err
	}
	a.loadRuns()
	if d.Resources != nil {
		d.Resources.RegisterSource("brag", func(_ context.Context, rest string) (string, error) {
			id, rel, ok := strings.Cut(rest, "/")
			if !ok || rel == "" {
				return "", fmt.Errorf("brag source must look like brag://<run_id>/<run-relative path>")
			}
			r, err := a.getRun(id)
			if err != nil {
				return "", err
			}
			return a.runPath(r, rel)
		})
	}
	return a, nil
}

func (a *app) Name() string { return "brag" }

func (a *app) Instructions() string { return instructions }

func (a *app) Close() error {
	a.mu.Lock()
	ids := append([]string(nil), a.jobIDs...)
	a.mu.Unlock()
	for _, id := range ids {
		if j, err := a.jobs.Get(id); err == nil {
			cancelJob(j)
		}
	}
	return nil
}

// cancelJob stops a job if the jobs package supports per-job cancellation.
func cancelJob(j *jobs.Job) bool {
	if c, ok := any(j).(interface{ Cancel() }); ok {
		c.Cancel()
		return true
	}
	return false
}

// startJob starts a background process and remembers it for Close.
func (a *app) startJob(runID, dir string, timeout time.Duration, name string, args ...string) (*jobs.Job, error) {
	j, err := a.jobs.Start(runID, dir, timeout, name, args...)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.jobIDs = append(a.jobIDs, j.ID)
	a.mu.Unlock()
	return j, nil
}

// waitDone blocks until j finishes.
func waitDone(j *jobs.Job) {
	for j.Running() {
		jobs.Wait(j, time.Minute)
	}
}

func (a *app) fileURL(r *run, rel string) string {
	if a.fileURLFn == nil {
		return ""
	}
	return a.fileURLFn("brag", r.ID+"/"+filepath.ToSlash(rel))
}

// --- runs registry (persisted so a restart doesn't lose the AI's run_id) ---

func (a *app) statePath() string { return filepath.Join(a.workdir, "runs.json") }

func (a *app) loadRuns() {
	b, err := os.ReadFile(a.statePath())
	if err != nil {
		return
	}
	var list []*run
	if json.Unmarshal(b, &list) == nil {
		for _, r := range list {
			if st, err := os.Stat(r.Dir); err == nil && st.IsDir() {
				a.runs[r.ID] = r
			}
		}
	}
}

func (a *app) sortedRunsLocked() []*run {
	list := make([]*run, 0, len(a.runs))
	for _, r := range a.runs {
		list = append(list, r)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Created.After(list[j].Created) })
	return list
}

func (a *app) saveRunsLocked() {
	b, _ := json.MarshalIndent(a.sortedRunsLocked(), "", "  ")
	_ = os.WriteFile(a.statePath(), b, 0o600)
}

func (a *app) getRun(id string) (*run, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	r, ok := a.runs[id]
	if !ok {
		return nil, fmt.Errorf("unknown run_id %q — call brag_start first", id)
	}
	return r, nil
}

// updateRun mutates a run under the lock, persists the registry and announces
// stage changes.
func (a *app) updateRun(r *run, f func(*run)) {
	a.mu.Lock()
	stage := r.Stage
	f(r)
	a.saveRunsLocked()
	snap := *r
	a.mu.Unlock()
	if snap.Stage != stage && a.bus != nil {
		a.bus.Publish("brag.run", snap)
	}
}

func (a *app) addRun(r *run) {
	a.mu.Lock()
	a.runs[r.ID] = r
	a.saveRunsLocked()
	snap := *r
	a.mu.Unlock()
	if a.bus != nil {
		a.bus.Publish("brag.run", snap)
	}
}

// ensureAssets extracts the embedded music/sfx once per binary version. A build
// without bundled audio gets an empty library directory.
func (a *app) ensureAssets() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.assetsDir != "" {
		return a.assetsDir, nil
	}
	commit, _ := embedded.ReadFile("embedded/brag/UPSTREAM_COMMIT")
	dir := filepath.Join(a.workdir, "assets", strings.TrimSpace(string(commit))[:min(12, len(strings.TrimSpace(string(commit))))])
	marker := filepath.Join(dir, ".complete")
	if _, err := fs.Stat(embedded, "embedded/brag/assets"); err != nil {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	} else if _, err := os.Stat(marker); err != nil {
		err := fs.WalkDir(embedded, "embedded/brag/assets", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel := strings.TrimPrefix(p, "embedded/brag/assets")
			dst := filepath.Join(dir, filepath.FromSlash(rel))
			if d.IsDir() {
				return os.MkdirAll(dst, 0o755)
			}
			b, err := embedded.ReadFile(p)
			if err != nil {
				return err
			}
			return os.WriteFile(dst, b, 0o644)
		})
		if err != nil {
			return "", err
		}
		_ = os.WriteFile(marker, nil, 0o644)
	}
	a.assetsDir = dir
	return dir, nil
}

// --- HTTP ---

// PublicRoutes serves run files (plan page, video, poster) on the tunneled
// server. The caller has already checked the secret token in prefix.
func (a *app) PublicRoutes(mux *http.ServeMux, prefix string) {
	mux.HandleFunc("GET "+prefix+"{run}/{path...}", a.serveRunFile)
}

// serveRunFile serves a file confined to a run directory ({run}, {path...}).
func (a *app) serveRunFile(w http.ResponseWriter, r *http.Request) {
	rn, err := a.getRun(r.PathValue("run"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	p, err := a.runPath(rn, r.PathValue("path"))
	if err != nil || isDir(p) {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, p)
}

type runInfo struct {
	ID      string    `json:"id"`
	Dir     string    `json:"dir"`
	Project string    `json:"project"`
	URL     string    `json:"url"`
	Created time.Time `json:"created"`
	Stage   string    `json:"stage"`
	Poster  string    `json:"poster,omitempty"` // run-relative poster image, once finalized
	PlanURL string    `json:"plan_url"`
	Video   string    `json:"video"`
}

// AdminRoutes exposes the run list to the desktop app.
func (a *app) AdminRoutes(mux *http.ServeMux, prefix string) {
	mux.HandleFunc("GET "+prefix+"runs/{run}/file/{path...}", a.serveRunFile)
	mux.HandleFunc("POST "+prefix+"runs/{run}/open/{path...}", func(w http.ResponseWriter, r *http.Request) {
		rn, err := a.getRun(r.PathValue("run"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		p, err := a.runPath(rn, r.PathValue("path"))
		if err == nil {
			err = osutil.Open(p)
		}
		w.Header().Set("Content-Type", "application/json")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"path": p})
	})
	mux.HandleFunc("GET "+prefix+"runs", func(w http.ResponseWriter, _ *http.Request) {
		a.mu.Lock()
		list := a.sortedRunsLocked()
		snaps := make([]run, len(list))
		for i, r := range list {
			snaps[i] = *r
		}
		a.mu.Unlock()
		out := make([]runInfo, 0, len(snaps))
		for i := range snaps {
			r := &snaps[i]
			info := runInfo{ID: r.ID, Dir: r.Dir, Project: r.Project, URL: r.URL, Created: r.Created, Stage: r.Stage}
			if _, err := os.Stat(filepath.Join(r.Dir, "plan.html")); err == nil {
				info.PlanURL = a.fileURL(r, "plan.html")
			}
			if _, err := os.Stat(filepath.Join(r.Dir, "brag.mp4")); err == nil {
				info.Video = "brag.mp4"
			}
			if _, err := os.Stat(filepath.Join(r.Dir, "brag.jpg")); err == nil {
				info.Poster = "brag.jpg"
			}
			out = append(out, info)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	})
}
