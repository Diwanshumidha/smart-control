// smart-control is the headless server: MCP over a tunnel plus the local
// dashboard in your browser. The desktop app (cmd/smart-control-desktop) runs
// the same kernel in a native window.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/Diwanshumidha/smart-control/internal/adminapi"
	"github.com/Diwanshumidha/smart-control/internal/app"
	"github.com/Diwanshumidha/smart-control/internal/browser"
	"github.com/Diwanshumidha/smart-control/internal/browser/shared"
	"github.com/Diwanshumidha/smart-control/internal/config"
	"github.com/Diwanshumidha/smart-control/internal/daemon"
	"github.com/Diwanshumidha/smart-control/internal/events"
	"github.com/Diwanshumidha/smart-control/internal/notify"
	"github.com/Diwanshumidha/smart-control/internal/osutil"
	"github.com/Diwanshumidha/smart-control/internal/toolsets/brag"
	"github.com/Diwanshumidha/smart-control/internal/tray"
	"github.com/Diwanshumidha/smart-control/internal/voice/kokoro"
	"github.com/Diwanshumidha/smart-control/web"
)

var version = "dev"

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func usage() {
	fmt.Fprintf(os.Stderr, `smart-control %s — personal MCP server (LinkedIn social media manager)

Usage:
  smart-control serve [flags]   start MCP + tunnel + local dashboard (default); keeps
                                running in the background with a tray icon, and the
                                desktop app connects to it
  smart-control doctor          check brave, node, ffmpeg, cloudflared, hyperframes
  smart-control setup           install the Hyperframes agent skills (for brag videos)
  smart-control browser [url]   open the AI's shared Brave window (log in to sites there)
  smart-control token --rotate  print (or rotate) the secret connector token
  smart-control version

Config lives in %s/config.json (override with SMART_CONTROL_HOME or --data).

Serve flags:
`, version, config.DefaultDataDir())
	flag.PrintDefaults()
}

func main() {
	cmd := "serve"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	fs.Usage = usage
	flag.CommandLine = fs
	var roots multiFlag
	dataDir := fs.String("data", config.DefaultDataDir(), "data directory (config, database, media, browser profile)")
	tunnelMode := fs.String("tunnel", "", "override tunnel mode for this run: quick|cloudflare|tailscale|ngrok|off")
	hostname := fs.String("hostname", "", "fixed hostname for --tunnel=cloudflare|ngrok (saved)")
	rotate := fs.Bool("rotate", false, "with `token`: generate a new connector token")
	noDashboard := fs.Bool("no-dashboard", false, "don't serve the local dashboard (the desktop app needs it)")
	noTray := fs.Bool("no-tray", false, "don't show the system tray icon")
	fs.Var(&roots, "root", "project directory the AI may read (repeatable; saved to config)")
	_ = fs.Parse(args)

	switch cmd {
	case "version":
		fmt.Println(version)
		return
	case "help", "-h", "--help":
		usage()
		return
	case "setup":
		if err := brag.Setup(); err != nil {
			log.Fatal(err)
		}
		fmt.Println("\nInstalling the local voice for brag voiceovers…")
		if err := kokoro.Install(context.Background(), *dataDir, os.Stdout); err != nil {
			log.Fatalf("voice: %v", err)
		}
		return
	}

	cfg, err := config.Load(*dataDir)
	if err != nil {
		log.Fatal(err)
	}
	switch cmd {
	case "doctor":
		fmt.Println(app.Doctor(context.Background(), version, cfg))
		return
	case "browser":
		// Open the shared Brave window (log in to sites the AI should use).
		b := browser.New(cfg.Browser, cfg.Sub("browser"), nil)
		if err := shared.Open(context.Background(), b.SettingsFor("shared"), fs.Arg(0)); err != nil {
			log.Fatal(err)
		}
		return
	case "token":
		if *rotate {
			cfg.Token = newToken()
			if err := cfg.Save(); err != nil {
				log.Fatal(err)
			}
			fmt.Fprintln(os.Stderr, "Token rotated. Update the ChatGPT connector URL.")
		}
		fmt.Println(cfg.Token)
		return
	case "serve":
	default:
		usage()
		os.Exit(2)
	}

	if len(roots) > 0 {
		cfg.Roots = nil
		for _, r := range roots {
			abs, _ := filepath.Abs(r)
			cfg.Roots = append(cfg.Roots, abs)
		}
	}
	if *hostname != "" {
		cfg.Tunnel.Hostname = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(*hostname, "https://"), "http://"), "/")
	}
	if len(roots) > 0 || *hostname != "" {
		_ = cfg.Save()
	}
	if *tunnelMode != "" {
		cfg.Tunnel.Mode = *tunnelMode // this run only
	}

	if info := daemon.Running(context.Background(), cfg.DataDir); info != nil {
		log.Fatalf("smart-control is already running (pid %d). Open the desktop app, or the dashboard at %s/?k=%s", info.PID, info.AdminURL, info.AdminToken)
	}
	// A restart hands the admin token over so open dashboards keep working.
	adminToken := os.Getenv(daemon.TokenEnv)
	os.Unsetenv(daemon.TokenEnv) // keep it out of jobs the AI starts
	if len(adminToken) < 32 {
		adminToken = newToken()
	}

	a, err := app.New(cfg, version)
	if err != nil {
		log.Fatal(err)
	}
	restart := make(chan struct{}, 1)
	a.OnRestart = func() {
		select {
		case restart <- struct{}{}:
		default:
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	if err := a.Start(ctx); err != nil {
		a.Close()
		log.Fatal(err)
	}
	for _, r := range a.Guard.Roots() {
		log.Printf("readable root: %s", r)
	}

	var srv *http.Server
	if !*noDashboard {
		ln, err := net.Listen("tcp", cfg.AdminAddr)
		if err != nil {
			a.Close()
			log.Fatalf("dashboard: %v", err)
		}
		srv = &http.Server{Handler: adminapi.New(a, adminToken, web.FS()), ReadHeaderTimeout: 15 * time.Second}
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("dashboard: %v", err)
			}
		}()
		adminURL := "http://" + ln.Addr().String()
		if err := daemon.Write(cfg.DataDir, daemon.Info{
			PID: os.Getpid(), Version: version, AdminURL: adminURL, AdminToken: adminToken, StartedAt: time.Now().UnixMilli(),
		}); err != nil {
			log.Printf("daemon.json: %v (the desktop app won't find this server)", err)
		}
		log.Printf("dashboard: %s/?k=%s", adminURL, adminToken)
	}

	desktop := hasDesktopSession()
	openApp := func(args ...string) func() {
		return func() {
			if err := launchDesktop(cfg.DataDir, args...); err != nil {
				log.Printf("open desktop app: %v", err)
			}
		}
	}
	var popup func()
	if desktop {
		popup = openApp("--approvals")
	}
	go notify.Run(ctx, a, popup)

	var tr *tray.Tray
	if desktop && !*noTray && tray.Available() {
		acts := tray.Actions{OpenApp: openApp(), OpenApprovals: popup, Restart: a.OnRestart, Quit: stop,
			OpenBrowser: func() {
				if err := a.OpenSharedBrowser(ctx, ""); err != nil {
					log.Printf("open AI browser: %v", err)
				}
			}}
		if srv != nil {
			acts.OpenDashboard = func() { _ = osutil.Open(fmt.Sprintf("http://%s/?k=%s", cfg.AdminAddr, adminToken)) }
		}
		tr = tray.Start(ctx, acts)
		go feedTray(ctx, a, tr)
	}

	go printConnectorWhenReady(ctx, a)
	restarting := false
	select {
	case <-ctx.Done():
		log.Print("shutting down")
	case <-restart:
		restarting = true
		log.Print("restarting")
	}

	stop()
	tr.Close()
	if srv != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = srv.Shutdown(shutdownCtx)
		cancel()
	}
	daemon.Remove(cfg.DataDir, os.Getpid())
	a.Close()
	if restarting {
		if err := osutil.Reexec(append(os.Environ(), daemon.TokenEnv+"="+adminToken)); err != nil {
			log.Fatalf("restart: %v", err)
		}
	}
}

