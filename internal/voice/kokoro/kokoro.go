// Package kokoro is the local, offline voice provider: Kokoro-82M through
// `npx hyperframes tts`, running in a Python venv that `smart-control setup`
// creates under <data>/voice/venv.
package kokoro

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/Diwanshumidha/smart-control/internal/voice"
)

const name = "kokoro"

var voices = []voice.Voice{
	{ID: "af_heart", Name: "Heart", Lang: "en-us", Note: "warm, product demos"},
	{ID: "af_nova", Name: "Nova", Lang: "en-us", Note: "clear, product demos"},
	{ID: "af_sky", Name: "Sky", Lang: "en-us", Note: "bright, promos"},
	{ID: "am_michael", Name: "Michael", Lang: "en-us", Note: "confident, promos"},
	{ID: "am_adam", Name: "Adam", Lang: "en-us", Note: "calm, tutorials"},
	{ID: "bf_emma", Name: "Emma", Lang: "en-gb", Note: "British, tutorials"},
	{ID: "bf_isabella", Name: "Isabella", Lang: "en-gb", Note: "British, polished"},
	{ID: "bm_george", Name: "George", Lang: "en-gb", Note: "British, documentary"},
	{ID: "ef_dora", Name: "Dora", Lang: "es", Note: "Spanish"},
	{ID: "ff_siwis", Name: "Siwis", Lang: "fr-fr", Note: "French"},
	{ID: "jf_alpha", Name: "Alpha", Lang: "ja", Note: "Japanese"},
	{ID: "zf_xiaobei", Name: "Xiaobei", Lang: "zh", Note: "Mandarin"},
}

func init() {
	voice.Register(voice.ProviderInfo{
		Name:         name,
		Title:        "Kokoro (local)",
		Description:  "Kokoro-82M on this machine through Hyperframes. Free and offline; about 3 s per line. Installed by `smart-control setup`.",
		DefaultVoice: "af_heart",
		Voices:       voices,
	}, factory, probe)
}

// Dir is where the venv and the ready marker live.
func Dir(dataDir string) string { return filepath.Join(dataDir, "voice", "kokoro") }

func python(dataDir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(Dir(dataDir), "venv", "Scripts", "python.exe")
	}
	return filepath.Join(Dir(dataDir), "venv", "bin", "python")
}

// ExportEnv points HYPERFRAMES_PYTHON at the installed venv for this process
// and everything it starts, so `hyperframes doctor` and `tts` find Kokoro.
func ExportEnv(dataDir string) {
	if os.Getenv("HYPERFRAMES_PYTHON") != "" {
		return
	}
	if _, err := os.Stat(readyMarker(dataDir)); err == nil {
		os.Setenv("HYPERFRAMES_PYTHON", python(dataDir))
	}
}

func readyMarker(dataDir string) string { return filepath.Join(Dir(dataDir), "ready") }

func probe(s voice.Settings) (bool, string) {
	if _, err := exec.LookPath("npx"); err != nil {
		return false, "npx not found: install Node.js 22+"
	}
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		return false, "ffmpeg not found"
	}
	if _, err := os.Stat(readyMarker(s.DataDir)); err != nil {
		return false, "not installed: run `smart-control setup`"
	}
	return true, python(s.DataDir)
}

type provider struct{ py string }

func factory(s voice.Settings) (voice.Provider, error) {
	return &provider{py: python(s.DataDir)}, nil
}

func (p *provider) Synthesize(ctx context.Context, req voice.Request) error {
	return synth(ctx, p.py, req)
}

