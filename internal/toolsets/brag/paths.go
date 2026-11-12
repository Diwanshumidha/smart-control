package brag

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/Diwanshumidha/smart-control/internal/security/pathguard"
)

var (
	deniedComponent = pathguard.DeniedComponent
	skipDirs        = pathguard.SkipDirs
	within          = pathguard.Within
	resolve         = pathguard.Resolve
)

// projectPath maps a path from the AI onto an allowed root.
func (a *app) projectPath(p string) (string, error) { return a.guard.Path(p) }

// runPath maps a run-relative path into the run directory. An absolute path
// that points inside the run is accepted too (the AI often echoes the full
// paths tools print).
func (a *app) runPath(r *run, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		dir := resolve(r.Dir)
		p := resolve(filepath.Clean(rel))
		if !within(dir, p) {
			return "", fmt.Errorf("%s is outside run %s (%s); use a run-relative path", rel, r.ID, r.Dir)
		}
		rel, _ = filepath.Rel(dir, p)
	}
	return pathguard.Under(r.Dir, rel)
}

// sourcePath resolves a file to copy into a run: an absolute path inside
// Smart Control's brag folder (this or another run, captures, bundled
// assets) or inside the project roots; a relative path is looked up in the
// run first (capture/…, frames/…), then in the run's project.
func (a *app) sourcePath(r *run, p string) (string, error) {
	if filepath.IsAbs(p) {
		root := resolve(a.workdir)
		abs := resolve(filepath.Clean(p))
		if within(root, abs) {
			rel, _ := filepath.Rel(root, abs)
			return pathguard.Under(root, rel)
		}
		return a.projectPath(p)
	}
	if rp, err := a.runPath(r, p); err == nil && !isDir(rp) {
		if _, err := os.Stat(rp); err == nil {
			return rp, nil
		}
	}
	if r.Project != "" {
		return a.projectPath(filepath.Join(r.Project, p))
	}
	return a.projectPath(p)
}

// pathFor picks run-relative resolution when run_id is set, project roots
// otherwise. An absolute path into a known run resolves there even without
// run_id.
func (a *app) pathFor(runID, p string) (string, error) {
	if runID == "" && filepath.IsAbs(p) {
		if r := a.runContaining(p); r != nil {
			return a.runPath(r, p)
		}
	}
	if runID != "" {
		r, err := a.getRun(runID)
		if err != nil {
			return "", err
		}
		return a.runPath(r, p)
	}
	return a.projectPath(p)
}

// runContaining returns the run whose directory holds the absolute path p.
func (a *app) runContaining(p string) *run {
	abs := resolve(filepath.Clean(p))
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, r := range a.runs {
		if within(resolve(r.Dir), abs) {
			return r
		}
	}
	return nil
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
