package brag

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Diwanshumidha/smart-control/internal/browser"
	"github.com/Diwanshumidha/smart-control/internal/config"
	"github.com/Diwanshumidha/smart-control/internal/events"
	"github.com/Diwanshumidha/smart-control/internal/jobs"
	"github.com/Diwanshumidha/smart-control/internal/mcpx"
	"github.com/Diwanshumidha/smart-control/internal/resources"
	"github.com/Diwanshumidha/smart-control/internal/security/pathguard"
	"github.com/Diwanshumidha/smart-control/internal/security/urlpolicy"
	"github.com/Diwanshumidha/smart-control/internal/store"
	"github.com/Diwanshumidha/smart-control/internal/toolsets/kit"
)

const testToken = "testtoken-0123456789"

// newTestDeps builds the shared dependencies a toolset gets in production.
func newTestDeps(t *testing.T, roots []string, publicURL *string) *kit.Deps {
	t.Helper()
	cfg, err := config.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	bus := events.NewBus()
	guard, err := pathguard.New(roots)
	if err != nil {
		t.Fatal(err)
	}
	pol := urlpolicy.New(st.DB, bus, time.Second, false)
	return &kit.Deps{
		Version: "test", Config: cfg, DB: st.DB, Bus: bus, Guard: guard, URLPolicy: pol,
		Browser:   browser.New(cfg.Browser, cfg.Sub("browser"), pol),
		Jobs:      jobs.NewManager(),
		Resources: resources.New(st.DB, bus, pol, cfg.Sub("resources")),
		PublicURL: func() string { return *publicURL },
		FileURL: func(toolset, rel string) string {
			if *publicURL == "" {
				return ""
			}
			return *publicURL + "/files/" + testToken + "/" + toolset + "/" + rel
		},
	}
}

// serve mounts the toolset the way the real server does: MCP under /mcp/<token>
// and public files under /files/<token>/brag/.
func serve(t *testing.T, ts mcpx.Toolset, publicURL *string) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test"}, nil)
	reg := mcpx.NewRegistry(server, mcpx.Recover())
	if err := reg.Install(ts); err != nil {
		t.Fatal(err)
	}
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	mux := http.NewServeMux()
	mux.Handle("/mcp/"+testToken, h)
	ts.(mcpx.PublicRoutes).PublicRoutes(mux, "/files/"+testToken+"/brag/")
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	*publicURL = srv.URL
	return srv
}

func setup(t *testing.T) (*mcp.ClientSession, *app, string) {
	t.Helper()
	project := t.TempDir()
	os.WriteFile(filepath.Join(project, "index.html"), []byte("<h1>Horse Tinder</h1>\nswipe right on horses\n"), 0o644)
	os.WriteFile(filepath.Join(project, ".env"), []byte("SECRET=1"), 0o644)
	os.MkdirAll(filepath.Join(project, "components"), 0o755)
	os.WriteFile(filepath.Join(project, "components", "BrandMark.tsx"), []byte(brandMarkTSX), 0o644)
	os.MkdirAll(filepath.Join(project, "public"), 0o755)
	os.WriteFile(filepath.Join(project, "public", "next.svg"), []byte("<svg/>"), 0o644)
	var public string
	d := newTestDeps(t, []string{project}, &public)
	ts, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	a := ts.(*app)
	t.Cleanup(func() { a.Close() })
	// Offline catalog fixture so tests don't need npx or the network.
	fixture, _ := os.ReadFile("testdata/catalog.json")
	os.WriteFile(filepath.Join(a.workdir, "catalog.json"), fixture, 0o644)
	theCatalog.items = nil
	srv := serve(t, a, &public)

	c := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	cs, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: srv.URL + "/mcp/" + testToken}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs, a, project
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			sb.WriteString(v.Text + "\n")
		case *mcp.ImageContent:
			sb.WriteString("[image " + v.MIMEType + "]\n")
		}
	}
	return sb.String(), res.IsError
}

