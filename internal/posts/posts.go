// Package posts stores social media posts drafted by the AI. Nothing is
// published automatically: the user reviews each post in the desktop app and
// publishes it through a Publisher (manual LinkedIn share for now).
package posts

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Diwanshumidha/smart-control/internal/events"
	"github.com/Diwanshumidha/smart-control/internal/store"
)

func init() {
	store.Register(store.Migration{Version: 400, Name: "posts", SQL: `
CREATE TABLE posts (
  id           TEXT PRIMARY KEY,
  platform     TEXT NOT NULL,          -- linkedin (more later)
  title        TEXT NOT NULL DEFAULT '',
  content      TEXT NOT NULL,
  media        TEXT NOT NULL DEFAULT '[]', -- JSON array of resource ids
  hashtags     TEXT NOT NULL DEFAULT '[]',
  notes        TEXT NOT NULL DEFAULT '',  -- AI's notes for the user (why, best time to post, ...)
  status       TEXT NOT NULL,          -- pending_review | approved | posted | discarded
  external_url TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  posted_at    INTEGER
);
CREATE INDEX posts_status ON posts(status, created_at DESC);`})
}

const (
	StatusPendingReview = "pending_review"
	StatusApproved      = "approved"
	StatusPosted        = "posted"
	StatusDiscarded     = "discarded"
)

var validStatus = map[string]bool{StatusPendingReview: true, StatusApproved: true, StatusPosted: true, StatusDiscarded: true}

type Post struct {
	ID          string   `json:"id"`
	Platform    string   `json:"platform"`
	Title       string   `json:"title"`
	Content     string   `json:"content"`
	Media       []string `json:"media"`
	Hashtags    []string `json:"hashtags"`
	Notes       string   `json:"notes"`
	Status      string   `json:"status"`
	ExternalURL string   `json:"external_url"`
	CreatedAt   int64    `json:"created_at"`
	UpdatedAt   int64    `json:"updated_at"`
	PostedAt    int64    `json:"posted_at,omitempty"`
}

// Validator enforces platform rules (length limits, media counts).
type Validator func(p *Post) error

type Repo struct {
	db         *sql.DB
	bus        *events.Bus
	validators map[string]Validator
	publishers map[string]Publisher
}

func NewRepo(db *sql.DB, bus *events.Bus) *Repo {
	return &Repo{db: db, bus: bus, validators: map[string]Validator{}, publishers: map[string]Publisher{}}
}

// RegisterPlatform adds validation for a platform.
func (r *Repo) RegisterPlatform(platform string, v Validator) { r.validators[platform] = v }

func (r *Repo) Create(ctx context.Context, p *Post) error {
	p.ID = store.NewID("post")
	p.CreatedAt, p.UpdatedAt = store.Now(), store.Now()
	if p.Status == "" {
		p.Status = StatusPendingReview
	}
	if err := r.validate(p); err != nil {
		return err
	}
	media, _ := json.Marshal(nonNil(p.Media))
	tags, _ := json.Marshal(nonNil(p.Hashtags))
	_, err := r.db.ExecContext(ctx, `INSERT INTO posts(id,platform,title,content,media,hashtags,notes,status,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
		p.ID, p.Platform, p.Title, p.Content, string(media), string(tags), p.Notes, p.Status, p.CreatedAt, p.UpdatedAt)
	if err == nil {
		r.bus.Publish(events.PostChanged, p)
	}
	return err
}

// Update applies f to the stored post and saves it.
func (r *Repo) Update(ctx context.Context, id string, f func(p *Post) error) (*Post, error) {
	p, err := r.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := f(p); err != nil {
		return nil, err
	}
	if !validStatus[p.Status] {
		return nil, fmt.Errorf("invalid status %q", p.Status)
	}
	if err := r.validate(p); err != nil {
		return nil, err
	}
	p.UpdatedAt = store.Now()
	if p.Status == StatusPosted && p.PostedAt == 0 {
		p.PostedAt = store.Now()
	}
	media, _ := json.Marshal(nonNil(p.Media))
	tags, _ := json.Marshal(nonNil(p.Hashtags))
	_, err = r.db.ExecContext(ctx, `UPDATE posts SET title=?,content=?,media=?,hashtags=?,notes=?,status=?,external_url=?,updated_at=?,posted_at=? WHERE id=?`,
		p.Title, p.Content, string(media), string(tags), p.Notes, p.Status, p.ExternalURL, p.UpdatedAt, nullable(p.PostedAt), p.ID)
	if err != nil {
		return nil, err
	}
	r.bus.Publish(events.PostChanged, p)
	return p, nil
}

func (r *Repo) Get(ctx context.Context, id string) (*Post, error) {
	list, err := r.query(ctx, `WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("unknown post %q", id)
	}
	return &list[0], nil
}

func (r *Repo) List(ctx context.Context, platform, status string, limit int) ([]Post, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return r.query(ctx, `WHERE (?='' OR platform=?) AND (?='' OR status=?) ORDER BY created_at DESC LIMIT ?`,
		platform, platform, status, status, limit)
}

func (r *Repo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM posts WHERE id=?`, id)
	if err == nil {
		r.bus.Publish(events.PostChanged, map[string]string{"id": id, "deleted": "true"})
	}
	return err
}

func (r *Repo) validate(p *Post) error {
	p.Content = strings.TrimSpace(p.Content)
	if p.Content == "" {
		return fmt.Errorf("content is required")
	}
	if v, ok := r.validators[p.Platform]; ok {
		return v(p)
	}
	return nil
}

func (r *Repo) query(ctx context.Context, where string, args ...any) ([]Post, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id,platform,title,content,media,hashtags,notes,status,external_url,created_at,updated_at,COALESCE(posted_at,0) FROM posts `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Post{}
	for rows.Next() {
		var p Post
		var media, tags string
		if err := rows.Scan(&p.ID, &p.Platform, &p.Title, &p.Content, &media, &tags, &p.Notes, &p.Status, &p.ExternalURL, &p.CreatedAt, &p.UpdatedAt, &p.PostedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(media), &p.Media)
		_ = json.Unmarshal([]byte(tags), &p.Hashtags)
		p.Media, p.Hashtags = nonNil(p.Media), nonNil(p.Hashtags)
		out = append(out, p)
	}
	return out, rows.Err()
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nullable(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
