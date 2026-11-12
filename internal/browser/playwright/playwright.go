// Package playwright is a browser driver that automates Brave through
// Playwright (playwright-go). The Playwright driver runtime (~50 MB, no
// bundled browsers) is downloaded on first use into the data dir; Brave is
// launched as a persistent Chromium context so logins are kept.
//
// Compared to cdp it intercepts main-frame navigations before they are sent,
// so a disallowed JS navigation never reaches the network.
package playwright

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	pw "github.com/mxschmitt/playwright-go"

	"github.com/Diwanshumidha/smart-control/internal/browser"
)

const idleTimeout = 5 * time.Minute

func init() {
	browser.Register(browser.DriverInfo{
		Name:  "playwright",
		Title: "Playwright",
		Description: "Drives Brave through Playwright. Downloads the Playwright driver (no browsers) on first use; " +
			"blocks disallowed navigations before they are sent.",
	}, New, probe)
}

func probe(s browser.Settings) (bool, string) {
	if s.ExecPath == "" {
		return false, "Brave not found"
	}
	if _, err := os.Stat(filepath.Join(s.CacheDir, "playwright", "package", "cli.js")); err == nil {
		return true, "driver installed"
	}
	if _, err := exec.LookPath("node"); err == nil {
		return true, "driver will be downloaded on first use (uses your node)"
	}
	return true, "driver and Node.js will be downloaded on first use"
}

type Driver struct {
	s browser.Settings

	mu      sync.Mutex
	pw      *pw.Playwright
	ctx     pw.BrowserContext
	busy    int
	lastUse time.Time
	done    chan struct{}
}

func New(s browser.Settings) (browser.Driver, error) {
	d := &Driver{s: s, done: make(chan struct{})}
	go d.reaper()
	return d, nil
}

func (d *Driver) runOptions() *pw.RunOptions {
	return &pw.RunOptions{DriverDirectory: filepath.Join(d.s.CacheDir, "playwright"), SkipInstallBrowsers: true, Verbose: false}
}

// start launches Playwright and Brave (persistent context) if not running.
func (d *Driver) acquire() (pw.BrowserContext, func(), error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.ctx == nil {
		if os.Getenv("PLAYWRIGHT_NODEJS_PATH") == "" {
			if node, err := exec.LookPath("node"); err == nil {
				_ = os.Setenv("PLAYWRIGHT_NODEJS_PATH", node)
			}
		}
		opts := d.runOptions()
		p, err := pw.Run(opts)
		if err != nil {
			// First use: fetch the driver runtime, then retry.
			if ierr := pw.Install(opts); ierr != nil {
				return nil, nil, fmt.Errorf("install playwright driver: %w (run error: %v)", ierr, err)
			}
			if p, err = pw.Run(opts); err != nil {
				return nil, nil, fmt.Errorf("start playwright: %w", err)
			}
		}
		bctx, err := p.Chromium.LaunchPersistentContext(d.s.ProfileDir, pw.BrowserTypeLaunchPersistentContextOptions{
			ExecutablePath: pw.String(d.s.ExecPath),
			Headless:       pw.Bool(d.s.Headless),
			Args:           []string{"--hide-scrollbars", "--mute-audio"},
		})
		if err != nil {
			_ = p.Stop()
			return nil, nil, fmt.Errorf("launch brave via playwright: %w", err)
		}
		d.pw, d.ctx = p, bctx
	}
	d.busy++
	d.lastUse = time.Now()
	return d.ctx, func() {
		d.mu.Lock()
		d.busy--
		d.lastUse = time.Now()
		d.mu.Unlock()
	}, nil
}

func (d *Driver) stopLocked() {
	if d.ctx != nil {
		_ = d.ctx.Close()
		d.ctx = nil
	}
	if d.pw != nil {
		_ = d.pw.Stop()
		d.pw = nil
	}
}

func (d *Driver) reaper() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-d.done:
			return
		case <-t.C:
		}
		d.mu.Lock()
		if d.ctx != nil && d.busy == 0 && time.Since(d.lastUse) > idleTimeout {
			d.stopLocked()
		}
		d.mu.Unlock()
	}
}