func TestWorkflow(t *testing.T) {
	cs, a, project := setup(t)

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) < 15 {
		t.Fatalf("tools: %v %d", err, len(tools.Tools))
	}

	out, _ := call(t, cs, "brag_guide", map[string]any{"topic": "index"})
	if !strings.Contains(out, "two phases") {
		t.Fatalf("guide: %s", out)
	}
	if out, _ := call(t, cs, "brag_guide", map[string]any{"topic": "step-3"}); !strings.Contains(out, "Composition Brief") {
		t.Fatalf("step-3: %.200s", out)
	}

	if out, _ := call(t, cs, "project_tree", map[string]any{}); !strings.Contains(out, "index.html") || strings.Contains(out, "SECRET") {
		t.Fatalf("tree: %s", out)
	}
	if out, isErr := call(t, cs, "read_file", map[string]any{"path": ".env"}); !isErr {
		t.Fatalf(".env should be blocked: %s", out)
	}
	if out, isErr := call(t, cs, "read_file", map[string]any{"path": "/etc/passwd"}); !isErr {
		t.Fatalf("outside root should be blocked: %s", out)
	}
	if out, _ := call(t, cs, "read_file", map[string]any{"path": "index.html"}); !strings.Contains(out, "swipe right") {
		t.Fatalf("read: %s", out)
	}

	out, _ = call(t, cs, "brag_start", map[string]any{"project_path": project})
	runID := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(out, "run_id: "), "\n", 2)[0])
	if _, err := a.getRun(runID); err != nil {
		t.Fatalf("start: %s", out)
	}

	if _, isErr := call(t, cs, "write_file", map[string]any{"run_id": runID, "path": "../escape.txt", "content": "x"}); !isErr {
		t.Fatal("escape write should fail")
	}

	// ---- Phase 1: building is locked until the plan is approved ----
	if out, isErr := call(t, cs, "write_file", map[string]any{"run_id": runID, "path": "composition/index.html", "content": "x"}); !isErr || !strings.Contains(out, "not approved") {
		t.Fatalf("composition write before approval should be gated: %s", out)
	}
	if _, isErr := call(t, cs, "hyperframes", map[string]any{"run_id": runID, "args": []string{"add", "whip-pan"}}); !isErr {
		t.Fatal("add before approval should be gated")
	}
	if _, isErr := call(t, cs, "hyperframes", map[string]any{"run_id": runID, "args": []string{"init"}}); !isErr {
		t.Fatal("init should be refused")
	}
	if out, isErr := call(t, cs, "write_file", map[string]any{"run_id": runID, "path": "notes.md", "content": "research"}); isErr {
		t.Fatalf("notes outside composition are allowed while planning: %s", out)
	}

	out, isErr := call(t, cs, "search_templates", map[string]any{})
	if isErr || !strings.Contains(out, "starter kit") || !strings.Contains(out, "https://hyperframes.heygen.com/catalog/blocks/whip-pan") {
		t.Fatalf("starter kit: %s", out)
	}
	if out, _ := call(t, cs, "search_templates", map[string]any{"query": "terminal"}); !strings.Contains(out, "terminal-simulator") || !strings.Contains(out, "catalog/components/terminal-simulator") {
		t.Fatalf("search: %s", out)
	}

	plan := testPlan(runID)
	weak := testPlan(runID)
	for _, sc := range weak["scenes"].([]map[string]any) {
		sc["transition_out"] = "fade"
	}
	weak["scenes"].([]map[string]any)[4]["transition_out"] = ""
	weak["palette"] = []map[string]any{{"role": "background", "hex": "#111111"}, {"role": "text", "hex": "#222222"}, {"role": "accent", "hex": "#ff5500"}}
	if out, isErr := call(t, cs, "submit_plan", weak); !isErr || !strings.Contains(out, "distinct kinds of transition") || !strings.Contains(out, "contrast") {
		t.Fatalf("weak plan should be rejected with reasons: %s", out)
	}
	bad := testPlan(runID)
	bad["scenes"].([]map[string]any)[0]["templates"] = []string{"not-a-real-template"}
	if out, isErr := call(t, cs, "submit_plan", bad); !isErr || !strings.Contains(out, "not in the registry") {
		t.Fatalf("unknown template should be rejected: %s", out)
	}
	// ---- brand assets: the real logo, never a redrawn one ----
	noBrand := testPlan(runID)
	delete(noBrand, "no_brand_assets")
	if out, isErr := call(t, cs, "submit_plan", noBrand); !isErr || !strings.Contains(out, "components/BrandMark.tsx") {
		t.Fatalf("a plan without the project's logo should be rejected, naming it: %s", out)
	}
	out, _ = call(t, cs, "find_brand_assets", map[string]any{"run_id": runID})
	if !strings.Contains(out, "components/BrandMark.tsx") || !strings.Contains(out, "placeholder") {
		t.Fatalf("find_brand_assets: %s", out)
	}
	if out, isErr := call(t, cs, "copy_from_project", map[string]any{"run_id": runID, "path": "components/BrandMark.tsx"}); !isErr {
		t.Fatalf("copying source without extract_svg should be refused: %s", out)
	}
	if out, isErr := call(t, cs, "copy_from_project", map[string]any{"run_id": runID, "path": ".env", "dest": "brand/x.svg"}); !isErr {
		t.Fatalf("secrets must stay blocked: %s", out)
	}
	if out, isErr := call(t, cs, "copy_from_project", map[string]any{"run_id": runID, "path": "components/BrandMark.tsx", "extract_svg": true, "dest": "composition/assets/logo.svg"}); !isErr {
		t.Fatalf("composition writes are gated before approval: %s", out)
	}
	// A redrawn look-alike written by hand is refused…
	fake := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 10 10"><path fill="#008fe5" d="M1 1 L9 1 L9 9 L1 9 Z M2 2 L8 2 L8 8 L2 8 Z M3 3 L7 3 L7 7 Z"/></svg>`
	call(t, cs, "write_file", map[string]any{"run_id": runID, "path": "brand/fake.svg", "content": fake})
	if out, isErr := call(t, cs, "submit_plan", noBrand); !isErr || !strings.Contains(out, "looks redrawn") {
		t.Fatalf("a redrawn logo must be refused: %s", out)
	}
	r0, _ := a.getRun(runID)
	os.Remove(filepath.Join(r0.Dir, "brand", "fake.svg"))
	// …while the exact SVG passes, however it got into brand/ and even when
	// the plan doesn't list it.
	out, isErr = call(t, cs, "copy_from_project", map[string]any{"run_id": runID, "path": "components/BrandMark.tsx", "extract_svg": true})
	if isErr || !strings.Contains(out, "brand/brandmark.svg") {
		t.Fatalf("extract logo: %s", out)
	}
	delete(plan, "no_brand_assets")

	// Files the run made itself (captures) can be copied and read by their
	// full path, as tools print it.
	os.MkdirAll(filepath.Join(r0.Dir, "capture"), 0o755)
	shot := filepath.Join(r0.Dir, "capture", "home.png")
	os.WriteFile(shot, []byte("\x89PNG fake"), 0o644)
	if out, isErr := call(t, cs, "copy_from_project", map[string]any{"run_id": runID, "path": shot, "dest": "brand/home.png"}); isErr {
		t.Fatalf("copy a capture by absolute path: %s", out)
	}
	if out, isErr := call(t, cs, "copy_from_project", map[string]any{"run_id": runID, "path": "capture/home.png", "dest": "brand/home2.png"}); isErr {
		t.Fatalf("copy a capture by run-relative path: %s", out)
	}
	if out, isErr := call(t, cs, "read_file", map[string]any{"path": filepath.Join(r0.Dir, "brand", "brandmark.svg")}); isErr || !strings.Contains(out, "<svg") {
		t.Fatalf("read a run file by absolute path without run_id: %s", out)
	}
	if out, isErr := call(t, cs, "read_file", map[string]any{"run_id": runID, "path": "/etc/hostname"}); !isErr {
		t.Fatalf("absolute paths outside the run stay blocked: %s", out)
	}
	os.Remove(filepath.Join(r0.Dir, "brand", "home.png"))
	os.Remove(filepath.Join(r0.Dir, "brand", "home2.png"))

	out, isErr = call(t, cs, "submit_plan", plan)
	if isErr || !strings.Contains(out, "LOCKED") || !strings.Contains(out, "plan.html") || !strings.Contains(out, "(https://hyperframes.heygen.com/catalog/") {
		t.Fatalf("submit_plan: %s", out)
	}
	r, _ := a.getRun(runID)
	page, err := os.ReadFile(filepath.Join(r.Dir, "plan.html"))
	if err != nil || !strings.Contains(string(page), "#0B1020") || !strings.Contains(string(page), "Swipe right") {
		t.Fatalf("plan page: %v", err)
	}
	if _, isErr := call(t, cs, "copy_asset", map[string]any{"run_id": runID, "asset": "music/track.mp3"}); !isErr {
		t.Fatal("copy_asset should be gated while awaiting approval")
	}

	// ---- Phase 2 ----
	out, isErr = call(t, cs, "approve_plan", map[string]any{"run_id": runID, "user_said": "looks great, go"})
	if isErr || !strings.Contains(out, "composition/compositions/hook.html") || !strings.Contains(out, "composition/assets/brandmark.svg") {
		t.Fatalf("approve: %s", out)
	}
	if logo, _ := os.ReadFile(filepath.Join(r.Dir, "composition", "assets", "brandmark.svg")); !strings.Contains(string(logo), "M244.5 364.9") {
		t.Fatalf("the real logo should be in composition/assets: %s", logo)
	}
	host, _ := os.ReadFile(filepath.Join(r.Dir, "composition", "index.html"))
	for _, want := range []string{`data-composition-src="compositions/hook.html"`, `--accent: #FF4D8D`, `data-duration="20"`} {
		if !strings.Contains(string(host), want) {
			t.Fatalf("scaffold host missing %q:\n%s", want, host)
		}
	}
	if scene, _ := os.ReadFile(filepath.Join(r.Dir, "composition", "compositions", "hook.html")); !strings.Contains(string(scene), "immediateRender: false") || !strings.Contains(string(scene), "Swipe right") {
		t.Fatalf("scene stub: %s", scene)
	}
	if out, isErr := call(t, cs, "patch_file", map[string]any{"run_id": runID, "path": "composition/compositions/hook.html", "old_string": "Swipe right", "new_string": "Swipe RIGHT"}); isErr {
		t.Fatal(out)
	}
	if out, isErr := call(t, cs, "list_assets", map[string]any{}); isErr || !strings.Contains(out, "No audio is bundled") {
		t.Fatalf("assets: %s", out)
	}
	if out, _ := call(t, cs, "brag_guide", map[string]any{"topic": "cues"}); !strings.Contains(out, "Not available") {
		t.Fatalf("cues without audio: %s", out)
	}
	music := testPlan(runID)
	music["music"] = "music/missing.mp3"
	if out, isErr := call(t, cs, "submit_plan", music); !isErr || !strings.Contains(out, "not found") {
		t.Fatalf("missing music should be rejected: %s", out)
	}
	if out, isErr := call(t, cs, "hyperframes", map[string]any{"run_id": runID, "args": []string{"render", "--quality", "delivery", "--output", "../brag.mp4"}}); !isErr || !strings.Contains(out, "review.md") {
		t.Fatalf("delivery render should need check+review: %s", out)
	}
	if log, _ := os.ReadFile(filepath.Join(r.Dir, "calls.log")); !strings.Contains(string(log), "submit_plan") || !strings.Contains(string(log), "approve_plan") {
		t.Fatalf("calls.log: %s", log)
	}

	if _, isErr := call(t, cs, "hyperframes", map[string]any{"run_id": runID, "args": []string{"publish"}}); !isErr {
		t.Fatal("publish should be blocked")
	}
	if _, isErr := call(t, cs, "hyperframes", map[string]any{"run_id": runID, "args": []string{"render", "--output", "/tmp/x.mp4"}}); !isErr {
		t.Fatal("absolute output outside run should be blocked")
	}
	if _, isErr := call(t, cs, "hyperframes", map[string]any{"run_id": runID, "args": []string{"render", "--output=../../x.mp4"}}); !isErr {
		t.Fatal("../.. output should be blocked")
	}

	if _, err := exec.LookPath("ffmpeg"); err == nil {
		gen := exec.Command("ffmpeg", "-y", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc=size=640x360:rate=30:duration=3",
			"-f", "lavfi", "-i", "sine=duration=3", "-shortest", "-c:v", "libx264", "-pix_fmt", "yuv420p", filepath.Join(r.Dir, "brag.mp4"))
		if b, err := gen.CombinedOutput(); err != nil {
			t.Fatalf("gen video: %v %s", err, b)
		}
		if out, isErr := call(t, cs, "extract_frames", map[string]any{"run_id": runID, "times": []float64{0.5, 2}}); isErr || strings.Count(out, "[image") != 2 {
			t.Fatalf("frames: %s", out)
		}
		if _, isErr := call(t, cs, "finalize", map[string]any{"run_id": runID, "poster_time": 1.5, "share_copy": "x"}); !isErr {
			t.Fatal("finalize should need check+review")
		}
		a.updateRun(r, func(r *run) { r.CheckedAt = time.Now() })
		os.WriteFile(filepath.Join(r.Dir, "review.md"), []byte(strings.Repeat("reviewed every beat against the bar. ", 10)), 0o644)
		if out, isErr := call(t, cs, "finalize", map[string]any{"run_id": runID, "poster_time": 1.5, "share_copy": "x"}); !isErr || !strings.Contains(out, "review_scene") || !strings.Contains(out, "## <scene id>") {
			t.Fatalf("finalize should need every scene reviewed and a section per scene: %s", out)
		}
		var sections strings.Builder
		a.updateRun(r, func(r *run) {
			r.Reviewed = map[string]time.Time{}
			for _, id := range []string{"hook", "reveal", "demo", "proof", "endcard"} {
				r.Reviewed[id] = time.Now().Add(time.Second)
				sections.WriteString("## " + id + "\nsubject clear, voice matches, legible.\n")
			}
		})
		os.WriteFile(filepath.Join(r.Dir, "review.md"), []byte(strings.Repeat("reviewed every beat against the bar. ", 10)+"\n"+sections.String()), 0o644)
		out, isErr := call(t, cs, "finalize", map[string]any{"run_id": runID, "poster_time": 1.5, "share_copy": "Swipe right on horses."})
		if isErr || !strings.Contains(out, "/files/"+testToken+"/brag/"+runID+"/brag.mp4") {
			t.Fatalf("finalize: %s", out)
		}
		if _, err := os.Stat(filepath.Join(r.Dir, "share-copy.txt")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPublicFilesAndResourceSource(t *testing.T) {
	cs, a, project := setup(t)
	out, _ := call(t, cs, "brag_start", map[string]any{"project_path": project})
	runID := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(out, "run_id: "), "\n", 2)[0])
	r, err := a.getRun(runID)
	if err != nil {
		t.Fatal(out)
	}
	os.WriteFile(filepath.Join(r.Dir, "brag.jpg"), []byte("jpeg"), 0o644)

	u := a.fileURL(r, "brag.jpg")
	resp, err := http.Get(u)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("public file %s: %v %v", u, err, resp)
	}
	resp.Body.Close()
	for _, bad := range []string{"/files/" + testToken + "/brag/nope/brag.jpg", "/files/" + testToken + "/brag/" + runID + "/../x"} {
		if resp, err := http.Get(strings.TrimSuffix(u, "/brag/"+runID+"/brag.jpg") + strings.TrimPrefix(bad, "/files/"+testToken)); err == nil {
			if resp.StatusCode != 404 {
				t.Fatalf("%s: want 404, got %d", bad, resp.StatusCode)
			}
			resp.Body.Close()
		}
	}

	var public string
	d := newTestDeps(t, []string{project}, &public)
	ts, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	b := ts.(*app)
	b.addRun(r)
	res, err := d.Resources.SaveFromSource(context.Background(), "brag://"+runID+"/brag.jpg", "", "poster")
	if err != nil || res.Origin != "brag" || res.Size != 4 {
		t.Fatalf("brag source: %v %+v", err, res)
	}
	if _, err := d.Resources.SaveFromSource(context.Background(), "brag://"+runID+"/../../etc/passwd", "", ""); err == nil {
		t.Fatal("escape via brag source should fail")
	}
}

