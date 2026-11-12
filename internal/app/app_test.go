package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Diwanshumidha/smart-control/internal/config"
	"github.com/Diwanshumidha/smart-control/internal/events"
	"github.com/Diwanshumidha/smart-control/internal/posts"
	"github.com/Diwanshumidha/smart-control/internal/security/urlpolicy"
)

func newTestApp(t *testing.T) (*App, *mcp.ClientSession, string) {
	t.Helper()
	root := t.TempDir()
	proj := filepath.Join(root, "horse-tinder")
	must(t, os.MkdirAll(filepath.Join(proj, "src"), 0o755))
	must(t, os.WriteFile(filepath.Join(proj, "package.json"), []byte(`{"name":"horse-tinder","description":"Tinder for horses"}`), 0o644))
	must(t, os.WriteFile(filepath.Join(proj, "src", "main.ts"), []byte("export const swipe = () => 'neigh'\n// TODO: matching\n"), 0o644))
	must(t, os.WriteFile(filepath.Join(proj, ".env"), []byte("SECRET=hunter2 TODO\n"), 0o644))
	// 1x1 PNG
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89\x00\x00\x00\rIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")
	must(t, os.WriteFile(filepath.Join(proj, "logo.png"), png, 0o644))

	cfg, err := config.Load(t.TempDir())
	must(t, err)
	cfg.Roots = []string{root}
	cfg.Tunnel.Mode = "off"
	cfg.URLPolicy.ApprovalTimeoutSec = 2
	a, err := New(cfg, "test")
	must(t, err)
	t.Cleanup(func() { a.Close() })

	st, ct := mcp.NewInMemoryTransports()
	_, err = a.mcpServer.Connect(context.Background(), st, nil)
	must(t, err)
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(context.Background(), ct, nil)
	must(t, err)
	t.Cleanup(func() { cs.Close() })
	return a, cs, proj
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	must(t, err)
	var b strings.Builder
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			b.WriteString(v.Text)
		case *mcp.ImageContent:
			b.WriteString("[image " + v.MIMEType + "]")
		}
	}
	return b.String(), res.IsError
}

func TestToolsAreRegistered(t *testing.T) {
	_, cs, _ := newTestApp(t)
	res, err := cs.ListTools(context.Background(), nil)
	must(t, err)
	have := map[string]bool{}
	for _, tool := range res.Tools {
		have[tool.Name] = true
		b, _ := json.Marshal(tool.InputSchema)
		if strings.Contains(string(b), `"null"`) {
			t.Errorf("%s schema has a null union (ChatGPT rejects these): %s", tool.Name, b)
		}
	}
	for _, tool := range res.Tools {
		if tool.Name != "browser_click" {
			continue
		}
		b, _ := json.Marshal(tool.InputSchema)
		for _, prop := range []string{`"tab_id"`, `"ref"`, `"selector"`, `"double"`, `"max_chars"`} {
			if !strings.Contains(string(b), prop) {
				t.Errorf("browser_click schema lacks %s: %s", prop, b)
			}
		}
	}
	for _, n := range []string{"search_project", "get_files_from_project", "grep_in_project", "file_tree",
		"open_url", "screenshot_url", "browser_open", "browser_click", "browser_type", "browser_snapshot", "post_linkedin_post", "save_video_resource", "brag_guide", "brag_start", "finalize"} {
		if !have[n] {
			t.Errorf("tool %s not registered", n)
		}
	}
}

