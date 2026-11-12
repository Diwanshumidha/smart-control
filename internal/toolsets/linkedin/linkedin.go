// Package linkedin lets the AI draft LinkedIn posts. Posts are stored in
// SQLite with status pending_review; the user reviews them in the desktop app
// and publishes with one click (a Publisher; manual share for now).
package linkedin

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/Diwanshumidha/smart-control/internal/mcpx"
	"github.com/Diwanshumidha/smart-control/internal/posts"
	"github.com/Diwanshumidha/smart-control/internal/resources"
	"github.com/Diwanshumidha/smart-control/internal/toolsets/kit"
)

const (
	Platform    = "linkedin"
	MaxChars    = 3000
	MaxMedia    = 9 // LinkedIn allows up to 9 images, or one video
	maxHashtags = 10
)

type Toolset struct {
	posts *posts.Repo
	res   *resources.Service
}

func New(d *kit.Deps) (mcpx.Toolset, error) {
	t := &Toolset{posts: d.Posts, res: d.Resources}
	d.Posts.RegisterPlatform(Platform, Validate)
	d.Posts.RegisterPublisher(Platform, ManualPublisher{})
	return t, nil
}

func (t *Toolset) Name() string { return "linkedin" }

func (t *Toolset) Instructions() string {
	return `LinkedIn tools: post_linkedin_post saves a draft for the user to review. It does NOT publish; the user publishes it from the Smart Control app.
Write in the user's voice: a strong first line (shown before "see more"), short paragraphs, concrete details from the project, at most 3-5 relevant hashtags, no engagement bait.
Attach media by resource id (from screenshot_url or save_video_resource). Use list_linkedin_posts to avoid repeating recent posts.`
}

func (t *Toolset) Register(r *mcpx.Registry) error {
	mcpx.Add(r, "post_linkedin_post",
		"Save a LinkedIn post for the user to review and publish (stored locally; nothing is published automatically). content max 3000 chars. media: resource ids from the media library (up to 9 images or 1 video).",
		t.create)
	mcpx.Add(r, "update_linkedin_post",
		"Edit a LinkedIn post that has not been published yet. Only fields you pass are changed.",
		t.update)
	mcpx.Add(r, "list_linkedin_posts",
		"List saved LinkedIn posts (newest first), optionally by status: pending_review, approved, posted, discarded.",
		t.list)
	return nil
}

// Validate enforces LinkedIn's limits; registered on the posts repo.
func Validate(p *posts.Post) error {
	full := FullText(p)
	if n := utf8.RuneCountInString(full); n > MaxChars {
		return fmt.Errorf("post is %d characters including hashtags; LinkedIn allows %d", n, MaxChars)
	}
	if len(p.Media) > MaxMedia {
		return fmt.Errorf("at most %d media items", MaxMedia)
	}
	if len(p.Hashtags) > maxHashtags {
		return fmt.Errorf("at most %d hashtags", maxHashtags)
	}
	for i, h := range p.Hashtags {
		h = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(h), "#"))
		if h == "" || strings.ContainsAny(h, " \t\n#") {
			return fmt.Errorf("hashtag %q must be a single word", p.Hashtags[i])
		}
		p.Hashtags[i] = h
	}
	return nil
}

// FullText is the post body plus hashtags not already in it.
func FullText(p *posts.Post) string {
	text := strings.TrimSpace(p.Content)
	var missing []string
	lower := strings.ToLower(text)
	for _, h := range p.Hashtags {
		tag := "#" + strings.TrimPrefix(strings.TrimSpace(h), "#")
		if tag != "#" && !strings.Contains(lower, strings.ToLower(tag)) {
			missing = append(missing, tag)
		}
	}
	if len(missing) > 0 {
		text += "\n\n" + strings.Join(missing, " ")
	}
	return text
}

// ShareURL opens LinkedIn's composer prefilled with the post text.
func ShareURL(p *posts.Post) string {
	return "https://www.linkedin.com/feed/?shareActive=true&text=" + url.QueryEscape(FullText(p))
}

// ManualPublisher opens LinkedIn's composer with the text prefilled; the user
// attaches media and clicks Post. Swap for an API publisher once OAuth is set up.
type ManualPublisher struct{}

