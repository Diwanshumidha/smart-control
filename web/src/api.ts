import type {
  ActivityEntry,
  Approval,
  BragRun,
  BrowserDriver,
  IndexStatus,
  Project,
  BusEvent,
  Decision,
  Post,
  PublishResult,
  Resource,
  Settings,
  Status,
  UrlRule,
  VoiceProvider,
} from "./types";

// --- Wails runtime (present only inside the desktop app) ---

interface WailsRuntime {
  EventsOn?: (name: string, cb: (...data: any[]) => void) => () => void;
  BrowserOpenURL?: (url: string) => void;
  Quit?: () => void;
  ClipboardSetText?: (text: string) => Promise<boolean>;
}

declare global {
  interface Window {
    runtime?: WailsRuntime;
  }
}

// --- admin token (headless dashboard only) ---

const TOKEN_KEY = "smart-control.admin-token";

function initToken(): string {
  let token = "";
  try {
    const params = new URLSearchParams(window.location.search);
    const k = params.get("k");
    if (k) {
      token = k;
      try {
        localStorage.setItem(TOKEN_KEY, k);
      } catch {
        /* storage unavailable */
      }
      params.delete("k");
      const qs = params.toString();
      window.history.replaceState(null, "", window.location.pathname + (qs ? `?${qs}` : "") + window.location.hash);
    } else {
      token = localStorage.getItem(TOKEN_KEY) ?? "";
    }
  } catch {
    /* storage unavailable */
  }
  return token;
}

const token = initToken();

export class ApiError extends Error {
  constructor(
    message: string,
    public status: number,
  ) {
    super(message);
  }
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = {};
  if (token) headers.Authorization = `Bearer ${token}`;
  if (body !== undefined) headers["Content-Type"] = "application/json";
  let res: Response;
  try {
    res = await fetch(path, { method, headers, body: body === undefined ? undefined : JSON.stringify(body) });
  } catch {
    throw new ApiError("Can't reach the Smart Control server. Check that it is running.", 0);
  }
  const text = await res.text();
  let data: any = undefined;
  if (text) {
    try {
      data = JSON.parse(text);
    } catch {
      data = text;
    }
  }
  if (!res.ok) {
    const msg = (data && typeof data === "object" && data.error) || (typeof data === "string" && data) || res.statusText;
    if (res.status === 401) throw new ApiError("Not authorized. Open the dashboard link printed by smart-control (it carries ?k=).", 401);
    throw new ApiError(String(msg), res.status);
  }
  return data as T;
}

function qs(params: Record<string, string | number | undefined>): string {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== "" && v !== 0) p.set(k, String(v));
  }
  const s = p.toString();
  return s ? `?${s}` : "";
}

export function withToken(url: string): string {
  if (!token) return url;
  return url + (url.includes("?") ? "&" : "?") + "k=" + encodeURIComponent(token);
}

export const fileURL = (id: string) => withToken(`/api/resources/${encodeURIComponent(id)}/file`);

export const api = {
  status: () => request<Status>("GET", "/api/status"),
  doctor: () => request<{ report: string }>("GET", "/api/doctor"),

  activity: (p: { limit?: number; before?: number; toolset?: string } = {}) =>
    request<ActivityEntry[]>("GET", "/api/activity" + qs(p)),

  posts: (p: { status?: string; platform?: string } = {}) => request<Post[]>("GET", "/api/posts" + qs(p)),
  post: (id: string) => request<Post>("GET", `/api/posts/${id}`),
  updatePost: (id: string, patch: Partial<Pick<Post, "title" | "content" | "hashtags" | "media" | "notes" | "status">>) =>
    request<Post>("PATCH", `/api/posts/${id}`, patch),
  deletePost: (id: string) => request<void>("DELETE", `/api/posts/${id}`),
  publishPost: (id: string) => request<PublishResult>("POST", `/api/posts/${id}/publish`),
  markPosted: (id: string, external_url: string) => request<Post>("POST", `/api/posts/${id}/mark-posted`, { external_url }),

  resources: (p: { kind?: string } = {}) => request<Resource[]>("GET", "/api/resources" + qs(p)),
  resource: (id: string) => request<Resource>("GET", `/api/resources/${id}`),
  deleteResource: (id: string) => request<void>("DELETE", `/api/resources/${id}`),
  revealResource: (id: string) => request<void>("POST", `/api/resources/${id}/reveal`),
  openResource: (id: string) => request<void>("POST", `/api/resources/${id}/open`),

  rules: () => request<UrlRule[]>("GET", "/api/url/rules"),
  addRule: (r: Pick<UrlRule, "pattern" | "action" | "capabilities" | "note">) => request<UrlRule>("POST", "/api/url/rules", r),
  deleteRule: (id: string) => request<void>("DELETE", `/api/url/rules/${id}`),
  approvals: (status?: string) => request<Approval[]>("GET", "/api/url/approvals" + qs({ status })),
  decide: (id: string, decision: Decision) => request<Approval>("POST", `/api/url/approvals/${id}`, { decision }),

  bragRuns: () => request<BragRun[]>("GET", "/api/brag/runs"),
  openBragFile: (run: string, path: string) =>
    request<void>("POST", `/api/brag/runs/${encodeURIComponent(run)}/open/${path.split("/").map(encodeURIComponent).join("/")}`),

  restartServer: () => request<{ ok: boolean }>("POST", "/api/server/restart"),
  /** Desktop app only: starts the background server if it isn't running. */
  startServer: () => request<{ ok: boolean; pid: number }>("POST", "/api/desktop/server/start"),
  desktop: () => request<{ mode: "main" | "approvals"; version: string; connected: boolean }>("GET", "/api/desktop"),

  settings: () => request<Settings>("GET", "/api/settings"),
  saveSettings: (s: Settings) => request<{ ok?: boolean; restart_required?: boolean }>("PUT", "/api/settings", s),

  projects: (p: { q?: string; limit?: number } = {}) => request<Project[]>("GET", "/api/projects" + qs(p)),
  projectIndex: () => request<IndexStatus>("GET", "/api/projects/index"),
  reindex: () => request<IndexStatus>("POST", "/api/projects/reindex"),

  openBrowser: (url?: string) => request<{ ok: boolean }>("POST", "/api/browser/open", url ? { url } : {}),
  voiceProviders: () => request<{ providers: VoiceProvider[] }>("GET", "/api/voice/providers"),
  browserDrivers: () => request<{ active: string; drivers: BrowserDriver[] }>("GET", "/api/browser/drivers"),
};

