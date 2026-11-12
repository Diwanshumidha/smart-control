// Package tray shows the background server in the system tray (Linux:
// StatusNotifierItem over D-Bus, which Waybar, Quickshell, KDE and GNOME's
// AppIndicator extension display), the way Discord stays reachable after its
// window closes.
package tray

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"runtime"

	"fyne.io/systray"
)

// Actions are the menu callbacks. Nil actions are left out of the menu.
type Actions struct {
	OpenApp       func()
	OpenDashboard func()
	OpenApprovals func()
	OpenBrowser   func()
	Restart       func()
	Quit          func()
}

// State is what the tray displays.
type State struct {
	Tunnel  string // up | starting | error | off
	Pending int    // URL approvals waiting for the user
	Running int    // tool calls in flight
	Version string
}

// Tray is a running tray icon.
type Tray struct {
	end     func()
	updates chan State
}

// Available reports whether a tray can be shown in this session.
func Available() bool {
	switch runtime.GOOS {
	case "linux", "freebsd", "openbsd", "netbsd":
		return os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" || os.Getenv("XDG_RUNTIME_DIR") != ""
	case "windows":
		return true
	}
	// macOS needs the tray on the main thread with a Cocoa run loop; the
	// headless server doesn't have one.
	return false
}

// Start shows the tray icon. Call Close to remove it.
func Start(ctx context.Context, acts Actions) *Tray {
	t := &Tray{updates: make(chan State, 8)}
	start, end := systray.RunWithExternalLoop(func() { t.ready(ctx, acts) }, nil)
	t.end = end
	start()
	return t
}

// Update changes the status line, tooltip and icon badge.
func (t *Tray) Update(s State) {
	select {
	case t.updates <- s:
	default:
	}
}

func (t *Tray) Close() {
	if t != nil && t.end != nil {
		t.end()
	}
}

func (t *Tray) ready(ctx context.Context, acts Actions) {
	systray.SetIcon(icon(false))
	systray.SetTitle("Smart Control")
	systray.SetTooltip("Smart Control")

	status := systray.AddMenuItem("Smart Control", "")
	status.Disable()
	approvals := systray.AddMenuItem("No approvals waiting", "Answer the AI's pending URL requests")
	approvals.Disable()
	systray.AddSeparator()

	type entry struct {
		item *systray.MenuItem
		fn   func()
	}
	var entries []entry
	add := func(title, tip string, fn func()) {
		if fn != nil {
			entries = append(entries, entry{systray.AddMenuItem(title, tip), fn})
		}
	}
	add("Open Smart Control", "Open the desktop app", acts.OpenApp)
	add("Open dashboard in browser", "The same UI in your web browser", acts.OpenDashboard)
	add("Open AI browser (log in to sites)", "The Brave window the AI uses; your logins there are shared with it", acts.OpenBrowser)
	systray.AddSeparator()
	add("Restart server", "Restart MCP, tunnel and dashboard (applies tunnel changes)", acts.Restart)
	add("Quit Smart Control", "Stop the server; ChatGPT loses access until you start it again", acts.Quit)

	for _, e := range entries {
		go func(e entry) {
			for range e.item.ClickedCh {
				e.fn()
			}
		}(e)
	}
	if acts.OpenApprovals != nil {
		go func() {
			for range approvals.ClickedCh {
				acts.OpenApprovals()
			}
		}()
	}

	var last State
	for {
		select {
		case <-ctx.Done():
			return
		case s := <-t.updates:
			if s == last {
				continue
			}
			last = s
			line := "Tunnel " + s.Tunnel
			if s.Running > 0 {
				line += fmt.Sprintf(" · %d call(s) running", s.Running)
			}
			status.SetTitle("Smart Control " + s.Version + " — " + line)
			if s.Pending > 0 {
				approvals.SetTitle(fmt.Sprintf("⚠ %d approval(s) waiting — answer now", s.Pending))
				approvals.Enable()
				systray.SetTooltip(fmt.Sprintf("Smart Control: %d approval(s) waiting", s.Pending))
			} else {
				approvals.SetTitle("No approvals waiting")
				approvals.Disable()
				systray.SetTooltip("Smart Control: " + line)
			}
			systray.SetIcon(icon(s.Pending > 0))
		}
	}
}

var iconCache = map[bool][]byte{}

// icon draws the tray icon: a rounded square with a ring, and an amber dot
// while approvals are waiting.
func icon(alert bool) []byte {
	if b, ok := iconCache[alert]; ok {
		return b
	}
	const n = 64
	img := image.NewNRGBA(image.Rect(0, 0, n, n))
	bg := color.NRGBA{R: 0x5b, G: 0x8c, B: 0xff, A: 0xff}
	fg := color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
	dot := color.NRGBA{R: 0xff, G: 0xb0, B: 0x20, A: 0xff}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			if !inRoundRect(fx, fy, 4, 4, n-4, n-4, 14) {
				continue
			}
			c := bg
			d := math.Hypot(fx-n/2, fy-n/2)
			if d > 12 && d < 19 {
				c = fg
			}
			if alert && math.Hypot(fx-48, fy-16) < 11 {
				c = dot
			}
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		log.Printf("tray icon: %v", err)
	}
	iconCache[alert] = buf.Bytes()
	return iconCache[alert]
}

func inRoundRect(x, y, x0, y0, x1, y1, r float64) bool {
	if x < x0 || x > x1 || y < y0 || y > y1 {
		return false
	}
	cx := math.Max(x0+r, math.Min(x, x1-r))
	cy := math.Max(y0+r, math.Min(y, y1-r))
	return math.Hypot(x-cx, y-cy) <= r
}
