import { useEffect, useState } from "react";
import { Plus, Trash2 } from "lucide-react";
import { api, subscribe } from "../api";
import type { ApprovalState } from "../App";
import { capabilityLabel, Countdown } from "../ApprovalBanner";
import type { Capability, UrlRule } from "../types";
import { Empty, ErrorNote, Header, timeAgo, useAction, useLoad } from "../ui";

const CAPS: Exclude<Capability, "*">[] = ["browse", "screenshot", "download", "interact"];

export function UrlAccess({ approvals }: { approvals: ApprovalState }) {
  const rules = useLoad(() => api.rules(), []);
  const history = useLoad(() => api.approvals(), []);

  useEffect(
    () =>
      subscribe((ev) => {
        if (ev.topic === "url.approval_decided" || ev.topic === "url.approval_requested") {
          history.reload();
          rules.reload();
        }
      }),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  );

  const past = (history.data ?? []).filter((a) => a.status !== "pending").slice(0, 30);

  return (
    <div className="page">
      <Header title="URL access" sub="The AI can only open, screenshot, download from, or click and type on sites you allow. Click-and-type (interact) is never included in “Everything”: it must be allowed per site." />

      <section className="section">
        <h2>Waiting for you</h2>
        {approvals.pending.length === 0 ? (
          <p className="muted">No requests right now. When the AI asks for a new site, you decide here, or from the banner on any screen.</p>
        ) : (
          <div className="approvals">
            {approvals.pending.map((a) => (
              <div key={a.id} className="approval">
                <div className="approval-head">
                  <strong>
                    {capabilityLabel[a.capability] ?? a.capability} <span className="host">{a.host}</span>
                  </strong>
                  <Countdown a={a} timeoutSec={approvals.timeoutSec} />
                </div>
                <code className="approval-url">{a.url}</code>
                {a.reason && <p className="approval-reason">{a.reason}</p>}
                <div className="approval-actions">
                  <button className="btn btn-go" onClick={() => approvals.decide(a, "allow_once")}>
                    Allow once
                  </button>
                  <button className="btn" onClick={() => approvals.decide(a, "allow_host")}>
                    Always allow {a.host}
                  </button>
                  <button className="btn btn-danger-quiet" onClick={() => approvals.decide(a, "deny")}>
                    Deny
                  </button>
                  <button className="btn btn-danger-quiet" onClick={() => approvals.decide(a, "deny_host")}>
                    Always deny {a.host}
                  </button>
                </div>
              </div>
            ))}
          </div>
        )}
      </section>

      <section className="section">
        <h2>Rules</h2>
        <p className="muted small">
          Patterns: <code>example.com</code>, <code>*.example.com</code> (subdomains too), <code>https://example.com/path*</code>, or <code>*</code>. Deny rules win over allow rules.
        </p>
        <RuleForm onAdded={(r) => rules.setData((cur) => [r, ...(cur ?? [])])} />
        <ErrorNote error={rules.error} onRetry={rules.reload} />
        {(rules.data ?? []).length === 0 ? (
          <Empty title="No rules yet">Every site starts as "ask me". Add a rule above, or choose "Always allow" on a request.</Empty>
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th>Pattern</th>
                <th>Effect</th>
                <th>Applies to</th>
                <th>Note</th>
                <th>Added</th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {(rules.data ?? []).map((r) => (
                <RuleRow key={r.id} r={r} onDeleted={() => rules.setData((cur) => cur?.filter((x) => x.id !== r.id))} />
              ))}
            </tbody>
          </table>
        )}
      </section>

      <section className="section">
        <h2>Recent decisions</h2>
        {past.length === 0 ? (
          <p className="muted">None yet.</p>
        ) : (
          <table className="table table-compact">
            <tbody>
              {past.map((a) => (
                <tr key={a.id}>
                  <td>
                    <span className={`decision decision-${a.status}`}>{a.status === "approved" ? "Allowed" : a.status === "denied" ? "Denied" : "Timed out"}</span>
                  </td>
                  <td>{a.capability}</td>
                  <td className="cell-url" title={a.url}>
                    {a.url}
                  </td>
                  <td className="muted">{a.decision.replace("_", " ")}</td>
                  <td className="muted">{timeAgo(a.decided_at || a.created_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>
    </div>
  );
}

function RuleRow({ r, onDeleted }: { r: UrlRule; onDeleted: () => void }) {
  const act = useAction();
  const all = r.capabilities.includes("*");
  return (
    <tr>
      <td>
        <code>{r.pattern}</code>
      </td>
      <td>
        <span className={`effect effect-${r.action}`}>{r.action === "allow" ? "Allow" : "Deny"}</span>
      </td>
      <td>{all ? (r.action === "deny" ? "Everything" : "Everything except interact") : r.capabilities.join(", ")}</td>
      <td className="muted">{r.note}</td>
      <td className="muted">{timeAgo(r.created_at)}</td>
      <td>
        <button
          className="icon-btn"
          aria-label={`Delete rule ${r.pattern}`}
          onClick={async () => {
            const ok = await act(() => api.deleteRule(r.id).then(() => true), "Rule deleted");
            if (ok) onDeleted();
          }}
        >
          <Trash2 size={14} />
        </button>
      </td>
    </tr>
  );
}

function RuleForm({ onAdded }: { onAdded: (r: UrlRule) => void }) {
  const [pattern, setPattern] = useState("");
  const [action, setAction] = useState<"allow" | "deny">("allow");
  const [caps, setCaps] = useState<Set<string>>(new Set(["*"]));
  const [note, setNote] = useState("");
  const act = useAction();

  const toggle = (c: string) =>
    setCaps((s) => {
      if (c === "*") return new Set(["*"]);
      const n = new Set(s);
      n.delete("*");
      if (n.has(c)) n.delete(c);
      else n.add(c);
      return n.size ? n : new Set(["*"]);
    });

  return (
    <form
      className="rule-form"
      onSubmit={async (e) => {
        e.preventDefault();
        if (!pattern.trim()) return;
        const r = await act(() => api.addRule({ pattern: pattern.trim(), action, capabilities: [...caps] as Capability[], note }), "Rule added");
        if (r) {
          onAdded(r);
          setPattern("");
          setNote("");
        }
      }}
    >
      <input className="input" placeholder="linkedin.com" value={pattern} onChange={(e) => setPattern(e.target.value)} aria-label="Pattern" />
      <select className="input" value={action} onChange={(e) => setAction(e.target.value as "allow" | "deny")} aria-label="Effect">
        <option value="allow">Allow</option>
        <option value="deny">Deny</option>
      </select>
      <fieldset className="caps">
        <legend className="sr-only">Applies to</legend>
        <label>
          <input type="checkbox" checked={caps.has("*")} onChange={() => toggle("*")} /> Everything
        </label>
        {CAPS.map((c) => (
          <label key={c}>
            <input type="checkbox" checked={caps.has(c)} onChange={() => toggle(c)} /> {c}
          </label>
        ))}
      </fieldset>
      <input className="input" placeholder="Note (optional)" value={note} onChange={(e) => setNote(e.target.value)} aria-label="Note" />
      <button className="btn btn-go" type="submit" disabled={!pattern.trim()}>
        <Plus size={14} /> Add rule
      </button>
    </form>
  );
}