func synth(ctx context.Context, py string, req voice.Request) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute) // the first call downloads the model
	defer cancel()
	// Text goes through a file so a line starting with "-" can't become a flag.
	txt, err := os.CreateTemp("", "smart-control-tts-*.txt")
	if err != nil {
		return err
	}
	defer os.Remove(txt.Name())
	txt.WriteString(req.Text)
	txt.Close()
	speed := req.Speed
	if speed <= 0 {
		speed = 1
	}
	pkg := "hyperframes"
	if v := os.Getenv("HYPERFRAMES_VERSION"); v != "" {
		pkg += "@" + v
	}
	cmd := exec.CommandContext(ctx, "npx", "--yes", pkg, "tts", "--text-file", txt.Name(),
		"--output", req.Out, "--voice", req.Voice, "--speed", fmt.Sprintf("%.2f", speed), "--json")
	cmd.Env = append(os.Environ(), "HYPERFRAMES_PYTHON="+py, "CI=1", "NO_COLOR=1")
	out, err := cmd.CombinedOutput()
	var res struct {
		OK    bool `json:"ok"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "{") {
			_ = json.Unmarshal([]byte(line), &res)
		}
	}
	switch {
	case res.OK:
		return nil
	case res.Error.Message != "":
		return fmt.Errorf("%s", res.Error.Message)
	case err != nil:
		return fmt.Errorf("hyperframes tts: %v: %s", err, tail(string(out), 600))
	}
	return fmt.Errorf("hyperframes tts gave no result: %s", tail(string(out), 600))
}

// Install creates the venv, installs kokoro-onnx and checks a test line.
func Install(ctx context.Context, dataDir string, w io.Writer) error {
	base, ver, err := findPython(ctx)
	if err != nil {
		return err
	}
	dir := Dir(dataDir)
	_ = os.Remove(readyMarker(dataDir))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	py := python(dataDir)
	if _, err := os.Stat(py); err != nil {
		fmt.Fprintf(w, "Creating a Python %s venv in %s…\n", ver, filepath.Join(dir, "venv"))
		if err := run(ctx, w, base, "-m", "venv", filepath.Join(dir, "venv")); err != nil {
			return err
		}
	}
	fmt.Fprintln(w, "Installing kokoro-onnx and soundfile (about 250 MB)…")
	if err := run(ctx, w, py, "-m", "pip", "install", "--quiet", "--disable-pip-version-check", "kokoro-onnx", "soundfile"); err != nil {
		return err
	}
	fmt.Fprintln(w, "Speaking a test line (the first run downloads the Kokoro model)…")
	out := filepath.Join(dir, "test.wav")
	if err := synth(ctx, py, voice.Request{Text: "Smart Control voice is ready.", Voice: "af_heart", Speed: 1, Out: out}); err != nil {
		return fmt.Errorf("test line failed: %w", err)
	}
	if err := os.WriteFile(readyMarker(dataDir), []byte(time.Now().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(w, "Voice ready: %s\n", out)
	return nil
}

// findPython picks a Python 3.10–3.13 (kokoro's espeak data doesn't load
// under 3.14 yet). SMART_CONTROL_PYTHON overrides the search.
func findPython(ctx context.Context) (path, version string, err error) {
	var cands []string
	if p := os.Getenv("SMART_CONTROL_PYTHON"); p != "" {
		cands = append(cands, p)
	}
	for _, n := range []string{"python3.12", "python3.11", "python3.13", "python3.10"} {
		if p, err := exec.LookPath(n); err == nil {
			cands = append(cands, p)
		}
	}
	home, _ := os.UserHomeDir()
	for _, g := range []string{
		".local/share/mise/installs/python/3.1[0-3]*/bin/python3",
		".pyenv/versions/3.1[0-3]*/bin/python3",
		".local/share/uv/python/cpython-3.1[0-3]*/bin/python3",
		".asdf/installs/python/3.1[0-3]*/bin/python3",
	} {
		m, _ := filepath.Glob(filepath.Join(home, g))
		cands = append(cands, m...)
	}
	if p, err := exec.LookPath("python3"); err == nil {
		cands = append(cands, p)
	}
	for _, c := range cands {
		out, err := exec.CommandContext(ctx, c, "-c", "import sys;print('%d.%d'%sys.version_info[:2])").Output()
		if err != nil {
			continue
		}
		v := strings.TrimSpace(string(out))
		if v == "3.10" || v == "3.11" || v == "3.12" || v == "3.13" {
			return c, v, nil
		}
	}
	return "", "", fmt.Errorf("the local voice needs Python 3.10–3.13 (3.14 isn't supported by kokoro yet). Install one (e.g. `mise install python@3.12`) or set SMART_CONTROL_PYTHON to its path, then run `smart-control setup` again")
}

func run(ctx context.Context, w io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout = io.MultiWriter(w, &buf)
	cmd.Stderr = io.MultiWriter(w, &buf)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", filepath.Base(name), strings.Join(args, " "), err)
	}
	return nil
}

func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	sc := bufio.NewScanner(strings.NewReader(s))
	var lines []string
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" {
			lines = append(lines, l)
		}
	}
	s = strings.Join(lines, " | ")
	if len(s) > n {
		s = "…" + s[len(s)-n:]
	}
	return s
}
