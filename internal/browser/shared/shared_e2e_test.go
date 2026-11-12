package shared

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Diwanshumidha/smart-control/internal/browser"
)

// BROWSER_E2E=1 go test ./internal/browser/shared -v
// Opens a real, visible Brave window with a throwaway profile.
func TestSharedWindowE2E(t *testing.T) {
	if os.Getenv("BROWSER_E2E") != "1" {
		t.Skip("set BROWSER_E2E=1 to open a real Brave window")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Cookie("session")
		user := "anonymous"
		if c != nil {
			user = c.Value
		}
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "diwanshu", Path: "/", Expires: time.Now().Add(time.Hour)})
		}
		fmt.Fprintf(w, "<h1>hello %s</h1>", user)
	}))
	defer srv.Close()

	s := browser.Settings{ExecPath: browser.ResolveExec(browser.FindBrave("")), ProfileDir: t.TempDir(), DebugPort: 9444, Width: 1200, Height: 800}
	ctx := context.Background()
	// "You" log in by hand in the window: here, a tab that sets the cookie.
	if err := Open(ctx, s, srv.URL+"/login"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2 * time.Second)
	d, _ := New(s)
	defer func() {
		d.Close()
		_ = exec.Command("pkill", "-f", fmt.Sprintf("remote-debugging-port=%d", s.DebugPort)).Run()
		for i := 0; i < 50; i++ { // let the port go before the next run
			if _, err := version(ctx, s.DebugPort); err != nil {
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
	}()
	allow := func(context.Context, string) error { return nil }
	for i := 0; i < 2; i++ {
		start := time.Now()
		p, err := d.Visit(ctx, browser.Request{URL: srv.URL + "/", Allow: allow,
			Options: browser.Options{Width: 1200, Height: 800, Scale: 1, Text: true, Screenshot: true, Delay: 300 * time.Millisecond}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(p.Text, "hello diwanshu") {
			t.Fatalf("visit %d should see the session from the window, got %q", i, p.Text)
		}
		if len(p.Screenshot) < 1000 {
			t.Fatalf("visit %d: screenshot too small", i)
		}
		t.Logf("visit %d ok in %s, screenshot %d bytes", i, time.Since(start).Round(time.Millisecond), len(p.Screenshot))
	}
}
