import { useCallback, useEffect, useState } from "react";
import { Activity, Clapperboard, FileText, FolderGit2, Gauge, Image as ImageIcon, Settings as SettingsIcon, ShieldCheck } from "lucide-react";
import { api, onConnection, subscribe, setInlineMedia } from "./api";
import type { Approval, Decision, Status } from "./types";
import { useAction } from "./ui";
import { ApprovalBanner } from "./ApprovalBanner";
import { ServerControl } from "./ServerControl";
import { Overview } from "./screens/Overview";
import { Projects } from "./screens/Projects";
import { ActivityScreen } from "./screens/Activity";
import { Posts } from "./screens/Posts";
import { Media } from "./screens/Media";
import { UrlAccess } from "./screens/UrlAccess";
import { Brag } from "./screens/Brag";
import { SettingsScreen } from "./screens/Settings";

export type ScreenId = "overview" | "projects" | "activity" | "posts" | "media" | "access" | "brag" | "settings";

const NAV: { id: ScreenId; label: string; icon: typeof Gauge }[] = [
  { id: "overview", label: "Overview", icon: Gauge },
  { id: "projects", label: "Projects", icon: FolderGit2 },
  { id: "activity", label: "Activity", icon: Activity },
  { id: "posts", label: "Posts", icon: FileText },
  { id: "media", label: "Media", icon: ImageIcon },
  { id: "access", label: "URL access", icon: ShieldCheck },
  { id: "brag", label: "Brag videos", icon: Clapperboard },
  { id: "settings", label: "Settings", icon: SettingsIcon },
];

const SCREEN_KEY = "smart-control.screen";

function initialScreen(): ScreenId {
  try {
    const s = localStorage.getItem(SCREEN_KEY) as ScreenId | null;
    if (s && NAV.some((n) => n.id === s)) return s;
  } catch {
    /* storage unavailable */
  }
  return "overview";
}

export interface ApprovalState {
  pending: Approval[];
  timeoutSec: number;
  decide: (a: Approval, d: Decision) => Promise<void>;
}

