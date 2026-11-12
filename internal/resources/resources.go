// Package resources is the media library: images, videos and other files the
// AI saves (from URLs, screenshots, brag renders) for use in posts.
//
// Files come from "sources". http(s) URLs are built in; toolsets register
// their own schemes (brag://<run>/<path>, project://<path>) with
// RegisterSource so save_video_resource can copy anything they produce.
package resources

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Diwanshumidha/smart-control/internal/events"
	"github.com/Diwanshumidha/smart-control/internal/security/urlpolicy"
	"github.com/Diwanshumidha/smart-control/internal/store"
)

const MaxDownload = 500 << 20

func init() {
	store.Register(store.Migration{Version: 300, Name: "resources", SQL: `
CREATE TABLE resources (
  id         TEXT PRIMARY KEY,
  kind       TEXT NOT NULL,          -- image | video | audio | document | other
  name       TEXT NOT NULL,
  path       TEXT NOT NULL,
  mime       TEXT NOT NULL,
  size       INTEGER NOT NULL,
  sha256     TEXT NOT NULL,
  origin     TEXT NOT NULL,          -- url | screenshot | brag | project | ...
  source     TEXT NOT NULL DEFAULT '',
  note       TEXT NOT NULL DEFAULT '',
  meta       TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL
);
CREATE INDEX resources_created ON resources(created_at DESC);`})
}

type Resource struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind"`
	Name      string            `json:"name"`
	Path      string            `json:"path"`
	MIME      string            `json:"mime"`
	Size      int64             `json:"size"`
	SHA256    string            `json:"sha256"`
	Origin    string            `json:"origin"`
	Source    string            `json:"source"`
	Note      string            `json:"note"`
	Meta      map[string]string `json:"meta"`
	CreatedAt int64             `json:"created_at"`
}

// SourceResolver maps "<scheme>://<rest>" to a local file the caller may copy.
type SourceResolver func(ctx context.Context, rest string) (localPath string, err error)

type Service struct {
	db     *sql.DB
	bus    *events.Bus
	policy *urlpolicy.Policy
	dir    string

	mu      sync.RWMutex
	sources map[string]SourceResolver
}

func New(db *sql.DB, bus *events.Bus, policy *urlpolicy.Policy, dir string) *Service {
	return &Service{db: db, bus: bus, policy: policy, dir: dir, sources: map[string]SourceResolver{}}
}

// RegisterSource adds a scheme (e.g. "brag") that SaveFromSource understands.
func (s *Service) RegisterSource(scheme string, r SourceResolver) {
	s.mu.Lock()
	s.sources[scheme] = r
	s.mu.Unlock()
}

func (s *Service) Schemes() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []string{"http", "https"}
	for k := range s.sources {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// SaveFromSource saves a URL or a registered-scheme source.
func (s *Service) SaveFromSource(ctx context.Context, source, name, note string) (*Resource, error) {
	scheme, rest, ok := strings.Cut(source, "://")
	if !ok {
		return nil, fmt.Errorf("source must look like https://… or one of %v as scheme://…", s.Schemes())
	}
	if scheme == "http" || scheme == "https" {
		return s.SaveFromURL(ctx, source, name, note)
	}
	s.mu.RLock()
	r, ok := s.sources[scheme]
	s.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unknown source scheme %q (known: %v)", scheme, s.Schemes())
	}
	p, err := r(ctx, rest)
	if err != nil {
		return nil, err
	}
	return s.SaveFile(ctx, p, Meta{Name: name, Origin: scheme, Source: source, Note: note})
}

// Meta describes a file being added.
type Meta struct {
	Name   string
	Origin string
	Source string
	Note   string
	Extra  map[string]string
}

