package brag

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type catalogItem struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"` // block | component
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Tags        []string `json:"tags"`
	Duration    float64  `json:"duration,omitempty"`
	Dimensions  *struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"dimensions,omitempty"`

	// From the brag starter kit, when curated.
	Role  string   `json:"role,omitempty"`
	Tones []string `json:"tones,omitempty"`
	Use   string   `json:"use,omitempty"`
}

func (c *catalogItem) kindDir() string {
	if c.Type == "component" {
		return "components"
	}
	return "blocks"
}

func (c *catalogItem) PageURL() string {
	return "https://hyperframes.heygen.com/catalog/" + c.kindDir() + "/" + c.Name
}

func (c *catalogItem) PreviewURL() string {
	return "https://static.heygen.ai/hyperframes-oss/docs/images/catalog/" + c.kindDir() + "/" + c.Name + ".png"
}

func (c *catalogItem) SourceURL() string {
	return "https://github.com/heygen-com/hyperframes/tree/main/registry/" + c.kindDir() + "/" + c.Name
}

type catalog struct {
	mu       sync.Mutex
	items    map[string]*catalogItem
	order    []string
	loadedAt time.Time
	previews map[string]bool // preview URL → reachable
}

var (
	catalogMu   sync.Mutex
	theCatalog  = &catalog{previews: map[string]bool{}}
	catalogTTL  = 24 * time.Hour
	httpTimeout = &http.Client{Timeout: 10 * time.Second}
)

// loadCatalog reads the registry via `npx hyperframes catalog --json`, cached on
// disk for a day, and overlays the embedded brag starter kit.
func (a *app) loadCatalog(ctx context.Context) (*catalog, error) {
	catalogMu.Lock()
	defer catalogMu.Unlock()
	c := theCatalog
	if c.items != nil && time.Since(c.loadedAt) < catalogTTL {
		return c, nil
	}
	cache := filepath.Join(a.workdir, "catalog.json")
	var raw []byte
	if st, err := os.Stat(cache); err == nil && time.Since(st.ModTime()) < catalogTTL {
		raw, _ = os.ReadFile(cache)
	}
	if len(raw) == 0 {
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(cctx, "npx", "--yes", "hyperframes", "catalog", "--json")
		cmd.Dir = a.workdir
		out, err := cmd.Output()
		if err != nil {
			if old, rerr := os.ReadFile(cache); rerr == nil {
				raw = old // stale beats nothing
			} else {
				return nil, fmt.Errorf("hyperframes catalog failed: %v", err)
			}
		} else {
			raw = out
			_ = os.WriteFile(cache, out, 0o644)
		}
	}
	var items []*catalogItem
	if err := json.Unmarshal(raw, &items); err != nil {
		var wrapped map[string]json.RawMessage
		if json.Unmarshal(raw, &wrapped) != nil {
			return nil, fmt.Errorf("unexpected catalog format: %v", err)
		}
		for _, v := range wrapped {
			if json.Unmarshal(v, &items) == nil && len(items) > 0 {
				break
			}
		}
	}
	c.items = map[string]*catalogItem{}
	c.order = nil
	for _, it := range items {
		c.items[it.Name] = it
		c.order = append(c.order, it.Name)
	}
	var kit struct {
		Items []catalogItem `json:"items"`
	}
	b, _ := embedded.ReadFile("embedded/templates.json")
	_ = json.Unmarshal(b, &kit)
	for _, k := range kit.Items {
		if it, ok := c.items[k.Name]; ok {
			it.Role, it.Tones, it.Use = k.Role, k.Tones, k.Use
		}
	}
	c.loadedAt = time.Now()
	return c, nil
}

func (c *catalog) get(name string) *catalogItem {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.items[name]
}

// previewOK reports whether the public preview image exists (cached).
func (c *catalog) previewOK(it *catalogItem) bool {
	u := it.PreviewURL()
	c.mu.Lock()
	ok, seen := c.previews[u]
	c.mu.Unlock()
	if seen {
		return ok
	}
	resp, err := httpTimeout.Head(u)
	ok = err == nil && resp.StatusCode == 200
	if resp != nil {
		resp.Body.Close()
	}
	c.mu.Lock()
	c.previews[u] = ok
	c.mu.Unlock()
	return ok
}

func fetchPreview(u string) ([]byte, error) {
	resp, err := httpTimeout.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 8<<20))
}

// ---- tool ----

type searchTemplatesIn struct {
	Query        string `json:"query,omitempty" jsonschema:"words to search names, titles, descriptions and tags, e.g. 'terminal', 'logo reveal', 'whip'"`
	Role         string `json:"role,omitempty" jsonschema:"starter-kit role: hook, product, code, proof, transition, camera, accent, end-card"`
	Tone         string `json:"tone,omitempty" jsonschema:"brag tone, e.g. chaotic, polished, cinematic: ranks matching starter-kit items first"`
	Type         string `json:"type,omitempty" jsonschema:"block or component"`
	StarterKit   bool   `json:"starter_kit,omitempty" jsonschema:"only the hand-picked brag starter kit (default true when query is empty)"`
	Limit        int    `json:"limit,omitempty" jsonschema:"default 25"`
	ShowPreviews int    `json:"show_previews,omitempty" jsonschema:"attach up to 4 preview images of the top results"`
}

func (a *app) toolSearchTemplates(ctx context.Context, _ *mcp.CallToolRequest, in searchTemplatesIn) (*mcp.CallToolResult, any, error) {
	c, err := a.loadCatalog(ctx)
	if err != nil {
		return nil, nil, err
	}
	kitOnly := in.StarterKit || (in.Query == "" && in.Type == "")
	words := strings.Fields(strings.ToLower(in.Query))
	type scored struct {
		it    *catalogItem
		score int
	}
	var hits []scored
	c.mu.Lock()
	for _, name := range c.order {
		it := c.items[name]
		if kitOnly && it.Role == "" {
			continue
		}
		if in.Role != "" && it.Role != in.Role {
			continue
		}
		if in.Type != "" && it.Type != in.Type {
			continue
		}
		hay := strings.ToLower(it.Name + " " + it.Title + " " + it.Description + " " + strings.Join(it.Tags, " ") + " " + it.Use)
		score := 0
		for _, w := range words {
			switch {
			case strings.Contains(it.Name, w):
				score += 5
			case strings.Contains(strings.ToLower(strings.Join(it.Tags, " ")), w):
				score += 3
			case strings.Contains(hay, w):
				score++
			default:
				score -= 2
			}
		}
		if len(words) > 0 && score <= 0 {
			continue
		}
		if it.Role != "" {
			score += 2
		}
		if in.Tone != "" && (len(it.Tones) == 0 || contains(it.Tones, in.Tone)) && it.Role != "" {
			score += 3
		}
		hits = append(hits, scored{it, score})
	}
	c.mu.Unlock()
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	limit := in.Limit
	if limit <= 0 {
		limit = 25
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}

	var b strings.Builder
	scope := "whole registry"
	if kitOnly {
		scope = "brag starter kit (search with a query to see the whole registry)"
	}
	fmt.Fprintf(&b, "%d results from the %s. Use the exact name in submit_plan and, after approval, `hyperframes add <name>`.\n\n", len(hits), scope)
	for _, h := range hits {
		it := h.it
		fmt.Fprintf(&b, "- **%s** (%s", it.Name, it.Type)
		if it.Duration > 0 {
			fmt.Fprintf(&b, ", %.1fs", it.Duration)
		}
		if it.Dimensions != nil {
			fmt.Fprintf(&b, ", %dx%d", it.Dimensions.Width, it.Dimensions.Height)
		}
		b.WriteString(")")
		if it.Role != "" {
			fmt.Fprintf(&b, " [role: %s", it.Role)
			if len(it.Tones) > 0 {
				fmt.Fprintf(&b, "; tones: %s", strings.Join(it.Tones, ", "))
			}
			b.WriteString("]")
		}
		desc := it.Use
		if desc == "" {
			desc = it.Description
		}
		fmt.Fprintf(&b, "\n  %s\n  page: %s\n", clip(desc, 240), it.PageURL())
	}
	content := []mcp.Content{&mcp.TextContent{Text: b.String()}}
	for i := 0; i < min(in.ShowPreviews, 4, len(hits)); i++ {
		it := hits[i].it
		if !c.previewOK(it) {
			continue
		}
		if data, err := fetchPreview(it.PreviewURL()); err == nil {
			content = append(content, &mcp.TextContent{Text: "preview: " + it.Name}, &mcp.ImageContent{Data: data, MIMEType: "image/png"})
		}
	}
	return &mcp.CallToolResult{Content: content}, nil, nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
