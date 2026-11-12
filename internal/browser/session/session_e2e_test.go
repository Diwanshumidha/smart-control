package session

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Diwanshumidha/smart-control/internal/browser"
	_ "github.com/Diwanshumidha/smart-control/internal/browser/cdp"
	"github.com/Diwanshumidha/smart-control/internal/config"
	"github.com/Diwanshumidha/smart-control/internal/events"
	"github.com/Diwanshumidha/smart-control/internal/security/urlpolicy"
	"github.com/Diwanshumidha/smart-control/internal/store"
)

const shopPage = `<!doctype html><title>Shop</title>
<h1>Welcome</h1>
<form action="/results"><input name="q" placeholder="Search products"><select name="size"><option value="s">Small</option><option value="l">Large</option></select><button>Go</button></form>
<a href="/next">Next page</a> <a href="https://example.com/">Elsewhere</a>
<div contenteditable="true" aria-label="Editor"></div>
<div style="height:3000px"></div><p id="end">The end</p>
<script>setTimeout(() => document.body.insertAdjacentHTML('beforeend', '<p>Loaded later</p>'), 800)</script>`

// BROWSER_E2E=1 go test ./internal/browser/session -v (headless Brave).
func TestInteractiveE2E(t *testing.T) {
	if os.Getenv("BROWSER_E2E") != "1" {
		t.Skip("set BROWSER_E2E=1 to drive a real (headless) Brave")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/results":
			fmt.Fprintf(w, "<title>Results</title><p>You searched for %s in size %s</p>", r.URL.Query().Get("q"), r.URL.Query().Get("size"))
		case "/next":
			fmt.Fprint(w, "<title>Next</title><p>Second page</p>")
		default:
			fmt.Fprint(w, shopPage)
		}
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	pol := urlpolicy.New(st.DB, events.NewBus(), time.Second, false)
	pol.SetLocalhost(true)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	// 127.0.0.1: browse + interact. localhost: browse only. example.com: denied.
	_, err = pol.AddRule(ctx, urlpolicy.Rule{Pattern: "127.0.0.1", Action: "allow", Capabilities: []string{urlpolicy.CapBrowse, urlpolicy.CapInteract}})
	must(err)
	_, err = pol.AddRule(ctx, urlpolicy.Rule{Pattern: "localhost", Action: "allow", Capabilities: []string{urlpolicy.CapAll}})
	must(err)
	_, err = pol.AddRule(ctx, urlpolicy.Rule{Pattern: "example.com", Action: "deny", Capabilities: []string{urlpolicy.CapAll}})
	must(err)

	svc := browser.New(config.Browser{Driver: "cdp", Headless: true, Width: 1200, Height: 800}, t.TempDir(), pol)
	defer svc.Close()
	m := New(svc)
	defer m.Shutdown()

	snap := func(tab *Tab) string {
		t.Helper()
		s, err := m.Snapshot(ctx, tab, 0)
		must(err)
		return s.Format(tab.ID, 4000)
	}
	ref := func(out, name string) string {
		t.Helper()
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, `"`+name+`"`) {
				return strings.Trim(strings.Fields(line)[0], "[]")
			}
		}
		t.Fatalf("no element %q in\n%s", name, out)
		return ""
	}

	t.Log("open")
	tab, err := m.Open(ctx, srv.URL+"/")
	must(err)
	out := snap(tab)
	if !strings.Contains(out, `textbox "Search products"`) || !strings.Contains(out, `link "Next page"`) || !strings.Contains(out, "Welcome") {
		t.Fatalf("snapshot:\n%s", out)
	}

	t.Log("form")
	// Fill the form by ref and submit with Enter.
	must(m.Select(ctx, tab, Target{Ref: ref(out, "Small")}, "Large"))
	must(m.Type(ctx, tab, Target{Ref: ref(out, "Search products")}, "red shoes", false, true))
	if out = snap(tab); !strings.Contains(out, "You searched for red shoes in size l") {
		t.Fatalf("after submit:\n%s", out)
	}
	must(m.Navigate(ctx, tab, "back"))
	out = snap(tab)

	t.Log("click")
	// Click by visible text; then a link to a denied host is blocked.
	must(m.Click(ctx, tab, Target{Text: "Next page"}, false))
	if out = snap(tab); !strings.Contains(out, "Second page") {
		t.Fatalf("after click:\n%s", out)
	}
	must(m.Navigate(ctx, tab, "back"))
	out = snap(tab)
	if err := m.Click(ctx, tab, Target{Ref: ref(out, "Elsewhere")}, false); err == nil || !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("navigation to a denied host must be blocked, got %v", err)
	}
	if out = snap(tab); !strings.Contains(out, "Welcome") {
		t.Fatalf("should still be on the page:\n%s", out)
	}

	t.Log("editor")
	// Rich editor, keys, scroll, wait, eval, screenshot.
	must(m.Type(ctx, tab, Target{Ref: ref(out, "Editor")}, "Hello", false, false))
	must(m.Press(ctx, tab, "Control+a"))
	v, err := m.Eval(ctx, tab, `document.querySelector('[contenteditable]').innerText`)
	must(err)
	if !strings.Contains(v, "Hello") {
		t.Fatalf("editor text = %s", v)
	}
	must(m.Scroll(ctx, tab, "bottom", 0, nil))
	if s, _ := m.Snapshot(ctx, tab, 0); s.ScrollY == 0 {
		t.Fatal("scroll didn't move")
	}
	must(m.Wait(ctx, tab, "Loaded later", "", false, 5*time.Second))
	png, err := m.Screenshot(ctx, tab, false, nil)
	must(err)
	if len(png) < 1000 {
		t.Fatal("screenshot too small")
	}

	t.Log("interact denied")
	// Same server as "localhost": browse is allowed, interact isn't (asks, times out).
	tab2, err := m.Open(ctx, "http://localhost:"+port+"/")
	must(err)
	out = snap(tab2)
	if err := m.Click(ctx, tab2, Target{Ref: ref(out, "Next page")}, false); err == nil || !strings.Contains(err.Error(), "denied") {
		t.Fatalf("click without interact permission must be refused, got %v", err)
	}
	if len(m.Tabs(ctx)) != 2 {
		t.Fatal("want 2 tabs")
	}
	must(m.Close(tab2.ID))
}
