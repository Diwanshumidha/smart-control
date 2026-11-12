// Package session gives the AI browser tabs that stay open across tool calls,
// so it can work a page like a person: read it, click, type, press keys,
// scroll, wait, take screenshots.
//
// Safety:
//   - Every main-frame navigation of a tab (typed URL, link click, form
//     submit, script redirect, back/forward) is paused and checked against
//     the URL policy for "browse" before it is sent.
//   - Acting on a page (click, type, press, select, eval) needs the
//     "interact" capability for its host, which "*" rules never grant, so the
//     user is asked per site before the AI acts as them there.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/kb"

	"github.com/Diwanshumidha/smart-control/internal/browser"
	"github.com/Diwanshumidha/smart-control/internal/security/urlpolicy"
)

const (
	maxTabs     = 6
	idleTimeout = 20 * time.Minute
)

// Manager owns the AI's open tabs.
type Manager struct {
	svc *browser.Service

	mu   sync.Mutex
	tabs map[string]*Tab
	seq  int
	done chan struct{}
}

func New(svc *browser.Service) *Manager {
	m := &Manager{svc: svc, tabs: map[string]*Tab{}, done: make(chan struct{})}
	go m.reaper()
	return m
}

// Tab is one open tab.
type Tab struct {
	ID string

	ctx      context.Context
	closeTab func()
	visible  bool // shared driver: a real window the user sees

	mu      sync.Mutex // one action at a time
	lastUse time.Time

	navMu   sync.Mutex
	blocked error  // last navigation the policy refused
	waiting string // URL waiting for the user's approval
}

// TabInfo is listed by Tabs.
type TabInfo struct {
	ID    string `json:"id"`
	URL   string `json:"url"`
	Title string `json:"title"`
	Idle  string `json:"idle"`
}

func (m *Manager) policy() *urlpolicy.Policy { return m.svc.Policy() }

// Open opens rawURL in a new tab (after the policy allows it).
func (m *Manager) Open(ctx context.Context, rawURL string) (*Tab, error) {
	if err := m.policy().Check(ctx, rawURL, urlpolicy.CapBrowse, "open this page in a browser tab the AI controls"); err != nil {
		return nil, err
	}
	m.mu.Lock()
	if len(m.tabs) >= maxTabs {
		m.closeOldestLocked()
	}
	m.mu.Unlock()

	tabCtx, closeTab, err := m.svc.OpenTab(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.seq++
	t := &Tab{ID: "t" + strconv.Itoa(m.seq), ctx: tabCtx, closeTab: closeTab, visible: m.svc.Driver() == "shared", lastUse: time.Now()}
	m.tabs[t.ID] = t
	m.mu.Unlock()

	if err := t.guard(m.policy()); err != nil {
		m.Close(t.ID)
		return nil, err
	}
	if t.visible {
		_ = t.bringToFront()
	} else {
		w, h := m.svc.Viewport()
		_ = chromedp.Do(t.ctx, chromedp.EmulateViewport(int64(w), int64(h)))
	}
	if err := t.navigate(ctx, rawURL); err != nil {
		m.Close(t.ID)
		return nil, err
	}
	return t, nil
}

// Get returns an open tab; "" means the most recently used one.
func (m *Manager) Get(id string) (*Tab, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id == "" {
		var last *Tab
		for _, t := range m.tabs {
			if last == nil || t.lastUse.After(last.lastUse) {
				last = t
			}
		}
		if last == nil {
			return nil, errors.New("no browser tab is open; call browser_open first")
		}
		return last, nil
	}
	t, ok := m.tabs[id]
	if !ok {
		return nil, fmt.Errorf("no open tab %q (it may have been closed after %s idle); call browser_tabs or browser_open", id, idleTimeout)
	}
	return t, nil
}

func (m *Manager) Tabs(ctx context.Context) []TabInfo {
	m.mu.Lock()
	list := make([]*Tab, 0, len(m.tabs))
	for _, t := range m.tabs {
		list = append(list, t)
	}
	m.mu.Unlock()
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	out := make([]TabInfo, 0, len(list))
	for _, t := range list {
		info := TabInfo{ID: t.ID, Idle: time.Since(t.lastUse).Round(time.Second).String()}
		c, cancel := context.WithTimeout(t.ctx, 3*time.Second)
		info.URL, _ = chromedp.Run(c, chromedp.Location())
		info.Title, _ = chromedp.Run(c, chromedp.Title())
		cancel()
		out = append(out, info)
	}
	return out
}

func (m *Manager) Close(id string) error {
	m.mu.Lock()
	t, ok := m.tabs[id]
	delete(m.tabs, id)
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no open tab %q", id)
	}
	t.closeTab()
	return nil
}

