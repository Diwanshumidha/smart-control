# smart-control

A personal MCP server that gives an AI (ChatGPT in the browser, or any MCP
client) tools to work as your **LinkedIn social media manager**. A desktop app
shows everything the AI does, live, and nothing is posted without you.

| Toolset | Tools |
|---|---|
| `project` | `search_project` (backed by the project index), `file_tree`, `get_files_from_project`, `grep_in_project` (read-only, confined to your project folders, secrets blocked) |
| `browser` | `open_url`, `screenshot_url` (one-shot), and interactive tabs that stay open: `browser_open`, `browser_navigate`, `browser_snapshot`, `browser_click`, `browser_type`, `browser_press`, `browser_select`, `browser_hover`, `browser_scroll`, `browser_wait`, `browser_screenshot`, `browser_eval`, `browser_tabs`, `browser_close` (Brave through a pluggable driver; every URL gated by URL access rules + live approval) |
| `media` | `save_video_resource` (from a URL, `brag://<run>/<file>` or `project:///<path>`), `list_resources` |
| `linkedin` | `post_linkedin_post`, `update_linkedin_post`, `list_linkedin_posts` (stored in SQLite; you publish) |
| `brag` | the full /brag launch-video workflow (replaces brag-bridge): `brag_guide`, `brag_start`, `find_brand_assets`, `copy_from_project`, `download_asset`, `submit_plan`, `approve_plan`, `hyperframes`, `voiceover`, `review_scene`, `review_sheet`, `finalize`, `list_runs`, … |

## Getting started

### 1. Install the prerequisites

