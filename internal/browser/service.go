// Package browser gives toolsets one policy-enforced way to open pages in
// Brave, independent of how the browser is automated. The automation backend
// is a Driver chosen by name in the config (dashboard → Settings → Browser);
// drivers live in sub-packages (cdp, playwright) and register themselves.
package browser

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/Diwanshumidha/smart-control/internal/config"
	"github.com/Diwanshumidha/smart-control/internal/security/urlpolicy"
)

const DefaultDriver = "cdp"

// FindBrave returns the Brave executable, preferring the configured path.
func FindBrave(configured string) string {
	if configured != "" {
		return configured
	}
	if p := os.Getenv("BRAVE_PATH"); p != "" {
		return p
	}
	for _, n := range []string{"brave", "brave-browser", "brave-browser-stable"} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser"}
	case "windows":
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			if base := os.Getenv(env); base != "" {
				candidates = append(candidates, base+`\BraveSoftware\Brave-Browser\Application\brave.exe`)
			}
		}
	default:
		candidates = []string{"/opt/brave-bin/brave", "/opt/brave.com/brave/brave", "/usr/bin/brave"}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// ResolveExec prefers the real binary over distro wrapper scripts (which add
// flags such as extensions that break automation).
func ResolveExec(p string) string {
	if p == "" {
		return ""
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	if b, err := os.ReadFile(p); err == nil && len(b) > 2 && string(b[:2]) == "#!" {
		for _, c := range []string{"/opt/brave-bin/brave", "/opt/brave.com/brave/brave"} {
			if _, err := os.Stat(c); err == nil {
				return c
			}
		}
	}
	return p
}

// Service owns the active driver and enforces the URL policy.
type Service struct {
	policy  *urlpolicy.Policy
	dataDir string

	mu     sync.Mutex
	cfg    config.Browser
	driver Driver
	active string
}

func New(cfg config.Browser, dataDir string, policy *urlpolicy.Policy) *Service {
	return &Service{cfg: cfg, dataDir: dataDir, policy: policy}
}

func (s *Service) settings(cfg config.Browser, driver string) Settings {
	return Settings{
		ExecPath:   ResolveExec(FindBrave(cfg.Path)),
		ProfileDir: filepath.Join(s.dataDir, "profiles", driver),
		CacheDir:   filepath.Join(s.dataDir, "drivers"),
		Headless:   cfg.Headless,
		Width:      cfg.Width,
		Height:     cfg.Height,
		DebugPort:  cfg.DebugPort,
	}
}

func driverName(cfg config.Browser) string {
	if cfg.Driver == "" {
		return DefaultDriver
	}
	return cfg.Driver
}

// SettingsFor returns the settings driver name would get, e.g. to open the
// shared window before switching to it.
func (s *Service) SettingsFor(name string) Settings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settings(s.cfg, name)
}

// Path is the Brave executable in use.
func (s *Service) Path() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return FindBrave(s.cfg.Path)
}

// Driver is the configured driver name.
func (s *Service) Driver() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return driverName(s.cfg)
}

// Drivers lists registered drivers and whether each can run here.
func (s *Service) Drivers() []DriverInfo {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	return Drivers(s.settings(cfg, "probe"))
}

// Configure applies new settings; the running driver is closed and the next
// visit starts the (possibly different) driver.
func (s *Service) Configure(cfg config.Browser) error {
	if _, err := lookup(driverName(cfg)); err != nil {
		return err
	}
	s.mu.Lock()
	old := s.driver
	s.cfg, s.driver, s.active = cfg, nil, ""
	s.mu.Unlock()
	if old != nil {
		return old.Close()
	}
	return nil
}

func (s *Service) current() (Driver, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.driver != nil {
		return s.driver, nil
	}
	name := driverName(s.cfg)
	reg, err := lookup(name)
	if err != nil {
		return nil, err
	}
	st := s.settings(s.cfg, name)
	if st.ExecPath == "" {
		return nil, errors.New("brave not found: install Brave or set the browser path in Settings")
	}
	if err := os.MkdirAll(st.ProfileDir, 0o700); err != nil {
		return nil, err
	}
	d, err := reg.factory(st)
	if err != nil {
		return nil, fmt.Errorf("browser driver %s: %w", name, err)
	}
	s.driver, s.active = d, name
	return d, nil
}

// Visit opens rawURL (after the URL policy allows it for capability) and
// returns what o asks for. Redirects and the final URL are checked too.
func (s *Service) Visit(ctx context.Context, rawURL, capability string, o Options) (*Page, error) {
	if err := s.policy.Check(ctx, rawURL, capability, "open this page in Brave"); err != nil {
		return nil, err
	}
	d, err := s.current()
	if err != nil {
		return nil, err
	}
	if o.Width == 0 {
		s.mu.Lock()
		o.Width, o.Height = s.cfg.Width, s.cfg.Height
		s.mu.Unlock()
	}
	if o.Scale == 0 {
		o.Scale = 1
	}
	p, err := d.Visit(ctx, Request{
		URL: rawURL, Options: o,
		Allow: func(ctx context.Context, u string) error {
			return s.policy.Check(ctx, u, capability, "redirect target while opening "+rawURL)
		},
	})
	if err != nil {
		return nil, err
	}
	if p.FinalURL != "" && p.FinalURL != rawURL && !strings.HasPrefix(p.FinalURL, "about:") {
		if err := s.policy.Check(ctx, p.FinalURL, capability, "page ended up here after opening "+rawURL); err != nil {
			return nil, err
		}
	}
	return p, nil
}

// OpenTab opens a long-lived tab for interactive browsing. The URL policy is
// enforced by the caller (internal/browser/session) on every navigation.
func (s *Service) OpenTab(ctx context.Context) (context.Context, func(), error) {
	d, err := s.current()
	if err != nil {
		return nil, nil, err
	}
	t, ok := d.(Tabber)
	if !ok {
		return nil, nil, fmt.Errorf("the %s browser driver can't keep tabs open; switch to cdp or shared in Settings → Browser automation", s.Driver())
	}
	return t.OpenTab(ctx)
}

// Policy is the URL policy the service enforces.
func (s *Service) Policy() *urlpolicy.Policy { return s.policy }

// Viewport is the configured default viewport.
func (s *Service) Viewport() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cfg.Width, s.cfg.Height
}

func (s *Service) Close() error {
	s.mu.Lock()
	d := s.driver
	s.driver = nil
	s.mu.Unlock()
	if d != nil {
		return d.Close()
	}
	return nil
}
