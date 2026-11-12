package posts

import (
	"context"
	"fmt"
)

// Publishing is how a post gets onto its platform. Manual publishers return a
// share URL plus text for the user to finish in the browser; an API-based
// publisher (LinkedIn API later) would post directly and return the URL.
type Publishing struct {
	ShareURL    string `json:"share_url,omitempty"`    // open this to finish manually
	Text        string `json:"text"`                   // full text to paste
	ExternalURL string `json:"external_url,omitempty"` // set when the publisher posted it itself
	Manual      bool   `json:"manual"`
}

type Publisher interface {
	Publish(ctx context.Context, p *Post) (*Publishing, error)
}

// RegisterPublisher sets the publisher for a platform.
func (r *Repo) RegisterPublisher(platform string, p Publisher) { r.publishers[platform] = p }

func (r *Repo) Publisher(platform string) (Publisher, error) {
	p, ok := r.publishers[platform]
	if !ok {
		return nil, fmt.Errorf("no publisher for platform %q", platform)
	}
	return p, nil
}
