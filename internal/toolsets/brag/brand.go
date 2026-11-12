package brag

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Brand assets are the product's real logo, icons and fonts. The video must
// use these files, never a redrawn look-alike, so they are found and copied
// into <run>/brand/ during planning, named in the plan, and copied into
// composition/assets/ when the plan is approved.

const maxAssetBytes = 50 << 20

var (
	brandName = regexp.MustCompile(`(?i)(logo|brand|wordmark|lockup|favicon|apple-touch|app-?icon|icon-\d|og-image)|(^|[-_.]|[a-z])Mark([-_.A-Z]|$)|(^|[-_.])mark([-_.]|$)`)
	// Files create-next-app, Vite, Turborepo & co. ship; never the product's brand.
	frameworkDefaults = map[string]bool{
		"next.svg": true, "vercel.svg": true, "turborepo-dark.svg": true, "turborepo-light.svg": true,
		"globe.svg": true, "window.svg": true, "file.svg": true, "file-text.svg": true, "vite.svg": true,
		"react.svg": true, "svelte.svg": true, "nuxt.svg": true, "astro.svg": true, "tauri.svg": true,
	}
	assetExt = map[string]bool{
		".svg": true, ".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true, ".avif": true, ".ico": true,
		".woff": true, ".woff2": true, ".ttf": true, ".otf": true,
		".mp4": true, ".webm": true, ".mov": true, ".mp3": true, ".wav": true, ".m4a": true, ".ogg": true,
		".json": true, ".lottie": true, // Lottie animations
	}
	componentExt = map[string]bool{".tsx": true, ".jsx": true, ".vue": true, ".svelte": true, ".astro": true, ".html": true}
	fontExt      = map[string]bool{".woff": true, ".woff2": true, ".ttf": true, ".otf": true}
)

type brandHit struct {
	path, kind string
	size       int64
}

type findBrandIn struct {
	RunID string `json:"run_id"`
	Path  string `json:"path,omitempty" jsonschema:"directory to search (default: the run's project)"`
}

func (a *app) toolFindBrand(_ context.Context, _ *mcp.CallToolRequest, in findBrandIn) (*mcp.CallToolResult, any, error) {
	r, err := a.getRun(in.RunID)
	if err != nil {
		return nil, nil, err
	}
	root := r.Project
	if in.Path != "" {
		if root, err = a.projectPath(in.Path); err != nil {
			return nil, nil, err
		}
	}
	if root == "" {
		return text("This run is about a website, so there's no project to search. Find the logo in capture_url's output " +
			"(the <img>/<svg> in the header, favicon, apple-touch-icon, og:image links) and fetch it with download_asset. " +
			"If the logo is an inline SVG, copy its markup verbatim into brand/logo.svg with write_file. Never redraw it."), nil, nil
	}
	hits := findBrandAssets(root)
	var b strings.Builder
	fmt.Fprintf(&b, "Brand assets in %s (paths relative to it):\n", root)
	if len(hits) == 0 {
		b.WriteString("\nNothing that looks like a logo. Look for the mark in the app's header/layout components and in the live site (capture_url) before concluding there is none.\n")
	}
	kinds := []struct{ kind, title string }{
		{"inline", "Logo components with an inline SVG — copy with copy_from_project extract_svg=true"},
		{"file", "Logo / icon files — copy with copy_from_project"},
		{"font", "Font files"},
		{"default", "Framework placeholder files (NOT the product's brand — don't use)"},
	}
	for _, k := range kinds {
		first := true
		for _, h := range hits {
			if h.kind != k.kind {
				continue
			}
			if first {
				fmt.Fprintf(&b, "\n%s:\n", k.title)
				first = false
			}
			fmt.Fprintf(&b, "  %s (%s)\n", h.path, humanSize(h.size))
		}
	}
	b.WriteString("\nCopy the real logo (and any icon or font the video shows) into the run now, then list the copied brand/… paths in submit_plan's brand_assets. Never redraw or approximate a logo with CSS or new SVG.")
	return text(b.String()), nil, nil
}

