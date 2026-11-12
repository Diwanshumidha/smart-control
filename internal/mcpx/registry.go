// Package mcpx is the extension point for tools. A Toolset registers typed
// tools on a Registry; every call flows through a middleware chain (activity
// recording, panic recovery, ...) before reaching the handler.
package mcpx

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime/debug"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Result is an alias so toolsets rarely need to import the SDK.
type Result = mcp.CallToolResult

// Toolset is a group of related tools (project, browser, linkedin, brag, ...).
type Toolset interface {
	Name() string
	Register(r *Registry) error
}

// Optional interfaces a Toolset may implement.
type (
	// Instructor contributes a paragraph to the MCP server instructions.
	Instructor interface{ Instructions() string }
	// PublicRoutes mounts handlers on the tunneled server. Paths are already
	// behind the secret token: they are served under /files/{token}/<toolset>/.
	PublicRoutes interface {
		PublicRoutes(mux *http.ServeMux, prefix string)
	}
	// AdminRoutes mounts handlers on the local-only admin API under /api/<toolset>/.
	AdminRoutes interface {
		AdminRoutes(mux *http.ServeMux, prefix string)
	}
	// Closer releases resources (browsers, jobs) on shutdown.
	Closer interface{ Close() error }
)

// Call describes one tool invocation as seen by middleware.
type Call struct {
	Toolset string
	Tool    string
	Args    any
}

// Handler is the untyped form of a tool handler used by middleware.
type Handler func(ctx context.Context, call *Call) (*mcp.CallToolResult, error)

type Middleware func(next Handler) Handler

// ToolInfo is a registry listing entry (shown in the desktop app).
type ToolInfo struct {
	Toolset     string `json:"toolset"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type Registry struct {
	server     *mcp.Server
	middleware []Middleware
	current    string // toolset being registered
	tools      []ToolInfo
}

func NewRegistry(server *mcp.Server, mw ...Middleware) *Registry {
	return &Registry{server: server, middleware: mw}
}

// Install registers a toolset's tools, tagging them with its name.
func (r *Registry) Install(ts Toolset) error {
	r.current = ts.Name()
	defer func() { r.current = "" }()
	if err := ts.Register(r); err != nil {
		return fmt.Errorf("toolset %s: %w", ts.Name(), err)
	}
	return nil
}

func (r *Registry) Tools() []ToolInfo {
	out := append([]ToolInfo(nil), r.tools...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Toolset != out[j].Toolset {
			return out[i].Toolset < out[j].Toolset
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// TypedHandler is what toolsets implement for each tool.
type TypedHandler[In any] func(ctx context.Context, in In) (*mcp.CallToolResult, error)

// Add registers a tool whose input schema is inferred from In.
func Add[In any](r *Registry, name, description string, h TypedHandler[In]) {
	schema, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("tool %s: %v", name, err))
	}
	simplifyTypes(schema)
	toolset := r.current
	tool := &mcp.Tool{Name: name, Description: description, InputSchema: schema}
	r.tools = append(r.tools, ToolInfo{Toolset: toolset, Name: name, Description: description})

	mcp.AddTool(r.server, tool, func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
		final := Handler(func(ctx context.Context, _ *Call) (*mcp.CallToolResult, error) { return h(ctx, in) })
		for i := len(r.middleware) - 1; i >= 0; i-- {
			final = r.middleware[i](final)
		}
		res, err := final(ctx, &Call{Toolset: toolset, Tool: name, Args: in})
		if err != nil {
			// Tool errors go back to the model as text it can act on, not as protocol errors.
			return Errorf("%v", err), nil, nil
		}
		return res, nil, nil
	})
}

// Recover turns a panicking handler into a tool error.
func Recover() Middleware {
	return func(next Handler) Handler {
		return func(ctx context.Context, call *Call) (res *mcp.CallToolResult, err error) {
			defer func() {
				if p := recover(); p != nil {
					err = fmt.Errorf("internal error in %s: %v\n%s", call.Tool, p, firstLines(string(debug.Stack()), 12))
				}
			}()
			return next(ctx, call)
		}
	}
}

// --- result helpers ---

func Text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func Textf(format string, a ...any) *mcp.CallToolResult { return Text(fmt.Sprintf(format, a...)) }

func Errorf(format string, a ...any) *mcp.CallToolResult {
	r := Textf(format, a...)
	r.IsError = true
	return r
}

// JSON renders v as indented JSON text.
func JSON(v any) *mcp.CallToolResult {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return Errorf("encode result: %v", err)
	}
	return Text(string(b))
}

func Image(data []byte, mime string) *mcp.ImageContent {
	return &mcp.ImageContent{Data: data, MIMEType: mime}
}

// ResultSummary is a short plain-text preview of a result, for activity logs.
func ResultSummary(res *mcp.CallToolResult, max int) string {
	if res == nil {
		return ""
	}
	var parts []string
	for _, c := range res.Content {
		switch v := c.(type) {
		case *mcp.TextContent:
			parts = append(parts, v.Text)
		case *mcp.ImageContent:
			parts = append(parts, fmt.Sprintf("[image %s, %d bytes]", v.MIMEType, len(v.Data)))
		default:
			parts = append(parts, "[content]")
		}
	}
	return Clip(strings.Join(parts, "\n"), max)
}

// Clip shortens s to at most max bytes without splitting a UTF-8 character.
func Clip(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// simplifyTypes turns ["null","array"] unions into plain types; ChatGPT's
// connector import rejects union types.
func simplifyTypes(s *jsonschema.Schema) {
	if s == nil {
		return
	}
	if len(s.Types) == 2 && (s.Types[0] == "null" || s.Types[1] == "null") {
		if s.Types[0] == "null" {
			s.Type = s.Types[1]
		} else {
			s.Type = s.Types[0]
		}
		s.Types = nil
	}
	for _, p := range s.Properties {
		simplifyTypes(p)
	}
	simplifyTypes(s.Items)
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