func (d *Driver) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	select {
	case <-d.done:
	default:
		close(d.done)
	}
	d.stopLocked()
	return nil
}

func (d *Driver) Visit(ctx context.Context, req browser.Request) (*browser.Page, error) {
	bctx, release, err := d.acquire()
	if err != nil {
		return nil, err
	}
	defer release()

	page, err := bctx.NewPage()
	if err != nil {
		return nil, fmt.Errorf("playwright: %w", err)
	}
	defer page.Close()
	stop := context.AfterFunc(ctx, func() { _ = page.Close() })
	defer stop()

	var (
		blockedMu sync.Mutex
		blocked   error
	)
	block := func(err error) {
		blockedMu.Lock()
		if blocked == nil {
			blocked = err
		}
		blockedMu.Unlock()
	}
	// New main-frame navigations (JS location changes, link clicks) are held
	// until the policy answers.
	first := true
	if err := page.Route("**/*", func(r pw.Route) {
		rq := r.Request()
		if !rq.IsNavigationRequest() || rq.Frame() != page.MainFrame() {
			_ = r.Continue()
			return
		}
		if first {
			first = false // the start URL was checked by the service
			_ = r.Continue()
			return
		}
		if err := req.Allow(ctx, rq.URL()); err != nil {
			block(err)
			_ = r.Abort("blockedbyclient")
			return
		}
		_ = r.Continue()
	}); err != nil {
		return nil, fmt.Errorf("playwright route: %w", err)
	}
	// Server redirects are not routed; check them as they happen and close
	// the page if one is not allowed.
	page.OnRequest(func(rq pw.Request) {
		if rq.RedirectedFrom() == nil || !rq.IsNavigationRequest() || rq.Frame() != page.MainFrame() {
			return
		}
		u := rq.URL()
		go func() {
			if err := req.Allow(ctx, u); err != nil {
				block(err)
				_ = page.Close()
			}
		}()
	})

	o := req.Options
	if err := page.SetViewportSize(o.Width, o.Height); err != nil {
		return nil, fmt.Errorf("playwright: %w", err)
	}
	p := &browser.Page{URL: req.URL}
	err = func() error {
		if _, err := page.Goto(req.URL, pw.PageGotoOptions{Timeout: pw.Float(60_000), WaitUntil: pw.WaitUntilStateLoad}); err != nil {
			return err
		}
		if o.WaitFor != "" {
			if _, err := page.WaitForSelector(o.WaitFor, pw.PageWaitForSelectorOptions{Timeout: pw.Float(30_000)}); err != nil {
				return err
			}
		}
		delay := o.Delay
		if delay == 0 {
			delay = 1500 * time.Millisecond
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
		p.FinalURL = page.URL()
		var err error
		if p.Title, err = page.Title(); err != nil {
			return err
		}
		if o.Text {
			if v, err := page.Evaluate(`document.body ? document.body.innerText : ""`); err == nil {
				p.Text, _ = v.(string)
			}
			if v, err := page.Evaluate(`Array.from(document.querySelectorAll('a[href]')).map(a => a.href).filter(h => h.startsWith('http')).slice(0, 200)`); err == nil {
				if list, ok := v.([]any); ok {
					for _, l := range list {
						if s, ok := l.(string); ok {
							p.Links = append(p.Links, s)
						}
					}
				}
			}
		}
		if o.HTML {
			if p.HTML, err = page.Content(); err != nil {
				return err
			}
		}
		if o.Screenshot {
			if o.Selector != "" {
				p.Screenshot, err = page.Locator(o.Selector).First().Screenshot()
			} else {
				p.Screenshot, err = page.Screenshot(pw.PageScreenshotOptions{FullPage: pw.Bool(o.FullPage), Type: pw.ScreenshotTypePng})
			}
		}
		return err
	}()
	blockedMu.Lock()
	defer blockedMu.Unlock()
	if blocked != nil {
		return nil, blocked
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("playwright: %w", err)
	}
	return p, nil
}
