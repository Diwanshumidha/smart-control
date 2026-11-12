package app

import (
	"context"
	"os"
	"testing"

	"github.com/Diwanshumidha/smart-control/internal/browser"
	"github.com/Diwanshumidha/smart-control/internal/security/urlpolicy"
)

// BROWSER_E2E=1 go test ./internal/app -run TestBrowserE2E -v
// Runs every registered driver against real Brave and the network. The
// playwright driver downloads its runtime on first run.
func TestBrowserE2E(t *testing.T) {
	if os.Getenv("BROWSER_E2E") == "" {
		t.Skip("set BROWSER_E2E=1")
	}
	a, _, _ := newTestApp(t)
	ctx := context.Background()
	_, err := a.Policy.AddRule(ctx, urlpolicy.Rule{Pattern: "example.com", Action: "allow"})
	must(t, err)
	for _, name := range browser.DriverNames() {
		t.Run(name, func(t *testing.T) {
			s := a.Settings()
			s.Browser.Driver = name
			_, err := a.ApplySettings(s)
			must(t, err)
			p, err := a.Browser.Visit(ctx, "https://example.com", urlpolicy.CapScreenshot, browser.Options{Screenshot: true, Text: true, HTML: true})
			must(t, err)
			if p.Title != "Example Domain" || len(p.Screenshot) < 1000 || p.HTML == "" || p.Text == "" {
				t.Fatalf("%s: title=%q shot=%d html=%d text=%d", name, p.Title, len(p.Screenshot), len(p.HTML), len(p.Text))
			}
			// Unknown host must not load (approval times out in the test config).
			if _, err := a.Browser.Visit(ctx, "https://example.org", urlpolicy.CapBrowse, browser.Options{}); err == nil {
				t.Fatalf("%s: unapproved host loaded", name)
			}
			t.Logf("%s ok: %q final=%s screenshot=%d bytes", name, p.Title, p.FinalURL, len(p.Screenshot))
		})
	}
}