// --- live events ---

type Listener = (ev: BusEvent) => void;
const listeners = new Set<Listener>();
let started = false;
const connectionListeners = new Set<(live: boolean) => void>();
let live = false;

function setLive(v: boolean) {
  if (live === v) return;
  live = v;
  connectionListeners.forEach((l) => l(v));
}

function dispatch(ev: BusEvent) {
  listeners.forEach((l) => {
    try {
      l(ev);
    } catch (e) {
      console.error(e);
    }
  });
}

function startEvents() {
  if (started) return;
  started = true;
  if (window.runtime?.EventsOn) {
    // The desktop app relays the background server's events; "connection"
    // says whether it can reach the server.
    window.runtime.EventsOn("event", (ev: BusEvent) => dispatch(ev));
    window.runtime.EventsOn("connection", (v: boolean) => setLive(!!v));
    // The relay may have connected before this page subscribed.
    api
      .desktop()
      .then((d) => setLive(!!d.connected))
      .catch(() => {});
    return;
  }
  let delay = 1000;
  const connect = () => {
    const es = new EventSource(withToken("/api/events"));
    es.onopen = () => {
      delay = 1000;
      setLive(true);
    };
    es.onmessage = (m) => {
      try {
        dispatch(JSON.parse(m.data));
      } catch {
        /* ignore malformed */
      }
    };
    es.onerror = () => {
      es.close();
      setLive(false);
      setTimeout(connect, delay);
      delay = Math.min(delay * 2, 15000);
    };
  };
  connect();
}

export function subscribe(l: Listener): () => void {
  startEvents();
  listeners.add(l);
  return () => listeners.delete(l);
}

/** Runs l when the desktop app asks the window to re-check its data. */
export function onDesktopEvent(name: string, l: () => void): () => void {
  return window.runtime?.EventsOn?.(name, l) ?? (() => {});
}

export function onConnection(l: (live: boolean) => void): () => void {
  startEvents();
  connectionListeners.add(l);
  l(live);
  return () => connectionListeners.delete(l);
}

// --- desktop integrations with browser fallbacks ---

export function openExternal(url: string) {
  if (window.runtime?.BrowserOpenURL) window.runtime.BrowserOpenURL(url);
  else window.open(url, "_blank", "noopener,noreferrer");
}

export async function copyText(text: string): Promise<void> {
  if (window.runtime?.ClipboardSetText) {
    await window.runtime.ClipboardSetText(text);
    return;
  }
  await navigator.clipboard.writeText(text);
}

// --- inline media ---
// In the desktop app (WebKitGTK on Linux) creating a <video>/<audio> element
// aborts the web process when GStreamer plugins are missing, freezing the
// window. The backend reports support in status.media_playback; until it has
// answered, the desktop app assumes "no" and shows posters + "Open in player".
// Regular browsers (headless dashboard) always play inline.
let desktopMediaOK = false;
const mediaListeners = new Set<() => void>();

export function isDesktop(): boolean {
  return !!window.runtime || location.protocol === "wails:";
}

export function setInlineMedia(supported: boolean) {
  if (supported === desktopMediaOK) return;
  desktopMediaOK = supported;
  mediaListeners.forEach((l) => l());
}

export function canPlayInline(): boolean {
  return !isDesktop() || desktopMediaOK;
}

export function subscribeInlineMedia(l: () => void): () => void {
  mediaListeners.add(l);
  return () => mediaListeners.delete(l);
}