// CloseAll closes every tab (driver switch, shutdown).
func (m *Manager) CloseAll() {
	m.mu.Lock()
	tabs := m.tabs
	m.tabs = map[string]*Tab{}
	m.mu.Unlock()
	for _, t := range tabs {
		t.closeTab()
	}
}

func (m *Manager) Shutdown() {
	select {
	case <-m.done:
	default:
		close(m.done)
	}
	m.CloseAll()
}

func (m *Manager) closeOldestLocked() {
	var old *Tab
	for _, t := range m.tabs {
		if old == nil || t.lastUse.Before(old.lastUse) {
			old = t
		}
	}
	if old != nil {
		delete(m.tabs, old.ID)
		go old.closeTab()
	}
}

func (m *Manager) reaper() {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-m.done:
			return
		case <-tick.C:
		}
		m.mu.Lock()
		for id, t := range m.tabs {
			if time.Since(t.lastUse) > idleTimeout && t.mu.TryLock() {
				delete(m.tabs, id)
				t.mu.Unlock()
				go t.closeTab()
			}
		}
		m.mu.Unlock()
	}
}

// --- navigation guard ---

// guard pauses every document request of the tab; main-frame navigations
// go through the URL policy, sub-frames pass (the page they're in was allowed).
func (t *Tab) guard(pol *urlpolicy.Policy) error {
	tree, err := chromedp.Call(t.ctx, page.GetFrameTree, cdp.Empty{})
	if err != nil {
		return fmt.Errorf("brave: %w", err)
	}
	mainFrame := tree.FrameTree.Frame.ID
	events := chromedp.Events(t.ctx, fetch.RequestPaused)
	if _, err := chromedp.Call(t.ctx, fetch.Enable, fetch.EnableParams{Patterns: []*fetch.RequestPattern{
		{URLPattern: "*", ResourceType: network.ResourceTypeDocument, RequestStage: fetch.RequestStageRequest},
	}}); err != nil {
		return fmt.Errorf("brave: %w", err)
	}
	go func() {
		for ev, err := range events {
			if err != nil {
				return
			}
			go t.decide(pol, ev, mainFrame)
		}
	}()
	return nil
}

func (t *Tab) decide(pol *urlpolicy.Policy, ev fetch.EventRequestPaused, mainFrame cdp.FrameID) {
	u := ev.Request.URL
	if ev.FrameID != mainFrame || strings.HasPrefix(u, "about:") || strings.HasPrefix(u, "data:") {
		_, _ = chromedp.Call(t.ctx, fetch.ContinueRequest, fetch.ContinueRequestParams{RequestID: ev.RequestID})
		return
	}
	t.navMu.Lock()
	t.waiting = u
	t.navMu.Unlock()
	err := pol.Check(t.ctx, u, urlpolicy.CapBrowse, "the AI's browser tab is navigating here")
	t.navMu.Lock()
	t.waiting = ""
	if err != nil {
		t.blocked = fmt.Errorf("navigation to %s was blocked: %w", u, err)
	}
	t.navMu.Unlock()
	if err != nil {
		_, _ = chromedp.Call(t.ctx, fetch.FailRequest, fetch.FailRequestParams{RequestID: ev.RequestID, ErrorReason: network.ErrorReasonBlockedByClient})
		return
	}
	_, _ = chromedp.Call(t.ctx, fetch.ContinueRequest, fetch.ContinueRequestParams{RequestID: ev.RequestID})
}

