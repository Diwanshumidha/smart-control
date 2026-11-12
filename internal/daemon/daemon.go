// Package daemon lets the desktop app find (or start) the background server.
//
// `smart-control serve` owns the MCP server, the tunnel and the admin API, and
// keeps running when the desktop window closes. While it runs it keeps
// <data>/daemon.json (0600) with the admin API address and token; the desktop
// app reads that file to connect, and starts the server when nobody answers.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Diwanshumidha/smart-control/internal/osutil"
)

// Info describes a running server.
type Info struct {
	PID        int    `json:"pid"`
	Version    string `json:"version"`
	AdminURL   string `json:"admin_url"` // http://127.0.0.1:8788
	AdminToken string `json:"admin_token"`
	StartedAt  int64  `json:"started_at"`
}

// TokenEnv carries the admin token across a server restart, so open
// dashboards and the desktop app keep working.
const TokenEnv = "SMART_CONTROL_ADMIN_TOKEN"

func path(dataDir string) string { return filepath.Join(dataDir, "daemon.json") }

// Write publishes info for clients.
func Write(dataDir string, info Info) error {
	b, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	tmp := path(dataDir) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path(dataDir))
}

// Remove deletes the file if it still belongs to pid.
func Remove(dataDir string, pid int) {
	if info, err := Read(dataDir); err == nil && info.PID == pid {
		_ = os.Remove(path(dataDir))
	}
}

func Read(dataDir string) (*Info, error) {
	b, err := os.ReadFile(path(dataDir))
	if err != nil {
		return nil, err
	}
	var info Info
	if err := json.Unmarshal(b, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// Alive reports whether the server in info answers its admin API.
func Alive(ctx context.Context, info *Info) bool {
	if info == nil || info.AdminURL == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.AdminURL+"/api/status", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+info.AdminToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == http.StatusOK
}

// Running returns the live server for dataDir, or nil.
func Running(ctx context.Context, dataDir string) *Info {
	info, err := Read(dataDir)
	if err != nil || !Alive(ctx, info) {
		return nil
	}
	return info
}

// FindBinary looks for name next to the current executable, then on PATH.
func FindBinary(name string) (string, error) {
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("%s not found next to this program or on PATH", name)
}

// Ensure returns the running server for dataDir, starting `smart-control
// serve` in the background (detached, logging to <data>/logs/server.log)
// when none answers.
func Ensure(ctx context.Context, dataDir string) (*Info, error) {
	if info := Running(ctx, dataDir); info != nil {
		return info, nil
	}
	bin, err := FindBinary("smart-control")
	if err != nil {
		return nil, fmt.Errorf("the Smart Control server isn't running and can't be started: %w", err)
	}
	logDir := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(filepath.Join(logDir, "server.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer logFile.Close()
	fmt.Fprintf(logFile, "\n--- %s: started by the desktop app ---\n", time.Now().Format(time.RFC3339))

	cmd := exec.Command(bin, "serve", "--data", dataDir)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.Env = append(os.Environ(), "SMART_CONTROL_STARTED_BY=desktop")
	if err := osutil.StartBackground(cmd); err != nil {
		return nil, fmt.Errorf("start %s: %w", bin, err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	deadline := time.After(20 * time.Second)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case err := <-exited:
			if info := Running(ctx, dataDir); info != nil {
				return info, nil // another instance won the race
			}
			return nil, fmt.Errorf("the server exited during startup (%v); see %s", err, logFile.Name())
		case <-deadline:
			return nil, errors.New("the server didn't come up within 20s; see " + logFile.Name())
		case <-tick.C:
			if info := Running(ctx, dataDir); info != nil {
				return info, nil
			}
		}
	}
}
