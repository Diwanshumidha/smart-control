// Package pathguard confines file access to configured project roots and
// blocks secrets (keys, .env, credentials) even inside them.
package pathguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Names the AI never gets to read, even inside an allowed root.
var deniedNames = map[string]bool{
	".ssh": true, ".aws": true, ".gnupg": true, ".kube": true, ".docker": true,
	".netrc": true, ".npmrc": true, ".pypirc": true, ".git-credentials": true,
	"id_rsa": true, "id_ed25519": true, "id_ecdsa": true, ".smart-control": true, ".brag-bridge": true,
}

// SkipDirs are not descended into when listing or searching (still readable directly).
var SkipDirs = map[string]bool{
	"node_modules": true, ".git": true, ".next": true, "dist": true, "build": true,
	".turbo": true, ".cache": true, "target": true, "vendor": true, "__pycache__": true,
	".venv": true, "venv": true, ".hyperframes": true, "coverage": true, ".svelte-kit": true,
	".idea": true, ".gradle": true,
}

func DeniedComponent(name string) bool {
	n := strings.ToLower(name)
	if deniedNames[n] {
		return true
	}
	if n == ".env" || strings.HasPrefix(n, ".env.") {
		return !strings.HasSuffix(n, ".example") && !strings.HasSuffix(n, ".sample")
	}
	for _, ext := range []string{".pem", ".key", ".p12", ".pfx", ".kdbx"} {
		if strings.HasSuffix(n, ext) {
			return true
		}
	}
	return false
}

func Within(base, p string) bool {
	rel, err := filepath.Rel(base, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// CheckDenied fails if any component of p below base is a secret.
func CheckDenied(base, p string) error {
	rel, _ := filepath.Rel(base, p)
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if DeniedComponent(part) {
			return fmt.Errorf("access to %q is blocked (secrets)", part)
		}
	}
	return nil
}

// Resolve follows symlinks for the existing part of p so a link can't escape base.
func Resolve(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	dir, file := filepath.Split(p)
	if dir == "" || dir == p {
		return p
	}
	return filepath.Join(Resolve(filepath.Clean(dir)), file)
}

// Guard maps AI-supplied paths onto allowed roots. Roots can be replaced at
// runtime (from the dashboard) with SetRoots.
type Guard struct {
	mu    sync.RWMutex
	roots []string
}

func New(roots []string) (*Guard, error) {
	g := &Guard{}
	return g, g.SetRoots(roots)
}

// SetRoots validates and installs a new root list.
func (g *Guard) SetRoots(roots []string) error {
	clean, err := CleanRoots(roots)
	if err != nil {
		return err
	}
	g.mu.Lock()
	g.roots = clean
	g.mu.Unlock()
	return nil
}

// CleanRoots expands ~, resolves symlinks and checks each root is a directory.
func CleanRoots(roots []string) ([]string, error) {
	var out []string
	for _, r := range roots {
		abs, err := filepath.Abs(ExpandHome(strings.TrimSpace(r)))
		if err != nil {
			return nil, err
		}
		abs = Resolve(abs)
		if st, err := os.Stat(abs); err != nil || !st.IsDir() {
			return nil, fmt.Errorf("root %q is not a directory", r)
		}
		out = append(out, abs)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no project folders configured")
	}
	return out, nil
}

func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(p[1:], "/"))
	}
	return p
}

func (g *Guard) Roots() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return append([]string(nil), g.roots...)
}

// Path resolves p (absolute, or relative to the first root) and checks it.
func (g *Guard) Path(p string) (string, error) {
	roots := g.Roots()
	if p == "" {
		p = roots[0]
	}
	p = ExpandHome(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(roots[0], p)
	}
	p = Resolve(filepath.Clean(p))
	for _, root := range roots {
		if Within(root, p) {
			return p, CheckDenied(root, p)
		}
	}
	return "", fmt.Errorf("%q is outside the allowed project folders %v", p, roots)
}

// Under resolves rel inside base (a directory the caller already trusts).
func Under(base, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path must be relative")
	}
	b := Resolve(base)
	p := Resolve(filepath.Join(b, filepath.FromSlash(rel)))
	if !Within(b, p) {
		return "", fmt.Errorf("%q escapes %s", rel, base)
	}
	return p, CheckDenied(b, p)
}
