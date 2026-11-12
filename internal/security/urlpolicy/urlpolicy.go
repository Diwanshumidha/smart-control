// Package urlpolicy decides which URLs the AI may open, screenshot or download.
//
// Rules are stored in SQLite and matched by pattern:
//
//	example.com              exact host
//	*.example.com            any subdomain and the apex
//	https://example.com/a/*  URL prefix (scheme+host+path prefix)
//	*                        everything
//
// Each rule grants or denies a set of capabilities (browse, screenshot,
// download, or *). Deny rules win. A URL with no matching rule creates a
// pending approval that the user answers in the desktop app; the tool call
// waits for the answer up to the configured timeout.
//
// Private/loopback addresses are refused unless config allows private networks
// AND an explicit rule allows that host.
package urlpolicy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Diwanshumidha/smart-control/internal/events"
	"github.com/Diwanshumidha/smart-control/internal/store"
)

// Capabilities a rule can grant.
const (
	CapBrowse     = "browse"
	CapScreenshot = "screenshot"
	CapDownload   = "download"
	// CapInteract lets the AI click, type and run scripts on a page, acting as
	// you on sites you're logged in to. "*" never implies it: an allow rule
	// must name it.
	CapInteract = "interact"
	CapAll      = "*"
)

var AllCaps = []string{CapBrowse, CapScreenshot, CapDownload, CapInteract}

func init() {
	store.Register(store.Migration{Version: 200, Name: "url_policy", SQL: `
CREATE TABLE url_rules (
  id           TEXT PRIMARY KEY,
  pattern      TEXT NOT NULL,
  action       TEXT NOT NULL,          -- allow | deny
  capabilities TEXT NOT NULL,          -- comma list or *
  note         TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL
);
CREATE TABLE url_approvals (
  id          TEXT PRIMARY KEY,
  url         TEXT NOT NULL,
  host        TEXT NOT NULL,
  capability  TEXT NOT NULL,
  reason      TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL,           -- pending | approved | denied | expired
  decision    TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  decided_at  INTEGER
);
CREATE INDEX url_approvals_status ON url_approvals(status, created_at DESC);`})
}

type Rule struct {
	ID           string   `json:"id"`
	Pattern      string   `json:"pattern"`
	Action       string   `json:"action"`
	Capabilities []string `json:"capabilities"`
	Note         string   `json:"note"`
	CreatedAt    int64    `json:"created_at"`
}

type Approval struct {
	ID         string `json:"id"`
	URL        string `json:"url"`
	Host       string `json:"host"`
	Capability string `json:"capability"`
	Reason     string `json:"reason"`
	Status     string `json:"status"`
	Decision   string `json:"decision"`
	CreatedAt  int64  `json:"created_at"`
	DecidedAt  int64  `json:"decided_at,omitempty"`
}

// Decisions the user can make on an approval.
const (
	AllowOnce = "allow_once" // this exact URL, this capability, this time
	AllowHost = "allow_host" // add an allow rule for the host (all capabilities)
	Deny      = "deny"
	DenyHost  = "deny_host" // add a deny rule for the host
)

var ErrDenied = errors.New("url access denied")

type Policy struct {
	db           *sql.DB
	bus          *events.Bus
	timeout      time.Duration
	allowPrivate bool
	allowLocal   bool
	reserved     map[string]bool // loopback ports of Smart Control itself, never reachable

	mu      sync.Mutex
	waiters map[string]chan string // approval id → decision
	// once holds short-lived allow_once grants (capability|host → expiry) so
	// redirects and the final URL of an approved visit don't prompt again.
	once map[string]time.Time
}

const onceWindow = 2 * time.Minute

func New(db *sql.DB, bus *events.Bus, timeout time.Duration, allowPrivate bool) *Policy {
	p := &Policy{db: db, bus: bus, timeout: timeout, allowPrivate: allowPrivate, waiters: map[string]chan string{}, once: map[string]time.Time{}}
	_, _ = db.Exec(`UPDATE url_approvals SET status='expired' WHERE status='pending'`)
	return p
}

