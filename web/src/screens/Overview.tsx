import { useMemo, useState } from "react";
import { Eye, EyeOff, RefreshCw, Stethoscope } from "lucide-react";
import { api } from "../api";
import type { ScreenId } from "../App";
import type { Status, ToolInfo } from "../types";
import { CopyButton, Empty, Header, Spinner, timeAgo, useAction } from "../ui";

function mask(url: string): string {
  return url.replace(/\/mcp\/([^/?#]+)/, (_, t: string) => `/mcp/${t.slice(0, 4)}${"•".repeat(Math.max(4, Math.min(20, t.length - 4)))}`);
}

export function Overview({
  status,
  reloadStatus,
  pendingPosts,
  pendingApprovals,
  go,
}: {
  status?: Status;
  reloadStatus: () => void;
  pendingPosts: number;
  pendingApprovals: number;
  go: (s: ScreenId) => void;
}) {
  const [reveal, setReveal] = useState(false);
  const [doctor, setDoctor] = useState<string>("");
  const [doctoring, setDoctoring] = useState(false);
  const act = useAction();

  const groups = useMemo(() => {
    const m = new Map<string, ToolInfo[]>();
    for (const t of status?.tools ?? []) {
      if (!m.has(t.toolset)) m.set(t.toolset, []);
      m.get(t.toolset)!.push(t);
    }
    return [...m.entries()];
  }, [status]);

  if (!status) {
    return (
      <div className="page">
        <Header title="Overview" />
        <Empty title="Waiting for the server">
          Smart Control's core isn't answering yet. If this persists, start it with <code>smart-control serve</code>.
        </Empty>
      </div>
    );
  }

  const t = status.tunnel;
  const connector = status.connector_url;

  return (
    <div className="page">
      <Header title="Overview" sub="Connect ChatGPT once, then watch and approve its work here.">
        <button className="btn btn-quiet" onClick={reloadStatus}>
          <RefreshCw size={14} /> Refresh
        </button>
      </Header>

      <section className="connector">
        <div className="connector-head">
          <h2>ChatGPT connector URL</h2>
          <span className={`state state-${t.state}`}>
            {t.state === "up" ? "Reachable" : t.state === "starting" ? <><Spinner /> Starting tunnel</> : t.state === "error" ? "Tunnel failed" : "No tunnel"}
          </span>
        </div>
        {connector ? (
          <>
            <div className="connector-url">
              <code>{reveal ? connector : mask(connector)}</code>
              <button className="icon-btn" aria-label={reveal ? "Hide token" : "Show token"} onClick={() => setReveal((r) => !r)}>
                {reveal ? <EyeOff size={15} /> : <Eye size={15} />}
              </button>
              <CopyButton text={connector} />
            </div>
            <p className="hint">
              In ChatGPT: Settings → Apps &amp; Connectors → Advanced → Developer mode → Create connector, with no authentication. The token in the URL is the password, so keep it private.
            </p>
          </>
        ) : (
          <p className="hint">
            {t.state === "error"
              ? `The ${t.mode} tunnel didn't come up: ${t.error || "unknown error"}. Check the tunnel settings, then restart.`
              : t.state === "off"
                ? "The tunnel is off, so ChatGPT can't reach this machine. Turn it on in Settings."
                : "The connector URL appears here once the tunnel is up."}
          </p>
        )}
      </section>

      <section className="stat-row">
        <button className="stat" onClick={() => go("posts")}>
          <span className="stat-num">{pendingPosts}</span>
          <span className="stat-label">{pendingPosts === 1 ? "post needs review" : "posts need review"}</span>
        </button>
        <button className={`stat ${pendingApprovals > 0 ? "stat-urgent" : ""}`} onClick={() => go("access")}>
          <span className="stat-num">{pendingApprovals}</span>
          <span className="stat-label">{pendingApprovals === 1 ? "URL waiting for you" : "URLs waiting for you"}</span>
        </button>
        <button className="stat" onClick={() => go("activity")}>
          <span className="stat-num">{status.tools.length}</span>
          <span className="stat-label">tools available to the AI</span>
        </button>
      </section>

      <section className="facts">
        <dl>
          <dt>Local MCP URL</dt>
          <dd>
            <code>{reveal ? status.mcp_local_url : mask(status.mcp_local_url)}</code>
          </dd>
          <dt>Public URL</dt>
          <dd>{status.public_url ? <code>{status.public_url}</code> : <span className="muted">none yet</span>}</dd>
          <dt>Project folders</dt>
          <dd>
            {status.roots.length ? (
              <ul className="plain-list">
                {status.roots.map((r) => (
                  <li key={r}>
                    <code>{r}</code>
                  </li>
                ))}
              </ul>
            ) : (
              <span className="muted">none, add one in Settings</span>
            )}
          </dd>
          <dt>Projects indexed</dt>
          <dd>
            {status.project_index ? (
              <button className="link" onClick={() => go("projects")}>
                {status.project_index.running ? "indexing…" : `${status.project_index.count} ${status.project_index.count === 1 ? "project" : "projects"}`}
                {status.project_index.last_run_at && !status.project_index.running ? `, ${timeAgo(status.project_index.last_run_at)}` : ""}
              </button>
            ) : (
              <span className="muted">unknown</span>
            )}
            {status.project_index?.error && <span className="warn-text"> · {status.project_index.error}</span>}
          </dd>
          <dt>Browser driver</dt>
          <dd>{status.browser_driver ? <code>{status.browser_driver}</code> : <span className="muted">unknown</span>}</dd>
          <dt>Brave</dt>
          <dd>{status.brave_path ? <code>{status.brave_path}</code> : <span className="warn-text">not found, browser tools won't work</span>}</dd>
          <dt>Data folder</dt>
          <dd>
            <code>{status.data_dir}</code>
          </dd>
        </dl>
      </section>

      <section className="section">
        <div className="section-head">
          <h2>Tools</h2>
          <button
            className="btn btn-quiet"
            disabled={doctoring}
            onClick={async () => {
              setDoctoring(true);
              const r = await act(() => api.doctor());
              setDoctoring(false);
              if (r) setDoctor(r.report);
            }}
          >
            {doctoring ? <Spinner /> : <Stethoscope size={14} />} Check dependencies
          </button>
        </div>
        {doctor && <pre className="doctor">{doctor}</pre>}
        <div className="toolsets">
          {groups.map(([ts, tools]) => (
            <div key={ts} className="toolset">
              <h3>
                {ts} <span className="muted">{tools.length}</span>
              </h3>
              <ul>
                {tools.map((tool) => (
                  <li key={tool.name}>
                    <code className="tool-name">{tool.name}</code>
                    <span className="tool-desc">{tool.description}</span>
                  </li>
                ))}
              </ul>
            </div>
          ))}
        </div>
      </section>
    </div>
  );
}
