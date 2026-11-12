// Package voice turns narration text into speech for brag videos. Speech
// engines are providers in a registry (like browser drivers): a provider
// package calls Register from init and internal/app/drivers.go imports it,
// so adding an engine is one new package plus one import line.
package voice

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/Diwanshumidha/smart-control/internal/config"
)

// Voice is one voice a provider offers.
type Voice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Lang string `json:"lang"`           // e.g. en-us, en-gb
	Note string `json:"note,omitempty"` // what it suits
}

// Request asks a provider to speak Text into a WAV file at Out.
type Request struct {
	Text  string
	Voice string
	Speed float64
	Out   string
}

// Provider is a speech engine.
type Provider interface {
	Synthesize(ctx context.Context, req Request) error
}

// Settings is what a provider needs from the config.
type Settings struct {
	DataDir string
	Voice   config.Voice
}

// Factory builds a provider; Probe reports whether it can run on this machine.
type (
	Factory func(s Settings) (Provider, error)
	Probe   func(s Settings) (ok bool, detail string)
)

// ProviderInfo describes a registered provider (shown in the dashboard).
type ProviderInfo struct {
	Name         string  `json:"name"`
	Title        string  `json:"title"`
	Description  string  `json:"description"`
	DefaultVoice string  `json:"default_voice"`
	Voices       []Voice `json:"voices"`
	Available    bool    `json:"available"`
	Detail       string  `json:"detail,omitempty"`
}

type registration struct {
	info    ProviderInfo
	factory Factory
	probe   Probe
}

var (
	regMu    sync.RWMutex
	registry = map[string]registration{}
)

// Register adds a provider. Provider packages call it from init.
func Register(info ProviderInfo, f Factory, p Probe) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := registry[info.Name]; dup {
		panic("voice provider registered twice: " + info.Name)
	}
	registry[info.Name] = registration{info: info, factory: f, probe: p}
}

func names() []string {
	var out []string
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Providers lists every registered provider with its availability.
func Providers(s Settings) []ProviderInfo {
	regMu.RLock()
	defer regMu.RUnlock()
	var out []ProviderInfo
	for _, n := range names() {
		r := registry[n]
		info := r.info
		if r.probe != nil {
			info.Available, info.Detail = r.probe(s)
		} else {
			info.Available = true
		}
		out = append(out, info)
	}
	return out
}

// Info returns one provider's description.
func Info(name string) (ProviderInfo, error) {
	regMu.RLock()
	defer regMu.RUnlock()
	r, ok := registry[name]
	if !ok {
		return ProviderInfo{}, fmt.Errorf("unknown voice provider %q (have %v)", name, names())
	}
	return r.info, nil
}

// New builds the provider named in s.Voice.Provider after checking it can run.
func New(s Settings) (Provider, ProviderInfo, error) {
	regMu.RLock()
	r, ok := registry[s.Voice.Provider]
	regMu.RUnlock()
	if !ok {
		return nil, ProviderInfo{}, fmt.Errorf("unknown voice provider %q (have %v); pick one in Settings → Voice", s.Voice.Provider, names())
	}
	if r.probe != nil {
		if ok, detail := r.probe(s); !ok {
			return nil, r.info, fmt.Errorf("voice provider %s isn't ready: %s", r.info.Title, detail)
		}
	}
	p, err := r.factory(s)
	return p, r.info, err
}

// HasVoice reports whether info offers the voice id.
func (info ProviderInfo) HasVoice(id string) bool {
	for _, v := range info.Voices {
		if v.ID == id {
			return true
		}
	}
	return false
}