// SetLocalhost lets localhost URLs (dev servers) through the normal rules and
// approval prompt. reservedPorts are Smart Control's own loopback ports, which
// stay blocked regardless.
func (p *Policy) SetLocalhost(allow bool, reservedPorts ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.allowLocal = allow
	p.reserved = map[string]bool{}
	for _, port := range reservedPorts {
		if port != "" {
			p.reserved[port] = true
		}
	}
}

// IsLocalName reports whether host itself names this machine: localhost,
// *.localhost or a loopback IP literal. A public name that merely resolves
// to 127.0.0.1 doesn't count, so DNS can't smuggle a page onto localhost.
func IsLocalName(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func portOf(u *url.URL) string {
	if p := u.Port(); p != "" {
		return p
	}
	if u.Scheme == "https" {
		return "443"
	}
	return "80"
}

// LocalAllowed returns nil when a connection to a loopback address on port
// is acceptable (downloads check the address they actually connected to).
func (p *Policy) LocalAllowed(port string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reserved[port] {
		return fmt.Errorf("%w: localhost:%s is Smart Control itself", ErrDenied, port)
	}
	if !p.allowLocal {
		return fmt.Errorf("%w: localhost is turned off (Settings → URL approvals)", ErrDenied)
	}
	return nil
}

// Configure updates the approval timeout and private-network setting.
func (p *Policy) Configure(timeout time.Duration, allowPrivate bool) {
	p.mu.Lock()
	p.timeout, p.allowPrivate = timeout, allowPrivate
	p.mu.Unlock()
}

func (p *Policy) settings() (time.Duration, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.timeout, p.allowPrivate
}

// Check returns nil when u may be used for capability, asking the user if no
// rule decides. reason is shown to the user in the approval prompt.
func (p *Policy) Check(ctx context.Context, rawURL, capability, reason string) error {
	u, err := Normalize(rawURL)
	if err != nil {
		return err
	}
	rules, err := p.Rules(ctx)
	if err != nil {
		return err
	}
	decision, rule := evaluate(rules, u, capability)
	if decision == "deny" {
		return fmt.Errorf("%w: %s matches deny rule %q", ErrDenied, u.Host, rule.Pattern)
	}
	if err := p.checkNetwork(ctx, u, decision == "allow"); err != nil {
		return err
	}
	if decision == "allow" || p.hasOnce(capability, u.Hostname()) {
		return nil
	}
	return p.ask(ctx, u, capability, reason)
}

// checkNetwork refuses private targets unless explicitly allowed. Localhost
// URLs are an exception when enabled: they go through rules and approval
// like any public site, except Smart Control's own ports.
func (p *Policy) checkNetwork(ctx context.Context, u *url.URL, explicitlyAllowed bool) error {
	host := u.Hostname()
	if IsLocalName(host) {
		err := p.LocalAllowed(portOf(u))
		if err == nil || p.reservedPort(portOf(u)) {
			return err
		}
		// localhost turned off: the private-network rule below decides.
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return fmt.Errorf("cannot resolve %s: %v", host, err)
	}
	for _, ip := range ips {
		if IsPrivate(ip.IP) {
			if _, allowPrivate := p.settings(); allowPrivate && explicitlyAllowed {
				return nil
			}
			return fmt.Errorf("%w: %s resolves to a private address (%s)", ErrDenied, host, ip.IP)
		}
	}
	return nil
}

func (p *Policy) reservedPort(port string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.reserved[port]
}

func IsPrivate(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.Equal(net.ParseIP("169.254.169.254"))
}

func (p *Policy) ask(ctx context.Context, u *url.URL, capability, reason string) error {
	a := Approval{
		ID: store.NewID("appr"), URL: u.String(), Host: u.Hostname(), Capability: capability,
		Reason: reason, Status: "pending", CreatedAt: store.Now(),
	}
	if _, err := p.db.ExecContext(ctx, `INSERT INTO url_approvals(id,url,host,capability,reason,status,created_at) VALUES (?,?,?,?,?,?,?)`,
		a.ID, a.URL, a.Host, a.Capability, a.Reason, a.Status, a.CreatedAt); err != nil {
		return err
	}
	ch := make(chan string, 1)
	p.mu.Lock()
	p.waiters[a.ID] = ch
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.waiters, a.ID)
		p.mu.Unlock()
	}()
	p.bus.Publish(events.ApprovalRequest, a)
	timeout, _ := p.settings()

	select {
	case d := <-ch:
		if d == AllowOnce || d == AllowHost {
			p.mu.Lock()
			p.once[capability+"|"+u.Hostname()] = time.Now().Add(onceWindow)
			p.mu.Unlock()
			return nil
		}
		return fmt.Errorf("%w: the user denied %s access to %s", ErrDenied, capability, u.Host)
	case <-time.After(timeout):
		_, _ = p.db.Exec(`UPDATE url_approvals SET status='expired', decided_at=? WHERE id=? AND status='pending'`, store.Now(), a.ID)
		p.bus.Publish(events.ApprovalDecided, map[string]string{"id": a.ID, "status": "expired"})
		return fmt.Errorf("%w: no approval for %s within %s. Ask the user to allow %s in the Smart Control desktop app (URL Access), then retry",
			ErrDenied, u.Host, timeout, u.Host)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *Policy) hasOnce(capability, host string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	exp, ok := p.once[capability+"|"+host]
	if ok && time.Now().After(exp) {
		delete(p.once, capability+"|"+host)
		return false
	}
	return ok
}

