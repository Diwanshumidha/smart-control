package brag

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Diwanshumidha/smart-control/internal/voice"
)

const (
	stagePlanning = "planning"
	stageAwaiting = "awaiting_approval"
	stageBuilding = "building"
)

type paletteColor struct {
	Role string `json:"role" jsonschema:"background, surface, text, muted, accent or accent2"`
	Hex  string `json:"hex" jsonschema:"#RRGGBB, taken from the project where possible"`
	Name string `json:"name,omitempty" jsonschema:"where it comes from, e.g. 'primary button in styles.css'"`
}

type planScene struct {
	ID            string   `json:"id" jsonschema:"short slug, e.g. hook, reveal, demo, payoff, endcard"`
	Name          string   `json:"name"`
	Duration      float64  `json:"duration" jsonschema:"seconds"`
	Script        string   `json:"script" jsonschema:"exact on-screen text for this beat, verbatim; empty if purely visual"`
	Visual        string   `json:"visual" jsonschema:"what the viewer sees, concretely, including which real UI/copy/asset is shown"`
	Motion        string   `json:"motion" jsonschema:"how things move: entrances, camera, exits, the beat it lands on"`
	Templates     []string `json:"templates,omitempty" jsonschema:"registry template names used in this scene (from search_templates)"`
	TransitionOut string   `json:"transition_out,omitempty" jsonschema:"how this scene hands over to the next: a registry transition name or a described technique (empty only for the last scene)"`
	SFX           string   `json:"sfx,omitempty" jsonschema:"sound moments in this scene"`
	Narration     string   `json:"narration,omitempty" jsonschema:"voiceover spoken during this beat (not read off the screen): one or two short sentences about what the viewer sees right now. [pause 0.4] inserts exact silence; {cue-name} marks the word a visual action lands on. Empty for a silent beat"`
	ShowsProduct  bool     `json:"shows_product,omitempty" jsonschema:"true if this beat shows the real product (UI, copy, output)"`
}

type planIn struct {
	RunID                string         `json:"run_id"`
	Title                string         `json:"title" jsonschema:"product name"`
	Angle                string         `json:"angle" jsonschema:"one paragraph: the creative angle"`
	Tone                 string         `json:"tone" jsonschema:"preset (default, polished, yc-parody, chaotic, deadpan, cinematic, app-store) plus any freeform direction"`
	Format               string         `json:"format" jsonschema:"landscape, vertical or square"`
	RequestedDuration    float64        `json:"requested_duration,omitempty" jsonschema:"only if the user asked for a specific length"`
	Palette              []paletteColor `json:"palette"`
	DisplayFont          string         `json:"display_font"`
	BodyFont             string         `json:"body_font"`
	Music                string         `json:"music" jsonschema:"asset path from list_assets, or 'none'"`
	MusicNotes           string         `json:"music_notes,omitempty" jsonschema:"volume, fades, beat-sync moments"`
	Voice                string         `json:"voice,omitempty" jsonschema:"voiceover voice id (from the voiceover tool's voice list); empty uses the dashboard default. Only used when scenes have narration"`
	Scenes               []planScene    `json:"scenes"`
	BrandAssets          []string       `json:"brand_assets,omitempty" jsonschema:"run-relative brand/… paths of the real logo (and icons/fonts) you copied with copy_from_project or download_asset; the video uses these files, never a redrawn logo"`
	NoBrandAssets        string         `json:"no_brand_assets,omitempty" jsonschema:"only if the product truly has no logo: what you searched (find_brand_assets, header components, the live site)"`
	ScreenshotsRequested string         `json:"screenshots_requested,omitempty" jsonschema:"only if the user asked for screenshots in the video: their words, quoted. Leave empty otherwise: the product UI is rebuilt from source and captures are only a reference"`
	ShareCopy            string         `json:"share_copy" jsonschema:"draft caption, 1-3 sentences"`
	Avoid                []string       `json:"avoid,omitempty" jsonschema:"anti-patterns you are deliberately avoiding"`
}

