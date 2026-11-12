package browserkit

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Diwanshumidha/smart-control/internal/browser/session"
	"github.com/Diwanshumidha/smart-control/internal/mcpx"
	"github.com/Diwanshumidha/smart-control/internal/resources"
)

const interactiveInstructions = `Interactive browsing (browser_* tools) keeps tabs open between calls, so you can work a page like a person:
browser_open → read the snapshot → browser_click / browser_type / browser_press / browser_select by ref → read the new snapshot that every action returns.
Refs are the [numbers] in the latest snapshot; they change when the page changes, so use refs from the most recent result. If a ref is stale, call browser_snapshot.
With the "shared" browser driver these tabs open in the user's own logged-in Brave window, so you act as the user on those sites.
Clicking, typing, pressing keys and running scripts need the user's "interact" permission per site; the call waits while they're asked.
NEVER publish, post, send messages, comment, like, follow, buy, delete, accept invitations or change account settings unless the user asked for that exact action in this conversation. Stop before the final button and ask them.`

// Element targets accepted by the action tools.
type targetIn struct {
	Ref      string `json:"ref,omitempty" jsonschema:"element number from the latest snapshot, e.g. 12"`
	Selector string `json:"selector,omitempty" jsonschema:"CSS selector, when there is no ref"`
	Text     string `json:"text,omitempty" jsonschema:"visible text/label of the element, when there is no ref"`
}

func (in targetIn) target() session.Target {
	return session.Target{Ref: in.Ref, Selector: in.Selector, Text: in.Text}
}

func (in targetIn) set() bool { return in.Ref != "" || in.Selector != "" || in.Text != "" }

type tabIn struct {
	TabID string `json:"tab_id,omitempty" jsonschema:"tab from browser_open (default: the last used tab)"`
}

type snapOpts struct {
	MaxChars    int `json:"max_chars,omitempty" jsonschema:"max page-text characters in the returned snapshot (default 6000)"`
	MaxElements int `json:"max_elements,omitempty" jsonschema:"max interactive elements listed (default 150)"`
}

func (t *Toolset) registerInteractive(r *mcpx.Registry) {
	mcpx.Add(r, "browser_open", "Open a URL in a new browser tab that stays open for further actions. Returns the tab_id and a snapshot: interactive elements with [ref] numbers, plus the page text. Requires URL permission (browse).", t.bOpen)
	mcpx.Add(r, "browser_navigate", "In an open tab, go to a URL, or to=back / forward / reload. Returns the new snapshot.", t.bNavigate)
	mcpx.Add(r, "browser_snapshot", "Read the current page of a tab again: interactive elements with [ref] numbers and the visible text. Use after the page changes on its own.", t.bSnapshot)
	mcpx.Add(r, "browser_click", "Click an element (by ref from the latest snapshot, CSS selector or visible text). Needs the site's interact permission. Returns the new snapshot.", t.bClick)
	mcpx.Add(r, "browser_type", "Type text into an input, textarea or rich editor (by ref). clear=true replaces existing text; submit=true presses Enter afterwards. Needs interact permission. Returns the new snapshot.", t.bType)
	mcpx.Add(r, "browser_press", "Press a key or combination in the tab: Enter, Tab, Escape, Backspace, ArrowDown, PageDown, Control+a, … Needs interact permission. Returns the new snapshot.", t.bPress)
	mcpx.Add(r, "browser_select", "Choose an option of a <select> dropdown by value or visible label. Needs interact permission. Returns the new snapshot.", t.bSelect)
	mcpx.Add(r, "browser_hover", "Move the mouse over an element (opens hover menus and tooltips). Returns the new snapshot.", t.bHover)
	mcpx.Add(r, "browser_scroll", "Scroll the page (direction down/up/top/bottom, optional pixels) or scroll an element (ref) into view. Returns the new snapshot.", t.bScroll)
	mcpx.Add(r, "browser_wait", "Wait until text appears (or disappears with gone=true) or a CSS selector matches, up to timeout_seconds (default 15, max 60). Returns the new snapshot.", t.bWait)
	mcpx.Add(r, "browser_screenshot", "Screenshot an open tab: viewport, full_page, or one element (ref). save=true also stores it in the media library for posts.", t.bScreenshot)
	mcpx.Add(r, "browser_eval", "Run JavaScript in the page and return the JSON result (promises are awaited). For reading data the snapshot doesn't show. Needs interact permission.", t.bEval)
	mcpx.Add(r, "browser_tabs", "List the open browser tabs (id, URL, title).", t.bTabs)
	mcpx.Add(r, "browser_close", "Close a browser tab when you're done with it.", t.bClose)
}

// snapshot returns the formatted snapshot of tab, prefixed by msg.
func (t *Toolset) snapshot(ctx context.Context, tab *session.Tab, msg string, o snapOpts) (*mcpx.Result, error) {
	s, err := t.tabs.Snapshot(ctx, tab, o.MaxElements)
	if err != nil {
		return mcpx.Textf("%s\n(snapshot failed: %v; try browser_snapshot)", msg, err), nil
	}
	if msg != "" {
		msg += "\n\n"
	}
	return mcpx.Text(msg + s.Format(tab.ID, o.MaxChars)), nil
}

