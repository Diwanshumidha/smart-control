// Package adminapi is the local-only JSON API used by the desktop app and the
// headless dashboard. See docs/admin-api.md. It is never mounted on the
// tunneled server.
package adminapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Diwanshumidha/smart-control/internal/app"
	"github.com/Diwanshumidha/smart-control/internal/mcpx"
	"github.com/Diwanshumidha/smart-control/internal/osutil"
	"github.com/Diwanshumidha/smart-control/internal/posts"
	"github.com/Diwanshumidha/smart-control/internal/resources"
	"github.com/Diwanshumidha/smart-control/internal/security/urlpolicy"
)

type API struct {
	app   *app.App
	token string // "" disables auth (in-process desktop)
	ui    fs.FS  // built frontend, may be nil
}

// New returns the admin handler. token protects the headless dashboard; pass
// "" when the handler is only reachable in-process (Wails asset server).
func New(a *app.App, token string, ui fs.FS) http.Handler {
	api := &API{app: a, token: token, ui: ui}
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/status", api.status)
	mux.HandleFunc("GET /api/doctor", api.doctor)
	mux.HandleFunc("GET /api/events", api.events)
	mux.HandleFunc("GET /api/activity", api.activity)

	mux.HandleFunc("GET /api/posts", api.listPosts)
	mux.HandleFunc("GET /api/posts/{id}", api.getPost)
	mux.HandleFunc("PATCH /api/posts/{id}", api.patchPost)
	mux.HandleFunc("DELETE /api/posts/{id}", api.deletePost)
	mux.HandleFunc("POST /api/posts/{id}/publish", api.publishPost)
	mux.HandleFunc("POST /api/posts/{id}/mark-posted", api.markPosted)

	mux.HandleFunc("GET /api/resources", api.listResources)
	mux.HandleFunc("GET /api/resources/{id}", api.getResource)
	mux.HandleFunc("GET /api/resources/{id}/file", api.resourceFile)
	mux.HandleFunc("DELETE /api/resources/{id}", api.deleteResource)
	mux.HandleFunc("POST /api/resources/{id}/reveal", api.revealResource)
	mux.HandleFunc("POST /api/resources/{id}/open", api.openResource)

	mux.HandleFunc("GET /api/url/rules", api.listRules)
	mux.HandleFunc("POST /api/url/rules", api.addRule)
	mux.HandleFunc("DELETE /api/url/rules/{id}", api.deleteRule)
	mux.HandleFunc("GET /api/url/approvals", api.listApprovals)
	mux.HandleFunc("POST /api/url/approvals/{id}", api.decide)

	mux.HandleFunc("GET /api/projects", api.listProjects)
	mux.HandleFunc("GET /api/projects/index", api.projectIndexStatus)
	mux.HandleFunc("POST /api/projects/reindex", api.reindexProjects)
	mux.HandleFunc("GET /api/browser/drivers", api.browserDrivers)
	mux.HandleFunc("POST /api/browser/open", api.openBrowser)
	mux.HandleFunc("GET /api/voice/providers", api.voiceProviders)

	mux.HandleFunc("POST /api/server/restart", api.restart)
	mux.HandleFunc("GET /api/settings", api.getSettings)
	mux.HandleFunc("PUT /api/settings", api.putSettings)

	for _, ts := range a.Toolsets {
		if ar, ok := ts.(mcpx.AdminRoutes); ok {
			ar.AdminRoutes(mux, "/api/"+ts.Name()+"/")
		}
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, fmt.Errorf("no route %s %s", r.Method, r.URL.Path))
	})
	if ui != nil {
		mux.Handle("/", spa(ui))
	}
	return api.auth(mux)
}

func (api *API) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if api.token == "" || !strings.HasPrefix(r.URL.Path, "/api/") {
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if got == "" {
			got = r.URL.Query().Get("k")
		}
		if subtle.ConstantTimeCompare([]byte(got), []byte(api.token)) != 1 {
			writeErr(w, http.StatusUnauthorized, errors.New("missing or wrong admin token"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// spa serves the built UI, falling back to index.html.
func spa(ui fs.FS) http.Handler {
	files := http.FileServerFS(ui)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if _, err := fs.Stat(ui, p); err == nil {
				files.ServeHTTP(w, r)
				return
			}
		}
		http.ServeFileFS(w, r, ui, "index.html")
	})
}

// --- status / doctor / events / activity ---

func (api *API) status(w http.ResponseWriter, _ *http.Request) { writeJSON(w, api.app.Status()) }

func (api *API) doctor(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"report": api.app.Doctor(r.Context())})
}