// BRAG_E2E=1 go test -run E2E -v  — exercises real npx hyperframes and headless Chrome.
func TestE2EExternal(t *testing.T) {
	if os.Getenv("BRAG_E2E") == "" {
		t.Skip("set BRAG_E2E=1")
	}
	cs, _, project := setup(t)
	out, _ := call(t, cs, "brag_start", map[string]any{"project_path": project})
	runID := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(out, "run_id: "), "\n", 2)[0])

	out, isErr := call(t, cs, "hyperframes", map[string]any{"run_id": runID, "args": []string{"--version"}})
	if !isErr {
		t.Fatal("flags as subcommand should be rejected")
	}
	out, isErr = call(t, cs, "hyperframes", map[string]any{"run_id": runID, "args": []string{"info"}, "wait_seconds": 50})
	t.Logf("hyperframes info:\n%s", out)
	if isErr || !strings.Contains(out, "status:") {
		t.Fatal(out)
	}
	out, isErr = call(t, cs, "capture_url", map[string]any{"url": "https://example.com", "run_id": runID})
	t.Logf("capture:\n%.600s", out)
	if isErr || !strings.Contains(out, "[image") {
		t.Fatal(out)
	}
}

func testPlan(runID string) map[string]any {
	scene := func(id, name string, d float64, script, tout string, tpl []string, product bool) map[string]any {
		return map[string]any{"id": id, "name": name, "duration": d, "script": script,
			"visual": "the real " + name + " UI", "motion": "push in, then exit left",
			"templates": tpl, "transition_out": tout, "shows_product": product}
	}
	return map[string]any{
		"run_id": runID, "title": "Horse Tinder", "angle": "Dating, but for horses, played completely straight.",
		"tone": "deadpan", "format": "landscape",
		"palette": []map[string]any{
			{"role": "background", "hex": "#0B1020", "name": "page bg"},
			{"role": "text", "hex": "#F3F6FC"},
			{"role": "accent", "hex": "#FF4D8D", "name": "swipe button"},
		},
		"display_font": "Inter", "body_font": "JetBrains Mono",
		"music": "none",
		"scenes": []map[string]any{
			scene("hook", "Hook", 3, "Swipe right on horses", "whip-pan", []string{"headline-slam"}, false),
			scene("reveal", "Reveal", 4, "Horse Tinder", "match-cut", []string{}, true),
			scene("demo", "Demo", 5, "", "iris-reveal", []string{"oversized-cursor"}, true),
			scene("proof", "Proof", 4, "12,000 neighs", "light-leak", []string{"number-wheel"}, false),
			scene("endcard", "End card", 4, "horsetinder.app", "", []string{"logo-sting"}, false),
		},
		"share_copy":      "Swipe right on horses.",
		"no_brand_assets": "test plan",
	}
}

