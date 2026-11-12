import { useEffect, useState } from "react";
import { RefreshCw, Search } from "lucide-react";
import { api, subscribe } from "../api";
import type { IndexStatus, Project } from "../types";
import { CopyButton, duration, Empty, ErrorNote, Header, Spinner, timeAgo, useAction, useTick } from "../ui";

export function Projects() {
  const [q, setQ] = useState("");
  const [query, setQuery] = useState("");
  const [items, setItems] = useState<Project[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [index, setIndex] = useState<IndexStatus | undefined>();
  const [reindexing, setReindexing] = useState(false);
  const act = useAction();
  useTick(30000);

  // Debounce the search box.
  useEffect(() => {
    const t = setTimeout(() => setQuery(q.trim()), 250);
    return () => clearTimeout(t);
  }, [q]);

  const load = async (search: string) => {
    setLoading(true);
    try {
      setItems(await api.projects({ q: search, limit: 200 }));
      setError("");
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    load(query);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query]);

  useEffect(() => {
    api.projectIndex().then(setIndex).catch(() => {});
  }, []);

  useEffect(
    () =>
      subscribe((ev) => {
        if (ev.topic !== "projects.indexed") return;
        if (ev.data && typeof ev.data === "object") setIndex(ev.data as IndexStatus);
        load(query);
      }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [query],
  );

  const reindex = async () => {
    setReindexing(true);
    setIndex((i) => (i ? { ...i, running: true } : i));
    const r = await act(() => api.reindex(), "Projects reindexed");
    setReindexing(false);
    if (r) {
      setIndex(r);
      load(query);
    } else {
      api.projectIndex().then(setIndex).catch(() => {});
    }
  };

  const running = reindexing || index?.running;

  return (
    <div className="page page-wide">
      <Header
        title="Projects"
        sub={
          index ? (
            <span className="index-line">
              {running && <Spinner />}
              {running
                ? "Indexing your project folders…"
                : `${index.count} ${index.count === 1 ? "project" : "projects"} indexed${index.last_run_at ? ` ${timeAgo(index.last_run_at)}` : ""}${index.duration_ms ? ` in ${duration(index.duration_ms)}` : ""}`}
              {index.error && <span className="warn-text"> · last run failed: {index.error}</span>}
            </span>
          ) : (
            "Everything the AI can find in your project folders."
          )
        }
      >
        <button className="btn" disabled={!!running} onClick={reindex}>
          {running ? <Spinner /> : <RefreshCw size={14} />} Reindex now
        </button>
      </Header>

      <div className="search">
        <Search size={15} className="search-icon" />
        <input className="input search-input" placeholder="Search by name, language, description or remote" value={q} onChange={(e) => setQ(e.target.value)} aria-label="Search projects" autoFocus />
        {loading && <Spinner />}
      </div>

      <ErrorNote error={error} onRetry={() => load(query)} />

      {!loading && items.length === 0 && !error ? (
        query ? (
          <Empty title={`No projects match “${query}”`}>Try a shorter name, or reindex if you just created it.</Empty>
        ) : (
          <Empty title="No projects indexed yet">Add your project folders in Settings, then reindex.</Empty>
        )
      ) : (
        <table className="table projects">
          <thead>
            <tr>
              <th>Project</th>
              <th>Path</th>
              <th>Git</th>
              <th>Changed</th>
            </tr>
          </thead>
          <tbody>
            {items.map((p) => (
              <tr key={p.path}>
                <td className="proj-main">
                  <div className="proj-name">
                    {p.name}
                    {p.kinds.map((k) => (
                      <span key={k} className="chip">
                        {k}
                      </span>
                    ))}
                  </div>
                  {p.description && <div className="proj-desc">{p.description}</div>}
                </td>
                <td className="proj-path">
                  <code title={p.path}>{p.path}</code>
                  <CopyButton text={p.path} label="" />
                </td>
                <td className="proj-git">
                  {p.git_remote || p.git_branch ? (
                    <>
                      {p.git_branch && <span className="chip">{p.git_branch}</span>}
                      {p.git_remote && (
                        <span className="muted small proj-remote" title={p.git_remote}>
                          {p.git_remote.replace(/^(https?:\/\/|git@)/, "").replace(/\.git$/, "")}
                        </span>
                      )}
                    </>
                  ) : (
                    <span className="muted">—</span>
                  )}
                </td>
                <td className="muted nowrap" title={p.modified_at ? new Date(p.modified_at).toLocaleString() : ""}>
                  {timeAgo(p.modified_at)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