func (ManualPublisher) Publish(_ context.Context, p *posts.Post) (*posts.Publishing, error) {
	return &posts.Publishing{ShareURL: ShareURL(p), Text: FullText(p), Manual: true}, nil
}

type createIn struct {
	Content  string   `json:"content" jsonschema:"the post text"`
	Title    string   `json:"title,omitempty" jsonschema:"short internal title shown in the app (not posted)"`
	Hashtags []string `json:"hashtags,omitempty" jsonschema:"hashtags without # (appended if not already in content)"`
	Media    []string `json:"media,omitempty" jsonschema:"resource ids to attach"`
	Notes    string   `json:"notes,omitempty" jsonschema:"notes for the user: angle, why now, suggested posting time, alt text"`
}

func (t *Toolset) create(ctx context.Context, in createIn) (*mcpx.Result, error) {
	if err := t.checkMedia(ctx, in.Media); err != nil {
		return nil, err
	}
	p := &posts.Post{Platform: Platform, Title: in.Title, Content: in.Content, Hashtags: in.Hashtags, Media: in.Media, Notes: in.Notes}
	if err := t.posts.Create(ctx, p); err != nil {
		return nil, err
	}
	return mcpx.Textf("Saved LinkedIn post %s (%d chars, %d media) for review. It is NOT published yet: the user reviews and publishes it from the Smart Control app.\n\n%s",
		p.ID, utf8.RuneCountInString(FullText(p)), len(p.Media), FullText(p)), nil
}

type updateIn struct {
	ID       string   `json:"id"`
	Content  string   `json:"content,omitempty"`
	Title    string   `json:"title,omitempty"`
	Hashtags []string `json:"hashtags,omitempty"`
	Media    []string `json:"media,omitempty" jsonschema:"replaces the attached media"`
	Notes    string   `json:"notes,omitempty"`
}

func (t *Toolset) update(ctx context.Context, in updateIn) (*mcpx.Result, error) {
	if in.Media != nil {
		if err := t.checkMedia(ctx, in.Media); err != nil {
			return nil, err
		}
	}
	p, err := t.posts.Update(ctx, in.ID, func(p *posts.Post) error {
		if p.Platform != Platform {
			return fmt.Errorf("%s is not a LinkedIn post", p.ID)
		}
		if p.Status == posts.StatusPosted {
			return fmt.Errorf("post %s is already published; create a new post instead", p.ID)
		}
		if in.Content != "" {
			p.Content = in.Content
		}
		if in.Title != "" {
			p.Title = in.Title
		}
		if in.Hashtags != nil {
			p.Hashtags = in.Hashtags
		}
		if in.Media != nil {
			p.Media = in.Media
		}
		if in.Notes != "" {
			p.Notes = in.Notes
		}
		p.Status = posts.StatusPendingReview // edits need a fresh review
		return nil
	})
	if err != nil {
		return nil, err
	}
	return mcpx.Textf("Updated post %s; it is back in review.\n\n%s", p.ID, FullText(p)), nil
}

type listIn struct {
	Status string `json:"status,omitempty"`
	Limit  int    `json:"limit,omitempty" jsonschema:"default 20"`
}

func (t *Toolset) list(ctx context.Context, in listIn) (*mcpx.Result, error) {
	if in.Limit <= 0 {
		in.Limit = 20
	}
	list, err := t.posts.List(ctx, Platform, in.Status, in.Limit)
	if err != nil {
		return nil, err
	}
	return mcpx.JSON(list), nil
}

func (t *Toolset) checkMedia(ctx context.Context, ids []string) error {
	videos := 0
	for _, id := range ids {
		r, err := t.res.Get(ctx, id)
		if err != nil {
			return fmt.Errorf("media %q: %v (save files first with save_video_resource or screenshot_url)", id, err)
		}
		if r.Kind == "video" {
			videos++
		} else if r.Kind != "image" && r.Kind != "document" {
			return fmt.Errorf("media %s is %s; LinkedIn posts take images, one video, or a PDF document", id, r.Kind)
		}
	}
	if videos > 0 && len(ids) > 1 {
		return fmt.Errorf("a LinkedIn post can have one video and nothing else")
	}
	return nil
}
