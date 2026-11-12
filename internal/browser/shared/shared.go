// Package shared is the browser driver for "the AI uses my browser": one
// visible Brave window with its own saved profile, which you also use. You
// log in to sites there by hand; the AI opens its pages as tabs in that same
// window and so has your sessions. Smart Control starts the window when it
// isn't running, and never closes it.
//
// Why a separate profile and not your everyday one: since Chromium 136 the
// browser ignores --remote-debugging-port on its default profile (it stops
// malware from taking over the logged-in sessions there), so automation needs
// a profile of its own. It lives in ~/.smart-control/browser/profiles/shared.
package shared

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/remote"

	"github.com/Diwanshumidha/smart-control/internal/browser"
	"github.com/Diwanshumidha/smart-control/internal/browser/cdp"
	"github.com/Diwanshumidha/smart-control/internal/osutil"
)

// WindowClass names the window on Linux (WM_CLASS / Wayland app id), so a
// window rule can place it, e.g. on its own workspace.
const WindowClass = "smart-control-brave"

func init() {
	browser.Register(browser.DriverInfo{
		Name:  "shared",
		Title: "Your Brave window (shared, logged in)",
		Description: "One visible Brave window with its own saved profile. Open it, log in to sites yourself, " +
			"and the AI opens its pages as tabs there with your sessions.",
	}, New, probe)
}

func probe(s browser.Settings) (bool, string) {
	if s.ExecPath == "" {
		return false, "Brave not found"
	}
	if v, err := version(context.Background(), s.DebugPort); err == nil {
		return true, fmt.Sprintf("running: %s on 127.0.0.1:%d", v, s.DebugPort)
	}
	return true, fmt.Sprintf("starts when needed (port %d)", s.DebugPort)
}

type Driver struct {
	s browser.Settings

	mu     sync.Mutex
	alloc  context.Context
	cancel context.CancelFunc
}

func New(s browser.Settings) (browser.Driver, error) {
	if s.DebugPort == 0 {
		s.DebugPort = 9333
	}
	return &Driver{s: s}, nil
}

func endpoint(port int) string { return fmt.Sprintf("http://127.0.0.1:%d", port) }

// version returns the browser's name if something answers DevTools on port.
func version(ctx context.Context, port int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint(port)+"/json/version", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var v struct {
		Browser string `json:"Browser"`
	}
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil {
		return "", err
	}
	return v.Browser, nil
}

// Open shows the shared window, starting Brave when it isn't running, and
// opens url in it (blank: just bring it up). It's what "Open AI browser"
// in the dashboard, the tray and `smart-control browser` call.
func Open(ctx context.Context, s browser.Settings, url string) error {
	if s.DebugPort == 0 {
		s.DebugPort = 9333
	}
	if _, err := version(ctx, s.DebugPort); err == nil {
		if url == "" {
			url = "about:blank"
		}
		// A running window: ask it for a new tab (this also raises it).
		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, endpoint(s.DebugPort)+"/json/new?"+url, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		res.Body.Close()
		return nil
	}
	return launch(ctx, s, url)
}

func launch(ctx context.Context, s browser.Settings, url string) error {
	if s.ExecPath == "" {
		return fmt.Errorf("brave not found: install Brave or set the browser path in Settings")
	}
	if err := os.MkdirAll(s.ProfileDir, 0o700); err != nil {
		return err
	}
	args := []string{
		"--user-data-dir=" + s.ProfileDir,
		fmt.Sprintf("--remote-debugging-port=%d", s.DebugPort),
		"--no-first-run", "--no-default-browser-check",
		"--class=" + WindowClass,
	}
	if url != "" {
		args = append(args, url)
	}
	cmd := exec.Command(s.ExecPath, args...)
	if err := osutil.StartBackground(cmd); err != nil {
		return fmt.Errorf("start brave: %w", err)
	}
	go cmd.Wait()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := version(ctx, s.DebugPort); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return fmt.Errorf("brave didn't open its debugging port %d within 20s. If a Brave with this profile was already open without it, close that window and try again", s.DebugPort)
}

// connect returns an allocator attached to the shared window, starting it
// when needed.
func (d *Driver) connect(ctx context.Context) (context.Context, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.alloc != nil {
		if _, err := version(ctx, d.s.DebugPort); err == nil {
			return d.alloc, nil
		}
		d.cancel() // the window was closed; start over
		d.alloc, d.cancel = nil, nil
	}
	if _, err := version(ctx, d.s.DebugPort); err != nil {
		if err := launch(ctx, d.s, ""); err != nil {
			return nil, err
		}
	}
	d.alloc, d.cancel = remote.NewAllocator(context.Background(), endpoint(d.s.DebugPort))
	return d.alloc, nil
}

// Visit opens the page as a new tab in the shared window, so it carries your
// logins, and closes the tab afterwards. The tab is made active while the AI
// works in it: Chromium doesn't paint background tabs, so screenshots of
// them would hang.
func (d *Driver) Visit(ctx context.Context, req browser.Request) (*browser.Page, error) {
	alloc, err := d.connect(ctx)
	if err != nil {
		return nil, err
	}
	tab, closeTab := chromedp.NewContext(alloc, chromedp.WithNewWindow(false))
	defer closeTab()
	if err := chromedp.Do(tab); err != nil {
		return nil, fmt.Errorf("brave: %w", err)
	}
	if c := chromedp.FromContext(tab); c != nil && c.Target != nil {
		_, _ = chromedp.CallBrowser(tab, target.ActivateTarget, target.ActivateTargetParams{TargetID: c.Target.TargetID})
	}
	return cdp.VisitTab(ctx, tab, req)
}

// OpenTab opens a tab in the shared window that stays until closeTab.
func (d *Driver) OpenTab(ctx context.Context) (context.Context, func(), error) {
	alloc, err := d.connect(ctx)
	if err != nil {
		return nil, nil, err
	}
	tab, cancel := chromedp.NewContext(alloc, chromedp.WithNewWindow(false))
	if err := chromedp.Do(tab); err != nil {
		cancel()
		return nil, nil, fmt.Errorf("brave: %w", err)
	}
	return tab, cancel, nil
}

// Close detaches from the window and leaves it open: it's your browser.
func (d *Driver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cancel != nil {
		d.cancel()
		d.alloc, d.cancel = nil, nil
	}
	return nil
}