func (api *API) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, errors.New("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	ch, cancel := api.app.Bus.Subscribe()
	defer cancel()
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			b, _ := json.Marshal(ev)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
	}
}

func (api *API) activity(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	list, err := api.app.Activity.List(r.Context(), limit, before, q.Get("toolset"))
	respond(w, list, err)
}

// --- posts ---

func (api *API) listPosts(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, err := api.app.Posts.List(r.Context(), q.Get("platform"), q.Get("status"), 200)
	respond(w, list, err)
}

func (api *API) getPost(w http.ResponseWriter, r *http.Request) {
	p, err := api.app.Posts.Get(r.Context(), r.PathValue("id"))
	respondStatus(w, p, err, http.StatusNotFound)
}

func (api *API) patchPost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Title    *string   `json:"title"`
		Content  *string   `json:"content"`
		Hashtags *[]string `json:"hashtags"`
		Media    *[]string `json:"media"`
		Notes    *string   `json:"notes"`
		Status   *string   `json:"status"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	p, err := api.app.Posts.Update(r.Context(), r.PathValue("id"), func(p *posts.Post) error {
		if in.Title != nil {
			p.Title = *in.Title
		}
		if in.Content != nil {
			p.Content = *in.Content
		}
		if in.Hashtags != nil {
			p.Hashtags = *in.Hashtags
		}
		if in.Media != nil {
			p.Media = *in.Media
		}
		if in.Notes != nil {
			p.Notes = *in.Notes
		}
		if in.Status != nil {
			p.Status = *in.Status
		}
		return nil
	})
	respondStatus(w, p, err, http.StatusBadRequest)
}

func (api *API) deletePost(w http.ResponseWriter, r *http.Request) {
	respond(w, map[string]bool{"ok": true}, api.app.Posts.Delete(r.Context(), r.PathValue("id")))
}

func (api *API) publishPost(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := api.app.Posts.Get(ctx, r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	pub, err := api.app.Posts.Publisher(p.Platform)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	out, err := pub.Publish(ctx, p)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	media := []resources.Resource{}
	for _, id := range p.Media {
		if res, err := api.app.Res.Get(ctx, id); err == nil {
			media = append(media, *res)
		}
	}
	if !out.Manual {
		// The publisher posted it itself.
		_, _ = api.app.Posts.Update(ctx, p.ID, func(p *posts.Post) error {
			p.Status, p.ExternalURL = posts.StatusPosted, out.ExternalURL
			return nil
		})
	} else if p.Status == posts.StatusPendingReview {
		_, _ = api.app.Posts.Update(ctx, p.ID, func(p *posts.Post) error { p.Status = posts.StatusApproved; return nil })
	}
	writeJSON(w, struct {
		*posts.Publishing
		Media []resources.Resource `json:"media"`
	}{out, media})
}

func (api *API) markPosted(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ExternalURL string `json:"external_url"`
	}
	if r.ContentLength > 0 && !readJSON(w, r, &in) {
		return
	}
	p, err := api.app.Posts.Update(r.Context(), r.PathValue("id"), func(p *posts.Post) error {
		p.Status = posts.StatusPosted
		if in.ExternalURL != "" {
			p.ExternalURL = in.ExternalURL
		}
		return nil
	})
	respondStatus(w, p, err, http.StatusBadRequest)
}

// --- resources ---

func (api *API) listResources(w http.ResponseWriter, r *http.Request) {
	list, err := api.app.Res.List(r.Context(), r.URL.Query().Get("kind"), 500)
	respond(w, list, err)
}

func (api *API) getResource(w http.ResponseWriter, r *http.Request) {
	res, err := api.app.Res.Get(r.Context(), r.PathValue("id"))
	respondStatus(w, res, err, http.StatusNotFound)
}

func (api *API) resourceFile(w http.ResponseWriter, r *http.Request) {
	res, err := api.app.Res.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	w.Header().Set("Content-Type", res.MIME)
	w.Header().Set("Cache-Control", "private, max-age=86400")
	http.ServeFile(w, r, res.Path)
}

func (api *API) deleteResource(w http.ResponseWriter, r *http.Request) {
	respond(w, map[string]bool{"ok": true}, api.app.Res.Delete(r.Context(), r.PathValue("id")))
}

func (api *API) openResource(w http.ResponseWriter, r *http.Request) {
	res, err := api.app.Res.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	respond(w, map[string]string{"path": res.Path}, osutil.Open(res.Path))
}

func (api *API) revealResource(w http.ResponseWriter, r *http.Request) {
	res, err := api.app.Res.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	respond(w, map[string]string{"path": res.Path}, osutil.Reveal(res.Path))
}

// --- url policy ---

func (api *API) listRules(w http.ResponseWriter, r *http.Request) {
	list, err := api.app.Policy.Rules(r.Context())
	respond(w, list, err)
}

func (api *API) addRule(w http.ResponseWriter, r *http.Request) {
	var in urlpolicy.Rule
	if !readJSON(w, r, &in) {
		return
	}
	rule, err := api.app.Policy.AddRule(r.Context(), in)
	respondStatus(w, rule, err, http.StatusBadRequest)
}

func (api *API) deleteRule(w http.ResponseWriter, r *http.Request) {
	respond(w, map[string]bool{"ok": true}, api.app.Policy.DeleteRule(r.Context(), r.PathValue("id")))
}

func (api *API) listApprovals(w http.ResponseWriter, r *http.Request) {
	list, err := api.app.Policy.Approvals(r.Context(), r.URL.Query().Get("status"), 100)
	respond(w, list, err)
}

func (api *API) decide(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Decision string `json:"decision"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	a, err := api.app.Policy.Decide(r.Context(), r.PathValue("id"), in.Decision)
	respondStatus(w, a, err, http.StatusBadRequest)
}

