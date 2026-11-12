// Package projects maintains a SQLite index of the projects found under the
// configured project folders: name, kind (go, node, ...), description, git
// remote and last change. It is rebuilt at startup, every RefreshEvery, when
// the folders change and on demand, so search_project answers instantly.
package projects

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Diwanshumidha/smart-control/internal/events"
	"github.com/Diwanshumidha/smart-control/internal/security/pathguard"
	"github.com/Diwanshumidha/smart-control/internal/store"
)

const (
	RefreshEvery = 15 * time.Minute
	scanDepth    = 4
	// Topic published after each reindex.
	TopicIndexed = "projects.indexed"
)

func init() {
	store.Register(store.Migration{Version: 500, Name: "projects", SQL: `
CREATE TABLE projects (
  path        TEXT PRIMARY KEY,
  name        TEXT NOT NULL,
  root        TEXT NOT NULL,
  kinds       TEXT NOT NULL DEFAULT '[]',
  description TEXT NOT NULL DEFAULT '',
  git_remote  TEXT NOT NULL DEFAULT '',
  git_branch  TEXT NOT NULL DEFAULT '',
  has_readme  INTEGER NOT NULL DEFAULT 0,
  modified_at INTEGER NOT NULL,
  indexed_at  INTEGER NOT NULL
);
CREATE INDEX projects_name ON projects(name);`})
}

type Project struct {
	Path        string   `json:"path"`
	Name        string   `json:"name"`
	Root        string   `json:"root"`
	Kinds       []string `json:"kinds"`
	Description string   `json:"description,omitempty"`
	GitRemote   string   `json:"git_remote,omitempty"`
	GitBranch   string   `json:"git_branch,omitempty"`
	HasReadme   bool     `json:"has_readme"`
	ModifiedAt  int64    `json:"modified_at"`
	IndexedAt   int64    `json:"indexed_at"`
}