// Decide answers a pending approval (from the desktop app).
func (p *Policy) Decide(ctx context.Context, id, decision string) (*Approval, error) {
	var a Approval
	err := p.db.QueryRowContext(ctx, `SELECT id,url,host,capability,reason,status FROM url_approvals WHERE id=?`, id).
		Scan(&a.ID, &a.URL, &a.Host, &a.Capability, &a.Reason, &a.Status)
	if err != nil {
		return nil, fmt.Errorf("approval %s: %w", id, err)
	}
	if a.Status != "pending" {
		return nil, fmt.Errorf("approval %s is already %s", id, a.Status)
	}
	status := "approved"
	switch decision {
	case AllowOnce:
	case AllowHost:
		caps := []string{CapAll}
		if a.Capability == CapInteract {
			caps = []string{CapInteract} // acting on a site is granted on its own
		}
		if _, err := p.AddRule(ctx, Rule{Pattern: a.Host, Action: "allow", Capabilities: caps, Note: "approved from prompt"}); err != nil {
			return nil, err
		}
	case Deny:
		status = "denied"
	case DenyHost:
		status = "denied"
		if _, err := p.AddRule(ctx, Rule{Pattern: a.Host, Action: "deny", Capabilities: []string{CapAll}, Note: "denied from prompt"}); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("decision must be one of %s, %s, %s, %s", AllowOnce, AllowHost, Deny, DenyHost)
	}
	a.Status, a.Decision, a.DecidedAt = status, decision, store.Now()
	if _, err := p.db.ExecContext(ctx, `UPDATE url_approvals SET status=?, decision=?, decided_at=? WHERE id=?`, a.Status, a.Decision, a.DecidedAt, a.ID); err != nil {
		return nil, err
	}
	p.mu.Lock()
	if ch, ok := p.waiters[id]; ok {
		ch <- decision
	}
	p.mu.Unlock()
	p.bus.Publish(events.ApprovalDecided, a)
	return &a, nil
}

