// Package server is the public (tunneled) HTTP surface: the MCP endpoint and
// toolset file routes, all behind the secret path token. The admin API is a
// separate handler that is never mounted here.
package server

import (
	"bufio"
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Diwanshumidha/smart-control/internal/mcpx"
)

type Server struct {
	token    string
	mcp      *mcp.Server
	toolsets []mcpx.Toolset
}

func New(token string, s *mcp.Server, toolsets []mcpx.Toolset) *Server {
	return &Server{token: token, mcp: s, toolsets: toolsets}
}

func (s *Server) Handler() http.Handler {
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp }, &mcp.StreamableHTTPOptions{
		// Requests arrive through a tunnel with a public Host header; the
		// secret token in the path is what authenticates them.
		DisableLocalhostProtection: true,
		// Stateless serves both the session-based and the sessionless
		// protocol; all state lives in SQLite and the job manager.
		Stateless:           true,
		JSONResponse:        true,
		MaxRequestBodyBytes: 16 << 20,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	mux.HandleFunc("/mcp/{token}", func(w http.ResponseWriter, r *http.Request) {
		if !s.tokenOK(r.PathValue("token")) {
			log.Printf("%s %s → 404 (wrong token)", r.Method, redact(r.URL.Path, s.token))
			http.NotFound(w, r)
			return
		}
		// Some clients send only one of the two media types the spec requires.
		if r.Method == http.MethodPost {
			accept := r.Header.Get("Accept")
			if !strings.Contains(accept, "application/json") || !strings.Contains(accept, "text/event-stream") {
				r.Header.Set("Accept", "application/json, text/event-stream")
			}
		}
		rec := &statusRecorder{ResponseWriter: w, status: 200}
		label := peekBody(r)
		h.ServeHTTP(rec, r)
		log.Printf("MCP %s %s → %d", r.Method, label, rec.status)
	})

	// Toolset file routes live under /files/{token}/<toolset>/…
	for _, ts := range s.toolsets {
		pr, ok := ts.(mcpx.PublicRoutes)
		if !ok {
			continue
		}
		sub := http.NewServeMux()
		prefix := "/files/{token}/" + ts.Name() + "/"
		pr.PublicRoutes(sub, prefix)
		mux.Handle(prefix, s.requireToken(sub))
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("%s %s → 404", r.Method, redact(r.URL.Path, s.token))
		http.NotFound(w, r)
	})
	return mux
}

func (s *Server) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/files/"), "/", 2)
		if len(parts) < 1 || !s.tokenOK(parts[0]) {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) tokenOK(t string) bool {
	return s.token != "" && subtle.ConstantTimeCompare([]byte(t), []byte(s.token)) == 1
}

// --- logging helpers ---

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, errors.New("hijack not supported")
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// peekBody returns a short "method[/tool]" label for a JSON-RPC body and
// restores the body for the real handler.
func peekBody(r *http.Request) string {
	if r.Body == nil || r.Method != http.MethodPost {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, 16<<20+1))
	r.Body = io.NopCloser(bytes.NewReader(b))
	if err != nil {
		return "?"
	}
	var msg struct {
		Method string `json:"method"`
		Params struct {
			Name string `json:"name"`
		} `json:"params"`
	}
	if json.Unmarshal(b, &msg) != nil {
		return "(batch or invalid json)"
	}
	if msg.Params.Name != "" {
		return msg.Method + "/" + msg.Params.Name
	}
	return msg.Method
}

func redact(p, token string) string {
	if token == "" {
		return p
	}
	return strings.ReplaceAll(p, token, "<token>")
}