// afterAction reports a navigation the policy blocked during the action and,
// if Brave left the tab on its "blocked" error page, steps back to the page
// the tab was on, so the AI can carry on there.
func (t *Tab) afterAction(c context.Context) error {
	blocked := t.takeBlocked()
	if blocked == nil {
		return nil
	}
	loc, err := chromedp.Run(c, chromedp.Location())
	if err != nil || !strings.HasPrefix(loc, "chrome-error:") {
		return blocked
	}
	if h, err := chromedp.Call(c, page.GetNavigationHistory, cdp.Empty{}); err == nil && h.CurrentIndex > 0 {
		_, _ = chromedp.Call(c, page.NavigateToHistoryEntry, page.NavigateToHistoryEntryParams{EntryID: h.Entries[h.CurrentIndex-1].ID})
		settle(c)
	}
	return blocked
}

// takeBlocked returns and clears the last blocked navigation.
func (t *Tab) takeBlocked() error {
	t.navMu.Lock()
	defer t.navMu.Unlock()
	err := t.blocked
	t.blocked = nil
	return err
}

func (t *Tab) waitingFor() string {
	t.navMu.Lock()
	defer t.navMu.Unlock()
	return t.waiting
}

// --- actions ---

// Do runs f with the tab locked and marks it used.
func (t *Tab) Do(ctx context.Context, timeout time.Duration, f func(ctx context.Context) error) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastUse = time.Now()
	c, cancel := context.WithTimeout(t.ctx, timeout)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()
	err := f(c)
	if errors.Is(err, context.DeadlineExceeded) {
		err = fmt.Errorf("timed out after %s", timeout)
	}
	return err
}

func (t *Tab) navigate(ctx context.Context, rawURL string) error {
	return t.Do(ctx, 3*time.Minute, func(c context.Context) error {
		err := chromedp.Do(c, chromedp.Navigate(rawURL))
		if b := t.afterAction(c); b != nil {
			return b
		}
		if err != nil {
			return fmt.Errorf("brave: %w", err)
		}
		settle(c)
		return nil
	})
}

// Navigate goes to a URL, or "back", "forward", "reload". History moves use
// the raw DevTools commands: a page restored from the back/forward cache
// fires no load event, which chromedp's NavigateBack would wait for forever.
func (m *Manager) Navigate(ctx context.Context, t *Tab, to string) error {
	switch to {
	case "back", "forward", "reload":
	default:
		if err := m.policy().Check(ctx, to, urlpolicy.CapBrowse, "open this page in a browser tab the AI controls"); err != nil {
			return err
		}
		return t.navigate(ctx, to)
	}
	return t.Do(ctx, 3*time.Minute, func(c context.Context) error {
		var err error
		if to == "reload" {
			_, err = chromedp.Call(c, page.Reload, page.ReloadParams{})
		} else {
			var h page.GetNavigationHistoryResult
			if h, err = chromedp.Call(c, page.GetNavigationHistory, cdp.Empty{}); err == nil {
				i := int(h.CurrentIndex) - 1
				if to == "forward" {
					i = int(h.CurrentIndex) + 1
				}
				if i < 0 || i >= len(h.Entries) {
					return fmt.Errorf("can't go %s: no history entry", to)
				}
				_, err = chromedp.Call(c, page.NavigateToHistoryEntry, page.NavigateToHistoryEntryParams{EntryID: h.Entries[i].ID})
			}
		}
		if err != nil {
			return fmt.Errorf("brave: %w", err)
		}
		settle(c)
		return t.afterAction(c)
	})
}