var (
	hexRe   = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	slugRe  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,30}$`)
	bundled = []string{"Inter", "Montserrat", "Outfit", "Nunito", "Oswald", "League Gothic", "Archivo Black", "Space Mono",
		"IBM Plex Mono", "JetBrains Mono", "EB Garamond", "Playfair Display", "Source Code Pro", "Noto Sans JP", "Roboto",
		"Open Sans", "Lato", "Poppins"}
)

func formatSize(f string) (int, int, bool) {
	switch f {
	case "landscape", "":
		return 1920, 1080, true
	case "vertical":
		return 1080, 1920, true
	case "square":
		return 1080, 1080, true
	}
	return 0, 0, false
}

func luminance(hex string) float64 {
	v, _ := strconv.ParseUint(hex[1:], 16, 32)
	ch := func(c uint64) float64 {
		s := float64(c) / 255
		if s <= 0.03928 {
			return s / 12.92
		}
		return math.Pow((s+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(v>>16&0xff) + 0.7152*ch(v>>8&0xff) + 0.0722*ch(v&0xff)
}

func contrast(a, b string) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

func (p *planIn) color(role string) string {
	for _, c := range p.Palette {
		if c.Role == role {
			return strings.ToUpper(c.Hex)
		}
	}
	return ""
}

func (p *planIn) totalDuration() float64 {
	t := 0.0
	for _, s := range p.Scenes {
		t += s.Duration
	}
	return math.Round(t*100) / 100
}

func (p *planIn) narrated() bool {
	for _, s := range p.Scenes {
		if strings.TrimSpace(s.Narration) != "" {
			return true
		}
	}
	return false
}

func (p *planIn) templateNames() []string {
	var out []string
	for _, s := range p.Scenes {
		for _, t := range s.Templates {
			if !contains(out, t) {
				out = append(out, t)
			}
		}
		if s.TransitionOut != "" && !strings.Contains(s.TransitionOut, " ") && !contains(out, s.TransitionOut) {
			out = append(out, s.TransitionOut) // a bare registry name used as the transition
		}
	}
	return out
}

// validate returns blocking problems and non-blocking warnings.
func (a *app) validatePlan(ctx context.Context, p *planIn) (errs, warns []string) {
	if strings.TrimSpace(p.Title) == "" || strings.TrimSpace(p.Angle) == "" {
		errs = append(errs, "title and angle are required")
	}
	if _, _, ok := formatSize(p.Format); !ok {
		errs = append(errs, "format must be landscape, vertical or square")
	}
	// Duration
	total := p.totalDuration()
	if p.RequestedDuration > 0 {
		if math.Abs(total-p.RequestedDuration) > 2 {
			errs = append(errs, fmt.Sprintf("scenes sum to %.1fs but the user asked for %.0fs", total, p.RequestedDuration))
		}
	} else if total < 15 || total > 25 {
		errs = append(errs, fmt.Sprintf("scenes sum to %.1fs; brag videos are 15-25s", total))
	}
	// Scenes
	if len(p.Scenes) < 4 {
		errs = append(errs, "plan at least 4 scenes (hook, reveal, highlights, payoff/end card)")
	}
	seen := map[string]bool{}
	transitions := map[string]bool{}
	product := false
	narrated := 0
	vcfg := a.voiceConfig()
	speed := vcfg.Speed
	for i, s := range p.Scenes {
		if !slugRe.MatchString(s.ID) {
			errs = append(errs, fmt.Sprintf("scene %d id %q must be a lowercase slug", i+1, s.ID))
		}
		if seen[s.ID] {
			errs = append(errs, fmt.Sprintf("duplicate scene id %q", s.ID))
		}
		seen[s.ID] = true
		if s.Duration < 1 {
			errs = append(errs, fmt.Sprintf("scene %q is %.1fs; nothing reads in under 1s", s.ID, s.Duration))
		}
		if strings.TrimSpace(s.Visual) == "" || strings.TrimSpace(s.Motion) == "" {
			errs = append(errs, fmt.Sprintf("scene %q needs both visual and motion", s.ID))
		}
		if i < len(p.Scenes)-1 {
			if strings.TrimSpace(s.TransitionOut) == "" {
				errs = append(errs, fmt.Sprintf("scene %q has no transition_out: every scene must hand over on purpose, not just disappear", s.ID))
			} else {
				transitions[strings.ToLower(strings.Fields(s.TransitionOut)[0])] = true
			}
		}
		if words := len(strings.Fields(s.Script)); words > 0 && s.Duration < 0.8+0.3*float64(words) {
			warns = append(warns, fmt.Sprintf("scene %q: %d words in %.1fs may not be readable (~0.3s per word plus settle)", s.ID, words, s.Duration))
		}
		product = product || s.ShowsProduct
		if p.ScreenshotsRequested == "" && screenshotWordRe.MatchString(s.Visual+" "+s.Motion) {
			errs = append(errs, fmt.Sprintf("scene %q puts a screenshot in the video, but the user didn't ask for screenshots. Rebuild that UI from the product's source (captures are your reference), or quote the user's request in screenshots_requested", s.ID))
		}
		if n := strings.TrimSpace(s.Narration); n != "" {
			narrated++
			if _, err := voice.Parse(n); err != nil {
				errs = append(errs, fmt.Sprintf("scene %q narration: %v", s.ID, err))
			} else if est, room := voice.EstimateSeconds(n, speed), s.Duration-voiceLead-voiceTail; est > room*1.1 {
				errs = append(errs, fmt.Sprintf("scene %q: the narration takes about %.1fs to say but the scene leaves %.1fs for it (the voice starts %.2fs in and stops %.2fs before the cut). Lengthen the scene, cut words, or split the line across two beats", s.ID, est, math.Max(room, 0), voiceLead, voiceTail))
			}
		}
	}
	if narrated > 0 {
		v := p.Voice
		if v == "" {
			v = vcfg.Voice
		}
		if info, err := voice.Info(vcfg.Provider); err != nil {
			warns = append(warns, err.Error())
		} else if !info.HasVoice(v) {
			errs = append(errs, fmt.Sprintf("voice %q isn't offered by %s; pick one of: %s", v, info.Title, voiceIDs(info)))
		}
	}
	if len(transitions) < 3 && len(p.Scenes) >= 4 {
		errs = append(errs, fmt.Sprintf("only %d distinct kinds of transition; use at least 3 (e.g. a match cut, a whip, a zoom-through)", len(transitions)))
	}
	if !product {
		errs = append(errs, "no scene has shows_product=true: at least one beat must show the real product")
	}
	// Palette
	for _, c := range p.Palette {
		if !hexRe.MatchString(c.Hex) {
			errs = append(errs, fmt.Sprintf("palette color %q is not #RRGGBB", c.Hex))
		}
	}
	bg, fg, ac := p.color("background"), p.color("text"), p.color("accent")
	if bg == "" || fg == "" || ac == "" {
		errs = append(errs, "palette needs at least background, text and accent roles")
	} else if hexRe.MatchString(bg) && hexRe.MatchString(fg) {
		if r := contrast(bg, fg); r < 4.5 {
			errs = append(errs, fmt.Sprintf("text %s on background %s has contrast %.1f:1; needs ≥ 4.5", fg, bg, r))
		}
		if hexRe.MatchString(ac) && contrast(bg, ac) < 3 {
			warns = append(warns, fmt.Sprintf("accent %s on background %s is only %.1f:1; fine for shapes, not for text", ac, bg, contrast(bg, ac)))
		}
	}
	// Fonts
	for _, f := range []string{p.DisplayFont, p.BodyFont} {
		if f == "" {
			errs = append(errs, "display_font and body_font are required")
			break
		}
		if !contains(bundled, f) {
			warns = append(warns, fmt.Sprintf("font %q is not bundled with Hyperframes: the build must ship it with @font-face (or use one of: %s)", f, strings.Join(bundled, ", ")))
		}
	}
	// Templates
	names := p.templateNames()
	if len(names) == 0 {
		errs = append(errs, "no registry templates: use search_templates and put at least one in a scene's templates")
	}
	if c, err := a.loadCatalog(ctx); err != nil {
		warns = append(warns, "could not verify template names: "+err.Error())
	} else {
		for _, n := range names {
			if c.get(n) == nil {
				errs = append(errs, fmt.Sprintf("template %q is not in the registry; use search_templates for exact names", n))
			}
		}
	}
	// Music
	if p.Music != "" && p.Music != "none" {
		dir, err := a.ensureAssets()
		if err == nil {
			if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(p.Music))); err != nil {
				errs = append(errs, fmt.Sprintf("music %q not found; use a path from list_assets or 'none'", p.Music))
			}
		}
	}
	return errs, warns
}

func (a *app) toolSubmitPlan(ctx context.Context, _ *mcp.CallToolRequest, in planIn) (*mcp.CallToolResult, any, error) {
	r, err := a.getRun(in.RunID)
	if err != nil {
		return nil, nil, err
	}
	errs, warns := a.validatePlan(ctx, &in)
	errs = append(errs, a.checkBrandAssets(r, &in)...)
	if len(errs) > 0 {
		return nil, nil, fmt.Errorf("plan not accepted — fix these and submit again:\n- %s%s", strings.Join(errs, "\n- "), warnText(warns))
	}
	b, _ := json.MarshalIndent(in, "", "  ")
	if err := os.WriteFile(filepath.Join(r.Dir, "plan.json"), b, 0o644); err != nil {
		return nil, nil, err
	}
	c, _ := a.loadCatalog(ctx)
	md := renderPlanMarkdown(&in, c)
	if err := os.WriteFile(filepath.Join(r.Dir, "brag-plan.md"), []byte(md), 0o644); err != nil {
		return nil, nil, err
	}
	if err := os.WriteFile(filepath.Join(r.Dir, "plan.html"), []byte(renderPlanHTML(&in, c)), 0o644); err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(b)
	a.updateRun(r, func(r *run) {
		r.Stage, r.PlanHash, r.ApprovedQuote, r.Templates = stageAwaiting, hex.EncodeToString(sum[:]), "", in.templateNames()
	})

	var out strings.Builder
	out.WriteString("Plan saved. Building is LOCKED until the user approves.\n\n")
	out.WriteString("Now show the user the plan below (as written — script, colours, templates with links), plus the visual plan page link if there is one, and ask them to approve or request changes. Do NOT start building. When they approve, call approve_plan with their words; if they ask for changes, edit and call submit_plan again.\n")
	if u := a.fileURL(r, "plan.html"); u != "" {
		fmt.Fprintf(&out, "\nVisual plan page (palette swatches, storyboard, template previews): %s\n", u)
	}
	out.WriteString(warnText(warns))
	out.WriteString("\n---\n\n")
	out.WriteString(md)
	return text(out.String()), nil, nil
}

func warnText(w []string) string {
	if len(w) == 0 {
		return ""
	}
	return "\n\nWarnings (non-blocking):\n- " + strings.Join(w, "\n- ") + "\n"
}

type approveIn struct {
	RunID    string `json:"run_id"`
	UserSaid string `json:"user_said" jsonschema:"the user's approval message, quoted exactly"`
}

func (a *app) toolApprovePlan(ctx context.Context, _ *mcp.CallToolRequest, in approveIn) (*mcp.CallToolResult, any, error) {
	r, err := a.getRun(in.RunID)
	if err != nil {
		return nil, nil, err
	}
	if r.Stage != stageAwaiting {
		return nil, nil, fmt.Errorf("run is in stage %q; submit_plan first and show it to the user", r.Stage)
	}
	if strings.TrimSpace(in.UserSaid) == "" {
		return nil, nil, fmt.Errorf("quote the user's approval in user_said")
	}
	b, err := os.ReadFile(filepath.Join(r.Dir, "plan.json"))
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != r.PlanHash {
		return nil, nil, fmt.Errorf("plan.json changed after it was shown to the user; call submit_plan again")
	}
	var p planIn
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, nil, err
	}
	files, err := a.writeScaffold(r, &p)
	if err != nil {
		return nil, nil, fmt.Errorf("scaffold: %v", err)
	}
	brand, err := a.promoteBrandAssets(r)
	if err != nil {
		return nil, nil, fmt.Errorf("brand assets: %v", err)
	}
	a.updateRun(r, func(r *run) {
		r.Stage, r.ApprovedQuote, r.EditedAt = stageBuilding, in.UserSaid, time.Now()
	})
	var out strings.Builder
	out.WriteString("Plan approved — building is unlocked.\n\nScaffold written from the plan (run-relative):\n")
	for _, f := range files {
		fmt.Fprintf(&out, "  %s\n", f)
	}
	if len(brand) > 0 {
		out.WriteString("\nBrand assets (the real files — use them as-is, e.g. <img src=\"assets/logo.svg\">; never redraw a logo):\n")
		for _, f := range brand {
			fmt.Fprintf(&out, "  %s\n", f)
		}
	}
	out.WriteString(`