// IndexStatus is shown in the dashboard.
type IndexStatus struct {
	Count      int    `json:"count"`
	Running    bool   `json:"running"`
	LastRunAt  int64  `json:"last_run_at"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

type Index struct {
	db    *sql.DB
	bus   *events.Bus
	guard *pathguard.Guard

	runMu  sync.Mutex // one reindex at a time
	mu     sync.Mutex
	status IndexStatus
	kick   chan struct{}
}

func New(db *sql.DB, bus *events.Bus, guard *pathguard.Guard) *Index {
	return &Index{db: db, bus: bus, guard: guard, kick: make(chan struct{}, 1)}
}

// Run reindexes now, then every RefreshEvery and whenever Refresh is called,
// until ctx ends.
func (x *Index) Run(ctx context.Context) {
	t := time.NewTicker(RefreshEvery)
	defer t.Stop()
	for {
		_, _ = x.Reindex(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-x.kick:
		}
	}
}

// Refresh asks the background loop to reindex soon.
func (x *Index) Refresh() {
	select {
	case x.kick <- struct{}{}:
	default:
	}
}

func (x *Index) Status() IndexStatus {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.status
}

// Reindex scans every project folder and replaces the index.
func (x *Index) Reindex(ctx context.Context) (int, error) {
	x.runMu.Lock()
	defer x.runMu.Unlock()
	x.mu.Lock()
	x.status.Running = true
	x.mu.Unlock()
	start := time.Now()

	found := map[string]Project{}
	for _, root := range x.guard.Roots() {
		for _, p := range Scan(ctx, root) {
			found[p.Path] = p
		}
	}
	err := x.replace(ctx, found)

	x.mu.Lock()
	x.status = IndexStatus{Count: len(found), LastRunAt: store.Now(), DurationMs: time.Since(start).Milliseconds()}
	if err != nil {
		x.status.Error = err.Error()
	}
	st := x.status
	x.mu.Unlock()
	x.bus.Publish(TopicIndexed, st)
	return len(found), err
}

func (x *Index) replace(ctx context.Context, found map[string]Project) error {
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM projects`); err != nil {
		return err
	}
	now := store.Now()
	for _, p := range found {
		kinds, _ := json.Marshal(p.Kinds)
		if _, err := tx.ExecContext(ctx, `INSERT INTO projects(path,name,root,kinds,description,git_remote,git_branch,has_readme,modified_at,indexed_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			p.Path, p.Name, p.Root, string(kinds), p.Description, p.GitRemote, p.GitBranch, p.HasReadme, p.ModifiedAt, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Search ranks indexed projects against q (empty q lists all, newest first).
func (x *Index) Search(ctx context.Context, q string, limit int) ([]Project, error) {
	all, err := x.List(ctx)
	if err != nil {
		return nil, err
	}
	q = strings.ToLower(strings.TrimSpace(q))
	type scored struct {
		p     Project
		score int
	}
	var hits []scored
	for _, p := range all {
		s := 1
		if q != "" {
			s = Score(p, q)
			if s == 0 {
				continue
			}
		}
		hits = append(hits, scored{p, s})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].score != hits[j].score {
			return hits[i].score > hits[j].score
		}
		return hits[i].p.ModifiedAt > hits[j].p.ModifiedAt
	})
	if limit <= 0 {
		limit = 30
	}
	out := []Project{}
	for i := 0; i < len(hits) && i < limit; i++ {
		out = append(out, hits[i].p)
	}
	return out, nil
}

// Score: exact name > name prefix > name contains > path/description contains > fuzzy.
func Score(p Project, q string) int {
	name := strings.ToLower(p.Name)
	switch {
	case name == q:
		return 100
	case strings.HasPrefix(name, q):
		return 80
	case strings.Contains(name, q):
		return 60
	case strings.Contains(strings.ToLower(p.Path), q):
		return 40
	case strings.Contains(strings.ToLower(p.Description), q):
		return 30
	case slicesContains(p.Kinds, q):
		return 20
	case fuzzy(name, q):
		return 10
	}
	return 0
}

func slicesContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func (x *Index) List(ctx context.Context) ([]Project, error) {
	rows, err := x.db.QueryContext(ctx, `SELECT path,name,root,kinds,description,git_remote,git_branch,has_readme,modified_at,indexed_at FROM projects ORDER BY modified_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Project{}
	for rows.Next() {
		var p Project
		var kinds string
		if err := rows.Scan(&p.Path, &p.Name, &p.Root, &kinds, &p.Description, &p.GitRemote, &p.GitBranch, &p.HasReadme, &p.ModifiedAt, &p.IndexedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(kinds), &p.Kinds)
		out = append(out, p)
	}
	return out, rows.Err()
}

// --- scanning ---

var markers = map[string]string{
	"go.mod": "go", "package.json": "node", "Cargo.toml": "rust", "pyproject.toml": "python",
	"requirements.txt": "python", "pom.xml": "java", "build.gradle": "java", "build.gradle.kts": "kotlin",
	"composer.json": "php", "Gemfile": "ruby", "pubspec.yaml": "dart", "deno.json": "deno", ".git": "git",
	"mix.exs": "elixir", "CMakeLists.txt": "c/c++", "Package.swift": "swift",
}

// Scan finds project directories under root (breadth-first, depth-limited).
// A directory with a manifest is a project; its children are not scanned
// unless it is only a git repo (monorepos keep their packages discoverable).
func Scan(ctx context.Context, root string) []Project {
	type item struct {
		dir   string
		depth int
	}
	var out []Project
	queue := []item{{root, 0}}
	for len(queue) > 0 && ctx.Err() == nil {
		it := queue[0]
		queue = queue[1:]
		entries, err := os.ReadDir(it.dir)
		if err != nil {
			continue
		}
		kinds := map[string]bool{}
		hasReadme := false
		for _, e := range entries {
			if k, ok := markers[e.Name()]; ok {
				kinds[k] = true
			}
			if strings.HasPrefix(strings.ToLower(e.Name()), "readme") {
				hasReadme = true
			}
		}
		descend := true
		if len(kinds) > 0 && it.dir != root {
			out = append(out, describe(it.dir, root, kinds, hasReadme))
			descend = len(kinds) == 1 && kinds["git"]
		}
		if !descend || it.depth >= scanDepth {
			continue
		}
		for _, e := range entries {
			n := e.Name()
			if !e.IsDir() || pathguard.SkipDirs[n] || pathguard.DeniedComponent(n) || strings.HasPrefix(n, ".") {
				continue
			}
			queue = append(queue, item{filepath.Join(it.dir, n), it.depth + 1})
		}
	}
	return out
}

func describe(dir, root string, kinds map[string]bool, hasReadme bool) Project {
	p := Project{Path: dir, Name: filepath.Base(dir), Root: root, HasReadme: hasReadme, Description: description(dir)}
	for k := range kinds {
		p.Kinds = append(p.Kinds, k)
	}
	sort.Strings(p.Kinds)
	p.ModifiedAt = lastModified(dir)
	if kinds["git"] {
		p.GitRemote = gitOut(dir, "config", "--get", "remote.origin.url")
		p.GitBranch = gitOut(dir, "rev-parse", "--abbrev-ref", "HEAD")
	}
	return p
}

// lastModified is the newest mtime among the dir and its top-level entries
// (cheap, and good enough to sort "what was I working on").
func lastModified(dir string) int64 {
	var newest time.Time
	if st, err := os.Stat(dir); err == nil {
		newest = st.ModTime()
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if pathguard.SkipDirs[e.Name()] {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return newest.UnixMilli()
}

func gitOut(dir string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func description(dir string) string {
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var pkg struct{ Description string }
		if json.Unmarshal(b, &pkg) == nil && pkg.Description != "" {
			return clip(pkg.Description)
		}
	}
	for _, n := range []string{"README.md", "readme.md", "README", "README.rst"} {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "<") || strings.HasPrefix(line, "[") || strings.HasPrefix(line, "!") || strings.HasPrefix(line, "---") {
				continue
			}
			return clip(line)
		}
	}
	return ""
}

func clip(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// fuzzy matches q as a subsequence of s ("hrstdr" matches "horse-tinder").
func fuzzy(s, q string) bool {
	if len(q) < 3 {
		return false
	}
	i := 0
	for j := 0; j < len(s) && i < len(q); j++ {
		if s[j] == q[i] {
			i++
		}
	}
	return i == len(q)
}
