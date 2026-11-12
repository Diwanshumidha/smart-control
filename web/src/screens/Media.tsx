import { useEffect, useState } from "react";
import { ExternalLink, FileIcon, FolderOpen, Music, Trash2, X } from "lucide-react";
import { api, fileURL, openExternal, subscribe } from "../api";
import type { Resource } from "../types";
import { bytes, CopyButton, Empty, ErrorNote, Header, timeAgo, useAction, useInlineMedia, useLoad, VideoFallback } from "../ui";

const KINDS = ["", "image", "video", "audio", "document", "other"] as const;
const KIND_LABEL: Record<string, string> = { "": "All", image: "Images", video: "Videos", audio: "Audio", document: "Documents", other: "Other" };

export function Media() {
  const [kind, setKind] = useState<string>("");
  const { data, setData, error, reload } = useLoad(() => api.resources({ kind }), [kind]);
  const [sel, setSel] = useState<Resource | null>(null);
  const act = useAction();

  useEffect(
    () =>
      subscribe((ev) => {
        if (ev.topic !== "resource.added") return;
        const r = ev.data as Resource;
        if (kind && r.kind !== kind) return;
        setData((cur) => (cur && !cur.some((x) => x.id === r.id) ? [r, ...cur] : cur));
      }),
    [kind, setData],
  );

  const items = data ?? [];

  return (
    <div className="page page-wide">
      <Header title="Media" sub="Screenshots, downloads and brag renders the AI saved for posts." />
      <div className="tabs">
        {KINDS.map((k) => (
          <button key={k} className={`tab ${kind === k ? "tab-active" : ""}`} onClick={() => setKind(k)}>
            {KIND_LABEL[k]}
          </button>
        ))}
      </div>
      <ErrorNote error={error} onRetry={reload} />
      {items.length === 0 && !error ? (
        <Empty title="No media yet">Ask ChatGPT to screenshot a page or save an image, and it shows up here.</Empty>
      ) : (
        <div className="grid">
          {items.map((r) => (
            <button key={r.id} className={`tile ${sel?.id === r.id ? "tile-active" : ""}`} onClick={() => setSel(r)}>
              <div className="tile-media">
                <Preview r={r} thumb />
              </div>
              <div className="tile-foot">
                <span className="tile-name" title={r.name}>
                  {r.name}
                </span>
                <span className={`origin origin-${r.origin}`}>{r.origin}</span>
              </div>
            </button>
          ))}
        </div>
      )}

      {sel && (
        <aside className="drawer" aria-label="Media details">
          <div className="drawer-head">
            <h2 title={sel.name}>{sel.name}</h2>
            <button className="icon-btn" aria-label="Close" onClick={() => setSel(null)}>
              <X size={16} />
            </button>
          </div>
          <div className="drawer-media">
            <Preview r={sel} />
          </div>
          <dl className="kv">
            <dt>ID</dt>
            <dd className="kv-copy">
              <code>{sel.id}</code> <CopyButton text={sel.id} />
            </dd>
            <dt>Type</dt>
            <dd>
              {sel.mime} · {bytes(sel.size)}
            </dd>
            <dt>Saved</dt>
            <dd>{timeAgo(sel.created_at)}</dd>
            <dt>From</dt>
            <dd>
              <span className={`origin origin-${sel.origin}`}>{sel.origin}</span>{" "}
              {sel.source &&
                (/^https?:/.test(sel.source) ? (
                  <button className="link" onClick={() => openExternal(sel.source)}>
                    {sel.source} <ExternalLink size={12} />
                  </button>
                ) : (
                  <code>{sel.source}</code>
                ))}
            </dd>
            {sel.note && (
              <>
                <dt>Note</dt>
                <dd>{sel.note}</dd>
              </>
            )}
            <dt>File</dt>
            <dd>
              <code className="break">{sel.path}</code>
            </dd>
          </dl>
          <div className="actionbar">
            <button className="btn" onClick={() => act(() => api.revealResource(sel.id))}>
              <FolderOpen size={14} /> Reveal in folder
            </button>
            <span className="actionbar-spacer" />
            <button
              className="btn btn-danger-quiet"
              onClick={async () => {
                if (!confirm(`Delete ${sel.name}? Posts that use it will lose the attachment.`)) return;
                const ok = await act(() => api.deleteResource(sel.id).then(() => true), "Media deleted");
                if (ok) {
                  setData((cur) => cur?.filter((x) => x.id !== sel.id));
                  setSel(null);
                }
              }}
            >
              <Trash2 size={14} /> Delete
            </button>
          </div>
        </aside>
      )}
    </div>
  );
}

function Preview({ r, thumb }: { r: Resource; thumb?: boolean }) {
  const inline = useInlineMedia();
  const open = () => api.openResource(r.id).catch(() => {});
  if (r.kind === "image") return <img src={fileURL(r.id)} alt={r.name} loading="lazy" />;
  if (r.kind === "video")
    return inline ? <video src={fileURL(r.id)} controls={!thumb} muted={thumb} preload="metadata" /> : <VideoFallback name={r.name} onOpen={open} compact={thumb} />;
  if (r.kind === "audio")
    return thumb || !inline ? (
      <button className="btn btn-quiet" onClick={open} title="Open in your player">
        <Music size={28} className="tile-icon" />
      </button>
    ) : (
      <audio src={fileURL(r.id)} controls />
    );
  return <FileIcon size={28} className="tile-icon" />;
}