func TestProjectTools(t *testing.T) {
	_, cs, proj := newTestApp(t)

	out, isErr := call(t, cs, "search_project", map[string]any{"query": "horse"})
	if isErr || !strings.Contains(out, proj) || !strings.Contains(out, "Tinder for horses") {
		t.Fatalf("search_project: %s", out)
	}
	out, _ = call(t, cs, "file_tree", map[string]any{"path": proj})
	if !strings.Contains(out, "main.ts") || strings.Contains(out, ".env") {
		t.Fatalf("file_tree: %s", out)
	}
	out, _ = call(t, cs, "get_files_from_project", map[string]any{"project": proj, "paths": []string{"src/main.ts", ".env", "logo.png"}})
	if !strings.Contains(out, "neigh") || !strings.Contains(out, "blocked") || strings.Contains(out, "hunter2") || !strings.Contains(out, "[image image/png]") {
		t.Fatalf("get_files_from_project: %s", out)
	}
	out, _ = call(t, cs, "grep_in_project", map[string]any{"path": proj, "pattern": "todo"})
	if !strings.Contains(out, "src/main.ts:2:") || strings.Contains(out, "hunter2") {
		t.Fatalf("grep_in_project: %s", out)
	}
	out, isErr = call(t, cs, "file_tree", map[string]any{"path": "/etc"})
	if !isErr || !strings.Contains(out, "outside the allowed project folders") {
		t.Fatalf("expected root confinement, got: %s", out)
	}
}

func TestMediaAndLinkedInFlow(t *testing.T) {
	a, cs, proj := newTestApp(t)
	out, isErr := call(t, cs, "save_video_resource", map[string]any{"source": "project://" + filepath.Join(proj, "logo.png"), "note": "logo"})
	if isErr {
		t.Fatalf("save_video_resource: %s", out)
	}
	list, err := a.Res.List(context.Background(), "image", 10)
	must(t, err)
	if len(list) != 1 {
		t.Fatalf("want 1 resource, got %d", len(list))
	}
	resID := list[0].ID

	out, isErr = call(t, cs, "post_linkedin_post", map[string]any{
		"content": "I built Tinder for horses.\n\nIt went well.", "hashtags": []string{"#buildinpublic", "go"}, "media": []string{resID},
	})
	if isErr || !strings.Contains(out, "NOT published") {
		t.Fatalf("post_linkedin_post: %s", out)
	}
	ps, err := a.Posts.List(context.Background(), "linkedin", posts.StatusPendingReview, 10)
	must(t, err)
	if len(ps) != 1 || ps[0].Hashtags[0] != "buildinpublic" || ps[0].Media[0] != resID {
		t.Fatalf("stored post: %+v", ps)
	}
	pub, err := a.Posts.Publisher("linkedin")
	must(t, err)
	p, err := pub.Publish(context.Background(), &ps[0])
	must(t, err)
	if !strings.Contains(p.ShareURL, "linkedin.com") || !strings.HasSuffix(p.Text, "#buildinpublic #go") {
		t.Fatalf("publish: %+v", p)
	}

	_, isErr = call(t, cs, "post_linkedin_post", map[string]any{"content": strings.Repeat("x", 3001)})
	if !isErr {
		t.Fatal("over-long post should fail")
	}
	_, isErr = call(t, cs, "post_linkedin_post", map[string]any{"content": "hi", "media": []string{"res_nope"}})
	if !isErr {
		t.Fatal("unknown media should fail")
	}

	acts, err := a.Activity.List(context.Background(), 50, 0, "")
	must(t, err)
	if len(acts) < 4 || acts[0].Status == "running" {
		t.Fatalf("activity not recorded: %+v", acts)
	}
}