func (t *Toolset) tab(id string) (*session.Tab, error) { return t.tabs.Get(id) }

type bOpenIn struct {
	URL string `json:"url" jsonschema:"http(s) URL"`
	snapOpts
}

func (t *Toolset) bOpen(ctx context.Context, in bOpenIn) (*mcpx.Result, error) {
	tab, err := t.tabs.Open(ctx, in.URL)
	if err != nil {
		return nil, err
	}
	return t.snapshot(ctx, tab, "Opened tab "+tab.ID+".", in.snapOpts)
}

type bNavigateIn struct {
	tabIn
	To string `json:"to" jsonschema:"URL, or back, forward, reload"`
	snapOpts
}

func (t *Toolset) bNavigate(ctx context.Context, in bNavigateIn) (*mcpx.Result, error) {
	tab, err := t.tab(in.TabID)
	if err != nil {
		return nil, err
	}
	if err := t.tabs.Navigate(ctx, tab, strings.TrimSpace(in.To)); err != nil {
		return nil, err
	}
	return t.snapshot(ctx, tab, "", in.snapOpts)
}

type bSnapshotIn struct {
	tabIn
	snapOpts
}

func (t *Toolset) bSnapshot(ctx context.Context, in bSnapshotIn) (*mcpx.Result, error) {
	tab, err := t.tab(in.TabID)
	if err != nil {
		return nil, err
	}
	return t.snapshot(ctx, tab, "", in.snapOpts)
}

type bClickIn struct {
	tabIn
	targetIn
	Double bool `json:"double,omitempty" jsonschema:"double-click"`
	snapOpts
}

func (t *Toolset) bClick(ctx context.Context, in bClickIn) (*mcpx.Result, error) {
	tab, err := t.tab(in.TabID)
	if err != nil {
		return nil, err
	}
	if err := t.tabs.Click(ctx, tab, in.target(), in.Double); err != nil {
		return nil, err
	}
	return t.snapshot(ctx, tab, "Clicked "+in.target().String()+".", in.snapOpts)
}

type bTypeIn struct {
	tabIn
	targetIn
	Value  string `json:"value" jsonschema:"text to type"`
	Clear  bool   `json:"clear,omitempty" jsonschema:"replace the existing text"`
	Submit bool   `json:"submit,omitempty" jsonschema:"press Enter after typing"`
	snapOpts
}

func (t *Toolset) bType(ctx context.Context, in bTypeIn) (*mcpx.Result, error) {
	tab, err := t.tab(in.TabID)
	if err != nil {
		return nil, err
	}
	if !in.targetIn.set() {
		return nil, fmt.Errorf("give the element to type into (ref, selector or text)")
	}
	if err := t.tabs.Type(ctx, tab, in.target(), in.Value, in.Clear, in.Submit); err != nil {
		return nil, err
	}
	return t.snapshot(ctx, tab, fmt.Sprintf("Typed %d characters into %s.", len([]rune(in.Value)), in.target()), in.snapOpts)
}

type bPressIn struct {
	tabIn
	Key string `json:"key" jsonschema:"Enter, Tab, Escape, Backspace, Delete, Space, ArrowUp/Down/Left/Right, PageUp, PageDown, Home, End, a character, or a combination like Control+a"`
	snapOpts
}

func (t *Toolset) bPress(ctx context.Context, in bPressIn) (*mcpx.Result, error) {
	tab, err := t.tab(in.TabID)
	if err != nil {
		return nil, err
	}
	if err := t.tabs.Press(ctx, tab, in.Key); err != nil {
		return nil, err
	}
	return t.snapshot(ctx, tab, "Pressed "+in.Key+".", in.snapOpts)
}

type bSelectIn struct {
	tabIn
	targetIn
	Option string `json:"option" jsonschema:"option value or visible label"`
	snapOpts
}

func (t *Toolset) bSelect(ctx context.Context, in bSelectIn) (*mcpx.Result, error) {
	tab, err := t.tab(in.TabID)
	if err != nil {
		return nil, err
	}
	if err := t.tabs.Select(ctx, tab, in.target(), in.Option); err != nil {
		return nil, err
	}
	return t.snapshot(ctx, tab, fmt.Sprintf("Selected %q.", in.Option), in.snapOpts)
}

type bHoverIn struct {
	tabIn
	targetIn
	snapOpts
}

func (t *Toolset) bHover(ctx context.Context, in bHoverIn) (*mcpx.Result, error) {
	tab, err := t.tab(in.TabID)
	if err != nil {
		return nil, err
	}
	if err := t.tabs.Hover(ctx, tab, in.target()); err != nil {
		return nil, err
	}
	return t.snapshot(ctx, tab, "Hovering "+in.target().String()+".", in.snapOpts)
}