// SaveFromURL downloads a URL after the URL policy allows it.
func (s *Service) SaveFromURL(ctx context.Context, rawURL, name, note string) (*Resource, error) {
	if err := s.policy.Check(ctx, rawURL, urlpolicy.CapDownload, "save a resource from this URL"); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) smart-control/1.0")
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("download failed: %s", resp.Status)
	}
	if name == "" {
		name = nameFromResponse(resp)
	}
	return s.store(ctx, io.LimitReader(resp.Body, MaxDownload+1), Meta{Name: name, Origin: "url", Source: rawURL, Note: note}, resp.Header.Get("Content-Type"))
}

// DownloadTo fetches rawURL (after the URL policy allows it) into dst,
// outside the media library. Used for files that belong to something else,
// such as a brag run's brand assets. It returns the response content type.
func (s *Service) DownloadTo(ctx context.Context, rawURL, dst, reason string, maxBytes int64) (string, error) {
	if err := s.policy.Check(ctx, rawURL, urlpolicy.CapDownload, reason); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) smart-control/1.0")
	resp, err := s.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("download failed: %s", resp.Status)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	f, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxBytes+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n > maxBytes {
		err = fmt.Errorf("file is larger than %d MB", maxBytes>>20)
	}
	if err != nil {
		os.Remove(dst)
		return "", err
	}
	return resp.Header.Get("Content-Type"), nil
}

// httpClient refuses connections to private addresses (DNS-rebinding safe)
// and re-checks the policy on every redirect.
func (s *Service) httpClient() *http.Client {
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	tr := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			if ta, ok := conn.RemoteAddr().(*net.TCPAddr); ok && urlpolicy.IsPrivate(ta.IP) {
				// Loopback only when the URL itself named localhost (no DNS tricks).
				host, _, _ := net.SplitHostPort(addr)
				if ta.IP.IsLoopback() && urlpolicy.IsLocalName(host) {
					if err := s.policy.LocalAllowed(strconv.Itoa(ta.Port)); err != nil {
						conn.Close()
						return nil, err
					}
					return conn, nil
				}
				conn.Close()
				return nil, fmt.Errorf("refusing to download from private address %s", ta.IP)
			}
			return conn, nil
		},
	}
	return &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 5 {
				return fmt.Errorf("too many redirects")
			}
			return s.policy.Check(req.Context(), req.URL.String(), urlpolicy.CapDownload, "redirect target of a download")
		},
	}
}

// SaveFile copies a local file into the library.
func (s *Service) SaveFile(ctx context.Context, src string, m Meta) (*Resource, error) {
	f, err := os.Open(src)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || st.IsDir() {
		return nil, fmt.Errorf("%s is not a file", src)
	}
	if m.Name == "" {
		m.Name = filepath.Base(src)
	}
	return s.store(ctx, f, m, mime.TypeByExtension(filepath.Ext(src)))
}

// SaveBytes stores in-memory data (screenshots).
func (s *Service) SaveBytes(ctx context.Context, data []byte, m Meta) (*Resource, error) {
	return s.store(ctx, strings.NewReader(string(data)), m, mime.TypeByExtension(filepath.Ext(m.Name)))
}

