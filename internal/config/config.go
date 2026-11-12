// Package config loads and persists smart-control settings. Everything lives
// under one data directory (default ~/.smart-control) so the CLI and the
// desktop app share state.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Tunnel describes how the MCP server is exposed to remote clients (ChatGPT).
type Tunnel struct {
	Mode      string `json:"mode"`                 // quick | cloudflare | tailscale | ngrok | off
	Hostname  string `json:"hostname,omitempty"`   // cloudflare: mcp.example.com, ngrok: x.ngrok-free.app
	Name      string `json:"name,omitempty"`       // cloudflare named tunnel
	PublicURL string `json:"public_url,omitempty"` // your own reverse proxy; disables the managed tunnel
}

// Browser configures the Brave instance used by browser tools.
type Browser struct {
	Driver   string `json:"driver"`         // automation backend: cdp | playwright (see internal/browser)
	Path     string `json:"path,omitempty"` // empty: auto-detect Brave
	Headless bool   `json:"headless"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	// DebugPort is where the "shared" driver's visible Brave listens for
	// automation (127.0.0.1 only).
	DebugPort int `json:"debug_port,omitempty"`
}

// URLPolicy configures how unknown URLs are handled.
type URLPolicy struct {
	// ApprovalTimeout is how long a tool call waits for the user to approve an
	// unknown URL in the desktop app before failing.
	ApprovalTimeoutSec int `json:"approval_timeout_sec"`
	// AllowLocalhost lets the AI open localhost URLs (your dev servers) after
	// the usual approval. Smart Control's own ports stay blocked.
	AllowLocalhost bool `json:"allow_localhost"`
	// AllowPrivateNetworks lets rules allow LAN addresses.
	AllowPrivateNetworks bool `json:"allow_private_networks"`
}

// Activity configures the tool-call log.
type Activity struct {
	// RetentionDays drops tool calls older than this many days. 0 keeps everything.
	RetentionDays int `json:"retention_days"`
}

// Notify configures how the server gets your attention when the AI is
// waiting on you (URL approvals).
type Notify struct {
	Desktop bool `json:"desktop"` // desktop notification with Allow / Deny actions
	Popup   bool `json:"popup"`   // small floating confirm window on the active workspace
}

// Voice configures text-to-speech for brag voiceovers.
type Voice struct {
	Provider string  `json:"provider"` // registered provider name (internal/voice), e.g. "kokoro"
	Voice    string  `json:"voice"`    // default voice id; the AI may pick another per run
	Speed    float64 `json:"speed"`    // speaking rate multiplier, 0.7–1.3
}

type Config struct {
	DataDir   string    `json:"-"`
	Roots     []string  `json:"roots"`
	MCPAddr   string    `json:"mcp_addr"`
	AdminAddr string    `json:"admin_addr"` // headless dashboard (never tunneled)
	Token     string    `json:"token"`      // secret path segment for the MCP URL
	Tunnel    Tunnel    `json:"tunnel"`
	Browser   Browser   `json:"browser"`
	URLPolicy URLPolicy `json:"url_policy"`
	Activity  Activity  `json:"activity"`
	Notify    Notify    `json:"notify"`
	Voice     Voice     `json:"voice"`
}

func DefaultDataDir() string {
	if d := os.Getenv("SMART_CONTROL_HOME"); d != "" {
		return d
	}
	if d, err := os.UserHomeDir(); err == nil {
		return filepath.Join(d, ".smart-control")
	}
	return ".smart-control"
}

func defaults() Config {
	home, _ := os.UserHomeDir()
	return Config{
		Roots:     []string{filepath.Join(home, "projects")},
		MCPAddr:   "127.0.0.1:8787",
		AdminAddr: "127.0.0.1:8788",
		Tunnel:    Tunnel{Mode: "quick"},
		Browser:   Browser{Driver: "cdp", Headless: true, Width: 1366, Height: 900},
		URLPolicy: URLPolicy{ApprovalTimeoutSec: 90, AllowLocalhost: true},
		Activity:  Activity{RetentionDays: 30},
		Notify:    Notify{Desktop: true, Popup: true},
		Voice:     Voice{Provider: "kokoro", Voice: "af_heart", Speed: 1},
	}
}

func (c *Config) path() string { return filepath.Join(c.DataDir, "config.json") }

// Load reads <dataDir>/config.json, filling defaults and generating the token
// on first run. The file is written back so users can edit it.
func Load(dataDir string) (*Config, error) {
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	c := defaults()
	c.DataDir = dataDir
	b, err := os.ReadFile(c.path())
	switch {
	case err == nil:
		if err := json.Unmarshal(b, &c); err != nil {
			return nil, fmt.Errorf("parse %s: %w", c.path(), err)
		}
	case !errors.Is(err, os.ErrNotExist):
		return nil, err
	}
	if len(strings.TrimSpace(c.Token)) < 16 {
		buf := make([]byte, 24)
		if _, err := rand.Read(buf); err != nil {
			return nil, err
		}
		c.Token = hex.EncodeToString(buf)
	}
	if c.URLPolicy.ApprovalTimeoutSec <= 0 {
		c.URLPolicy.ApprovalTimeoutSec = 90
	}
	if c.Browser.Driver == "" {
		c.Browser.Driver = "cdp"
	}
	if c.Activity.RetentionDays < 0 {
		c.Activity.RetentionDays = 0
	}
	if c.Browser.DebugPort == 0 {
		c.Browser.DebugPort = 9333
	}
	if c.Voice.Provider == "" {
		c.Voice.Provider = "kokoro"
	}
	if c.Voice.Speed <= 0 {
		c.Voice.Speed = 1
	}
	if c.Browser.Width == 0 {
		c.Browser.Width, c.Browser.Height = 1366, 900
	}
	return &c, c.Save()
}

func (c *Config) Save() error {
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.path(), b, 0o600)
}

// Sub returns a directory inside the data dir, creating it.
func (c *Config) Sub(parts ...string) string {
	p := filepath.Join(append([]string{c.DataDir}, parts...)...)
	_ = os.MkdirAll(p, 0o700)
	return p
}

func (c *Config) ApprovalTimeout() time.Duration {
	return time.Duration(c.URLPolicy.ApprovalTimeoutSec) * time.Second
}