// settle waits for the page to finish what an action started (navigation,
// network) without failing when it doesn't.
func settle(ctx context.Context) {
	time.Sleep(400 * time.Millisecond)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		state, err := chromedp.Run(ctx, chromedp.Evaluate[string](`document.readyState`))
		if err == nil && state == "complete" {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Target selects an element: a ref from the last snapshot, a CSS selector,
// or visible text.
type Target struct {
	Ref      string
	Selector string
	Text     string
}

func (tg Target) String() string {
	switch {
	case tg.Ref != "":
		return "element [" + tg.Ref + "]"
	case tg.Selector != "":
		return "selector " + tg.Selector
	}
	return fmt.Sprintf("element with text %q", tg.Text)
}

// resolve returns a CSS selector for tg.
func resolve(ctx context.Context, tg Target) (string, error) {
	switch {
	case tg.Ref != "":
		ref := strings.Trim(tg.Ref, "[] ")
		if _, err := strconv.Atoi(ref); err != nil {
			return "", fmt.Errorf("ref %q should be a number from the snapshot", tg.Ref)
		}
		sel := `[data-sc-ref="` + ref + `"]`
		n, err := chromedp.Run(ctx, chromedp.Evaluate[int](`document.querySelectorAll('`+sel+`').length`))
		if err != nil || n == 0 {
			return "", fmt.Errorf("element [%s] isn't on the page any more (it changed); take a new browser_snapshot", ref)
		}
		return sel, nil
	case tg.Selector != "":
		return tg.Selector, nil
	case tg.Text != "":
		q, _ := json.Marshal(tg.Text)
		ref, err := chromedp.Run(ctx, chromedp.Evaluate[string](snapshotJS(400)+`;(() => {
  const want = `+string(q)+`.toLowerCase().trim();
  const els = [...document.querySelectorAll('[data-sc-ref]')];
  const name = e => (e.getAttribute('aria-label') || e.innerText || e.value || e.getAttribute('placeholder') || '').replace(/\s+/g,' ').trim().toLowerCase();
  const hit = els.find(e => name(e) === want) || els.find(e => name(e).includes(want));
  return hit ? hit.getAttribute('data-sc-ref') : '';
})()`))
		if err != nil {
			return "", err
		}
		if ref == "" {
			return "", fmt.Errorf("no clickable element with text %q; take a browser_snapshot and use a ref", tg.Text)
		}
		return `[data-sc-ref="` + ref + `"]`, nil
	}
	return "", errors.New("give ref, selector or text")
}

// rect is an element's box in viewport CSS pixels.
type rect struct{ X, Y, W, H float64 }

func (r rect) center() (float64, float64) { return r.X + r.W/2, r.Y + r.H/2 }

// locate scrolls the element into view and returns its box. It reads the
// page with JavaScript rather than chromedp's DOM queries, which lose track
// of the document when a page comes back from the back/forward cache.
func locate(c context.Context, sel string, tg Target) (rect, error) {
	q, _ := json.Marshal(sel)
	var r struct {
		Found, Visible bool
		X, Y, W, H     float64
	}
	raw, err := chromedp.Run(c, chromedp.Evaluate[string](`(() => {
  const e = document.querySelector(`+string(q)+`); if (!e) return JSON.stringify({Found: false});
  e.scrollIntoView({block: 'center', inline: 'center', behavior: 'instant'});
  const b = e.getBoundingClientRect(), s = getComputedStyle(e);
  return JSON.stringify({Found: true, Visible: b.width > 0 && b.height > 0 && s.visibility !== 'hidden' && s.display !== 'none',
    X: b.left, Y: b.top, W: b.width, H: b.height}); })()`))
	if err != nil {
		return rect{}, err
	}
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return rect{}, err
	}
	if !r.Found {
		return rect{}, fmt.Errorf("%s isn't on the page; take a new browser_snapshot", tg)
	}
	if !r.Visible {
		return rect{}, fmt.Errorf("%s is hidden (zero size or display:none)", tg)
	}
	return rect{r.X, r.Y, r.W, r.H}, nil
}

// allowInteract checks the interact capability for the tab's current page.
func (m *Manager) allowInteract(ctx context.Context, c context.Context, what string) error {
	u, err := chromedp.Run(c, chromedp.Location())
	if err != nil {
		return err
	}
	if strings.HasPrefix(u, "about:") {
		return nil
	}
	return m.policy().Check(ctx, u, urlpolicy.CapInteract, "let the AI "+what+" on this page, acting as you")
}

func (m *Manager) Click(ctx context.Context, t *Tab, tg Target, double bool) error {
	return t.Do(ctx, 3*time.Minute, func(c context.Context) error {
		sel, err := resolve(c, tg)
		if err != nil {
			return err
		}
		if err := m.allowInteract(ctx, c, "click "+tg.String()); err != nil {
			return err
		}
		r, err := locate(c, sel, tg)
		if err != nil {
			return err
		}
		x, y := r.center()
		var opts []chromedp.MouseOption
		if double {
			opts = append(opts, chromedp.ClickCount(2))
		}
		if err := chromedp.Do(c, chromedp.MouseEvent(input.DispatchMouseEventTypeMouseMoved, x, y), chromedp.MouseClickXY(x, y, opts...)); err != nil {
			return fmt.Errorf("click %s: %w", tg, err)
		}
		settle(c)
		return t.afterAction(c)
	})
}

func (m *Manager) Hover(ctx context.Context, t *Tab, tg Target) error {
	return t.Do(ctx, 30*time.Second, func(c context.Context) error {
		sel, err := resolve(c, tg)
		if err != nil {
			return err
		}
		r, err := locate(c, sel, tg)
		if err != nil {
			return err
		}
		x, y := r.center()
		if err := chromedp.Do(c, chromedp.MouseEvent(input.DispatchMouseEventTypeMouseMoved, x, y)); err != nil {
			return err
		}
		time.Sleep(500 * time.Millisecond)
		return nil
	})
}

// Type types text into an element (input, textarea or rich editor).
func (m *Manager) Type(ctx context.Context, t *Tab, tg Target, text string, clear, submit bool) error {
	return t.Do(ctx, 3*time.Minute, func(c context.Context) error {
		sel, err := resolve(c, tg)
		if err != nil {
			return err
		}
		if err := m.allowInteract(ctx, c, "type into "+tg.String()); err != nil {
			return err
		}
		r, err := locate(c, sel, tg)
		if err != nil {
			return err
		}
		x, y := r.center()
		if err := chromedp.Do(c, chromedp.MouseClickXY(x, y)); err != nil {
			return fmt.Errorf("focus %s: %w", tg, err)
		}
		if clear {
			q, _ := json.Marshal(sel)
			_, err := chromedp.Run(c, chromedp.Evaluate[bool](`(() => {
  const e = document.querySelector(`+string(q)+`); if (!e) return false; e.focus();
  if (e.isContentEditable) { document.execCommand('selectAll'); document.execCommand('delete'); }
  else { const set = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(e), 'value')?.set; set ? set.call(e, '') : (e.value = ''); e.dispatchEvent(new Event('input', {bubbles: true})); }
  return true; })()`))
			if err != nil {
				return err
			}
		}
		if err := chromedp.Do(c, chromedp.KeyEvent(text)); err != nil {
			return fmt.Errorf("type: %w", err)
		}
		if submit {
			if err := chromedp.Do(c, chromedp.KeyEvent(kb.Enter)); err != nil {
				return err
			}
			settle(c)
		}
		return t.afterAction(c)
	})
}

var keyNames = map[string]string{
	"enter": kb.Enter, "return": kb.Enter, "tab": kb.Tab, "escape": kb.Escape, "esc": kb.Escape,
	"backspace": kb.Backspace, "delete": kb.Delete, "space": " ",
	"arrowup": kb.ArrowUp, "arrowdown": kb.ArrowDown, "arrowleft": kb.ArrowLeft, "arrowright": kb.ArrowRight,
	"up": kb.ArrowUp, "down": kb.ArrowDown, "left": kb.ArrowLeft, "right": kb.ArrowRight,
	"pageup": kb.PageUp, "pagedown": kb.PageDown, "home": kb.Home, "end": kb.End,
}

// Press sends a key or combination such as "Enter", "Escape", "Control+a".
func (m *Manager) Press(ctx context.Context, t *Tab, combo string) error {
	parts := strings.Split(combo, "+")
	key := parts[len(parts)-1]
	var mods []chromedp.Modifier
	for _, p := range parts[:len(parts)-1] {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "ctrl", "control":
			mods = append(mods, chromedp.ModifierCtrl)
		case "shift":
			mods = append(mods, chromedp.ModifierShift)
		case "alt", "option":
			mods = append(mods, chromedp.ModifierAlt)
		case "meta", "cmd", "command", "super":
			mods = append(mods, chromedp.ModifierMeta)
		default:
			return fmt.Errorf("unknown modifier %q (use Control, Shift, Alt, Meta)", p)
		}
	}
	if k, ok := keyNames[strings.ToLower(strings.TrimSpace(key))]; ok {
		key = k
	} else if len([]rune(key)) != 1 {
		return fmt.Errorf("unknown key %q (Enter, Tab, Escape, Backspace, Delete, Space, Arrow keys, PageUp/Down, Home, End, or one character)", key)
	}
	return t.Do(ctx, 3*time.Minute, func(c context.Context) error {
		if err := m.allowInteract(ctx, c, "press "+combo); err != nil {
			return err
		}
		var opts []chromedp.KeyOption
		if len(mods) > 0 {
			opts = append(opts, chromedp.KeyModifiers(mods...))
		}
		if err := chromedp.Do(c, chromedp.KeyEvent(key, opts...)); err != nil {
			return err
		}
		settle(c)
		return t.afterAction(c)
	})
}