func findBrandAssets(root string) []brandHit {
	var hits []brandHit
	fonts := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if p != root && (skipDirs[name] || deniedComponent(name) || strings.HasPrefix(name, "brag-output-")) {
				return filepath.SkipDir
			}
			return nil
		}
		if deniedComponent(name) || len(hits) > 200 {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(name))
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		info, _ := d.Info()
		var size int64
		if info != nil {
			size = info.Size()
		}
		switch {
		case frameworkDefaults[strings.ToLower(name)]:
			hits = append(hits, brandHit{rel, "default", size})
		case componentExt[ext] && brandName.MatchString(strings.TrimSuffix(name, ext)):
			if b, err := os.ReadFile(p); err == nil && size < 2<<20 && strings.Contains(string(b), "<svg") {
				hits = append(hits, brandHit{rel, "inline", size})
			}
		case fontExt[ext]:
			if fonts < 12 {
				hits = append(hits, brandHit{rel, "font", size})
				fonts++
			}
		case assetExt[ext] && ext != ".json" && brandName.MatchString(name):
			hits = append(hits, brandHit{rel, "file", size})
		}
		return nil
	})
	sort.SliceStable(hits, func(i, j int) bool {
		// "logo" beats "icon": rank the likeliest logo first.
		return strings.Contains(strings.ToLower(hits[i].path), "logo") && !strings.Contains(strings.ToLower(hits[j].path), "logo")
	})
	return hits
}

type copyFromProjectIn struct {
	RunID      string `json:"run_id"`
	Path       string `json:"path" jsonschema:"file to copy: in the project (absolute, or relative to it), or in the run itself (e.g. capture/home.png, frames/at-2s.jpg)"`
	Dest       string `json:"dest,omitempty" jsonschema:"run-relative destination; default brand/<name> while planning, composition/assets/<name> after approval"`
	ExtractSVG bool   `json:"extract_svg,omitempty" jsonschema:"path is a component (.tsx/.jsx/.vue/.svelte/.astro/.html) with an inline <svg> logo: save that SVG as a standalone .svg file"`
}

func (a *app) toolCopyFromProject(_ context.Context, _ *mcp.CallToolRequest, in copyFromProjectIn) (*mcp.CallToolResult, any, error) {
	r, err := a.getRun(in.RunID)
	if err != nil {
		return nil, nil, err
	}
	src, err := a.sourcePath(r, in.Path)
	if err != nil {
		return nil, nil, err
	}
	st, err := os.Stat(src)
	if err != nil {
		return nil, nil, err
	}
	if st.IsDir() || st.Size() > maxAssetBytes {
		return nil, nil, fmt.Errorf("%s must be a file under %d MB", in.Path, maxAssetBytes>>20)
	}
	base := filepath.Base(src)
	ext := strings.ToLower(filepath.Ext(base))

	var data []byte
	var notes []string
	if in.ExtractSVG {
		b, err := os.ReadFile(src)
		if err != nil {
			return nil, nil, err
		}
		svg, n, err := extractSVG(string(b))
		if err != nil {
			return nil, nil, err
		}
		data, notes = []byte(svg), n
		base = strings.ToLower(strings.TrimSuffix(base, filepath.Ext(base))) + ".svg"
	} else if !assetExt[ext] {
		return nil, nil, fmt.Errorf("%s isn't an image, font, video, audio or Lottie file; for a logo inside a component use extract_svg=true", base)
	}

	dest, err := a.assetDest(r, in.Dest, base)
	if err != nil {
		return nil, nil, err
	}
	dst, err := a.runPath(r, dest)
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return nil, nil, err
	}
	if data != nil {
		err = os.WriteFile(dst, data, 0o644)
	} else {
		err = copyFile(src, dst)
	}
	if err != nil {
		return nil, nil, err
	}
	a.markEdited(r, dest)
	return text(a.copiedMessage(r, dest, notes)), nil, nil
}

