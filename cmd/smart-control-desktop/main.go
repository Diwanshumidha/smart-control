// smart-control-desktop is the window onto the background server. It finds
// `smart-control serve` through <data>/daemon.json (starting it when it isn't
// running), proxies the UI's /api calls to it and relays its live events.
// Closing the window leaves the server running, reachable from the tray.
//
// With --approvals it opens the small floating window that answers pending URL
// approvals; the server launches it when the AI is waiting on you.
//
// Build: see Makefile (`make desktop`); Wails needs the tags
// desktop,production (and webkit2_41 on current Linux distros).
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	wruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/Diwanshumidha/smart-control/internal/config"
	"github.com/Diwanshumidha/smart-control/internal/daemon"
	"github.com/Diwanshumidha/smart-control/web"
)

var version = "dev"

func main() {
	dataDir := flag.String("data", config.DefaultDataDir(), "data directory")
	approvals := flag.Bool("approvals", false, "open the floating window for pending URL approvals")
	flag.Parse()
	dir, err := filepath.Abs(*dataDir)
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	info, startErr := daemon.Ensure(ctx, dir)
	if startErr != nil {
		log.Print(startErr)
	} else if info.Version != version {
		log.Printf("note: server is %s, this window is %s; restart the server from the tray to update it", info.Version, version)
	}
	c := newClient(dir, info)

	if !web.Built() {
		log.Print("warning: web/dist is empty; run `bun run build` in web/ and rebuild")
	}
	zoom := prepareDisplay() // before Wails initialises GTK
	px := func(n int) int { return int(float64(n) * zoom) }

	mode := "main"
	opts := &options.App{
		Title:            "Smart Control",
		Width:            px(1280),
		Height:           px(820),
		MinWidth:         px(960),
		MinHeight:        px(640),
		BackgroundColour: &options.RGBA{R: 14, G: 15, B: 18, A: 255},
		Linux:            &linux.Options{ProgramName: "smart-control"},
	}
	instance := "io.smart-control.desktop."
	if *approvals {
		mode = "approvals"
		instance = "io.smart-control.approvals."
		opts.Title = "Smart Control · Approval needed"
		opts.Width, opts.Height = px(480), px(340)
		opts.MinWidth, opts.MinHeight = opts.Width, opts.Height
		opts.MaxWidth, opts.MaxHeight = opts.Width, opts.Height
		opts.DisableResize = true
		opts.AlwaysOnTop = true
	}

	var wctx context.Context
	opts.AssetServer = &assetserver.Options{Assets: web.FS(), Handler: debugHTTP(c.handler(mode))}
	opts.SingleInstanceLock = &options.SingleInstanceLock{
		// One window per data dir, so a second profile (or a test copy) can run alongside.
		UniqueId: instance + instanceKey(dir),
		OnSecondInstanceLaunch: func(options.SecondInstanceData) {
			if wctx == nil {
				return
			}
			wruntime.WindowUnminimise(wctx)
			wruntime.WindowShow(wctx)
			if *approvals {
				wruntime.EventsEmit(wctx, "approvals.refresh")
				floatOnActiveWorkspace()
			}
		},
	}
	opts.OnStartup = func(x context.Context) {
		wctx = x
		// Without a server the window still opens and offers "Start server".
		go c.forwardEvents(ctx, x)
	}
	opts.OnDomReady = func(x context.Context) {
		if zoom != 1 {
			wruntime.WindowExecJS(x, fmt.Sprintf("document.documentElement.style.zoom = %q", strconv.FormatFloat(zoom, 'f', 3, 64)))
		}
		if *approvals {
			floatOnActiveWorkspace()
		}
	}
	opts.OnShutdown = func(context.Context) { cancel() }

	if err := wails.Run(opts); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

// debugHTTP logs every request the webview makes when SMART_CONTROL_DEBUG_HTTP=1.
func debugHTTP(next http.Handler) http.Handler {
	if os.Getenv("SMART_CONTROL_DEBUG_HTTP") != "1" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		log.Printf("http → %s %s", r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
		log.Printf("http ← %s %s (%s)", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func instanceKey(dataDir string) string {
	sum := sha256.Sum256([]byte(dataDir))
	return hex.EncodeToString(sum[:6])
}