type bScrollIn struct {
	tabIn
	Direction string `json:"direction,omitempty" jsonschema:"down (default), up, top, bottom"`
	Pixels    int    `json:"pixels,omitempty" jsonschema:"how far (default: most of a screen)"`
	Ref       string `json:"ref,omitempty" jsonschema:"scroll this element into view instead"`
	snapOpts
}

func (t *Toolset) bScroll(ctx context.Context, in bScrollIn) (*mcpx.Result, error) {
	tab, err := t.tab(in.TabID)
	if err != nil {
		return nil, err
	}
	var tg *session.Target
	if in.Ref != "" {
		tg = &session.Target{Ref: in.Ref}
	}
	if err := t.tabs.Scroll(ctx, tab, strings.ToLower(in.Direction), in.Pixels, tg); err != nil {
		return nil, err
	}
	return t.snapshot(ctx, tab, "", in.snapOpts)
}

type bWaitIn struct {
	tabIn
	Text           string `json:"text,omitempty" jsonschema:"text to wait for"`
	Selector       string `json:"selector,omitempty" jsonschema:"CSS selector to wait for"`
	Gone           bool   `json:"gone,omitempty" jsonschema:"wait until it disappears instead"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"default 15, max 60"`
	snapOpts
}

func (t *Toolset) bWait(ctx context.Context, in bWaitIn) (*mcpx.Result, error) {
	tab, err := t.tab(in.TabID)
	if err != nil {
		return nil, err
	}
	msg := "Done waiting."
	if err := t.tabs.Wait(ctx, tab, in.Text, in.Selector, in.Gone, time.Duration(in.TimeoutSeconds)*time.Second); err != nil {
		msg = "Wait: " + err.Error() + "."
	}
	return t.snapshot(ctx, tab, msg, in.snapOpts)
}

type bScreenshotIn struct {
	tabIn
	FullPage bool   `json:"full_page,omitempty"`
	Ref      string `json:"ref,omitempty" jsonschema:"capture only this element"`
	Save     bool   `json:"save,omitempty" jsonschema:"also save it to the media library (for LinkedIn posts)"`
	Name     string `json:"name,omitempty" jsonschema:"file name when saving"`
}

func (t *Toolset) bScreenshot(ctx context.Context, in bScreenshotIn) (*mcpx.Result, error) {
	tab, err := t.tab(in.TabID)
	if err != nil {
		return nil, err
	}
	var tg *session.Target
	if in.Ref != "" {
		tg = &session.Target{Ref: in.Ref}
	}
	png, err := t.tabs.Screenshot(ctx, tab, in.FullPage, tg)
	if err != nil {
		return nil, err
	}
	msg := "Screenshot of tab " + tab.ID + "."
	if in.Save && t.res != nil {
		name := in.Name
		if name == "" {
			name = "screenshot-" + time.Now().Format("20060102-150405")
		}
		if !strings.HasSuffix(strings.ToLower(name), ".png") {
			name += ".png"
		}
		saved, err := t.res.SaveBytes(ctx, png, resources.Meta{Name: name, Origin: "screenshot", Source: "browser tab " + tab.ID})
		if err != nil {
			return nil, fmt.Errorf("save screenshot: %w", err)
		}
		msg += fmt.Sprintf(" Saved as resource %s; attach it to a post with media: [%q].", saved.ID, saved.ID)
	}
	res := mcpx.Text(msg)
	res.Content = append(res.Content, mcpx.Image(png, "image/png"))
	return res, nil
}

type bEvalIn struct {
	tabIn
	Script string `json:"script" jsonschema:"JavaScript expression, e.g. document.querySelectorAll('article').length or an async IIFE"`
}

func (t *Toolset) bEval(ctx context.Context, in bEvalIn) (*mcpx.Result, error) {
	tab, err := t.tab(in.TabID)
	if err != nil {
		return nil, err
	}
	out, err := t.tabs.Eval(ctx, tab, in.Script)
	if err != nil {
		return nil, err
	}
	if len(out) > 20000 {
		out = out[:20000] + "… (truncated)"
	}
	return mcpx.Text(out), nil
}

func (t *Toolset) bTabs(ctx context.Context, _ struct{}) (*mcpx.Result, error) {
	list := t.tabs.Tabs(ctx)
	if len(list) == 0 {
		return mcpx.Text("No browser tabs open. Use browser_open."), nil
	}
	var b strings.Builder
	for _, x := range list {
		fmt.Fprintf(&b, "%s  %s  %q  (idle %s)\n", x.ID, x.URL, x.Title, x.Idle)
	}
	return mcpx.Text(b.String()), nil
}

type bCloseIn struct {
	TabID string `json:"tab_id"`
}

func (t *Toolset) bClose(_ context.Context, in bCloseIn) (*mcpx.Result, error) {
	if err := t.tabs.Close(in.TabID); err != nil {
		return nil, err
	}
	return mcpx.Textf("Closed tab %s.", in.TabID), nil
}