type downloadAssetIn struct {
	RunID string `json:"run_id"`
	URL   string `json:"url" jsonschema:"direct URL of the logo, icon, font or image (from capture_url's links, favicon, og:image)"`
	Dest  string `json:"dest,omitempty" jsonschema:"run-relative destination; default brand/<name> while planning, composition/assets/<name> after approval"`
}

func (a *app) toolDownloadAsset(ctx context.Context, _ *mcp.CallToolRequest, in downloadAssetIn) (*mcp.CallToolResult, any, error) {
	r, err := a.getRun(in.RunID)
	if err != nil {
		return nil, nil, err
	}
	if a.res == nil {
		return nil, nil, fmt.Errorf("downloads are not available")
	}
	u, err := url.Parse(in.URL)
	if err != nil {
		return nil, nil, err
	}
	base := path.Base(u.Path)
	if base == "." || base == "/" || base == "" {
		base = "asset"
	}
	dest, err := a.assetDest(r, in.Dest, slugFile(base))
	if err != nil {
		return nil, nil, err
	}
	dst, err := a.runPath(r, dest)
	if err != nil {
		return nil, nil, err
	}
	ctype, err := a.res.DownloadTo(ctx, in.URL, dst, "download a brand asset for a brag video", maxAssetBytes)
	if err != nil {
		return nil, nil, err
	}
	if filepath.Ext(dst) == "" {
		if ext := extForType(ctype); ext != "" {
			if err := os.Rename(dst, dst+ext); err == nil {
				dest += ext
			}
		}
	}
	a.markEdited(r, dest)
	return text(a.copiedMessage(r, dest, nil)), nil, nil
}

// assetDest picks and checks the run-relative destination for a brand asset.
// Before approval only brand/ is writable.
func (a *app) assetDest(r *run, dest, base string) (string, error) {
	building := a.requireBuilding(r) == nil
	if dest == "" {
		if building {
			return "composition/assets/" + base, nil
		}
		return "brand/" + base, nil
	}
	dest = filepath.ToSlash(filepath.Clean(dest))
	if !building && !strings.HasPrefix(dest, "brand/") {
		return "", fmt.Errorf("before the plan is approved, brand assets go in brand/ (they're copied into composition/assets/ on approval)")
	}
	return dest, nil
}

func (a *app) copiedMessage(r *run, dest string, notes []string) string {
	msg := "copied to " + dest
	if strings.HasPrefix(dest, "brand/") {
		msg += fmt.Sprintf(" (it will be at composition/assets/%s once the plan is approved). List it in submit_plan's brand_assets.", strings.TrimPrefix(dest, "brand/"))
	}
	for _, n := range notes {
		msg += "\nnote: " + n
	}
	if strings.HasSuffix(dest, ".svg") || isImage(dest) {
		msg += "\nLook at it with view_images to confirm it's the real logo."
	}
	return msg
}

