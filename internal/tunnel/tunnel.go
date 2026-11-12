// Package tunnel exposes the local MCP server on a public HTTPS URL so remote
// clients (ChatGPT) can reach it: Cloudflare quick/named tunnels, Tailscale
// Funnel or ngrok.
package tunnel

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Diwanshumidha/smart-control/internal/config"
	"github.com/Diwanshumidha/smart-control/internal/jobs"
)

type tunnelConfig = config.Tunnel

// Modes lists the accepted tunnel modes.
var Modes = []string{"quick", "cloudflare", "tailscale", "ngrok", "off"}

// Start starts the configured tunnel and returns its public base URL once
// /healthz answers through it. The tunnel process dies with ctx.
func Start(ctx context.Context, c config.Tunnel, port string) (string, error) {
	return startTunnel(ctx, c, port)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// startTunnel starts the configured tunnel and returns its public base URL once
// /healthz answers through it. The tunnel process dies with ctx.
func startTunnel(ctx context.Context, c tunnelConfig, port string) (string, error) {
	local := "http://127.0.0.1:" + port
	var (
		cmd    *exec.Cmd
		public string
		err    error
	)
	switch c.Mode {
	case "quick":
		return startQuickTunnel(ctx, local)
	case "cloudflare":
		cmd, public, err = cloudflareNamed(ctx, c, local)
	case "tailscale":
		cmd, public, err = tailscaleFunnel(ctx, port)
	case "ngrok":
		cmd, public, err = ngrokStatic(ctx, c, local)
	default:
		return "", fmt.Errorf("unknown tunnel mode %q", c.Mode)
	}
	if err != nil {
		return "", err
	}
	logs := &jobs.TailBuffer{}
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		return "", err
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	if err := waitHealthy(ctx, public, exited); err != nil {
		return "", fmt.Errorf("%v\n%s output:\n%s", err, filepath.Base(cmd.Path), clip(logs.String(), 3000))
	}
	return public, nil
}

func waitHealthy(ctx context.Context, public string, exited <-chan error) error {
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-exited:
			return fmt.Errorf("tunnel process exited: %v", err)
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
		resp, err := client.Get(public + "/healthz")
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
			resp.Body.Close()
			if resp.StatusCode == 200 && strings.TrimSpace(string(body)) == "ok" {
				return nil
			}
		}
	}
	return fmt.Errorf("%s/healthz did not answer within 90s", public)
}

// ---- Cloudflare quick tunnel (random URL each run) ----

var quickURLRe = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

func startQuickTunnel(ctx context.Context, local string) (string, error) {
	bin, err := exec.LookPath("cloudflared")
	if err != nil {
		return "", fmt.Errorf("cloudflared not on PATH")
	}
	cmd := exec.CommandContext(ctx, bin, "tunnel", "--no-autoupdate", "--url", local)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	found := make(chan string, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			if m := quickURLRe.FindString(sc.Text()); m != "" {
				select {
				case found <- m:
				default:
				}
			}
		}
		_ = cmd.Wait()
	}()
	select {
	case u := <-found:
		return u, nil
	case <-time.After(45 * time.Second):
		return "", fmt.Errorf("cloudflared did not report a URL within 45s")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

// ---- Cloudflare named tunnel (your own domain, fixed hostname) ----

func cloudflareNamed(ctx context.Context, c tunnelConfig, local string) (*exec.Cmd, string, error) {
	bin, err := exec.LookPath("cloudflared")
	if err != nil {
		return nil, "", fmt.Errorf("cloudflared not on PATH")
	}
	if c.Hostname == "" {
		return nil, "", fmt.Errorf("cloudflare mode needs --hostname (e.g. brag.example.com, a domain on your Cloudflare account)")
	}
	home, _ := os.UserHomeDir()
	if _, err := os.Stat(filepath.Join(home, ".cloudflared", "cert.pem")); err != nil {
		return nil, "", fmt.Errorf("not logged in to Cloudflare: run `cloudflared tunnel login` once, then retry")
	}
	name := c.Name
	if name == "" {
		name = "smart-control"
	}
	// Create the tunnel once; `create` fails harmlessly if it already exists.
	out, _ := exec.CommandContext(ctx, bin, "tunnel", "list", "--output", "json", "--name", name).Output()
	var existing []struct{ Name string }
	if json.Unmarshal(out, &existing) != nil || len(existing) == 0 {
		if out, err := exec.CommandContext(ctx, bin, "tunnel", "create", name).CombinedOutput(); err != nil {
			return nil, "", fmt.Errorf("cloudflared tunnel create: %v\n%s", err, out)
		}
		log.Printf("created Cloudflare tunnel %q", name)
	}
	// Point the hostname at the tunnel (idempotent with --overwrite-dns).
	if out, err := exec.CommandContext(ctx, bin, "tunnel", "route", "dns", "--overwrite-dns", name, c.Hostname).CombinedOutput(); err != nil {
		return nil, "", fmt.Errorf("cloudflared tunnel route dns: %v\n%s", err, out)
	}
	cmd := exec.CommandContext(ctx, bin, "tunnel", "--no-autoupdate", "run", "--url", local, name)
	return cmd, "https://" + c.Hostname, nil
}

// ---- Tailscale Funnel (free https://<machine>.<tailnet>.ts.net, no domain) ----

func tailscaleFunnel(ctx context.Context, port string) (*exec.Cmd, string, error) {
	bin, err := exec.LookPath("tailscale")
	if err != nil {
		return nil, "", fmt.Errorf("tailscale not on PATH (install from https://tailscale.com/download and run `tailscale up`)")
	}
	out, err := exec.CommandContext(ctx, bin, "status", "--json").Output()
	if err != nil {
		return nil, "", fmt.Errorf("tailscale status failed — is tailscaled running and are you logged in (`tailscale up`)? %v", err)
	}
	var st struct {
		Self struct{ DNSName string }
	}
	if err := json.Unmarshal(out, &st); err != nil || st.Self.DNSName == "" {
		return nil, "", fmt.Errorf("could not read this machine's tailnet name from `tailscale status --json`")
	}
	host := strings.TrimSuffix(st.Self.DNSName, ".")
	// Foreground funnel: stops when smart-control stops.
	cmd := exec.CommandContext(ctx, bin, "funnel", port)
	return cmd, "https://" + host, nil
}

// ---- ngrok static domain (one free static domain per account) ----

func ngrokStatic(ctx context.Context, c tunnelConfig, local string) (*exec.Cmd, string, error) {
	bin, err := exec.LookPath("ngrok")
	if err != nil {
		return nil, "", fmt.Errorf("ngrok not on PATH (install it and run `ngrok config add-authtoken <token>`)")
	}
	if c.Hostname == "" {
		return nil, "", fmt.Errorf("ngrok mode needs --hostname set to your static domain (dashboard → Domains), e.g. your-name.ngrok-free.app")
	}
	cmd := exec.CommandContext(ctx, bin, "http", "--url=https://"+c.Hostname, local, "--log=stdout")
	return cmd, "https://" + c.Hostname, nil
}
