import { useCallback, useEffect, useMemo, useState } from "react";
import { ChevronRight, Pause, Play } from "lucide-react";
import { api, subscribe } from "../api";
import type { ActivityEntry, ToolInfo } from "../types";
import { clock, duration, Empty, ErrorNote, Header, Spinner, timeAgo, useTick } from "../ui";

const PAGE = 100;

/** One-line summary of the most telling argument, so the feed reads without expanding. */
function argSummary(args: unknown): string {
  if (!args || typeof args !== "object") return "";
  const a = args as Record<string, unknown>;
  for (const k of ["url", "path", "query", "pattern", "project", "source", "run_id", "title", "topic", "name"]) {
    const v = a[k];
    if (typeof v === "string" && v) return v;
  }
  for (const v of Object.values(a)) {
    if (typeof v === "string" && v) return v.length > 90 ? v.slice(0, 90) + "…" : v;
  }
  return "";
}

export function ActivityScreen({ tools }: { tools: ToolInfo[] }) {
  const [items, setItems] = useState<ActivityEntry[]>([]);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [more, setMore] = useState(true);
  const [toolset, setToolset] = useState("");
  const [paused, setPaused] = useState(false);
  const [queued, setQueued] = useState<ActivityEntry[]>([]);
  const [open, setOpen] = useState<Set<string>>(new Set());
  useTick(10000);

  const toolsets = useMemo(() => [...new Set(tools.map((t) => t.toolset))].sort(), [tools]);

  const load = useCallback(
    async (before?: number) => {
      setLoading(true);
      try {
        const page = await api.activity({ limit: PAGE, before, toolset });
        setItems((cur) => (before ? [...cur, ...page] : page));
        setMore(page.length === PAGE);
        setError("");
      } catch (e) {
        setError(e instanceof Error ? e.message : String(e));
      } finally {
        setLoading(false);
      }
    },
    [toolset],
  );

  useEffect(() => {
    setQueued([]);
    load();
  }, [load]);

  const merge = useCallback((list: ActivityEntry[], e: ActivityEntry) => {
    const i = list.findIndex((x) => x.id === e.id);
    if (i >= 0) {
      const next = list.slice();
      next[i] = e;
      return next;
    }
    return [e, ...list];
  }, []);

  useEffect(
    () =>
      subscribe((ev) => {
        if (ev.topic !== "tool.started" && ev.topic !== "tool.finished") return;
        const e = ev.data as ActivityEntry;
        if (toolset && e.toolset !== toolset) return;
        if (paused) {
          // Updates to calls already on screen still apply; new calls wait.
          setItems((cur) => (cur.some((x) => x.id === e.id) ? merge(cur, e) : cur));
          setQueued((q) => (items.some((x) => x.id === e.id) ? q : merge(q, e)));
          return;
        }
        setItems((cur) => merge(cur, e));
      }),
    [toolset, paused, merge, items],
  );

  const resume = () => {
    setItems((cur) => queued.reduceRight((acc, e) => merge(acc, e), cur));
    setQueued([]);
    setPaused(false);
  };

  const toggle = (id: string) =>
    setOpen((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });

  const running = items.filter((i) => i.status === "running").length;

  // Group consecutive entries by day for orientation in long histories.
  let lastDay = "";

  return (
    <div className="page page-wide">
      <Header
        title="Activity"
        sub={
          running > 0 ? (
            <span className="live-line">
              <Spinner /> {running} tool {running === 1 ? "call" : "calls"} running now
            </span>
          ) : (
            "Every tool call the AI makes, as it happens."
          )
        }
      >
        <select className="input input-sm" value={toolset} onChange={(e) => setToolset(e.target.value)} aria-label="Filter by toolset">
          <option value="">All toolsets</option>
          {toolsets.map((t) => (
            <option key={t} value={t}>
              {t}
            </option>
          ))}
        </select>
        {paused ? (
          <button className="btn" onClick={resume}>
            <Play size={14} /> Resume{queued.length > 0 ? ` (${queued.length} new)` : ""}
          </button>
        ) : (
          <button className="btn btn-quiet" onClick={() => setPaused(true)}>
            <Pause size={14} /> Pause feed
          </button>
        )}
      </Header>

      <ErrorNote error={error} onRetry={() => load()} />

      {!loading && items.length === 0 && !error ? (
        <Empty title="No tool calls yet">Ask ChatGPT to use Smart Control and its calls will stream in here.</Empty>
      ) : (
        <ol className="timeline">
          {items.map((e) => {
            const day = new Date(e.started_at).toDateString();
            const showDay = day !== lastDay;
            lastDay = day;
            const isOpen = open.has(e.id);
            const summary = argSummary(e.args);
            return (
              <li key={e.id} className={`tl-item tl-${e.status}`}>
                {showDay && <div className="tl-day">{day === new Date().toDateString() ? "Today" : day}</div>}
                <div className="tl-row">
                  <span className="tl-node" aria-hidden>
                    {e.status === "running" ? <Spinner size={10} /> : null}
                  </span>
                  <button className="tl-main" onClick={() => toggle(e.id)} aria-expanded={isOpen}>
                    <ChevronRight size={14} className={`tl-chevron ${isOpen ? "tl-chevron-open" : ""}`} />
                    <span className="tl-time" title={new Date(e.started_at).toLocaleString()}>
                      {clock(e.started_at)}
                    </span>
                    <span className="chip">{e.toolset}</span>
                    <span className="tl-tool">{e.tool}</span>
                    <span className="tl-summary">{summary}</span>
                    <span className="tl-meta">
                      {e.status === "running" ? (
                        <span className="tl-running">running · {timeAgo(e.started_at)}</span>
                      ) : (
                        <>
                          {e.status === "error" && <span className="tl-err">failed</span>}
                          <span className="tl-dur">{duration(e.duration_ms)}</span>
                        </>
                      )}
                    </span>
                  </button>
                </div>
                {isOpen && (
                  <div className="tl-detail">
                    <div>
                      <h4>Arguments</h4>
                      <pre>{JSON.stringify(e.args, null, 2)}</pre>
                    </div>
                    <div>
                      <h4>{e.status === "error" ? "Error" : "Result"}</h4>
                      <pre className={e.status === "error" ? "pre-error" : ""}>{e.status === "running" ? "Still running…" : e.result || "(empty)"}</pre>
                    </div>
                  </div>
                )}
              </li>
            );
          })}
        </ol>
      )}

      {items.length > 0 && more && (
        <div className="load-more">
          <button className="btn btn-quiet" disabled={loading} onClick={() => load(items[items.length - 1].started_at)}>
            {loading ? <Spinner /> : null} Load older calls
          </button>
        </div>
      )}
    </div>
  );
}