func TestURLApproval(t *testing.T) {
	a, _, _ := newTestApp(t)
	ctx := context.Background()

	// Private addresses are refused outright.
	if err := a.Policy.Check(ctx, "http://127.0.0.1:8080/", urlpolicy.CapBrowse, "test"); err == nil {
		t.Fatal("loopback should be denied")
	}
	// Unknown host: approval requested, user allows the host.
	ch, cancel := a.Bus.Subscribe()
	defer cancel()
	go func() {
		for ev := range ch {
			if ev.Topic == events.ApprovalRequest {
				appr := ev.Data.(urlpolicy.Approval)
				if _, err := a.Policy.Decide(ctx, appr.ID, urlpolicy.AllowHost); err != nil {
					t.Error(err)
				}
				return
			}
		}
	}()
	if err := a.Policy.Check(ctx, "https://example.com/a", urlpolicy.CapScreenshot, "test"); err != nil {
		t.Fatalf("approved check failed: %v", err)
	}
	// Now covered by a rule: no prompt, immediate.
	start := time.Now()
	if err := a.Policy.Check(ctx, "https://example.com/b", urlpolicy.CapDownload, "test"); err != nil || time.Since(start) > time.Second {
		t.Fatalf("rule should allow immediately: %v", err)
	}
	// Deny rule wins over allow.
	_, err := a.Policy.AddRule(ctx, urlpolicy.Rule{Pattern: "https://example.com/private*", Action: "deny"})
	must(t, err)
	if err := a.Policy.Check(ctx, "https://example.com/private/x", urlpolicy.CapBrowse, "test"); err == nil {
		t.Fatal("deny rule should win")
	}
	// No answer → expires.
	if err := a.Policy.Check(ctx, "https://example.org/", urlpolicy.CapBrowse, "test"); err == nil || !strings.Contains(err.Error(), "no approval") {
		t.Fatalf("expected timeout, got %v", err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestProjectIndexAndLiveRoots(t *testing.T) {
	a, cs, proj := newTestApp(t)
	ctx := context.Background()
	if _, err := a.Projects.Reindex(ctx); err != nil {
		t.Fatal(err)
	}
	if st := a.Projects.Status(); st.Count != 1 {
		t.Fatalf("want 1 indexed project, got %+v", st)
	}
	found, err := a.Projects.Search(ctx, "tinder", 10)
	must(t, err)
	if len(found) != 1 || found[0].Path != proj || found[0].Kinds[0] != "node" {
		t.Fatalf("search: %+v", found)
	}

	// Switch the project folder from the dashboard: applies without restart.
	other := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(other, "rusty-bot"), 0o755))
	must(t, os.WriteFile(filepath.Join(other, "rusty-bot", "Cargo.toml"), []byte("[package]\nname='rusty-bot'\n"), 0o644))
	s := a.Settings()
	s.Roots = []string{other}
	restart, err := a.ApplySettings(s)
	must(t, err)
	if restart {
		t.Fatal("changing project folders should not need a restart")
	}
	out, _ := call(t, cs, "search_project", map[string]any{"query": "rusty", "refresh": true})
	if !strings.Contains(out, "rusty-bot") || strings.Contains(out, "horse-tinder") {
		t.Fatalf("search after root change: %s", out)
	}
	if out, isErr := call(t, cs, "file_tree", map[string]any{"path": proj}); !isErr {
		t.Fatalf("old root should no longer be readable: %s", out)
	}

	s.Roots = []string{"/definitely/not/here"}
	if _, err := a.ApplySettings(s); err == nil {
		t.Fatal("invalid root should be rejected")
	}
}

func TestBrowserDrivers(t *testing.T) {
	a, _, _ := newTestApp(t)
	names := map[string]bool{}
	for _, d := range a.Browser.Drivers() {
		names[d.Name] = true
	}
	if !names["cdp"] || !names["playwright"] {
		t.Fatalf("drivers: %v", names)
	}
	s := a.Settings()
	s.Browser.Driver = "playwright"
	_, err := a.ApplySettings(s)
	must(t, err)
	if a.Browser.Driver() != "playwright" {
		t.Fatal("driver not switched")
	}
	s.Browser.Driver = "selenium-from-1999"
	if _, err := a.ApplySettings(s); err == nil {
		t.Fatal("unknown driver should be rejected")
	}
}

func TestActivityRetention(t *testing.T) {
	a, cs, _ := newTestApp(t)
	call(t, cs, "list_resources", map[string]any{})
	old := time.Now().AddDate(0, 0, -40).UnixMilli()
	_, err := a.Store.DB.Exec(`INSERT INTO tool_calls(id, toolset, tool, args, status, started_at) VALUES ('call_old','media','list_resources','{}','ok',?)`, old)
	must(t, err)

	s := a.Settings()
	s.Activity.RetentionDays = 35 // default is 30; a change prunes right away
	_, err = a.ApplySettings(s)
	must(t, err)

	list, err := a.Activity.List(context.Background(), 100, 0, "")
	must(t, err)
	if len(list) != 1 || list[0].ID == "call_old" {
		t.Fatalf("want only the recent call kept, got %+v", list)
	}
	if n, _ := a.Activity.Prune(context.Background(), 0); n != 0 {
		t.Fatal("retention 0 must keep everything")
	}
}