func (s *Service) store(ctx context.Context, r io.Reader, m Meta, contentType string) (*Resource, error) {
	id := store.NewID("res")
	name := sanitizeName(m.Name)
	if name == "" {
		name = "file"
	}
	month := time.Now().Format("2006-01")
	dir := filepath.Join(s.dir, month)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	dst := filepath.Join(dir, id+"-"+name)
	f, err := os.Create(dst)
	if err != nil {
		return nil, err
	}
	h := sha256.New()
	head := &headWriter{max: 512}
	n, err := io.Copy(io.MultiWriter(f, h, head), r)
	f.Close()
	if err == nil && n > MaxDownload {
		err = fmt.Errorf("file is larger than %d MB", MaxDownload>>20)
	}
	if err != nil {
		os.Remove(dst)
		return nil, err
	}
	mt := pickMIME(contentType, head.buf, name)
	if filepath.Ext(name) == "" {
		if exts, _ := mime.ExtensionsByType(mt); len(exts) > 0 {
			newDst := dst + exts[0]
			if os.Rename(dst, newDst) == nil {
				dst, name = newDst, name+exts[0]
			}
		}
	}
	res := &Resource{
		ID: id, Kind: kindOf(mt), Name: name, Path: dst, MIME: mt, Size: n,
		SHA256: hex.EncodeToString(h.Sum(nil)), Origin: m.Origin, Source: m.Source, Note: m.Note,
		Meta: m.Extra, CreatedAt: store.Now(),
	}
	if res.Meta == nil {
		res.Meta = map[string]string{}
	}
	meta, _ := json.Marshal(res.Meta)
	if _, err := s.db.ExecContext(ctx, `INSERT INTO resources(id,kind,name,path,mime,size,sha256,origin,source,note,meta,created_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		res.ID, res.Kind, res.Name, res.Path, res.MIME, res.Size, res.SHA256, res.Origin, res.Source, res.Note, string(meta), res.CreatedAt); err != nil {
		os.Remove(dst)
		return nil, err
	}
	s.bus.Publish(events.ResourceAdded, res)
	return res, nil
}

func (s *Service) Get(ctx context.Context, id string) (*Resource, error) {
	list, err := s.query(ctx, `WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("unknown resource %q", id)
	}
	return &list[0], nil
}

func (s *Service) List(ctx context.Context, kind string, limit int) ([]Resource, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.query(ctx, `WHERE (?='' OR kind=?) ORDER BY created_at DESC LIMIT ?`, kind, kind, limit)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	r, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM resources WHERE id=?`, id); err != nil {
		return err
	}
	_ = os.Remove(r.Path)
	return nil
}

func (s *Service) query(ctx context.Context, where string, args ...any) ([]Resource, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,kind,name,path,mime,size,sha256,origin,source,note,meta,created_at FROM resources `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Resource{}
	for rows.Next() {
		var r Resource
		var meta string
		if err := rows.Scan(&r.ID, &r.Kind, &r.Name, &r.Path, &r.MIME, &r.Size, &r.SHA256, &r.Origin, &r.Source, &r.Note, &meta, &r.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(meta), &r.Meta)
		out = append(out, r)
	}
	return out, rows.Err()
}

// --- helpers ---

type headWriter struct {
	buf []byte
	max int
}

func (h *headWriter) Write(p []byte) (int, error) {
	if room := h.max - len(h.buf); room > 0 {
		h.buf = append(h.buf, p[:min(room, len(p))]...)
	}
	return len(p), nil
}

func pickMIME(header string, head []byte, name string) string {
	if mt, _, err := mime.ParseMediaType(header); err == nil && mt != "" && mt != "application/octet-stream" {
		return mt
	}
	if mt := mime.TypeByExtension(filepath.Ext(name)); mt != "" {
		mt, _, _ = mime.ParseMediaType(mt)
		return mt
	}
	mt, _, _ := mime.ParseMediaType(http.DetectContentType(head))
	return mt
}

func kindOf(mt string) string {
	switch {
	case strings.HasPrefix(mt, "image/"):
		return "image"
	case strings.HasPrefix(mt, "video/"):
		return "video"
	case strings.HasPrefix(mt, "audio/"):
		return "audio"
	case mt == "application/pdf", strings.HasPrefix(mt, "text/"):
		return "document"
	}
	return "other"
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func sanitizeName(n string) string {
	n = unsafeName.ReplaceAllString(filepath.Base(n), "-")
	n = strings.Trim(n, "-.")
	if len(n) > 80 {
		ext := filepath.Ext(n)
		n = n[:80-len(ext)] + ext
	}
	return n
}

func nameFromResponse(resp *http.Response) string {
	if _, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition")); err == nil && params["filename"] != "" {
		return params["filename"]
	}
	if b := filepath.Base(resp.Request.URL.Path); b != "" && b != "/" && b != "." {
		return b
	}
	return "download"
}
