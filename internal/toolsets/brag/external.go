package brag

import (
	"context"
	"fmt"
	"github.com/Diwanshumidha/smart-control/internal/browser"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

func findChrome() string {
	if p := os.Getenv("CHROME_PATH"); p != "" {
		return p
	}
	for _, n := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome", "msedge", "microsoft-edge"} {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
		}
	case "windows":
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			if base := os.Getenv(env); base != "" {
				candidates = append(candidates,
					filepath.Join(base, `Google\Chrome\Application\chrome.exe`),
					filepath.Join(base, `Microsoft\Edge\Application\msedge.exe`))
			}
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func cmdOutput(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func hyperframesSkillDirs() []string {
	home, _ := os.UserHomeDir()
	return []string{
		filepath.Join(home, ".agents", "skills"),
		filepath.Join(home, ".claude", "skills"),
		filepath.Join(home, ".codex", "skills"),
		filepath.Join(home, ".gemini", "config", "skills"),
	}
}

func findHyperframesSkill(name string) string {
	for _, d := range hyperframesSkillDirs() {
		p := filepath.Join(d, name)
		if isDir(p) {
			return p
		}
	}
	return ""
}

// Doctor reports whether the desktop can render brag videos.
func Doctor(ctx context.Context, version string) string { return doctorReport(ctx, version) }

func doctorReport(ctx context.Context, version string) string {
	var b strings.Builder
	ok := func(good bool, name, detail string) {
		mark := "OK  "
		if !good {
			mark = "MISS"
		}
		fmt.Fprintf(&b, "[%s] %-22s %s\n", mark, name, detail)
	}
	fmt.Fprintf(&b, "smart-control %s on %s/%s\n\n", version, runtime.GOOS, runtime.GOARCH)

	if v, err := cmdOutput(ctx, "node", "--version"); err == nil {
		major, _ := strconv.Atoi(strings.SplitN(strings.TrimPrefix(v, "v"), ".", 2)[0])
		ok(major >= 22, "node (>=22)", v)
	} else {
		ok(false, "node (>=22)", "not found — install Node.js 22+ (https://nodejs.org)")
	}
	_, err := exec.LookPath("npx")
	ok(err == nil, "npx", "")
	if v, err := cmdOutput(ctx, "ffmpeg", "-version"); err == nil {
		ok(true, "ffmpeg", strings.SplitN(v, "\n", 2)[0])
	} else {
		ok(false, "ffmpeg", "not found — install ffmpeg and put it on PATH")
	}
	c := findChrome()
	ok(c != "", "chrome/chromium/edge", c+" (hyperframes can also fetch its own: `npx hyperframes browser`)")
	bp := browser.FindBrave("")
	ok(bp != "", "brave (capture_url)", bp)
	_, err = exec.LookPath("cloudflared")
	ok(err == nil, "cloudflared (for --tunnel)", "")
	for _, s := range []string{"hyperframes", "hyperframes-cli", "hyperframes-registry", "media-use"} {
		p := findHyperframesSkill(s)
		ok(p != "", "skill "+s, p+map[bool]string{true: "run `smart-control setup`", false: ""}[p == ""])
	}
	return b.String()
}

// Setup installs the Hyperframes agent skills.
func Setup() error { return runSetup() }

func runSetup() error {
	fmt.Println("Installing Hyperframes agent skills (npx -y hyperframes skills)…")
	cmd := exec.Command("npx", "-y", "hyperframes", "skills")
	cmd.Stdout, cmd.Stderr, cmd.Stdin = os.Stdout, os.Stderr, os.Stdin
	return cmd.Run()
}

// toPreviewJPEG shrinks an image so it fits comfortably in a tool result.
// Falls back to the original bytes if ffmpeg isn't available.
func toPreviewJPEG(src string, width int) ([]byte, string, error) {
	if strings.EqualFold(filepath.Ext(src), ".svg") {
		if b, err := svgPreview(src, width); err == nil {
			return b, "image/png", nil
		}
	}
	tmp, err := os.CreateTemp("", "brag-preview-*.jpg")
	if err != nil {
		return nil, "", err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", "-y", "-loglevel", "error", "-i", src,
		"-vf", fmt.Sprintf("scale='min(%d,iw)':-2", width), "-frames:v", "1", "-q:v", "4", tmp.Name())
	if err := cmd.Run(); err == nil {
		if b, err := os.ReadFile(tmp.Name()); err == nil && len(b) > 0 {
			return b, "image/jpeg", nil
		}
	}
	b, err := os.ReadFile(src)
	return b, mimeFor(src), err
}

func mimeFor(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	}
	return "application/octet-stream"
}

var viewBoxRe = regexp.MustCompile(`viewBox="\s*[-0-9.]+[\s,]+[-0-9.]+[\s,]+([0-9.]+)[\s,]+([0-9.]+)\s*"`)

// svgPreview rasterises an SVG (logos are often tiny at their natural size)
// so its longest side is size px, keeping transparency. ffmpeg's librsvg
// keep_ar is unreliable, so both sides come from the viewBox.
func svgPreview(src string, size int) ([]byte, error) {
	b, err := os.ReadFile(src)
	if err != nil {
		return nil, err
	}
	m := viewBoxRe.FindSubmatch(b)
	if m == nil {
		return nil, fmt.Errorf("no viewBox")
	}
	vw, _ := strconv.ParseFloat(string(m[1]), 64)
	vh, _ := strconv.ParseFloat(string(m[2]), 64)
	if vw <= 0 || vh <= 0 {
		return nil, fmt.Errorf("bad viewBox")
	}
	size = min(size, 1024)
	w, h := size, int(float64(size)*vh/vw)
	if vh > vw {
		w, h = int(float64(size)*vw/vh), size
	}
	tmp, err := os.CreateTemp("", "brag-svg-*.png")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "ffmpeg", "-y", "-loglevel", "error", "-width", strconv.Itoa(max(w, 1)), "-height", strconv.Itoa(max(h, 1)),
		"-i", src, "-frames:v", "1", tmp.Name()).Run(); err != nil {
		return nil, err
	}
	return os.ReadFile(tmp.Name())
}
