// Package cdp is the default browser driver: it drives Brave over the Chrome
// DevTools Protocol with chromedp. One browser process is started lazily with
// a persistent profile and closed after a period of inactivity.
package cdp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/Diwanshumidha/smart-control/internal/browser"
)

const idleTimeout = 5 * time.Minute

func init() {
	browser.Register(browser.DriverInfo{
		Name:        "cdp",
		Title:       "Chrome DevTools Protocol",
		Description: "Drives Brave directly over CDP (chromedp). No extra downloads; fastest start.",
	}, New, func(s browser.Settings) (bool, string) {
		if s.ExecPath == "" {
			return false, "Brave not found"
		}
		return true, s.ExecPath
	})
}

type Driver struct {
	s browser.Settings

	mu       sync.Mutex
	allocCtx context.Context
	cancel   context.CancelFunc
	lastUse  time.Time
	busy     int
	done     chan struct{}
}

func New(s browser.Settings) (browser.Driver, error) {
	d := &Driver{s: s, done: make(chan struct{})}
	go d.reaper()
	return d, nil
}

// acquire returns the root browser context, starting Brave if needed.
func (d *Driver) acquire() (context.Context, func(), error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.allocCtx == nil {
		opts := append(chromedp.DefaultExecAllocatorOptions[:],
			chromedp.ExecPath(d.s.ExecPath),
			chromedp.UserDataDir(d.s.ProfileDir),
			chromedp.WindowSize(d.s.Width, d.s.Height),
			chromedp.Flag("headless", d.s.Headless),
			chromedp.Flag("hide-scrollbars", true),
			chromedp.Flag("mute-audio", true),
		)
		allocCtx, cancelAlloc := chromedp.NewExecAllocator(context.Background(), opts...)
		// A root browser context keeps the process alive between calls.
		rootCtx, cancelRoot := chromedp.NewContext(allocCtx)
		if err := chromedp.Do(rootCtx); err != nil {
			cancelRoot()
			cancelAlloc()
			return nil, nil, fmt.Errorf("start brave: %w", err)
		}
		d.allocCtx = rootCtx
		d.cancel = func() { cancelRoot(); cancelAlloc() }
	}
	d.busy++
	d.lastUse = time.Now()
	root := d.allocCtx
	return root, func() {
		d.mu.Lock()
		d.busy--
		d.lastUse = time.Now()
		d.mu.Unlock()
	}, nil
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
		if d.allocCtx != nil && d.busy == 0 && time.Since(d.lastUse) > idleTimeout {
			d.cancel()
			d.allocCtx, d.cancel = nil, nil
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
	if d.cancel != nil {
		d.cancel()
		d.allocCtx, d.cancel = nil, nil
	}
	return nil
}

// OpenTab keeps a tab open (and the browser alive) until closeTab.
func (d *Driver) OpenTab(ctx context.Context) (context.Context, func(), error) {
	root, release, err := d.acquire()
	if err != nil {
		return nil, nil, err
	}
	tab, cancel := chromedp.NewContext(root)
	if err := chromedp.Do(tab); err != nil {
		cancel()
		release()
		return nil, nil, fmt.Errorf("brave: %w", err)
	}
	return tab, func() { cancel(); release() }, nil
}

func (d *Driver) Visit(ctx context.Context, req browser.Request) (*browser.Page, error) {
	root, release, err := d.acquire()
	if err != nil {
		return nil, err
	}
	defer release()

	tab, closeTab := chromedp.NewContext(root)
	defer closeTab()
	return VisitTab(ctx, tab, req)
}

// VisitTab runs one page visit in tab (a chromedp tab context), re-checking
// every redirect with req.Allow. Other CDP-based drivers reuse it.
func VisitTab(ctx, tab context.Context, req browser.Request) (*browser.Page, error) {
	tab, cancel := context.WithTimeout(tab, 90*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()

	// Re-check every redirect of the main navigation against the policy.
	var (
		blockedMu sync.Mutex
		blocked   error
	)
	if err := chromedp.Do(tab, chromedp.Action[chromedp.Void](func(ctx context.Context, t *chromedp.Target) (chromedp.Void, error) {
		_, err := cdp.Call(ctx, t, network.Enable, network.EnableParams{})
		return chromedp.Void{}, err
	})); err != nil {
		return nil, fmt.Errorf("brave: %w", err)
	}
	reqs := chromedp.Events(tab, network.RequestWillBeSent)
	go func() {
		var navID network.RequestID
		for e, err := range reqs {
			if err != nil {
				return
			}
			if e.Type != network.ResourceTypeDocument || e.Request == nil {
				continue
			}
			if navID == "" {
				navID = e.RequestID
				continue
			}
			if e.RequestID != navID || e.RedirectResponse == nil {
				continue
			}
			if err := req.Allow(ctx, e.Request.URL); err != nil {
				blockedMu.Lock()
				blocked = err
				blockedMu.Unlock()
				cancel()
				return
			}
		}
	}()

	o, rawURL := req.Options, req.URL
	w, h := o.Width, o.Height
	delay := o.Delay
	if delay == 0 {
		delay = 1500 * time.Millisecond
	}
	p := &browser.Page{URL: rawURL}
	err := func() error {
		steps := []chromedp.Action[chromedp.Void]{
			chromedp.EmulateViewport(int64(w), int64(h), chromedp.EmulateScale(o.Scale)),
			chromedp.Navigate(rawURL),
		}
		if o.WaitFor != "" {
			steps = append(steps, chromedp.WaitVisible(chromedp.CSS(o.WaitFor)))
		}
		steps = append(steps, chromedp.Sleep(delay))
		if err := chromedp.Do(tab, steps...); err != nil {
			return err
		}
		var err error
		if p.FinalURL, err = chromedp.Run(tab, chromedp.Location()); err != nil {
			return err
		}
		if p.Title, err = chromedp.Run(tab, chromedp.Title()); err != nil {
			return err
		}
		if o.Text {
			if p.Text, err = chromedp.Run(tab, chromedp.Evaluate[string](`document.body ? document.body.innerText : ""`)); err != nil {
				return err
			}
			p.Links, _ = chromedp.Run(tab, chromedp.Evaluate[[]string](`Array.from(document.querySelectorAll('a[href]')).map(a => a.href).filter(h => h.startsWith('http')).slice(0, 200)`))
		}
		if o.HTML {
			if p.HTML, err = chromedp.Run(tab, chromedp.Evaluate[string](`document.documentElement.outerHTML`)); err != nil {
				return err
			}
		}
		if o.Screenshot {
			switch {
			case o.Selector != "":
				p.Screenshot, err = chromedp.Run(tab, chromedp.Screenshot(chromedp.CSS(o.Selector), chromedp.NodeVisible))
			case o.FullPage:
				p.Screenshot, err = chromedp.Run(tab, chromedp.FullScreenshot(100))
			default:
				p.Screenshot, err = chromedp.Run(tab, chromedp.CaptureScreenshot())
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
		return nil, fmt.Errorf("brave: %w", err)
	}
	return p, nil
}