// Select picks an option of a <select> by value or visible label.
func (m *Manager) Select(ctx context.Context, t *Tab, tg Target, option string) error {
	return t.Do(ctx, time.Minute, func(c context.Context) error {
		sel, err := resolve(c, tg)
		if err != nil {
			return err
		}
		if err := m.allowInteract(ctx, c, "choose an option in "+tg.String()); err != nil {
			return err
		}
		qs, _ := json.Marshal(sel)
		qo, _ := json.Marshal(option)
		got, err := chromedp.Run(c, chromedp.Evaluate[string](`(() => {
  const e = document.querySelector(`+string(qs)+`); if (!e || e.tagName !== 'SELECT') return 'not a <select>';
  const want = `+string(qo)+`;
  const o = [...e.options].find(o => o.value === want) || [...e.options].find(o => o.label.trim().toLowerCase() === want.trim().toLowerCase());
  if (!o) return 'no option ' + JSON.stringify(want) + '; options: ' + [...e.options].map(o => o.label.trim()).join(', ');
  e.value = o.value; e.dispatchEvent(new Event('input', {bubbles: true})); e.dispatchEvent(new Event('change', {bubbles: true}));
  return ''; })()`))
		if err != nil {
			return err
		}
		if got != "" {
			return errors.New(got)
		}
		settle(c)
		return nil
	})
}

