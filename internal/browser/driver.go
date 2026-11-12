package browser

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Driver is one way of automating a browser (CDP via chromedp, Playwright,
// a remote debugging session, ...). Drivers know nothing about URL policy:
// the Service checks the start URL and the final URL, and passes Allow so the
// driver can veto redirects and navigations in between.
type Driver interface {
	Visit(ctx context.Context, req Request) (*Page, error)
	Close() error
}

// Tabber is implemented by drivers that can keep a tab open across tool
// calls, for interactive browsing (internal/browser/session). The returned
// context is a chromedp tab context; closeTab closes the tab.
type Tabber interface {
	OpenTab(ctx context.Context) (tab context.Context, closeTab func(), err error)
}

// Settings are what every driver factory receives.
type Settings struct {
	ExecPath   string // browser executable (Brave)
	ProfileDir string // persistent profile for this driver
	CacheDir   string // shared root where drivers install runtimes (use a subdir named after the driver)
	Headless   bool
	Width      int
	Height     int
	DebugPort  int // shared driver: the visible browser's debugging port
}

// Request is one page visit.
type Request struct {
	URL string
	Options
	// Allow is called for every main-frame navigation after the first (server
	// redirects, JS location changes). A non-nil error must abort the visit.
	Allow func(ctx context.Context, url string) error
}

// Options for a page visit.
type Options struct {
	Width      int           // viewport width (default config)
	Height     int           // viewport height
	FullPage   bool          // capture the whole scrollable page
	Selector   string        // capture only this element
	WaitFor    string        // CSS selector to wait for before capturing
	Delay      time.Duration // extra settle time after load
	Screenshot bool
	Text       bool // extract visible text and links
	HTML       bool // return the DOM as HTML
	Scale      float64
}

type Page struct {
	URL        string   `json:"url"`
	FinalURL   string   `json:"final_url"`
	Title      string   `json:"title"`
	Text       string   `json:"text,omitempty"`
	Links      []string `json:"links,omitempty"`
	HTML       string   `json:"-"`
	Screenshot []byte   `json:"-"` // PNG
}

// Factory builds a driver; Probe reports whether it can run on this machine.
type (
	Factory func(s Settings) (Driver, error)
	Probe   func(s Settings) (ok bool, detail string)
)

// DriverInfo describes a registered driver (shown in the dashboard).
type DriverInfo struct {
	Name        string `json:"name"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Available   bool   `json:"available"`
	Detail      string `json:"detail,omitempty"`
}

type registration struct {
	info    DriverInfo
	factory Factory
	probe   Probe
}

var (
	regMu    sync.RWMutex
	registry = map[string]registration{}
)

// Register adds a driver. Driver packages call it from init; the app imports
// them (internal/app/drivers.go) so adding a driver is one new package plus
// one import line.
func Register(info DriverInfo, f Factory, p Probe) {
	regMu.Lock()
	defer regMu.Unlock()
	if _, dup := registry[info.Name]; dup {
		panic("browser driver registered twice: " + info.Name)
	}
	registry[info.Name] = registration{info: info, factory: f, probe: p}
}

func lookup(name string) (registration, error) {
	regMu.RLock()
	defer regMu.RUnlock()
	r, ok := registry[name]
	if !ok {
		return r, fmt.Errorf("unknown browser driver %q (have %v)", name, driverNames())
	}
	return r, nil
}

func driverNames() []string {
	var out []string
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// Drivers lists every registered driver with its availability.
func Drivers(s Settings) []DriverInfo {
	regMu.RLock()
	defer regMu.RUnlock()
	var out []DriverInfo
	for _, n := range driverNames() {
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

// DriverNames lists registered driver names.
func DriverNames() []string {
	regMu.RLock()
	defer regMu.RUnlock()
	return driverNames()
}
