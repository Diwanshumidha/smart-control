import { useEffect, useMemo, useState } from "react";
import { Check, ExternalLink, FolderOpen, Send, Trash2, X } from "lucide-react";
import { api, copyText, fileURL, openExternal, subscribe } from "../api";
import type { Post, PostStatus, PublishResult, Resource } from "../types";
import { Empty, ErrorNote, Header, statusLabel, timeAgo, useAction, useInlineMedia, useLoad, useTick, useToast, VideoFallback } from "../ui";

const TABS: PostStatus[] = ["pending_review", "approved", "posted", "discarded"];
const LIMIT = 3000;

function fullText(content: string, hashtags: string[]): string {
  const tags = hashtags.map((h) => "#" + h.replace(/^#/, "")).filter((h) => h.length > 1);
  if (!tags.length) return content;
  return `${content.trimEnd()}\n\n${tags.join(" ")}`;
}

export function Posts() {
  const [tab, setTab] = useState<PostStatus>("pending_review");
  const { data, setData, error, reload } = useLoad(() => api.posts({}), []);
  const [selected, setSelected] = useState<string>("");
  useTick();

  useEffect(
    () =>
      subscribe((ev) => {
        if (ev.topic !== "post.changed") return;
        const p = ev.data as Post & { deleted?: string };
        setData((cur) => {
          if (!cur) return cur;
          if (p.deleted) return cur.filter((x) => x.id !== p.id);
          const i = cur.findIndex((x) => x.id === p.id);
          if (i >= 0) {
            const next = cur.slice();
            next[i] = p;
            return next;
          }
          return [p, ...cur];
        });
      }),
    [setData],
  );

  const posts = data ?? [];
  const counts = useMemo(() => {
    const c: Record<string, number> = {};
    for (const p of posts) c[p.status] = (c[p.status] ?? 0) + 1;
    return c;
  }, [posts]);
  const visible = posts.filter((p) => p.status === tab);
  const current = posts.find((p) => p.id === selected) ?? visible[0];

  return (
    <div className="page page-split">
      <Header title="Posts" sub="Drafts the AI wrote. Nothing goes to LinkedIn until you post it." />
      <ErrorNote error={error} onRetry={reload} />
      <div className="tabs" role="tablist">
        {TABS.map((t) => (
          <button key={t} role="tab" aria-selected={tab === t} className={`tab ${tab === t ? "tab-active" : ""}`} onClick={() => setTab(t)}>
            {statusLabel[t]}
            {counts[t] ? <span className="tab-count">{counts[t]}</span> : null}
          </button>
        ))}
      </div>
      <div className="split">
        <ul className="list">
          {visible.length === 0 && (
            <li className="list-empty">
              {tab === "pending_review" ? "Nothing to review. Ask ChatGPT to draft a LinkedIn post." : `No ${statusLabel[tab].toLowerCase()} posts.`}
            </li>
          )}
          {visible.map((p) => (
            <li key={p.id}>
              <button className={`list-item ${current?.id === p.id ? "list-item-active" : ""}`} onClick={() => setSelected(p.id)}>
                <span className="list-title">{p.title || p.content.split("\n")[0].slice(0, 80) || "Untitled"}</span>
                <span className="list-sub">
                  {p.media.length > 0 && <span>{p.media.length} media · </span>}
                  {timeAgo(p.updated_at)}
                </span>
              </button>
            </li>
          ))}
        </ul>
        <div className="detail">
          {current ? <PostDetail key={current.id} post={current} onChange={(p) => setData((cur) => cur?.map((x) => (x.id === p.id ? p : x)))} onDeleted={(id) => setData((cur) => cur?.filter((x) => x.id !== id))} /> : <Empty title="Select a post" />}
        </div>
      </div>
    </div>
  );
}

function PostDetail({ post, onChange, onDeleted }: { post: Post; onChange: (p: Post) => void; onDeleted: (id: string) => void }) {
  const [content, setContent] = useState(post.content);
  const [tags, setTags] = useState(post.hashtags.join(" "));
  const [notes, setNotes] = useState(post.notes);
  const [title, setTitle] = useState(post.title);
  const [media, setMedia] = useState<Resource[]>([]);
  const [publish, setPublish] = useState<PublishResult | null>(null);
  const [postedUrl, setPostedUrl] = useState("");
  const act = useAction();
  const toast = useToast();

  // Reset local edits when the AI updates the post remotely and we have no unsaved changes.
  useEffect(() => {
    setContent(post.content);
    setTags(post.hashtags.join(" "));
    setNotes(post.notes);
    setTitle(post.title);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [post.updated_at]);

  useEffect(() => {
    let cancelled = false;
    Promise.all(post.media.map((id) => api.resource(id).catch(() => null))).then((rs) => {
      if (!cancelled) setMedia(rs.filter((r): r is Resource => !!r));
    });
    return () => {
      cancelled = true;
    };
  }, [post.media.join(",")]);

  const hashtags = tags
    .split(/[\s,]+/)
    .map((t) => t.replace(/^#/, ""))
    .filter(Boolean);
  const text = fullText(content, hashtags);
  const dirty = content !== post.content || hashtags.join(" ") !== post.hashtags.join(" ") || notes !== post.notes || title !== post.title;
  const over = text.length > LIMIT;
  const editable = post.status !== "posted";

  const save = async (extra: Partial<Post> = {}, msg?: string) => {
    const p = await act(() => api.updatePost(post.id, { title, content, hashtags, notes, ...extra }), msg);
    if (p) onChange(p);
    return p;
  };

  const doPublish = async () => {
    if (dirty && !(await save())) return;
    const r = await act(() => api.publishPost(post.id));
    if (!r) return;
    setPublish(r);
    try {
      await copyText(r.text);
      toast("Post text copied. Paste it into LinkedIn.", "ok");
    } catch {
      toast("Couldn't copy the text. Copy it from the preview instead.", "error");
    }
    openExternal(r.share_url);
  };

  const markPosted = async () => {
    const p = await act(() => api.markPosted(post.id, postedUrl.trim()), "Marked as posted");
    if (p) {
      onChange(p);
      setPublish(null);
    }
  };

  return (
    <div className="post-detail">
      <div className="post-cols">
        <div className="post-edit">
          <label className="field">
            <span>Title (only you see this)</span>
            <input className="input" value={title} disabled={!editable} onChange={(e) => setTitle(e.target.value)} />
          </label>
          <label className="field">
            <span className="field-row">
              Post text
              <span className={`counter ${over ? "counter-over" : text.length > LIMIT * 0.9 ? "counter-near" : ""}`}>
                {text.length.toLocaleString()} / {LIMIT.toLocaleString()}
              </span>
            </span>
            <textarea className="input textarea" value={content} disabled={!editable} onChange={(e) => setContent(e.target.value)} rows={14} />
          </label>
          <label className="field">
            <span>Hashtags</span>
            <input className="input" value={tags} disabled={!editable} placeholder="golang ai buildinpublic" onChange={(e) => setTags(e.target.value)} />
          </label>
          <label className="field">
            <span>Notes from the AI</span>
            <textarea className="input" value={notes} disabled={!editable} rows={3} onChange={(e) => setNotes(e.target.value)} />
          </label>
        </div>

        <div className="post-preview-col">
          <div className="li-card" aria-label="LinkedIn preview">
            <div className="li-head">
              <div className="li-avatar" aria-hidden>
                You
              </div>
              <div>
                <div className="li-name">You</div>
                <div className="li-meta">Now · Anyone</div>
              </div>
            </div>
            <div className="li-body">
              {content}
              {hashtags.length > 0 && (
                <p className="li-tags">
                  {hashtags.map((h) => (
                    <span key={h}>#{h} </span>
                  ))}
                </p>
              )}
            </div>
            {media.length > 0 && (
              <div className={`li-media li-media-${Math.min(media.length, 3)}`}>
                {media.slice(0, 4).map((m) => (
                  <MediaThumb key={m.id} r={m} />
                ))}
              </div>
            )}
          </div>
          {post.external_url && (
            <button className="btn btn-quiet" onClick={() => openExternal(post.external_url)}>
              <ExternalLink size={14} /> View on LinkedIn
            </button>
          )}
        </div>
      </div>

      {publish && (
        <div className="publish-panel">
          <h3>Finish posting on LinkedIn</h3>
          <p>The text is on your clipboard and LinkedIn's composer is open. Paste the text{publish.media.length ? ", attach these files" : ""}, and post it.</p>
          {publish.media.length > 0 && (
            <ul className="attach-list">
              {publish.media.map((m) => (
                <li key={m.id}>
                  <span>{m.name}</span>
                  <button className="btn btn-quiet" onClick={() => act(() => api.revealResource(m.id))}>
                    <FolderOpen size={14} /> Reveal in folder
                  </button>
                </li>
              ))}
            </ul>
          )}
          <div className="publish-done">
            <input className="input" placeholder="Link to the live post (optional)" value={postedUrl} onChange={(e) => setPostedUrl(e.target.value)} />
            <button className="btn btn-go" onClick={markPosted}>
              <Check size={14} /> Mark as posted
            </button>
            <button className="btn btn-quiet" onClick={() => openExternal(publish.share_url)}>
              Open LinkedIn again
            </button>
          </div>
        </div>
      )}

      <div className="actionbar">
        {editable && (
          <button className="btn" disabled={!dirty || over} onClick={() => save({}, "Changes saved")}>
            Save changes
          </button>
        )}
        {post.status === "pending_review" && (
          <button className="btn" disabled={over} onClick={() => save({ status: "approved" }, "Post approved")}>
            <Check size={14} /> Approve
          </button>
        )}
        {(post.status === "pending_review" || post.status === "approved") && (
          <button className="btn btn-go" disabled={over || !content.trim()} onClick={doPublish}>
            <Send size={14} /> Post to LinkedIn
          </button>
        )}
        {post.status === "approved" && !publish && (
          <button className="btn btn-quiet" onClick={() => setPublish({ share_url: "https://www.linkedin.com/feed/", text, media })}>
            Already posted it
          </button>
        )}
        <span className="actionbar-spacer" />
        {post.status !== "discarded" && post.status !== "posted" && (
          <button className="btn btn-quiet" onClick={() => save({ status: "discarded" }, "Post discarded")}>
            <X size={14} /> Discard
          </button>
        )}
        {post.status === "discarded" && (
          <button className="btn btn-quiet" onClick={() => save({ status: "pending_review" }, "Post restored")}>
            Restore
          </button>
        )}
        <button
          className="btn btn-danger-quiet"
          onClick={async () => {
            if (!confirm("Delete this post permanently?")) return;
            const ok = await act(() => api.deletePost(post.id).then(() => true), "Post deleted");
            if (ok) onDeleted(post.id);
          }}
        >
          <Trash2 size={14} /> Delete
        </button>
      </div>
    </div>
  );
}

function MediaThumb({ r }: { r: Resource }) {
  const inline = useInlineMedia();
  if (r.kind === "image") return <img src={fileURL(r.id)} alt={r.name} loading="lazy" />;
  if (r.kind === "video")
    return inline ? <video src={fileURL(r.id)} controls preload="metadata" /> : <VideoFallback name={r.name} onOpen={() => api.openResource(r.id).catch(() => {})} />;
  return <div className="li-file">{r.name}</div>;
}