// checkBrandAssets enforces that the plan uses the product's real logo. How
// a file got into brand/ doesn't matter (copy_from_project, download_asset or
// write_file); an SVG must carry the path data of the project's logo
// component, so a redrawn look-alike is refused.
func (a *app) checkBrandAssets(r *run, p *planIn) []string {
	if len(p.BrandAssets) == 0 && strings.TrimSpace(p.NoBrandAssets) == "" {
		p.BrandAssets = a.brandFiles(r) // files already in brand/ count
	}
	var errs []string
	for _, rel := range p.BrandAssets {
		if !strings.HasPrefix(filepath.ToSlash(rel), "brand/") {
			errs = append(errs, fmt.Sprintf("brand_assets entry %q must be a brand/… path inside the run", rel))
			continue
		}
		if p, err := a.runPath(r, rel); err != nil || !fileExists(p) {
			errs = append(errs, fmt.Sprintf("brand_assets entry %q doesn't exist in the run; copy it first", rel))
		}
	}
	if len(errs) > 0 {
		return errs
	}
	var logos []string // project components with an inline SVG logo
	var found []string
	if r.Project != "" {
		for _, h := range findBrandAssets(r.Project) {
			if h.kind == "inline" {
				logos = append(logos, h.path)
			}
			if h.kind == "inline" || h.kind == "file" {
				found = append(found, h.path)
			}
		}
	}
	if len(p.BrandAssets) > 0 {
		if len(logos) > 0 && !a.hasRealLogo(r, p.BrandAssets, logos) {
			return []string{fmt.Sprintf("none of the SVGs in brand_assets carries the path data of the project's logo (%s): it looks redrawn. Copy the <svg> from that file verbatim (copy_from_project with extract_svg=true, or write_file into brand/ with the exact paths)",
				strings.Join(logos, ", "))}
		}
		return nil
	}
	if strings.TrimSpace(p.NoBrandAssets) != "" {
		return nil
	}
	if len(found) > 0 {
		return []string{fmt.Sprintf("the project has brand assets (%s) but the run's brand/ folder is empty. Put the real logo in brand/ (copy_from_project with extract_svg=true for an inline SVG component, or write_file with its exact SVG) and resubmit; never redraw a logo",
			strings.Join(found[:min(len(found), 5)], ", "))}
	}
	return []string{"no brand assets: put the real logo in the run's brand/ folder (find_brand_assets, copy_from_project, download_asset, or write_file with the exact SVG); if the product truly has no logo, say what you searched in no_brand_assets"}
}

// brandFiles lists the run's brand/ files, run-relative.
func (a *app) brandFiles(r *run) []string {
	entries, _ := os.ReadDir(filepath.Join(r.Dir, "brand"))
	var out []string
	for _, e := range entries {
		if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, "brand/"+e.Name())
		}
	}
	return out
}

var svgPathData = regexp.MustCompile(`\bd=["'{]+([^"'}]{20,})`)

// hasRealLogo reports whether one of the listed SVGs shares a path (the
// long d="…" data) with a logo component of the project. Non-SVG files
// (PNG, ICO) are copies by construction and pass.
func (a *app) hasRealLogo(r *run, assets, logos []string) bool {
	want := map[string]bool{}
	for _, l := range logos {
		b, err := os.ReadFile(filepath.Join(r.Project, l))
		if err != nil {
			continue
		}
		for _, m := range svgPathData.FindAllStringSubmatch(string(b), -1) {
			want[strings.Join(strings.Fields(m[1]), " ")] = true
		}
	}
	if len(want) == 0 {
		return true
	}
	for _, rel := range assets {
		p, err := a.runPath(r, rel)
		if err != nil {
			continue
		}
		if !strings.EqualFold(filepath.Ext(p), ".svg") {
			return true
		}
		b, _ := os.ReadFile(p)
		for _, m := range svgPathData.FindAllStringSubmatch(string(b), -1) {
			if want[strings.Join(strings.Fields(m[1]), " ")] {
				return true
			}
		}
	}
	return false
}

// promoteBrandAssets copies brand/* into composition/assets/ on approval.
func (a *app) promoteBrandAssets(r *run) ([]string, error) {
	dir := filepath.Join(r.Dir, "brand")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		dst := filepath.Join(r.Dir, "composition", "assets", e.Name())
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return out, err
		}
		if err := copyFile(filepath.Join(dir, e.Name()), dst); err != nil {
			return out, err
		}
		out = append(out, "composition/assets/"+e.Name())
	}
	return out, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func isImage(p string) bool {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".webp", ".gif", ".avif", ".ico":
		return true
	}
	return false
}

var unsafeFile = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func slugFile(s string) string {
	s = strings.Trim(unsafeFile.ReplaceAllString(s, "-"), "-.")
	if s == "" {
		return "asset"
	}
	return s
}

func extForType(ct string) string {
	ct, _, _ = strings.Cut(ct, ";")
	switch strings.TrimSpace(ct) {
	case "image/svg+xml":
		return ".svg"
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/x-icon", "image/vnd.microsoft.icon":
		return ".ico"
	case "font/woff2":
		return ".woff2"
	}
	return ""
}