export default function App() {
  const [screen, setScreenState] = useState<ScreenId>(initialScreen);
  const [status, setStatus] = useState<Status | undefined>();
  const [live, setLive] = useState(false);
  const [pending, setPending] = useState<Approval[]>([]);
  const [timeoutSec, setTimeoutSec] = useState(90);
  const [pendingPosts, setPendingPosts] = useState(0);
  const [runningCalls, setRunningCalls] = useState(0);
  const act = useAction();

  const setScreen = (s: ScreenId) => {
    setScreenState(s);
    try {
      localStorage.setItem(SCREEN_KEY, s);
    } catch {
      /* storage unavailable */
    }
  };

  useEffect(() => {
    if (status?.media_playback) setInlineMedia(status.media_playback.supported);
  }, [status]);

  const loadStatus = useCallback(() => {
    api.status().then(setStatus).catch(() => {});
  }, []);
  const loadPending = useCallback(() => {
    api.approvals("pending").then(setPending).catch(() => {});
  }, []);
  const loadPostCount = useCallback(() => {
    api
      .posts({ status: "pending_review" })
      .then((p) => setPendingPosts(p.length))
      .catch(() => {});
  }, []);

  useEffect(() => {
    loadStatus();
    loadPending();
    loadPostCount();
    api
      .settings()
      .then((s) => s.url_policy?.approval_timeout_sec && setTimeoutSec(s.url_policy.approval_timeout_sec))
      .catch(() => {});
  }, [loadStatus, loadPending, loadPostCount]);

  // After a reconnect (e.g. the server restarted) refetch what may have changed.
  useEffect(
    () =>
      onConnection((v) => {
        setLive(v);
        if (v) {
          loadStatus();
          loadPending();
        }
      }),
    [loadStatus, loadPending],
  );

  useEffect(
    () =>
      subscribe((ev) => {
        switch (ev.topic) {
          case "server.status":
            if (ev.data && typeof ev.data === "object" && "tunnel" in ev.data) setStatus(ev.data as Status);
            else loadStatus();
            break;
          case "projects.indexed":
            if (ev.data && typeof ev.data === "object") setStatus((s) => (s ? { ...s, project_index: ev.data } : s));
            break;
          case "url.approval_requested":
            setPending((p) => (p.some((x) => x.id === ev.data.id) ? p : [ev.data as Approval, ...p]));
            break;
          case "url.approval_decided":
            setPending((p) => p.filter((x) => x.id !== ev.data?.id));
            break;
          case "post.changed":
            loadPostCount();
            break;
          case "tool.started":
            setRunningCalls((n) => n + 1);
            break;
          case "tool.finished":
            setRunningCalls((n) => Math.max(0, n - 1));
            break;
        }
      }),
    [loadStatus, loadPostCount],
  );

  const decide = useCallback(
    async (a: Approval, d: Decision) => {
      const r = await act(() => api.decide(a.id, d));
      if (r) setPending((p) => p.filter((x) => x.id !== a.id));
      else loadPending();
    },
    [act, loadPending],
  );

  const approvals: ApprovalState = { pending, timeoutSec, decide };

  const badges: Partial<Record<ScreenId, number>> = {
    posts: pendingPosts,
    access: pending.length,
  };

  return (
    <div className="shell">
      <nav className="sidebar" aria-label="Main">
        <div className="brand">
          <span className={`tally ${live ? "tally-on" : ""}`} aria-hidden />
          <div>
            <div className="brand-name">Smart Control</div>
            <div className="brand-sub">{status ? `v${status.version}` : "connecting…"}</div>
          </div>
        </div>
        <ul className="nav">
          {NAV.map((n) => {
            const Icon = n.icon;
            const count = badges[n.id] ?? 0;
            return (
              <li key={n.id}>
                <button className={`nav-item ${screen === n.id ? "nav-active" : ""}`} onClick={() => setScreen(n.id)} aria-current={screen === n.id ? "page" : undefined}>
                  <Icon size={16} />
                  <span>{n.label}</span>
                  {n.id === "activity" && runningCalls > 0 && <span className="nav-live" title={`${runningCalls} running`} />}
                  {count > 0 && <span className={`nav-count ${n.id === "access" ? "nav-count-urgent" : ""}`}>{count}</span>}
                </button>
              </li>
            );
          })}
        </ul>
        <div className="sidebar-foot">
          <TunnelPill status={status} live={live} />
          <ServerControl live={live} canRestart={status?.can_restart ?? true} />
        </div>
      </nav>

      <main className="main">
        {screen !== "access" && <ApprovalBanner approvals={approvals} onOpen={() => setScreen("access")} />}
        {screen === "overview" && <Overview status={status} reloadStatus={loadStatus} pendingPosts={pendingPosts} pendingApprovals={pending.length} go={setScreen} />}
        {screen === "projects" && <Projects />}
        {screen === "activity" && <ActivityScreen tools={status?.tools ?? []} />}
        {screen === "posts" && <Posts />}
        {screen === "media" && <Media />}
        {screen === "access" && <UrlAccess approvals={approvals} />}
        {screen === "brag" && <Brag />}
        {screen === "settings" && <SettingsScreen onSaved={loadStatus} />}
      </main>
    </div>
  );
}

function TunnelPill({ status, live }: { status?: Status; live: boolean }) {
  if (!live) return <div className="pill pill-error">Server unreachable</div>;
  if (!status) return <div className="pill">Connecting…</div>;
  const t = status.tunnel;
  const label =
    t.state === "up" ? "Tunnel up" : t.state === "starting" ? "Tunnel starting" : t.state === "error" ? "Tunnel failed" : "Tunnel off";
  return (
    <div className={`pill pill-${t.state}`} title={t.error || status.public_url || ""}>
      <span className="pill-dot" />
      {label}
      <span className="pill-mode">{t.mode}</span>
    </div>
  );
}