// Scroll scrolls the page ("down", "up", "top", "bottom") or an element into view.
func (m *Manager) Scroll(ctx context.Context, t *Tab, direction string, pixels int, tg *Target) error {
	return t.Do(ctx, 30*time.Second, func(c context.Context) error {
		if tg != nil {
			sel, err := resolve(c, *tg)
			if err != nil {
				return err
			}
			_, err = locate(c, sel, *tg)
			return err
		}
		js := ""
		amount := "Math.round(innerHeight*0.85)"
		if pixels > 0 {
			amount = strconv.Itoa(pixels)
		}
		switch direction {
		case "", "down":
			js = "scrollBy(0, " + amount + ")"
		case "up":
			js = "scrollBy(0, -" + amount + ")"
		case "top":
			js = "scrollTo(0, 0)"
		case "bottom":
			js = "scrollTo(0, document.documentElement.scrollHeight)"
		default:
			return fmt.Errorf("direction must be down, up, top or bottom")
		}
		_, err := chromedp.Run(c, chromedp.Evaluate[any](js+"; 0"))
		time.Sleep(400 * time.Millisecond) // lazy content
		return err
	})
}

// Wait waits until text appears on the page or a selector matches.
func (m *Manager) Wait(ctx context.Context, t *Tab, text, selector string, gone bool, timeout time.Duration) error {
	if text == "" && selector == "" {
		return errors.New("give text or selector")
	}
	if timeout <= 0 || timeout > time.Minute {
		timeout = 15 * time.Second
	}
	qt, _ := json.Marshal(text)
	qs, _ := json.Marshal(selector)
	cond := fmt.Sprintf(`(() => { const t = %s, s = %s;
  const has = (s ? !!document.querySelector(s) : true) && (t ? (document.body?.innerText || '').toLowerCase().includes(t.toLowerCase()) : true);
  return %v ? !has : has; })()`, qt, qs, gone)
	return t.Do(ctx, timeout+5*time.Second, func(c context.Context) error {
		deadline := time.Now().Add(timeout)
		for time.Now().Before(deadline) {
			ok, err := chromedp.Run(c, chromedp.Evaluate[bool](cond))
			if err == nil && ok {
				return nil
			}
			time.Sleep(300 * time.Millisecond)
		}
		return fmt.Errorf("still waiting after %s", timeout)
	})
}

