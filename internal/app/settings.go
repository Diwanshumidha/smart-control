package app

import (
	"context"
	"fmt"
	"slices"

	"github.com/Diwanshumidha/smart-control/internal/config"
	"github.com/Diwanshumidha/smart-control/internal/events"
	"github.com/Diwanshumidha/smart-control/internal/security/pathguard"
	"github.com/Diwanshumidha/smart-control/internal/tunnel"
	"github.com/Diwanshumidha/smart-control/internal/voice"
)

// Settings is the part of the config editable from the dashboard.
type Settings struct {
	Roots     []string         `json:"roots"`
	Tunnel    config.Tunnel    `json:"tunnel"`
	Browser   config.Browser   `json:"browser"`
	URLPolicy config.URLPolicy `json:"url_policy"`
	Activity  config.Activity  `json:"activity"`
	Notify    config.Notify    `json:"notify"`
	Voice     config.Voice     `json:"voice"`
}

func (a *App) Settings() Settings {
	a.mu.Lock()
	defer a.mu.Unlock()
	c := a.Config
	return Settings{Roots: append([]string(nil), c.Roots...), Tunnel: c.Tunnel, Browser: c.Browser, URLPolicy: c.URLPolicy, Activity: c.Activity, Notify: c.Notify, Voice: c.Voice}
}

// ApplySettings validates, saves and applies settings. Project folders,
// browser and URL policy apply immediately; a tunnel change needs a restart.
func (a *App) ApplySettings(in Settings) (restartRequired bool, err error) {
	if !slices.Contains(tunnel.Modes, in.Tunnel.Mode) {
		return false, fmt.Errorf("tunnel mode must be one of %v", tunnel.Modes)
	}
	roots, err := pathguard.CleanRoots(in.Roots)
	if err != nil {
		return false, err
	}
	if in.URLPolicy.ApprovalTimeoutSec < 1 || in.URLPolicy.ApprovalTimeoutSec > 3600 {
		return false, fmt.Errorf("approval timeout must be 1-3600 seconds")
	}
	if in.Activity.RetentionDays < 0 || in.Activity.RetentionDays > 3650 {
		return false, fmt.Errorf("activity retention must be 0-3650 days (0 keeps everything)")
	}
	if in.Browser.DebugPort == 0 {
		in.Browser.DebugPort = 9333
	}
	if in.Browser.DebugPort < 1024 || in.Browser.DebugPort > 65535 {
		return false, fmt.Errorf("browser debug port must be 1024-65535")
	}
	for _, p := range a.ownPorts() {
		if p == fmt.Sprint(in.Browser.DebugPort) {
			return false, fmt.Errorf("browser debug port %d is Smart Control's own port", in.Browser.DebugPort)
		}
	}
	if in.Browser.Width < 320 || in.Browser.Height < 240 {
		return false, fmt.Errorf("browser viewport must be at least 320x240")
	}
	if err := validateVoice(in.Voice); err != nil {
		return false, err
	}
	if a.Browser.Driver() != in.Browser.Driver && in.Browser.Driver != "" {
		a.Tabs.CloseAll() // their driver is going away
	}
	if err := a.Browser.Configure(in.Browser); err != nil {
		return false, err
	}

	a.mu.Lock()
	c := a.Config
	rootsChanged := !slices.Equal(c.Roots, in.Roots)
	restartRequired = c.Tunnel != in.Tunnel
	retentionChanged := c.Activity != in.Activity
	c.Roots, c.Tunnel, c.Browser, c.URLPolicy = in.Roots, in.Tunnel, in.Browser, in.URLPolicy
	c.Activity, c.Notify, c.Voice = in.Activity, in.Notify, in.Voice
	err = c.Save()
	a.mu.Unlock()
	if err != nil {
		return false, err
	}

	if rootsChanged {
		if err := a.Guard.SetRoots(roots); err != nil {
			return false, err
		}
		a.Projects.Refresh()
	}
	if retentionChanged {
		_, _ = a.Activity.Prune(context.Background(), in.Activity.RetentionDays)
	}
	a.Policy.Configure(a.Config.ApprovalTimeout(), in.URLPolicy.AllowPrivateNetworks)
	a.Policy.SetLocalhost(in.URLPolicy.AllowLocalhost, a.ownPorts()...)
	a.Bus.Publish(events.ServerStatus, a.Status())
	return restartRequired, nil
}

func validateVoice(v config.Voice) error {
	info, err := voice.Info(v.Provider)
	if err != nil {
		return err
	}
	if v.Voice != "" && !info.HasVoice(v.Voice) {
		return fmt.Errorf("%s has no voice %q", info.Title, v.Voice)
	}
	if v.Speed < 0.7 || v.Speed > 1.3 {
		return fmt.Errorf("voice speed must be 0.7-1.3")
	}
	return nil
}

// VoiceProviders lists the text-to-speech providers and whether each is ready.
func (a *App) VoiceProviders() []voice.ProviderInfo {
	a.mu.Lock()
	s := voice.Settings{DataDir: a.Config.DataDir, Voice: a.Config.Voice}
	a.mu.Unlock()
	return voice.Providers(s)
}
