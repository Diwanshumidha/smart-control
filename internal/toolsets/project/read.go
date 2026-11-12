package project

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io/fs"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Diwanshumidha/smart-control/internal/mcpx"
	"github.com/Diwanshumidha/smart-control/internal/security/pathguard"
)

const (
	maxFileBytes   = 256 << 10
	maxTotalBytes  = 600 << 10
	maxImageBytes  = 4 << 20
	defaultMaxHits = 200
)

// --- get_files_from_project ---

type getFilesIn struct {
	Project   string   `json:"project,omitempty" jsonschema:"project directory; paths are relative to it (default: first root)"`
	Paths     []string `json:"paths" jsonschema:"files to read (relative to project, or absolute)"`
	StartLine int      `json:"start_line,omitempty" jsonschema:"first line to return, 1-based (applies to every file)"`
	EndLine   int      `json:"end_line,omitempty" jsonschema:"last line to return (default: whole file up to the size cap)"`
}

func (t *Toolset) getFiles(ctx context.Context, in getFilesIn) (*mcpx.Result, error) {
	if len(in.Paths) == 0 {
		return nil, fmt.Errorf("paths is required")
	}
	if len(in.Paths) > 30 {
		return nil, fmt.Errorf("read at most 30 files per call")
	}
	res := &mcp.CallToolResult{}
	total := 0
	for _, rel := range in.Paths {
		p := rel
		if in.Project != "" && !filepath.IsAbs(rel) {
			p = filepath.Join(in.Project, rel)
		}
		abs, err := t.guard.Path(p)
		if err != nil {
			res.Content = append(res.Content, &mcp.TextContent{Text: fmt.Sprintf("== %s ==\nerror: %v", rel, err)})
			continue
		}
		if mt := mime.TypeByExtension(filepath.Ext(abs)); strings.HasPrefix(mt, "image/") && mt != "image/svg+xml" {
			b, err := os.ReadFile(abs)
			switch {
			case err != nil:
				res.Content = append(res.Content, &mcp.TextContent{Text: fmt.Sprintf("== %s ==\nerror: %v", rel, err)})
			case len(b) > maxImageBytes:
				res.Content = append(res.Content, &mcp.TextContent{Text: fmt.Sprintf("== %s ==\nimage is %s, too large to view", rel, humanSize(int64(len(b))))})
			default:
				res.Content = append(res.Content, &mcp.TextContent{Text: "== " + rel + " (image) =="}, mcpx.Image(b, mt))
			}
			continue
		}
		text, err := readLines(abs, in.StartLine, in.EndLine, min(maxFileBytes, maxTotalBytes-total))
		if err != nil {
			text = "error: " + err.Error()
		}
		total += len(text)
		res.Content = append(res.Content, &mcp.TextContent{Text: fmt.Sprintf("== %s ==\n%s", abs, text)})
		if total >= maxTotalBytes {
			res.Content = append(res.Content, &mcp.TextContent{Text: "… output budget reached; read the remaining files in another call"})
			break
		}
	}
	return res, nil
}

func readLines(p string, start, end, budget int) (string, error) {
	st, err := os.Stat(p)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("is a directory; use file_tree")
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	head := make([]byte, 8000)
	n, _ := f.Read(head)
	if bytes.IndexByte(head[:n], 0) >= 0 || !utf8.Valid(trimPartialRune(head[:n])) {
		return fmt.Sprintf("(binary file, %s)", humanSize(st.Size())), nil
	}
	if _, err := f.Seek(0, 0); err != nil {
		return "", err
	}
	if start <= 0 {
		start = 1
	}
	var b strings.Builder
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	line := 0
	for sc.Scan() {
		line++
		if line < start {
			continue
		}
		if end > 0 && line > end {
			break
		}
		if b.Len() > budget {
			fmt.Fprintf(&b, "… truncated at line %d (file has more; use start_line=%d)\n", line-1, line)
			return b.String(), nil
		}
		fmt.Fprintf(&b, "%5d  %s\n", line, sc.Text())
	}
	if line < start {
		return fmt.Sprintf("(file has %d lines)", line), nil
	}
	return b.String(), sc.Err()
}

func trimPartialRune(b []byte) []byte {
	for i := 0; i < 3 && len(b) > 0 && !utf8.Valid(b); i++ {
		b = b[:len(b)-1]
	}
	return b
}

// --- grep_in_project ---

type grepIn struct {
	Path          string `json:"path" jsonschema:"project directory or file to search"`
	Pattern       string `json:"pattern" jsonschema:"regular expression (RE2 syntax) unless literal is true"`
	Literal       bool   `json:"literal,omitempty" jsonschema:"treat pattern as plain text"`
	CaseSensitive bool   `json:"case_sensitive,omitempty" jsonschema:"default false (case-insensitive)"`
	Glob          string `json:"glob,omitempty" jsonschema:"only files matching this glob, e.g. *.go or src/**/*.tsx"`
	Context       int    `json:"context,omitempty" jsonschema:"lines of context around each match (0-5)"`
	MaxResults    int    `json:"max_results,omitempty" jsonschema:"max matching lines (default 200)"`
}

