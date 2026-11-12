package urlpolicy

import (
	"context"
	"testing"
)

func TestMatches(t *testing.T) {
	cases := []struct {
		pattern, url string
		want         bool
	}{
		{"example.com", "https://example.com/x", true},
		{"example.com", "https://www.example.com/x", false},
		{"*.example.com", "https://www.example.com/x", true},
		{"*.example.com", "https://example.com", true},
		{"*.example.com", "https://badexample.com", false},
		{"https://example.com/docs/*", "https://example.com/docs/a/b", true},
		{"https://example.com/docs/*", "https://example.com/blog", false},
		{"https://example.com/docs/*", "http://example.com/docs/a", false},
		{"*", "https://anything.dev", true},
	}
	for _, c := range cases {
		u, err := Normalize(c.url)
		if err != nil {
			t.Fatal(err)
		}
		if got := Matches(c.pattern, u); got != c.want {
			t.Errorf("Matches(%q, %q) = %v, want %v", c.pattern, c.url, got, c.want)
		}
	}
}

func TestNormalizeRejects(t *testing.T) {
	for _, raw := range []string{"file:///etc/passwd", "javascript:alert(1)", "https://user:pw@example.com", "https://"} {
		if _, err := Normalize(raw); err == nil {
			t.Errorf("Normalize(%q) should fail", raw)
		}
	}
}

func TestEvaluateCapabilities(t *testing.T) {
	rules := []Rule{{Pattern: "example.com", Action: "allow", Capabilities: []string{CapScreenshot}}}
	u, _ := Normalize("https://example.com")
	if d, _ := evaluate(rules, u, CapScreenshot); d != "allow" {
		t.Errorf("screenshot should be allowed, got %q", d)
	}
	if d, _ := evaluate(rules, u, CapDownload); d != "" {
		t.Errorf("download should be unknown, got %q", d)
	}
}

func TestLocalhost(t *testing.T) {
	for host, want := range map[string]bool{
		"localhost": true, "app.localhost": true, "127.0.0.1": true, "::1": true,
		"example.com": false, "localhost.evil.com": false, "10.0.0.5": false,
	} {
		if got := IsLocalName(host); got != want {
			t.Errorf("IsLocalName(%q) = %v, want %v", host, got, want)
		}
	}

	p := &Policy{}
	p.SetLocalhost(true, "8787", "8788")
	ctx := context.Background()
	for raw, ok := range map[string]bool{
		"http://localhost:3000":       true,
		"http://127.0.0.1:5173/x":     true,
		"http://localhost:8788/api":   false, // Smart Control's admin API
		"http://127.0.0.1:8787/mcp/x": false, // Smart Control's MCP server
	} {
		u, _ := Normalize(raw)
		if err := p.checkNetwork(ctx, u, false); (err == nil) != ok {
			t.Errorf("checkNetwork(%s) = %v, want allowed=%v", raw, err, ok)
		}
	}

	p.SetLocalhost(false, "8787")
	u, _ := Normalize("http://localhost:3000")
	if err := p.checkNetwork(ctx, u, false); err == nil {
		t.Error("localhost should be refused when turned off")
	}
}

func TestInteractNeedsExplicitRule(t *testing.T) {
	u, _ := Normalize("https://www.linkedin.com/feed")
	all := []Rule{{Pattern: "*", Action: "allow", Capabilities: []string{CapAll}}}
	if d, _ := evaluate(all, u, CapInteract); d != "" {
		t.Errorf("an allow-* rule must not grant interact, got %q", d)
	}
	if d, _ := evaluate(all, u, CapBrowse); d != "allow" {
		t.Errorf("allow-* should still grant browse, got %q", d)
	}
	named := append(all, Rule{Pattern: "www.linkedin.com", Action: "allow", Capabilities: []string{CapInteract}})
	if d, _ := evaluate(named, u, CapInteract); d != "allow" {
		t.Errorf("a rule naming interact should grant it, got %q", d)
	}
	deny := append(named, Rule{Pattern: "*.linkedin.com", Action: "deny", Capabilities: []string{CapAll}})
	if d, _ := evaluate(deny, u, CapInteract); d != "deny" {
		t.Errorf("deny-* still covers interact, got %q", d)
	}
}