// hasDesktopSession reports whether there's a graphical session to show the
// tray, notifications and the approval window in.
func hasDesktopSession() bool {
	switch runtime.GOOS {
	case "windows", "darwin":
		return true
	}
	return os.Getenv("WAYLAND_DISPLAY") != "" || os.Getenv("DISPLAY") != ""
}

// launchDesktop starts smart-control-desktop (main window, or the floating
// approval window with --approvals). A second launch just focuses the
// existing window.
func launchDesktop(dataDir string, args ...string) error {
	bin, err := daemon.FindBinary("smart-control-desktop")
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, append([]string{"--data", dataDir}, args...)...)
	_ = os.MkdirAll(filepath.Join(dataDir, "logs"), 0o700)
	if f, err := os.OpenFile(filepath.Join(dataDir, "logs", "desktop.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		cmd.Stdout, cmd.Stderr = f, f
		defer f.Close()
	}
	if err := osutil.StartBackground(cmd); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}

// feedTray keeps the tray's status line, approval count and badge current.
func feedTray(ctx context.Context, a *app.App, tr *tray.Tray) {
	ch, cancel := a.Bus.Subscribe()
	defer cancel()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	running := 0
	push := func() {
		pending, _ := a.Policy.Approvals(ctx, "pending", 100)
		tr.Update(tray.State{Tunnel: a.Tunnel().State, Pending: len(pending), Running: running, Version: a.Version})
	}
	push()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			push()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			switch ev.Topic {
			case events.ToolStarted:
				running++
			case events.ToolFinished:
				running = max(0, running-1)
			case events.ApprovalRequest, events.ApprovalDecided, events.ServerStatus:
			default:
				continue
			}
			push()
		}
	}
}

func printConnectorWhenReady(ctx context.Context, a *app.App) {
	if u := a.ConnectorURL(); u != "" {
		printConnector(u)
		return
	}
	ch, cancel := a.Bus.Subscribe()
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-ch:
			if ev.Topic != events.ServerStatus {
				continue
			}
			t := a.Tunnel()
			switch {
			case t.State == "up" && a.ConnectorURL() != "":
				printConnector(a.ConnectorURL())
				return
			case t.State == "error":
				log.Printf("no public URL: tunnel failed (%s). ChatGPT cannot reach localhost.", t.Error)
				return
			case t.State == "off":
				log.Printf("tunnel is off: ChatGPT cannot reach %s. Use --tunnel=quick or set tunnel.mode.", a.LocalMCPURL())
				return
			}
		}
	}
}

func printConnector(u string) {
	fmt.Printf("\n  ChatGPT connector URL (keep it secret; the token is the password):\n\n    %s\n\n", u)
	fmt.Printf("  ChatGPT → Settings → Apps & Connectors → Advanced → Developer mode → Create connector\n")
	fmt.Printf("  Authentication: No authentication (the token in the URL is the auth)\n\n")
}

func newToken() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
