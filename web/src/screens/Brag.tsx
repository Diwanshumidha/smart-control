import { useEffect } from "react";
import { ExternalLink } from "lucide-react";
import { api, openExternal, subscribe, withToken } from "../api";
import type { BragRun } from "../types";
import { Empty, ErrorNote, Header, timeAgo, useLoad, useInlineMedia, VideoFallback } from "../ui";

const STAGE: Record<string, string> = {
  planning: "Planning",
  awaiting_approval: "Plan waiting for approval",
  building: "Building",
  done: "Done",
};

export function Brag() {
  const { data, error, reload } = useLoad(() => api.bragRuns(), []);

  useEffect(
    () =>
      subscribe((ev) => {
        if (ev.topic === "brag.run") reload();
      }),
    [reload],
  );

  const runs = data ?? [];

  return (
    <div className="page">
      <Header title="Brag videos" sub="Launch videos ChatGPT planned and rendered from your projects." />
      <ErrorNote error={error} onRetry={reload} />
      {runs.length === 0 && !error ? (
        <Empty title="No brag runs yet">Ask ChatGPT to "brag about" one of your projects to make a launch video.</Empty>
      ) : (
        <div className="runs">
          {runs.map((r) => (
            <RunCard key={r.id} r={r} />
          ))}
        </div>
      )}
    </div>
  );
}

function RunCard({ r }: { r: BragRun }) {
  const created = Date.parse(r.created);
  const subject = r.project || r.url || r.dir;
  const inline = useInlineMedia();
  const runFile = (p: string) => withToken(`/api/brag/runs/${encodeURIComponent(r.id)}/file/${p.split("/").map(encodeURIComponent).join("/")}`);
  const videoSrc = r.video ? runFile(r.video) : "";
  const poster = r.poster ? runFile(r.poster) : undefined;
  let video;
  if (!videoSrc) video = <div className="run-video run-video-empty">No render yet</div>;
  else if (inline) video = <video className="run-video" src={videoSrc} poster={poster} controls preload="metadata" />;
  else
    video = (
      <div className="run-video">
        <VideoFallback poster={poster} name={r.video} onOpen={() => api.openBragFile(r.id, r.video).catch(() => {})} />
      </div>
    );
  return (
    <article className="run">
      {video}
      <div className="run-body">
        <h3 title={subject}>{subject.split("/").filter(Boolean).pop() || subject}</h3>
        <p className="muted small break">{subject}</p>
        <div className="run-meta">
          <span className={`stage stage-${r.stage || "planning"}`}>{STAGE[r.stage] ?? r.stage ?? "Planning"}</span>
          {!isNaN(created) && <span className="muted small">{timeAgo(created)}</span>}
        </div>
        {r.plan_url && (
          <button className="btn btn-quiet" onClick={() => openExternal(r.plan_url)}>
            <ExternalLink size={14} /> Open plan
          </button>
        )}
      </div>
    </article>
  );
}