// Screenshot captures the viewport, the full page or one element (PNG).
func (m *Manager) Screenshot(ctx context.Context, t *Tab, full bool, tg *Target) ([]byte, error) {
	var out []byte
	err := t.Do(ctx, time.Minute, func(c context.Context) error {
		if t.visible {
			_ = t.bringToFront()
		}
		var err error
		switch {
		case tg != nil:
			sel, rerr := resolve(c, *tg)
			if rerr != nil {
				return rerr
			}
			r, lerr := locate(c, sel, *tg)
			if lerr != nil {
				return lerr
			}
			var res page.CaptureScreenshotResult
			res, err = chromedp.Call(c, page.CaptureScreenshot, page.CaptureScreenshotParams{Clip: &page.Viewport{X: r.X, Y: r.Y, Width: r.W, Height: r.H, Scale: 1}})
			out = res.Data
		case full:
			out, err = chromedp.Run(c, chromedp.FullScreenshot(100))
		default:
			out, err = chromedp.Run(c, chromedp.CaptureScreenshot())
		}
		return err
	})
	return out, err
}

// Eval runs JavaScript in the page and returns its JSON result.
func (m *Manager) Eval(ctx context.Context, t *Tab, js string) (string, error) {
	var out string
	err := t.Do(ctx, 3*time.Minute, func(c context.Context) error {
		if err := m.allowInteract(ctx, c, "run a script"); err != nil {
			return err
		}
		v, err := chromedp.Run(c, chromedp.Evaluate[any](js, chromedp.EvalAwaitPromise))
		if err != nil {
			return err
		}
		b, _ := json.Marshal(v)
		out = string(b)
		return t.afterAction(c)
	})
	return out, err
}

func (t *Tab) bringToFront() error {
	_, err := chromedp.Call(t.ctx, page.BringToFront, cdp.Empty{})
	return err
}