func (p *Policy) Approvals(ctx context.Context, status string, limit int) ([]Approval, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := p.db.QueryContext(ctx, `SELECT id,url,host,capability,reason,status,decision,created_at,COALESCE(decided_at,0)
FROM url_approvals WHERE (?='' OR status=?) ORDER BY created_at DESC LIMIT ?`, status, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Approval{}
	for rows.Next() {
		var a Approval
		if err := rows.Scan(&a.ID, &a.URL, &a.Host, &a.Capability, &a.Reason, &a.Status, &a.Decision, &a.CreatedAt, &a.DecidedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// --- rules ---

func (p *Policy) Rules(ctx context.Context) ([]Rule, error) {
	rows, err := p.db.QueryContext(ctx, `SELECT id,pattern,action,capabilities,note,created_at FROM url_rules ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		var r Rule
		var caps string
		if err := rows.Scan(&r.ID, &r.Pattern, &r.Action, &caps, &r.Note, &r.CreatedAt); err != nil {
			return nil, err
		}
		r.Capabilities = strings.Split(caps, ",")
		out = append(out, r)
	}
	return out, rows.Err()
}

func (p *Policy) AddRule(ctx context.Context, r Rule) (*Rule, error) {
	r.Pattern = strings.TrimSpace(strings.ToLower(r.Pattern))
	if r.Pattern == "" {
		return nil, fmt.Errorf("pattern is required")
	}
	if r.Action != "allow" && r.Action != "deny" {
		return nil, fmt.Errorf("action must be allow or deny")
	}
	if len(r.Capabilities) == 0 {
		r.Capabilities = []string{CapAll}
	}
	for _, c := range r.Capabilities {
		if c != CapAll && !slices.Contains(AllCaps, c) {
			return nil, fmt.Errorf("unknown capability %q (use %v or *)", c, AllCaps)
		}
	}
	r.ID, r.CreatedAt = store.NewID("rule"), store.Now()
	_, err := p.db.ExecContext(ctx, `INSERT INTO url_rules(id,pattern,action,capabilities,note,created_at) VALUES (?,?,?,?,?,?)`,
		r.ID, r.Pattern, r.Action, strings.Join(r.Capabilities, ","), r.Note, r.CreatedAt)
	return &r, err
}

func (p *Policy) DeleteRule(ctx context.Context, id string) error {
	_, err := p.db.ExecContext(ctx, `DELETE FROM url_rules WHERE id=?`, id)
	return err
}

// --- matching ---

// Normalize parses and validates an http(s) URL.
func Normalize(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid url: %v", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("only http(s) URLs are allowed, got %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("url has no host")
	}
	if u.User != nil {
		return nil, fmt.Errorf("urls with credentials are not allowed")
	}
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	return u, nil
}

// evaluate returns "deny", "allow" or "" (unknown) and the deciding rule.
func evaluate(rules []Rule, u *url.URL, capability string) (string, Rule) {
	var allow *Rule
	for i := range rules {
		r := rules[i]
		named := slices.Contains(r.Capabilities, capability)
		wild := slices.Contains(r.Capabilities, CapAll) && (capability != CapInteract || r.Action == "deny")
		if !Matches(r.Pattern, u) || !(named || wild) {
			continue
		}
		if r.Action == "deny" {
			return "deny", r
		}
		allow = &rules[i]
	}
	if allow != nil {
		return "allow", *allow
	}
	return "", Rule{}
}

// Matches reports whether pattern covers u.
func Matches(pattern string, u *url.URL) bool {
	host := u.Hostname()
	switch {
	case pattern == "*":
		return true
	case strings.Contains(pattern, "://"):
		target := u.Scheme + "://" + u.Host + u.EscapedPath()
		if strings.HasSuffix(pattern, "*") {
			return strings.HasPrefix(target, strings.TrimSuffix(pattern, "*"))
		}
		return target == pattern || target == pattern+"/"
	case strings.HasPrefix(pattern, "*."):
		apex := pattern[2:]
		return host == apex || strings.HasSuffix(host, "."+apex)
	default:
		return host == pattern
	}
}
