package session

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
)

// Element is one interactive element of a snapshot. Ref addresses it in
// click/type/select until the next snapshot.
type Element struct {
	Ref       string `json:"ref"`
	Role      string `json:"role"`
	Name      string `json:"name"`
	Href      string `json:"href,omitempty"`
	Value     string `json:"value,omitempty"`
	Checked   *bool  `json:"checked,omitempty"`
	Disabled  bool   `json:"disabled,omitempty"`
	Offscreen bool   `json:"offscreen,omitempty"`
}

// Snapshot is what the AI sees of a page.
type Snapshot struct {
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	ScrollY   int       `json:"scrollY"`
	ScrollMax int       `json:"scrollMax"`
	Elements  []Element `json:"elements"`
	Text      string    `json:"text"`
	Waiting   string    `json:"-"` // a navigation waiting for the user's approval
}

// snapshotJS marks interactive elements with data-sc-ref and returns the
// snapshot as JSON.
func snapshotJS(max int) string {
	return fmt.Sprintf(`(() => {
  const MAX = %d;
  document.querySelectorAll('[data-sc-ref]').forEach(e => e.removeAttribute('data-sc-ref'));
  const sel = 'a[href],button,input:not([type=hidden]),select,textarea,summary,[role=button],[role=link],[role=tab],[role=menuitem],[role=menuitemcheckbox],[role=option],[role=checkbox],[role=radio],[role=switch],[role=combobox],[role=textbox],[role=searchbox],[contenteditable=""],[contenteditable=true],[onclick]';
  const vis = e => { const r = e.getBoundingClientRect(); if (r.width < 2 || r.height < 2) return false;
    const s = getComputedStyle(e); return s.visibility !== 'hidden' && s.display !== 'none' && s.opacity !== '0'; };
  const clean = s => (s || '').replace(/\s+/g, ' ').trim();
  const name = e => clean(e.getAttribute('aria-label') || e.getAttribute('title') || (e.labels && e.labels[0] && e.labels[0].innerText)
    || e.getAttribute('placeholder') || e.innerText || (e.type === 'submit' || e.type === 'button' ? e.value : '')
    || e.getAttribute('alt') || (e.querySelector && e.querySelector('img[alt]')?.alt) || e.getAttribute('name') || '').slice(0, 90);
  const roleOf = e => e.getAttribute('role') || ({A: 'link', BUTTON: 'button', SELECT: 'select', TEXTAREA: 'textbox', SUMMARY: 'button'})[e.tagName]
    || (e.tagName === 'INPUT' ? ({checkbox: 'checkbox', radio: 'radio', submit: 'button', button: 'button', search: 'searchbox', file: 'file'})[e.type] || 'textbox'
    : (e.isContentEditable ? 'textbox' : e.tagName.toLowerCase()));
  const out = [], seen = new Set(); let n = 0;
  for (const e of document.querySelectorAll(sel)) {
    if (out.length >= MAX) break;
    if (!vis(e)) continue;
    // A clickable inside one already listed (an icon in a button) adds nothing.
    let p = e.parentElement, nested = false;
    while (p) { if (seen.has(p)) { nested = true; break; } p = p.parentElement; }
    if (nested && !['INPUT', 'SELECT', 'TEXTAREA'].includes(e.tagName) && !e.isContentEditable) continue;
    seen.add(e);
    const r = e.getBoundingClientRect(), ref = String(++n);
    e.setAttribute('data-sc-ref', ref);
    const it = {ref, role: roleOf(e), name: name(e)};
    if (e.tagName === 'A') it.href = e.getAttribute('href');
    if (['INPUT', 'TEXTAREA', 'SELECT'].includes(e.tagName) && !['submit', 'button', 'checkbox', 'radio'].includes(e.type))
      it.value = String(e.tagName === 'SELECT' ? (e.selectedOptions[0]?.label || '') : e.value).slice(0, 80);
    if (e.type === 'checkbox' || e.type === 'radio') it.checked = e.checked;
    else if (e.getAttribute('aria-checked')) it.checked = e.getAttribute('aria-checked') === 'true';
    if (e.disabled || e.getAttribute('aria-disabled') === 'true') it.disabled = true;
    if (r.bottom < 0 || r.top > innerHeight) it.offscreen = true;
    out.push(it);
  }
  return JSON.stringify({url: location.href, title: document.title, scrollY: Math.round(scrollY),
    scrollMax: Math.max(0, Math.round(document.documentElement.scrollHeight - innerHeight)),
    elements: out, text: (document.body ? document.body.innerText : '')});
})()`, max)
}

// Snapshot reads the page: title, URL, interactive elements with refs, text.
func (m *Manager) Snapshot(ctx context.Context, t *Tab, maxElements int) (*Snapshot, error) {
	if maxElements <= 0 || maxElements > 400 {
		maxElements = 150
	}
	var s Snapshot
	err := t.Do(ctx, 30*time.Second, func(c context.Context) error {
		var raw string
		var err error
		for i := 0; i < 5; i++ { // the page may be mid-navigation
			if raw, err = chromedp.Run(c, chromedp.Evaluate[string](snapshotJS(maxElements))); err == nil {
				break
			}
			time.Sleep(300 * time.Millisecond)
		}
		if err != nil {
			return err
		}
		return json.Unmarshal([]byte(raw), &s)
	})
	if err != nil {
		return nil, err
	}
	s.Waiting = t.waitingFor()
	return &s, nil
}

var blankRuns = regexp.MustCompile(`\n[ \t]*(\n[ \t]*)+`)

// Format renders a snapshot for the AI. maxText bounds the page text.
func (s *Snapshot) Format(tabID string, maxText int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "tab: %s\nurl: %s\ntitle: %s\n", tabID, s.URL, s.Title)
	if s.ScrollMax > 0 {
		fmt.Fprintf(&b, "scroll: %d / %d px\n", s.ScrollY, s.ScrollMax)
	}
	if s.Waiting != "" {
		fmt.Fprintf(&b, "note: navigation to %s is waiting for the user's approval\n", s.Waiting)
	}
	b.WriteString("\n--- interactive elements (use the number as ref) ---\n")
	if len(s.Elements) == 0 {
		b.WriteString("(none)\n")
	}
	for _, e := range s.Elements {
		fmt.Fprintf(&b, "[%s] %s %q", e.Ref, e.Role, e.Name)
		if e.Href != "" && !strings.HasPrefix(e.Href, "javascript:") {
			fmt.Fprintf(&b, " → %s", clip(e.Href, 100))
		}
		if e.Value != "" {
			fmt.Fprintf(&b, " value=%q", e.Value)
		}
		if e.Checked != nil {
			fmt.Fprintf(&b, " checked=%v", *e.Checked)
		}
		if e.Disabled {
			b.WriteString(" (disabled)")
		}
		if e.Offscreen {
			b.WriteString(" (off-screen)")
		}
		b.WriteByte('\n')
	}
	if maxText <= 0 {
		maxText = 6000
	}
	text := strings.TrimSpace(blankRuns.ReplaceAllString(s.Text, "\n\n"))
	b.WriteString("\n--- page text ---\n")
	if len(text) > maxText {
		fmt.Fprintf(&b, "%s\n… (%d more characters; raise max_chars or scroll)\n", text[:maxText], len(text)-maxText)
	} else {
		b.WriteString(text + "\n")
	}
	return b.String()
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
