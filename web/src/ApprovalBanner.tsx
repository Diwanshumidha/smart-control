import { useEffect, useState } from "react";
import { ShieldAlert } from "lucide-react";
import type { ApprovalState } from "./App";
import type { Approval } from "./types";

export const capabilityLabel: Record<string, string> = {
  browse: "open",
  screenshot: "screenshot",
  download: "download from",
  interact: "click and type on",
};

/** Seconds left before the waiting tool call gives up. */
export function useCountdown(a: Approval, timeoutSec: number): number {
  const deadline = a.created_at + timeoutSec * 1000;
  const [left, setLeft] = useState(() => Math.max(0, Math.round((deadline - Date.now()) / 1000)));
  useEffect(() => {
    const t = setInterval(() => setLeft(Math.max(0, Math.round((deadline - Date.now()) / 1000))), 500);
    return () => clearInterval(t);
  }, [deadline]);
  return left;
}

export function Countdown({ a, timeoutSec }: { a: Approval; timeoutSec: number }) {
  const left = useCountdown(a, timeoutSec);
  const frac = timeoutSec > 0 ? left / timeoutSec : 0;
  return (
    <span className={`countdown ${left <= 15 ? "countdown-low" : ""}`} title="Seconds before the AI's request times out">
      <span className="countdown-bar" style={{ transform: `scaleX(${frac})` }} />
      <span className="countdown-num">{left > 0 ? `${left}s` : "timed out"}</span>
    </span>
  );
}

/** Shown on every screen except URL access while an approval is pending. */
export function ApprovalBanner({ approvals, onOpen }: { approvals: ApprovalState; onOpen: () => void }) {
  const a = approvals.pending[0];
  if (!a) return null;
  const more = approvals.pending.length - 1;
  return (
    <div className="banner" role="alert">
      <ShieldAlert size={18} className="banner-icon" />
      <div className="banner-text">
        <strong>The AI wants to {capabilityLabel[a.capability] ?? a.capability} {a.host}</strong>
        <span className="banner-url" title={a.url}>
          {a.url}
        </span>
      </div>
      <Countdown a={a} timeoutSec={approvals.timeoutSec} />
      <div className="banner-actions">
        <button className="btn btn-go" onClick={() => approvals.decide(a, "allow_once")}>
          Allow once
        </button>
        <button className="btn" onClick={() => approvals.decide(a, "allow_host")}>
          Always allow host
        </button>
        <button className="btn btn-danger-quiet" onClick={() => approvals.decide(a, "deny")}>
          Deny
        </button>
        {more > 0 && (
          <button className="btn btn-quiet" onClick={onOpen}>
            {more} more
          </button>
        )}
      </div>
    </div>
  );
}