Next (brag_guide "index", Phase 2):
1. hyperframes ["add", <name>] for every template in the plan; read each installed file's header before wiring it.
2. Build each scene in composition/compositions/<scene>.html — one scene per write, patch_file for fixes. Keep the plan's script verbatim.
3. hyperframes ["check","--json"] until zero errors, then review_sheet, write review.md, revise.
4. Draft render → extract_frames → delivery render → finalize.
Follow the approved plan. If something in it can't work, tell the user what you're changing and why.`)
	return text(out.String()), nil, nil
}

// requireBuilding gates composition writes and builds behind plan approval.
func (a *app) requireBuilding(r *run) error {
	if r.Stage == "" || r.Stage == stageBuilding {
		return nil // runs from before the two-step flow aren't gated
	}
	return fmt.Errorf("the plan is not approved yet (stage: %s). Phase 1 first: search_templates → submit_plan → show the user → approve_plan after they confirm", r.Stage)
}

// ---- rendering the plan for people ----

func templateLine(name string, c *catalog) string {
	if c != nil {
		if it := c.get(name); it != nil {
			desc := it.Use
			if desc == "" {
				desc = it.Description
			}
			return fmt.Sprintf("[%s](%s) — %s", name, it.PageURL(), clip(desc, 140))
		}
	}
	return name
}

func renderPlanMarkdown(p *planIn, c *catalog) string {
	var b strings.Builder
	w, h, _ := formatSize(p.Format)
	fmt.Fprintf(&b, "# Brag plan: %s\n\n", p.Title)
	fmt.Fprintf(&b, "**Angle.** %s\n\n", p.Angle)
	fmt.Fprintf(&b, "**Tone:** %s · **Format:** %s %dx%d · **Length:** %.1fs\n\n", p.Tone, p.Format, w, h, p.totalDuration())

	b.WriteString("## Theme\n\n| Role | Colour | From |\n|---|---|---|\n")
	for _, col := range p.Palette {
		fmt.Fprintf(&b, "| %s | `%s` | %s |\n", col.Role, strings.ToUpper(col.Hex), col.Name)
	}
	fmt.Fprintf(&b, "\n**Fonts:** %s (display), %s (body)\n\n", p.DisplayFont, p.BodyFont)
	if p.ScreenshotsRequested != "" {
		fmt.Fprintf(&b, "**Screenshots:** used, because you asked: \u201c%s\u201d\n\n", p.ScreenshotsRequested)
	}
	if len(p.BrandAssets) > 0 {
		b.WriteString("**Brand assets (real files from the product):** ")
		for i, f := range p.BrandAssets {
			if i > 0 {
				b.WriteString(", ")
			}
			fmt.Fprintf(&b, "`%s`", f)
		}
		b.WriteString("\n\n")
	} else if p.NoBrandAssets != "" {
		fmt.Fprintf(&b, "**Brand assets:** none — %s\n\n", p.NoBrandAssets)
	}

	b.WriteString("## Script & storyboard\n\n")
	t := 0.0
	for i, s := range p.Scenes {
		fmt.Fprintf(&b, "### %d. %s — %.1fs → %.1fs\n", i+1, s.Name, t, t+s.Duration)
		t += s.Duration
		if s.Script != "" {
			fmt.Fprintf(&b, "> %s\n\n", strings.ReplaceAll(s.Script, "\n", "\n> "))
		}
		fmt.Fprintf(&b, "- **Visual:** %s\n- **Motion:** %s\n", s.Visual, s.Motion)
		if len(s.Templates) > 0 {
			fmt.Fprintf(&b, "- **Templates:** %s\n", strings.Join(s.Templates, ", "))
		}
		if s.TransitionOut != "" {
			fmt.Fprintf(&b, "- **Transition out:** %s\n", s.TransitionOut)
		}
		if s.Narration != "" {
			fmt.Fprintf(&b, "- **Voiceover:** \u201c%s\u201d\n", s.Narration)
		}
		if s.SFX != "" {
			fmt.Fprintf(&b, "- **Sound:** %s\n", s.SFX)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Templates\n\n")
	for _, n := range p.templateNames() {
		fmt.Fprintf(&b, "- %s\n", templateLine(n, c))
	}
	music := p.Music
	if music == "" {
		music = "none"
	}
	fmt.Fprintf(&b, "\n## Audio\n\n**Music:** %s\n", music)
	if p.narrated() {
		v := p.Voice
		if v == "" {
			v = "the dashboard default"
		}
		fmt.Fprintf(&b, "\n**Voiceover:** %s, one line per scene in the storyboard above\n", v)
	}
	if p.MusicNotes != "" {
		fmt.Fprintf(&b, "%s\n", p.MusicNotes)
	}
	fmt.Fprintf(&b, "\n## Share copy (draft)\n\n%s\n", p.ShareCopy)
	if len(p.Avoid) > 0 {
		fmt.Fprintf(&b, "\n## Deliberately avoiding\n\n- %s\n", strings.Join(p.Avoid, "\n- "))
	}
	return b.String()
}