// --- settings ---

// openBrowser shows the shared Brave window (body: {"url": "..."} optional).
func (api *API) openBrowser(w http.ResponseWriter, r *http.Request) {
	var in struct {
		URL string `json:"url"`
	}
	if r.ContentLength > 0 && !readJSON(w, r, &in) {
		return
	}
	if in.URL != "" && !strings.HasPrefix(in.URL, "http://") && !strings.HasPrefix(in.URL, "https://") {
		writeErr(w, http.StatusBadRequest, errors.New("url must be http(s)"))
		return
	}
	if err := api.app.OpenSharedBrowser(r.Context(), in.URL); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

// restart answers first, then restarts the server; clients reconnect.
func (api *API) restart(w http.ResponseWriter, _ *http.Request) {
	if api.app.OnRestart == nil {
		writeErr(w, http.StatusNotImplemented, errors.New("this server can't restart itself; restart it by hand"))
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
	go func() {
		time.Sleep(300 * time.Millisecond)
		api.app.OnRestart()
	}()
}

func (api *API) getSettings(w http.ResponseWriter, _ *http.Request) { writeJSON(w, api.app.Settings()) }

func (api *API) putSettings(w http.ResponseWriter, r *http.Request) {
	var in app.Settings
	if !readJSON(w, r, &in) {
		return
	}
	restart, err := api.app.ApplySettings(in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true, "restart_required": restart})
}

// --- projects ---

func (api *API) listProjects(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 {
		limit = 1000
	}
	list, err := api.app.Projects.Search(r.Context(), r.URL.Query().Get("q"), limit)
	respond(w, list, err)
}

func (api *API) projectIndexStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, api.app.Projects.Status())
}

func (api *API) reindexProjects(w http.ResponseWriter, r *http.Request) {
	_, err := api.app.Projects.Reindex(r.Context())
	respond(w, api.app.Projects.Status(), err)
}

// --- browser ---

func (api *API) browserDrivers(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"active": api.app.Browser.Driver(), "drivers": api.app.Browser.Drivers()})
}

// --- voice ---

func (api *API) voiceProviders(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"providers": api.app.VoiceProviders()})
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

func respond(w http.ResponseWriter, v any, err error) {
	respondStatus(w, v, err, http.StatusInternalServerError)
}

func respondStatus(w http.ResponseWriter, v any, err error, status int) {
	if err != nil {
		writeErr(w, status, err)
		return
	}
	writeJSON(w, v)
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("bad json: %v", err))
		return false
	}
	return true
}
