package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/Diwanshumidha/smart-control/internal/daemon"
)

// client connects the window to the background server. The server's address
// and admin token come from daemon.json and are re-read whenever a request
// fails, so a server restart is picked up without reopening the window.
type client struct {
	dataDir string

	mu   sync.Mutex
	info *daemon.Info
	kick chan struct{} // wakes the event relay to reconnect now

	// connected mirrors the last "connection" event, for pages that load
	// after it was sent.
	connected atomic.Bool
}

func newClient(dataDir string, info *daemon.Info) *client {
	return &client{dataDir: dataDir, info: info, kick: make(chan struct{}, 1)}
}

// start runs the server when it isn't running (no-op when it is).
func (c *client) start(ctx context.Context) (*daemon.Info, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	info, err := daemon.Ensure(ctx, c.dataDir)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.info = info
	c.mu.Unlock()
	select {
	case c.kick <- struct{}{}:
	default:
	}
	return info, nil
}

func (c *client) current() *daemon.Info {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info
}

func (c *client) reload() *daemon.Info {
	info, err := daemon.Read(c.dataDir)
	if err != nil {
		return c.current()
	}
	c.mu.Lock()
	c.info = info
	c.mu.Unlock()
	return info
}

// handler serves /api/desktop itself and proxies every other /api/ call to
// the server with the admin token attached. The UI's static files are served
// by Wails before this handler is reached.
func (c *client) handler(mode string) http.Handler {
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			info := c.current()
			target, _ := url.Parse(info.AdminURL)
			pr.SetURL(target)
			pr.Out.Header.Set("Authorization", "Bearer "+info.AdminToken)
		},
		ModifyResponse: func(res *http.Response) error {
			if res.StatusCode == http.StatusUnauthorized {
				c.reload() // the server restarted with a new token
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			c.reload()
			writeErr(w, http.StatusBadGateway, errors.New("can't reach the Smart Control server: "+err.Error()))
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/desktop/server/start" && r.Method == http.MethodPost:
			info, err := c.start(context.WithoutCancel(r.Context()))
			if err != nil {
				writeErr(w, http.StatusBadGateway, err)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "pid": info.PID})
		case r.URL.Path == "/api/desktop":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"mode": mode, "version": version, "connected": c.connected.Load()})
		case !strings.HasPrefix(r.URL.Path, "/api/"):
			http.NotFound(w, r)
		case c.current() == nil && c.reload() == nil:
			writeErr(w, http.StatusServiceUnavailable, errors.New("the Smart Control server isn't running"))
		default:
			proxy.ServeHTTP(w, r)
		}
	})
}

// forwardEvents relays the server's SSE stream to the UI as Wails "event"
// messages (Wails' asset handler buffers responses, so SSE can't pass
// through it), and reports the connection state as "connection".
func (c *client) forwardEvents(ctx, wctx context.Context) {
	delay := time.Second
	for ctx.Err() == nil {
		info := c.reload()
		if info != nil {
			err := c.stream(ctx, wctx, info)
			if err == nil {
				delay = time.Second
			}
		}
		c.connected.Store(false)
		wruntime.EventsEmit(wctx, "connection", false)
		select {
		case <-ctx.Done():
			return
		case <-c.kick:
			delay = time.Second
			continue
		case <-time.After(delay):
		}
		delay = min(delay*2, 10*time.Second)
	}
}

func (c *client) stream(ctx, wctx context.Context, info *daemon.Info) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.AdminURL+"/api/events", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+info.AdminToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return errors.New(res.Status)
	}
	c.connected.Store(true)
	wruntime.EventsEmit(wctx, "connection", true)
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 64<<10), 8<<20)
	for sc.Scan() {
		if data, ok := strings.CutPrefix(sc.Text(), "data: "); ok {
			wruntime.EventsEmit(wctx, "event", json.RawMessage(data))
		}
	}
	return nil
}

func writeErr(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}
