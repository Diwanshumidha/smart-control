import { useCallback, useEffect, useRef, useState } from "react";
import { ShieldAlert } from "lucide-react";
import { api, onDesktopEvent, subscribe } from "./api";
import type { Approval, Decision } from "./types";
import { capabilityLabel, Countdown } from "./ApprovalBanner";

/**
 * The floating confirm window the server opens when the AI is waiting on a
 * URL approval. It answers them one at a time and closes itself when none
 * are left.
 */
export function ApprovalPopup() {
  const [pending, setPending] = useState<Approval[] | null>(null);
  const [timeoutSec, setTimeoutSec] = useState(90);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const loaded = useRef(false);

  const load = useCallback(() => {
    api
      .approvals("pending")
      .then((list) => {
        loaded.current = true;
        // Oldest first: answer in the order the AI asked.
        setPending([...list].sort((a, b) => a.created_at - b.created_at));
      })
      .catch((e) => setError(e instanceof Error ? e.message : String(e)));
  }, []);

  useEffect(() => {
    load();
    api
      .settings()
      .then((s) => s.url_policy?.approval_timeout_sec && setTimeoutSec(s.url_policy.approval_timeout_sec))
      .catch(() => {});
  }, [load]);

  useEffect(() => onDesktopEvent("approvals.refresh", load), [load]);
  useEffect(
    () =>
      subscribe((ev) => {
        if (ev.topic === "url.approval_requested") {
          setPending((p) => (p && !p.some((x) => x.id === ev.data.id) ? [...p, ev.data as Approval] : p));
        } else if (ev.topic === "url.approval_decided") {
          setPending((p) => p && p.filter((x) => x.id !== ev.data?.id));
        }
      }),
    [],
  );

  // Nothing left to answer: close the window.
  useEffect(() => {
    if (!loaded.current || pending === null || pending.length > 0) return;
    const t = setTimeout(() => window.runtime?.Quit?.(), 400);
    return () => clearTimeout(t);
  }, [pending]);

  const a = pending?.[0];

  const decide = async (d: Decision) => {
    if (!a) return;
    setBusy(true);
    setError("");
    try {
      await api.decide(a.id, d);
      setPending((p) => p && p.filter((x) => x.id !== a.id));
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
      load();
    } finally {
      setBusy(false);
    }
  };

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (busy || !a) return;
      if (e.key === "Enter") void decide("allow_once");
      if (e.key === "Escape") void decide("deny");
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  });

  if (!a) {
    return (
      <div className="popup">
        <p className="muted">{error || (pending === null ? "Loading…" : "Nothing waiting. Closing…")}</p>
      </div>
    );
  }

  const more = (pending?.length ?? 1) - 1;
  return (
    <div className="popup" role="alertdialog" aria-labelledby="popup-title" aria-describedby="popup-url">
      <div className="popup-head">
        <ShieldAlert size={20} className="popup-icon" />
        <div className="popup-title" id="popup-title">
          The AI wants to {capabilityLabel[a.capability] ?? a.capability} <strong>{a.host}</strong>
        </div>
        <Countdown a={a} timeoutSec={timeoutSec} />
      </div>
      <div className="popup-url" id="popup-url" title={a.url}>
        {a.url}
      </div>
      {a.reason && <p className="popup-reason">{a.reason}</p>}
      {error && <div className="error-note">{error}</div>}

      <div className="popup-actions">
        <button className="btn btn-danger-quiet popup-main" disabled={busy} onClick={() => decide("deny")}>
          Decline <kbd>Esc</kbd>
        </button>
        <button className="btn btn-go popup-main" disabled={busy} onClick={() => decide("allow_once")} autoFocus>
          Accept <kbd>Enter</kbd>
        </button>
      </div>
      <div className="popup-more">
        <button className="link" disabled={busy} onClick={() => decide("allow_host")}>
          Always allow {a.host}
        </button>
        <button className="link" disabled={busy} onClick={() => decide("deny_host")}>
          Always deny {a.host}
        </button>
        {more > 0 && <span className="muted">{more} more waiting</span>}
      </div>
    </div>
  );
}
