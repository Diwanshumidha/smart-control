// Package media lets the AI save images/videos into the media library from a
// URL or from files other toolsets produce (brag renders, project assets).
package media

import (
	"context"
	"fmt"
	"strings"

	"github.com/Diwanshumidha/smart-control/internal/mcpx"
	"github.com/Diwanshumidha/smart-control/internal/resources"
	"github.com/Diwanshumidha/smart-control/internal/security/pathguard"
	"github.com/Diwanshumidha/smart-control/internal/toolsets/kit"
)

type Toolset struct{ res *resources.Service }

func New(d *kit.Deps) (mcpx.Toolset, error) {
	// project://<path> copies files from the readable project roots.
	guard := d.Guard
	d.Resources.RegisterSource("project", func(_ context.Context, rest string) (string, error) {
		return guardPath(guard, rest)
	})
	return &Toolset{res: d.Resources}, nil
}

func guardPath(g *pathguard.Guard, p string) (string, error) {
	if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~") {
		p = "/" + p // project:///abs/path and project://abs/path both work
	}
	return g.Path(p)
}

func (t *Toolset) Name() string { return "media" }

func (t *Toolset) Register(r *mcpx.Registry) error {
	mcpx.Add(r, "save_video_resource",
		"Save an image, video or other file into the media library so it can be attached to posts. source is an http(s) URL (needs download permission), "+
			"brag://<run_id>/<path> for brag outputs (e.g. brag://<run_id>/brag.mp4), or project:///<absolute path> for a file in a project. Returns the resource id.",
		t.save)
	mcpx.Add(r, "list_resources",
		"List files in the media library (newest first), optionally filtered by kind: image, video, audio, document, other.",
		t.list)
	return nil
}

type saveIn struct {
	Source string `json:"source" jsonschema:"https://…, brag://<run_id>/<path>, or project:///<path>"`
	Name   string `json:"name,omitempty" jsonschema:"file name to store it under"`
	Note   string `json:"note,omitempty" jsonschema:"what it is / how to use it (alt text)"`
}

func (t *Toolset) save(ctx context.Context, in saveIn) (*mcpx.Result, error) {
	r, err := t.res.SaveFromSource(ctx, strings.TrimSpace(in.Source), in.Name, in.Note)
	if err != nil {
		return nil, err
	}
	return mcpx.Textf("Saved %s as resource %s (%s, %s, %d bytes). Attach it to a post with media: [%q].",
		in.Source, r.ID, r.Name, r.MIME, r.Size, r.ID), nil
}

type listIn struct {
	Kind  string `json:"kind,omitempty"`
	Limit int    `json:"limit,omitempty" jsonschema:"default 50"`
}

func (t *Toolset) list(ctx context.Context, in listIn) (*mcpx.Result, error) {
	if in.Limit <= 0 {
		in.Limit = 50
	}
	list, err := t.res.List(ctx, in.Kind, in.Limit)
	if err != nil {
		return nil, err
	}
	if len(list) == 0 {
		return mcpx.Text("The media library is empty."), nil
	}
	var b strings.Builder
	for _, r := range list {
		fmt.Fprintf(&b, "%s  %-8s %-40s %s  %s\n", r.ID, r.Kind, r.Name, r.Origin, r.Note)
	}
	return mcpx.Text(b.String()), nil
}
