package brag

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// By default the product UI is rebuilt from source, and captures are only a
// reference. A capture may appear in the video only when the user asked for
// screenshots (plan.screenshots_requested).

var (
	screenshotWordRe = regexp.MustCompile(`(?i)\bscreen\s?-?shots?\b`)
	dataImageRe      = regexp.MustCompile(`data:image/(?:png|jpe?g|webp);base64,([A-Za-z0-9+/=]+)`)
	captureRefRe     = regexp.MustCompile(`(?:\.\./)+capture/[^"')\s]+`)
	imageExts        = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true}
)

// screenshotUses lists places in the composition that show a capture. It
// returns nil when the user asked for screenshots.
func (a *app) screenshotUses(r *run) []string {
	if plan, _ := a.readPlan(r); plan == nil || strings.TrimSpace(plan.ScreenshotsRequested) != "" {
		return nil
	}
	captures := map[[32]byte]string{}
	caps, _ := filepath.Glob(filepath.Join(r.Dir, "capture", "*"))
	for _, c := range caps {
		if !imageExts[strings.ToLower(filepath.Ext(c))] {
			continue
		}
		if b, err := os.ReadFile(c); err == nil {
			captures[sha256.Sum256(b)] = filepath.Base(c)
		}
	}
	comp := filepath.Join(r.Dir, "composition")
	var html []string
	images := map[string][32]byte{} // basename → hash, for images inside the composition
	_ = filepath.WalkDir(comp, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if n := d.Name(); n == ".hyperframes" || n == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		ext := strings.ToLower(filepath.Ext(p))
		switch {
		case ext == ".html":
			if b, err := os.ReadFile(p); err == nil {
				html = append(html, string(b))
			}
		case imageExts[ext]:
			if b, err := os.ReadFile(p); err == nil {
				images[filepath.Base(p)] = sha256.Sum256(b)
			}
		}
		return nil
	})
	found := map[string]bool{}
	for _, h := range html {
		for _, m := range captureRefRe.FindAllString(h, -1) {
			found[fmt.Sprintf("%s (linked straight from the run's capture folder)", m)] = true
		}
		for _, m := range dataImageRe.FindAllStringSubmatch(h, -1) {
			if b, err := base64.StdEncoding.DecodeString(m[1]); err == nil {
				if c, ok := captures[sha256.Sum256(b)]; ok {
					found[fmt.Sprintf("capture %s pasted into the HTML as base64", c)] = true
				}
			}
		}
		for name, sum := range images {
			if c, ok := captures[sum]; ok && strings.Contains(h, name) {
				found[fmt.Sprintf("assets/%s (a copy of capture %s)", name, c)] = true
			}
		}
	}
	var out []string
	for f := range found {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

func screenshotProblem(uses []string) string {
	return "screenshots in the video, but the user didn't ask for them: " + strings.Join(uses, "; ") +
		". Rebuild that UI in HTML/CSS from the product's source (captures are only a reference), or, if the user asked for screenshots, quote them in the plan's screenshots_requested"
}
