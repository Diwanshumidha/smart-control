# Admin API (local only)

The desktop app (and the headless dashboard) talk to the core through this
JSON API. It is **never** exposed through the tunnel. The server
(`smart-control serve`) listens on `admin_addr` (default `127.0.0.1:8788`) and requires
`Authorization: Bearer <admin token>` or `?k=<admin token>` (the dashboard URL printed at
startup carries it, the UI stores it in localStorage and sends it as a header).

While the server runs it writes `<data>/daemon.json` (0600) with `pid`, `version`,
`admin_url` and `admin_token`. The desktop app reads it, proxies the UI's `/api/` calls with
the token attached, and relays `/api/events` to the window. It also answers
`GET /api/desktop` → `{"mode":"main"|"approvals","version"}` itself; the browser dashboard
has no such route.

All timestamps are unix **milliseconds**. Errors: `{"error": "message"}` with a 4xx/5xx status.

## Status

`GET /api/status`
```json
{
  "version": "dev",
  "mcp_local_url": "http://127.0.0.1:8787/mcp/<token>",
  "public_url": "https://x.trycloudflare.com",        // "" while tunnel is starting/off
  "connector_url": "https://x.trycloudflare.com/mcp/<token>", // "" when no public url
  "tunnel": {"mode": "quick", "state": "starting|up|error|off", "error": ""},
  "roots": ["/home/u/projects"],
  "brave_path": "/usr/bin/brave",
  "browser_driver": "cdp",
  "project_index": {"count": 42, "running": false, "last_run_at": 0, "duration_ms": 0, "error": ""},
  "data_dir": "/home/u/.smart-control",
  "tools": [{"toolset": "project", "name": "file_tree", "description": "..."}]
}
```

`GET /api/doctor` → `{"report": "plain text"}`

## Live events

`GET /api/events` — Server-Sent Events. Each message: `data: {"topic": "...", "at": "RFC3339", "data": {...}}`.
In the Wails desktop app the same events are also emitted as Wails runtime events named `"event"`
(`window.runtime.EventsOn("event", cb)`) — prefer that when `window.runtime` exists, since
Wails asset handlers may buffer SSE.

Topics: `tool.started`, `tool.finished` (data = activity entry), `post.changed` (data = post),
`resource.added` (data = resource), `url.approval_requested` (data = approval),
`url.approval_decided`, `server.status` (data = status object), `brag.run`.

## Activity (what the AI is doing)

`GET /api/activity?limit=100&before=<started_at>&toolset=<name>`
```json
[{"id":"call_..","toolset":"project","tool":"file_tree","args":{...},"status":"running|ok|error",
  "result":"text preview","started_at":0,"finished_at":0,"duration_ms":0}]
```

## Posts

- `GET /api/posts?status=&platform=` → `[Post]`
- `GET /api/posts/{id}` → `Post`
- `PATCH /api/posts/{id}` body any of `{"title","content","hashtags":[],"media":[],"notes","status"}` → `Post`
- `DELETE /api/posts/{id}`
- `POST /api/posts/{id}/publish` → `{"share_url": "https://www.linkedin.com/feed/?shareActive=true&text=...", "text": "full text incl. hashtags", "media": [Resource]}`
  The UI copies `text` to the clipboard, opens `share_url` in the system browser
  (Wails: `window.runtime.BrowserOpenURL`), and shows the media files to attach manually.
- `POST /api/posts/{id}/mark-posted` body `{"external_url": ""}` → `Post` (status → posted)

Post:
```json
{"id":"post_..","platform":"linkedin","title":"","content":"","media":["res_.."],"hashtags":["go"],
 "notes":"","status":"pending_review|approved|posted|discarded","external_url":"",
 "created_at":0,"updated_at":0,"posted_at":0}
```

## Resources (media library)

- `GET /api/resources?kind=image|video|...` → `[Resource]`
- `GET /api/resources/{id}` → `Resource`
- `GET /api/resources/{id}/file` → the raw file (use as `<img src>` / `<video src>`; headless: append `?k=`)
- `DELETE /api/resources/{id}`
- `POST /api/resources/{id}/reveal` → opens the containing folder in the OS file manager

Resource:
```json
{"id":"res_..","kind":"image|video|audio|document|other","name":"","path":"/abs/path","mime":"image/png",
 "size":0,"sha256":"","origin":"url|screenshot|brag|project","source":"","note":"","meta":{},"created_at":0}
```

## URL access

- `GET /api/url/rules` → `[{"id","pattern","action":"allow|deny","capabilities":["*"|"browse"|"screenshot"|"download"],"note","created_at"}]`
- `POST /api/url/rules` body `{"pattern","action","capabilities":[],"note"}` → rule
- `DELETE /api/url/rules/{id}`
- `GET /api/url/approvals?status=pending` → `[{"id","url","host","capability","reason","status","decision","created_at","decided_at"}]`
- `POST /api/url/approvals/{id}` body `{"decision":"allow_once|allow_host|deny|deny_host"}` → approval

Pattern syntax: `example.com`, `*.example.com`, `https://example.com/path*`, `*`.

## Projects (index of everything under the project folders)

- `GET /api/projects?q=&limit=` → `[{"path","name","root","kinds":["go","git"],"description","git_remote","git_branch","has_readme","modified_at","indexed_at"}]`
  (ranked by match when `q` is set, else most recently changed first)
- `GET /api/projects/index` → `{"count","running","last_run_at","duration_ms","error"}`
- `GET /api/voice/providers` → `{"providers":[{"name","title","description","default_voice","voices":[{"id","name","lang","note"}],"available","detail"}]}` (text-to-speech for brag voiceovers)
- `POST /api/projects/reindex` → index status (runs synchronously)
- Event `projects.indexed` (data = index status) after every reindex. The index refreshes
  at startup, every 15 min, and whenever project folders change.

## Browser drivers

- `GET /api/browser/drivers` → `{"active":"cdp","drivers":[{"name":"cdp","title","description","available":true,"detail"},{"name":"playwright",...}]}`
  The driver is chosen with `settings.browser.driver` and switches without restart.

## Settings

- `GET /api/settings` → `{"roots":[], "tunnel":{"mode","hostname","name","public_url"}, "browser":{"driver","path","headless","width","height"}, "url_policy":{"approval_timeout_sec","allow_localhost","allow_private_networks"}, "activity":{"retention_days"}, "notify":{"desktop","popup"}, "voice":{"provider","voice","speed"}}`
- `PUT /api/settings` same shape → `{"ok":true,"restart_required":bool}`. Project folders ("roots",
  e.g. `~/projects`), browser driver/options and URL policy apply immediately (folders trigger a
  reindex); only a tunnel change needs `restart_required`. 400 with `{"error"}` for invalid
  folders/driver. `activity.retention_days` (0 keeps everything) prunes right away when changed,
  then hourly.
- `POST /api/server/restart` → `{"ok":true}`, then the server re-executes itself (same PID and
  admin token on Unix) to apply tunnel changes. 501 when the host can't restart itself.
  `status.can_restart` says which.

## Toolset routes

Toolsets may add routes under `/api/<toolset>/...`. Brag: `GET /api/brag/runs` →
`[{"id","dir","project","url","created","stage","plan_url","video":"rel path or ''"}]`.