// BRAG_E2E=1 go test -run E2EScaffold -v — the approved-plan scaffold must pass the real `hyperframes check`.
func TestE2EScaffoldPassesCheck(t *testing.T) {
	if os.Getenv("BRAG_E2E") == "" {
		t.Skip("set BRAG_E2E=1")
	}
	cs, a, project := setup(t)
	out, _ := call(t, cs, "brag_start", map[string]any{"project_path": project})
	runID := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(out, "run_id: "), "\n", 2)[0])
	if out, isErr := call(t, cs, "submit_plan", testPlan(runID)); isErr {
		t.Fatal(out)
	}
	if out, isErr := call(t, cs, "approve_plan", map[string]any{"run_id": runID, "user_said": "go"}); isErr {
		t.Fatal(out)
	}
	out, _ = call(t, cs, "hyperframes", map[string]any{"run_id": runID, "args": []string{"check", "--json"}, "wait_seconds": 50})
	for i := 0; strings.Contains(out, "status: running") && i < 8; i++ {
		id := strings.TrimSpace(strings.SplitN(strings.TrimPrefix(out, "job_id: "), "\n", 2)[0])
		out, _ = call(t, cs, "job_status", map[string]any{"job_id": id, "wait_seconds": 50})
	}
	t.Logf("check:\n%s", clip(out, 6000))
	if !strings.Contains(out, "status: succeeded") {
		t.Fatal("scaffold failed hyperframes check")
	}
	time.Sleep(200 * time.Millisecond)
	if r, _ := a.getRun(runID); r.CheckedAt.IsZero() {
		t.Fatal("passing check was not recorded")
	}
	out, isErr := call(t, cs, "review_sheet", map[string]any{"run_id": runID})
	for i := 0; strings.Contains(out, "still running") && i < 6; i++ {
		id := regexp.MustCompile(`job-\d+`).FindString(out)
		out, isErr = call(t, cs, "review_sheet", map[string]any{"run_id": runID, "job_id": id})
	}
	t.Logf("review_sheet:\n%s", clip(out, 1500))
	if isErr || !strings.Contains(out, "[image") {
		t.Fatal("review_sheet returned no images")
	}
}
