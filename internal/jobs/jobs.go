// Package jobs runs allowlisted external commands (hyperframes, ffmpeg) in the
// background and keeps the tail of their output for the model to poll.
package jobs

import (
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// MaxOutput is how much process output a job keeps.
	MaxOutput      = 64 << 10
	DefaultTimeout = 10 * time.Minute
)

// TailBuffer keeps the last MaxOutput bytes of a process's output.
type TailBuffer struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	lost bool
}

func (t *TailBuffer) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf.Write(p)
	if t.buf.Len() > MaxOutput {
		b := t.buf.Bytes()
		keep := append([]byte(nil), b[len(b)-MaxOutput:]...)
		t.buf.Reset()
		t.buf.Write(keep)
		t.lost = true
	}
	return len(p), nil
}

func (t *TailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lost {
		return "…(earlier output truncated)…\n" + t.buf.String()
	}
	return t.buf.String()
}

type Job struct {
	ID       string
	RunID    string
	Command  string
	Dir      string
	Started  time.Time
	Finished time.Time
	ExitCode int
	Err      string
	out      TailBuffer
	done     chan struct{}
	cancel   context.CancelFunc
}

func (j *Job) Running() bool {
	select {
	case <-j.done:
		return false
	default:
		return true
	}
}

type Manager struct {
	mu   sync.Mutex
	seq  int
	jobs map[string]*Job
}

func NewManager() *Manager { return &Manager{jobs: map[string]*Job{}} }

func (m *Manager) Start(runID, dir string, timeout time.Duration, name string, args ...string) (*Job, error) {
	bin, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("%s not found on PATH — run `smart-control doctor` on the desktop", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "CI=1", "NO_COLOR=1", "FORCE_COLOR=0")

	m.mu.Lock()
	m.seq++
	j := &Job{
		ID: fmt.Sprintf("job-%d", m.seq), RunID: runID, Dir: dir,
		Command: name + " " + strings.Join(args, " "),
		Started: time.Now(), done: make(chan struct{}), cancel: cancel,
	}
	m.jobs[j.ID] = j
	m.mu.Unlock()

	cmd.Stdout, cmd.Stderr = &j.out, &j.out
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	go func() {
		err := cmd.Wait()
		j.Finished = time.Now()
		if err != nil {
			j.Err = err.Error()
			if ee, ok := err.(*exec.ExitError); ok {
				j.ExitCode = ee.ExitCode()
			} else {
				j.ExitCode = -1
			}
			if ctx.Err() == context.DeadlineExceeded {
				j.Err = "timed out after " + timeout.String()
			}
		}
		cancel()
		close(j.done)
	}()
	return j, nil
}

func (m *Manager) Get(id string) (*Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return nil, fmt.Errorf("unknown job_id %q", id)
	}
	return j, nil
}

func (m *Manager) CancelAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, j := range m.jobs {
		j.cancel()
	}
}

func Wait(j *Job, wait time.Duration) {
	select {
	case <-j.done:
	case <-time.After(wait):
	}
}

// report renders a job for the model, including media files it produced.
func (j *Job) Report(runDir string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "job_id: %s\ncommand: %s\n", j.ID, j.Command)
	if j.Running() {
		fmt.Fprintf(&b, "status: running (%.0fs elapsed) — call job_status again\n", time.Since(j.Started).Seconds())
	} else {
		status := "succeeded"
		if j.ExitCode != 0 || j.Err != "" {
			status = fmt.Sprintf("failed (exit %d) %s", j.ExitCode, j.Err)
		}
		fmt.Fprintf(&b, "status: %s in %.1fs\n", status, j.Finished.Sub(j.Started).Seconds())
		if files := NewFiles(runDir, j.Started); len(files) > 0 {
			fmt.Fprintf(&b, "new files (run-relative; open images with view_images):\n  %s\n", strings.Join(files, "\n  "))
		}
	}
	out := strings.TrimSpace(dropProgress(j.out.String()))
	if out != "" {
		fmt.Fprintf(&b, "output:\n%s\n", out)
	}
	return b.String()
}

func NewFiles(root string, since time.Time) []string {
	if root == "" {
		return nil
	}
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && d.Name() == "node_modules" {
			return filepath.SkipDir
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(p)) {
		case ".png", ".jpg", ".jpeg", ".webp", ".mp4", ".webm", ".json", ".gif":
		default:
			return nil
		}
		if info, err := d.Info(); err == nil && !info.ModTime().Before(since) {
			rel, _ := filepath.Rel(root, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	if len(out) > 40 {
		out = append(out[:40], fmt.Sprintf("…and %d more", len(out)-40))
	}
	return out
}

var progressRe = regexp.MustCompile(`^(.*?)\d+/\d+\s*$`)

// dropProgress keeps only the last line of each run of "label N/M" progress
// lines, which otherwise push the useful output out of the buffer.
func dropProgress(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	for i, l := range lines {
		if m := progressRe.FindStringSubmatch(l); m != nil && i+1 < len(lines) {
			if n := progressRe.FindStringSubmatch(lines[i+1]); n != nil && n[1] == m[1] {
				continue
			}
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// Cancel stops a running job.
func (j *Job) Cancel() { j.cancel() }