func (t *Toolset) grep(ctx context.Context, in grepIn) (*mcpx.Result, error) {
	if in.Pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	root, err := t.guard.Path(in.Path)
	if err != nil {
		return nil, err
	}
	if in.MaxResults <= 0 {
		in.MaxResults = defaultMaxHits
	}
	in.Context = max(0, min(in.Context, 5))
	var out string
	if rg, err := exec.LookPath("rg"); err == nil {
		out, err = t.grepRipgrep(ctx, rg, root, in)
		if err != nil {
			return nil, err
		}
	} else {
		out, err = t.grepNative(ctx, root, in)
		if err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(out) == "" {
		return mcpx.Textf("No matches for %q in %s.", in.Pattern, root), nil
	}
	return mcpx.Text(out), nil
}

func (t *Toolset) grepRipgrep(ctx context.Context, rg, root string, in grepIn) (string, error) {
	args := []string{"--null", "--line-number", "--no-heading", "--color", "never", "--max-columns", "300", "--max-columns-preview", "--max-filesize", "2M"}
	if !in.CaseSensitive {
		args = append(args, "--ignore-case")
	}
	if in.Literal {
		args = append(args, "--fixed-strings")
	}
	if in.Context > 0 {
		args = append(args, "-C", fmt.Sprint(in.Context))
	}
	if in.Glob != "" {
		args = append(args, "--glob", in.Glob)
	}
	for d := range pathguard.SkipDirs {
		args = append(args, "--glob", "!"+d+"/")
	}
	for _, g := range []string{"!.env", "!.env.*", "!*.pem", "!*.key", "!*.p12", "!*.pfx", "!*.kdbx", "!.ssh/", "!.aws/", "!.gnupg/", "!id_rsa", "!id_ed25519", "!id_ecdsa"} {
		args = append(args, "--glob", g)
	}
	args = append(args, "--", in.Pattern, root)
	cmd := exec.CommandContext(ctx, rg, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
		return "", nil // no matches
	} else if err != nil {
		return "", fmt.Errorf("rg: %v %s", err, strings.TrimSpace(stderr.String()))
	}
	var b strings.Builder
	hits := 0
	for _, line := range strings.Split(stdout.String(), "\n") {
		if line == "" {
			continue
		}
		if line == "--" {
			b.WriteString("--\n")
			continue
		}
		// --null separates the path from "N:text" (match) or "N-text" (context).
		file, rest, ok := strings.Cut(line, "\x00")
		if !ok {
			continue
		}
		// Belt and braces: drop anything under a denied path component.
		if pathguard.CheckDenied(root, file) != nil {
			continue
		}
		if matchLineRe.MatchString(rest) {
			hits++
			if hits > in.MaxResults {
				fmt.Fprintf(&b, "… more matches; narrow the pattern or glob (max_results=%d)\n", in.MaxResults)
				break
			}
		}
		rel, err := filepath.Rel(root, file)
		if err != nil || rel == "." {
			rel = filepath.Base(file)
		}
		b.WriteString(rel + ":" + rest)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

var matchLineRe = regexp.MustCompile(`^\d+:`)

func (t *Toolset) grepNative(ctx context.Context, root string, in grepIn) (string, error) {
	pat := in.Pattern
	if in.Literal {
		pat = regexp.QuoteMeta(pat)
	}
	if !in.CaseSensitive {
		pat = "(?i)" + pat
	}
	re, err := regexp.Compile(pat)
	if err != nil {
		return "", fmt.Errorf("bad pattern: %v", err)
	}
	var b strings.Builder
	hits := 0
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || ctx.Err() != nil {
			return ctx.Err()
		}
		if pathguard.DeniedComponent(d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if p != root && pathguard.SkipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		if in.Glob != "" {
			if ok, _ := filepath.Match(in.Glob, d.Name()); !ok {
				if ok, _ := filepath.Match(in.Glob, rel); !ok {
					return nil
				}
			}
		}
		if info, err := d.Info(); err != nil || info.Size() > 2<<20 {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil || bytes.IndexByte(data[:min(len(data), 8000)], 0) >= 0 {
			return nil
		}
		lines := strings.Split(string(data), "\n")
		for i, l := range lines {
			if !re.MatchString(l) {
				continue
			}
			hits++
			if hits > in.MaxResults {
				return fs.SkipAll
			}
			for j := max(0, i-in.Context); j <= min(len(lines)-1, i+in.Context); j++ {
				sep := "-"
				if j == i {
					sep = ":"
				}
				fmt.Fprintf(&b, "%s%s%d%s%s\n", rel, sep, j+1, sep, clipLine(lines[j]))
			}
			if in.Context > 0 {
				b.WriteString("--\n")
			}
		}
		return nil
	})
	if hits > in.MaxResults {
		fmt.Fprintf(&b, "… more matches; narrow the pattern or glob (max_results=%d)\n", in.MaxResults)
	}
	return b.String(), err
}

func clipLine(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