| Needed for | Install |
|---|---|
| Building | [Go](https://go.dev) 1.27 (pinned in `mise.toml`, so `mise install` works) and [Bun](https://bun.sh) for the dashboard |
| Desktop app on Linux | `webkit2gtk-4.1` from your package manager. The tray icon needs a StatusNotifierItem host (Waybar, KDE, or GNOME's AppIndicator extension) |
| Browser tools | [Brave](https://brave.com) |
| Brag videos | [Node.js](https://nodejs.org) 22+ and `ffmpeg` |
| Voiceovers | Python 3.10–3.13 (not 3.14 yet). `smart-control setup` finds it and builds its own venv |
| Reaching ChatGPT | [`cloudflared`](https://developers.cloudflare.com/cloudflare-one/connections/connect-networks/downloads/) for the default quick tunnel, or Tailscale / ngrok |

### 2. Build and set up

```sh
git clone https://github.com/Diwanshumidha/smart-control.git
cd smart-control
mise install                      # optional: the pinned Go
make                              # dashboard + bin/smart-control + bin/smart-control-desktop
./bin/smart-control setup         # Hyperframes skills + the local voice (about 250 MB, once)
./bin/smart-control doctor        # every line should say OK
```

### 3. Start it

```sh
./bin/smart-control-desktop       # opens the dashboard and starts the server in the background
```

The Overview screen shows the **connector URL** (`https://….trycloudflare.com/mcp/<token>`).
Under Settings, set your project folders (default `~/projects`).

### 4. Connect ChatGPT

In ChatGPT open **Settings → Apps & Connectors → Advanced → Developer mode → Create
connector**, paste the connector URL and choose **No authentication** (the token in the URL is
the password). Any other MCP client works with the same URL.

### 5. Make your first video

Open a new chat with the connector enabled and paste a prompt from
[examples/prompts.md](examples/prompts.md), for example:

```
Make a brag video for my project at ~/projects/<project>. It's for <audience>; the one thing
worth bragging about is <the moment>. Call brag_guide with topic "index" first and follow it.
Show me the plan, including every voiceover line, and wait for my approval before building.
```

You approve the plan in the chat, the AI builds and reviews the video, and `finalize` returns
links to `brag.mp4`, the poster and the share copy. Watch it all happen live in the dashboard.
More in [examples/](examples/).

## Brag videos

The AI follows a playbook, [`internal/toolsets/brag/embedded/CHATGPT.md`](internal/toolsets/brag/embedded/CHATGPT.md),
in three phases, and the server enforces the steps that matter:

1. **Plan.** Research the product, screenshot it to see how it looks, storyboard each scene (the one
   detail in focus, the order elements arrive in, the voiceover line), and `submit_plan`. The
   server rejects weak plans: wrong length, too few kinds of transition, no product, low
   contrast, unknown templates, a redrawn logo, a voiceover line that can't fit its scene, a
   screenshot in the video you didn't ask for. **Nothing is built until you approve the plan.**
2. **Build.** The scaffold gives every scene the same layout grid (caption, stage and footnote
   zones inside the platform's safe area). The product UI is rebuilt from its source code, not
   pasted as screenshots, unless you ask for screenshots. `voiceover` speaks each scene's line,
   places it in its scene, and returns the film time of every `{cue}` so actions land on the word.
3. **Review and deliver.** `review_scene` shows each scene at a readable size at the moments that
   matter (settled, every cue, exit, the cut) beside what the plan and the voice say should be there,
   and measures empty and frozen frames. The final render stays locked until `check` passes, every
   scene has been reviewed since its last edit, `review.md` covers every scene, every scene uses the
   layout grid, and no unrequested screenshot is in the video.

**Voice** engines are pluggable like browser drivers (`internal/voice`). Kokoro runs locally with
no account; pick the engine, default voice and speed under Settings → Voice. See
[examples/narration.md](examples/narration.md) for `[pause]` and `{cue}` markup.

## Run

```sh
make            # builds web/, bin/smart-control and bin/smart-control-desktop
./bin/smart-control-desktop          # native window; starts the server if it isn't running
./bin/smart-control serve            # the server: MCP + tunnel + dashboard, tray icon
./bin/smart-control doctor           # checks brave, node, ffmpeg, cloudflared, hyperframes
./bin/smart-control setup            # installs Hyperframes skills (brag videos)
```

**The server runs in the background.** `smart-control serve` owns MCP, the tunnel and the
dashboard, and shows a tray icon (Open Smart Control, Open dashboard in browser, Restart
server, Quit). The desktop app is only a window onto it: it finds the server through
`~/.smart-control/daemon.json`, starts it (detached, logging to `logs/server.log`) when
nobody answers, and closing the window leaves the server and ChatGPT's connection running.
Quit from the tray to stop it. Pass `--no-tray` to `serve` to hide the icon.

**Approvals reach you wherever you are.** When the AI is waiting on a URL approval, the server
sends a desktop notification with Allow once / Deny buttons and opens a small floating
Accept / Decline window on the workspace you're on (Enter accepts, Esc declines; on Hyprland
it is floated, centred and pulled to your current workspace). Either can be turned off under
Settings → Notifications.

The tunnel is **on by default** (Cloudflare quick tunnel). The connector URL
looks like `https://….trycloudflare.com/mcp/<token>`. In ChatGPT open **Settings → Apps &
Connectors → Advanced → Developer mode → Create connector** and choose **No authentication**.
The token in the URL is the password; rotate it with `smart-control token --rotate`.

For a URL that never changes, set `tunnel.mode` to `cloudflare` (with `hostname`), `tailscale` or
`ngrok` in Settings or `~/.smart-control/config.json`. The modes work as in brag-bridge.

**Project folders** (default `~/projects`) are set in the dashboard under Settings and apply
immediately. Everything under them is kept in a **project index** in SQLite: name, kind,
description, git remote and branch, and last change. The index is rebuilt at startup,
every 15 minutes, when the folders change, from the Projects screen, and when the AI calls
`search_project` with `refresh=true`.

**Browser automation** is a driver you pick in Settings. The switch takes effect without
a restart:

| driver | how | notes |
|---|---|---|
| `cdp` (default) | chromedp over the DevTools protocol | no downloads, fastest start |
| `playwright` | playwright-go, launching Brave as a persistent Chromium context | downloads the Playwright driver (no browsers) on first use; blocks disallowed navigations before they're sent |
| `shared` | one **visible** Brave window with its own saved profile, attached over CDP on `127.0.0.1:<debug_port>` (default 9333) | you log in to sites there; the AI opens its pages as tabs in it with your sessions, then closes them. Never closed by Smart Control |

**Letting the AI use your logins.** Pick the `shared` driver, then open the AI's browser
(Settings → Open AI browser, the tray, or `smart-control browser [url]`) and log in to the
sites the AI should see. Logins persist in `~/.smart-control/browser/profiles/shared`. Your
everyday Brave profile can't be used: since Chromium 136 the browser ignores
`--remote-debugging-port` on its default profile, to protect those sessions from malware.
The window has the class `smart-control-brave`, so a Hyprland rule such as
`windowrule = workspace 9 silent, class:^(smart-control-brave)$` can keep it out of the way.
Any local program can drive a browser that has a debugging port open, so keep that profile
for the sites the AI needs.

Everything lives in `~/.smart-control/`: `config.json`, `smart-control.db` (SQLite),
`daemon.json` (running server's address and admin token), `logs/`,
`resources/` (media library), `browser/profiles/<driver>/` (log in to sites here once),
`browser/drivers/` (driver runtimes) and `brag/`. Every brag run goes in
`brag/runs/<run_id>/` (plan, composition, review, `brag.mp4`, share copy), never in your
project; the AI finds earlier runs with `list_runs` and reads them by `run_id`. Brag videos use
the product's **real logo**: during planning the AI finds it (`find_brand_assets`), copies it into
the run's `brand/` folder (`copy_from_project`, which can pull an inline `<svg>` out of a
component such as `BrandMark.tsx`; `download_asset` for websites), and `submit_plan` refuses a
plan that doesn't list it. On approval the files are copied to `composition/assets/`.

The **activity log** keeps tool calls for 30 days by default (Settings → Activity log; 0 keeps
everything). Older calls are pruned at startup, when you change the setting, and hourly.

## Safety model

- **Two HTTP surfaces.** The tunneled server only has `/mcp/<token>`, token-guarded
  `/files/<token>/…` and `/healthz`. The admin API (posts, approvals, settings) is never
  tunneled: it listens on `127.0.0.1:8788` behind an admin token that only `daemon.json`
  (0600) and the printed dashboard link carry.
- **URLs.** Rules are `allow` or `deny` per pattern (`example.com`, `*.example.com`,
  `https://site/path*`, `*`) and per capability (`browse`, `screenshot`, `download`).
  Deny wins. An unknown host pauses the tool call and pops an approval in the app with
  four choices: Allow once, Always allow host, Deny, Always deny. Redirects and final
  URLs are re-checked. Localhost URLs (`localhost`, `*.localhost`, `127.x`, `::1`) go
  through the same rules and approvals, so the AI can screenshot your dev server; turn
  this off in Settings. Smart Control's own ports are always refused, and only a URL that
  names localhost counts (a public name resolving to 127.0.0.1 doesn't). LAN and metadata
  IPs are refused, and downloads check the connected IP, which blocks DNS rebinding.
- **Acting on pages.** The interactive tools return a snapshot of the page: its buttons, links
  and fields as numbered refs, plus the text. The AI clicks, types, presses keys and picks
  options by ref. That needs the `interact` capability for the site, which an `*` allow rule
  never includes, so you're asked per site the first time; "Always allow" from that prompt
  grants interact only. Every navigation a tab makes (link click, form submit, redirect, back)
  is paused and checked against the browse rules before it's sent; a blocked one leaves the
  tab on the page it was on. The AI is told never to publish, send, buy or delete without
  your explicit request. Tabs close after 20 minutes idle (at most 6 open).
- **Files.** Reads are confined to `roots`. `.env*`, keys, `.ssh`, `.aws` and similar
  are always blocked. Brag writes only inside its run directories.
- **Posting.** The AI can only save drafts. You publish from the app: **Post to LinkedIn**
  copies the text, opens LinkedIn's composer, and shows the media to attach.

## Architecture

```
cmd/smart-control           the server (serve: MCP, tunnel, dashboard, tray) + CLI (doctor, setup, token)
cmd/smart-control-desktop   Wails v2 window: proxies /api to the server; --approvals = floating confirm window
internal/app                kernel: builds services, installs toolsets, MCP server, tunnel
internal/app/toolsets.go    ← the list of installed toolsets
internal/mcpx               Toolset interface, Registry, middleware chain, result helpers
internal/toolsets/kit       Deps container + Factory type handed to every toolset
internal/toolsets/*         project, browserkit, media, linkedin, brag
internal/activity           middleware that records every tool call (SQLite + events)
internal/events             in-process pub/sub (desktop live view, SSE)
internal/store              SQLite + per-package registered migrations
internal/posts              posts repo + Validator/Publisher per platform
internal/resources          media library + pluggable source schemes
internal/security/pathguard root confinement + secret denylist
internal/security/urlpolicy URL rules, capabilities, live approvals
internal/browser            policy-enforcing Service + Driver interface/registry
internal/browser/session    interactive tabs (snapshot refs, click/type/…, navigation guard)
internal/browser/shared     the visible, logged-in Brave window driver
internal/browser/cdp        chromedp driver          (each driver: lazy start, idle shutdown)
internal/browser/playwright playwright-go driver
internal/projects           project index (SQLite) + background refresher
internal/jobs, tunnel       background processes; cloudflare/tailscale/ngrok tunnels
internal/server             public MCP HTTP surface
internal/adminapi           local admin JSON API + SSE (docs/admin-api.md)
internal/daemon             daemon.json discovery; the desktop app finds or starts the server
internal/notify             approval notifications (freedesktop D-Bus, Allow/Deny actions) + popup
internal/tray               system tray icon (StatusNotifierItem on Linux)
web/                        React + Vite UI, embedded into both binaries
```

### Adding a tool

1. Create `internal/toolsets/<name>/<name>.go`:

   ```go
   type Toolset struct{ /* deps you need */ }

   func New(d *kit.Deps) (mcpx.Toolset, error) { return &Toolset{}, nil }
   func (t *Toolset) Name() string           { return "<name>" }

   func (t *Toolset) Register(r *mcpx.Registry) error {
       mcpx.Add(r, "my_tool", "What it does, for the model.", t.myTool)
       return nil
   }

   type myToolIn struct {
       Query string `json:"query" jsonschema:"what to look for"`
   }

   func (t *Toolset) myTool(ctx context.Context, in myToolIn) (*mcpx.Result, error) {
       return mcpx.Textf("got %s", in.Query), nil
   }
   ```

2. Append `<name>.New` to `internal/app/toolsets.go`.

The input schema is inferred from the struct. Activity logging, panic recovery
and error-to-tool-result conversion come from the middleware. A toolset can also
implement `Instructor` (adds to the server instructions), `PublicRoutes`,
`AdminRoutes` or `Closer`. New tables go in a `store.Register(store.Migration{…})`
call in the package's `init`, with a unique version per package (100s, 200s, …).
A new social platform registers a `posts.Validator` and a `posts.Publisher`.

### Adding a browser driver

Create `internal/browser/<name>/` with a type implementing `browser.Driver`
(`Visit(ctx, browser.Request) (*browser.Page, error)` and `Close()`). Call
`browser.Register(browser.DriverInfo{Name: "<name>", …}, New, probe)` in `init`, and
add a blank import to `internal/app/drivers.go`. The driver then appears in the
dashboard. The service checks the URL policy on the start URL and the final URL;
the driver must call `req.Allow(ctx, url)` for any other main-frame navigation it
sees (redirects, JS navigations) and abort if that returns an error. Run
`BROWSER_E2E=1 go test ./internal/app -run TestBrowserE2E -v` to exercise every
registered driver against real Brave.

## Develop

```sh
go test ./...                  # unit + end-to-end over an in-memory MCP client
make dev-web                   # UI hot reload against `smart-control serve` on :8788
SMART_CONTROL_HOME=/tmp/sc ./bin/smart-control serve   # a separate test instance (set other ports in its config.json)
```

Go comes from `mise` (go 1.27.3); the UI builds with `bun`. Linux desktop builds need
webkit2gtk-4.1 (the Makefile adds the `webkit2_41` tag automatically).

### Linux / Wayland rendering

WebKitGTK (GTK3) can't do fractional scaling on native Wayland. At a 1.2 monitor
scale it renders every frame at 2× and the compositor scales it down. Measured on an
RTX 3050 laptop running Hyprland at 144 Hz, that gave **~36 fps** whatever the GPU
policy or DMA-BUF setting. The desktop app therefore runs WebKit through **XWayland**
by default, which measured **~140 fps**. On Hyprland with
`xwayland:force_zero_scaling = true`, it also zooms the page by the monitor scale so
the text stays sharp and correctly sized (`cmd/smart-control-desktop/display_linux.go`).
To opt back into native Wayland, run with `SMART_CONTROL_WAYLAND=1` or `GDK_BACKEND=wayland`.

## License

[MIT](LICENSE). The bundled /brag skill in `internal/toolsets/brag/embedded/brag/` is
MIT-licensed by its author; see its own [LICENSE](internal/toolsets/brag/embedded/brag/LICENSE).
