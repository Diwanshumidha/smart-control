import { useEffect, useState } from "react";
import { Plus, X } from "lucide-react";
import { api, ApiError } from "../api";
import type { BrowserDriver, Settings } from "../types";
import { ErrorNote, Header, useLoad, useToast } from "../ui";

const MODES: { id: string; label: string; needsHost: boolean; hint: string }[] = [
  { id: "quick", label: "Cloudflare quick tunnel", needsHost: false, hint: "Random trycloudflare.com URL, new on every start. No setup." },
  { id: "cloudflare", label: "Cloudflare, your domain", needsHost: true, hint: "Fixed URL on a domain in your Cloudflare account. Run `cloudflared tunnel login` once." },
  { id: "tailscale", label: "Tailscale Funnel", needsHost: false, hint: "Fixed https://<machine>.<tailnet>.ts.net URL. Needs Tailscale with Funnel enabled." },
  { id: "ngrok", label: "ngrok static domain", needsHost: true, hint: "Your free static ngrok domain. Run `ngrok config add-authtoken` once." },
  { id: "off", label: "Off", needsHost: false, hint: "Local only. ChatGPT can't reach this machine." },
];

export function SettingsScreen({ onSaved }: { onSaved: () => void }) {
  const { data, error, reload } = useLoad(() => api.settings(), []);
  const [s, setS] = useState<Settings | null>(null);
  const [restart, setRestart] = useState(false);
  const [restarting, setRestarting] = useState(false);
  const [opening, setOpening] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState("");
  const drivers = useLoad(() => api.browserDrivers(), []);
  const voices = useLoad(() => api.voiceProviders(), []);
  const toast = useToast();

  useEffect(() => {
    if (data) setS(structuredClone(data));
  }, [data]);

  if (!s) {
    return (
      <div className="page">
        <Header title="Settings" />
        <ErrorNote error={error} onRetry={reload} />
      </div>
    );
  }

  const dirty = JSON.stringify(s) !== JSON.stringify(data);
  const mode = MODES.find((m) => m.id === s.tunnel.mode) ?? MODES[0];
  const voiceProvider = voices.data?.providers.find((p) => p.name === s.voice.provider);
  const set = (f: (d: Settings) => void) =>
    setS((cur) => {
      if (!cur) return cur;
      const n = structuredClone(cur);
      f(n);
      return n;
    });

  return (
    <div className="page">
      <Header title="Settings" sub={<>Saved to <code>config.json</code> in the data folder.</>} />

      {restart && (
        <div className="notice">
          The tunnel change takes effect after the server restarts. Everything else is already applied.{" "}
          <button
            type="button"
            className="btn"
            disabled={restarting}
            onClick={async () => {
              setRestarting(true);
              try {
                await api.restartServer();
                setRestart(false);
                toast("Restarting the server…", "ok");
              } catch (err) {
                toast(err instanceof Error ? err.message : String(err), "error");
              } finally {
                setRestarting(false);
              }
            }}
          >
            Restart server now
          </button>
        </div>
      )}

      <form
        className="settings"
        onSubmit={async (e) => {
          e.preventDefault();
          setSaving(true);
          setSaveError("");
          const clean = { ...s, roots: s.roots.map((r) => r.trim()).filter(Boolean) };
          try {
            const r = await api.saveSettings(clean);
            setRestart(!!r?.restart_required);
            toast(r?.restart_required ? "Settings saved. Restart to apply the tunnel change." : "Settings saved", "ok");
            await reload();
            drivers.reload();
            onSaved();
          } catch (err) {
            const msg = err instanceof Error ? err.message : String(err);
            if (err instanceof ApiError && err.status === 400) setSaveError(msg);
            else toast(msg, "error");
          } finally {
            setSaving(false);
          }
        }}
      >
        <fieldset className="group">
          <legend>Project folders</legend>
          <p className="muted small">
            Where the AI looks for your projects, e.g. <code>~/projects</code>. Changes apply immediately and reindex. File reads and grep stay inside
            these folders, and secrets like .env and SSH keys are always blocked.
          </p>
          {s.roots.map((r, i) => (
            <div className="row" key={i}>
              <input
                className="input"
                value={r}
                placeholder="/home/you/projects"
                aria-label={`Folder ${i + 1}`}
                onChange={(e) =>
                  set((d) => {
                    d.roots[i] = e.target.value;
                  })
                }
              />
              <button
                type="button"
                className="icon-btn"
                aria-label="Remove folder"
                onClick={() =>
                  set((d) => {
                    d.roots.splice(i, 1);
                  })
                }
              >
                <X size={14} />
              </button>
            </div>
          ))}
          <button type="button" className="btn btn-quiet" onClick={() => set((d) => void d.roots.push(""))}>
            <Plus size={14} /> Add folder
          </button>
        </fieldset>

        <fieldset className="group">
          <legend>Tunnel</legend>
          <label className="field">
            <span>How ChatGPT reaches this machine</span>
            <select className="input" value={s.tunnel.mode} onChange={(e) => set((d) => void (d.tunnel.mode = e.target.value))}>
              {MODES.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.label}
                </option>
              ))}
            </select>
          </label>
          <p className="muted small">{mode.hint}</p>
          {mode.needsHost && (
            <label className="field">
              <span>Hostname</span>
              <input
                className="input"
                value={s.tunnel.hostname}
                placeholder={mode.id === "ngrok" ? "you.ngrok-free.app" : "mcp.example.com"}
                onChange={(e) => set((d) => void (d.tunnel.hostname = e.target.value))}
              />
            </label>
          )}
          {mode.id === "cloudflare" && (
            <label className="field">
              <span>Tunnel name</span>
              <input className="input" value={s.tunnel.name} placeholder="smart-control" onChange={(e) => set((d) => void (d.tunnel.name = e.target.value))} />
            </label>
          )}
          <label className="field">
            <span>Own public URL (skips the managed tunnel)</span>
            <input className="input" value={s.tunnel.public_url} placeholder="https://mcp.example.com" onChange={(e) => set((d) => void (d.tunnel.public_url = e.target.value))} />
          </label>
        </fieldset>

        <fieldset className="group">
          <legend>Browser automation</legend>
          <p className="muted small">How the AI drives Brave to open pages and take screenshots. Switching applies immediately.</p>
          <ErrorNote error={drivers.error} onRetry={drivers.reload} />
          <div className="drivers" role="radiogroup" aria-label="Browser driver">
            {(drivers.data?.drivers ?? []).map((d: BrowserDriver) => {
              const checked = (s.browser.driver || drivers.data?.active) === d.name;
              return (
                <label key={d.name} className={`driver ${checked ? "driver-on" : ""} ${d.available ? "" : "driver-off"}`}>
                  <input
                    type="radio"
                    name="driver"
                    value={d.name}
                    checked={checked}
                    disabled={!d.available}
                    onChange={() => set((x) => void (x.browser.driver = d.name))}
                  />
                  <span className="driver-body">
                    <span className="driver-title">
                      {d.title}
                      <span className={`avail ${d.available ? "avail-yes" : "avail-no"}`}>{d.available ? "Available" : "Unavailable"}</span>
                    </span>
                    <span className="driver-desc">{d.description}</span>
                    {d.detail && <span className="driver-detail">{d.detail}</span>}
                  </span>
                </label>
              );
            })}
          </div>
          {(s.browser.driver || drivers.data?.active) === "shared" && (
            <div className="notice shared-browser">
              <p>
                The AI uses one visible Brave window with its own saved profile. Open it, log in to the sites the AI should use (LinkedIn, dashboards…),
                and leave it open or close it: logins are kept. The AI opens its pages as tabs there and closes them when done; URL approvals still apply.
              </p>
              <p className="muted small">
                Your everyday Brave profile can't be shared: Brave refuses automation of its default profile, to protect your sessions from malware.
              </p>
              <div className="row">
                <button
                  type="button"
                  className="btn btn-go"
                  disabled={opening}
                  onClick={async () => {
                    setOpening(true);
                    try {
                      await api.openBrowser();
                      toast("AI browser opened. Log in to sites there.", "ok");
                    } catch (err) {
                      toast(err instanceof Error ? err.message : String(err), "error");
                    } finally {
                      setOpening(false);
                    }
                  }}
                >
                  Open AI browser
                </button>
                <label className="field">
                  <span>Debugging port (127.0.0.1 only)</span>
                  <input
                    className="input input-narrow"
                    type="number"
                    min={1024}
                    max={65535}
                    value={s.browser.debug_port ?? 9333}
                    onChange={(e) => set((d) => void (d.browser.debug_port = Number(e.target.value)))}
                  />
                </label>
              </div>
            </div>
          )}
          <label className="field">
            <span>Brave executable (blank to detect)</span>
            <input className="input" value={s.browser.path} placeholder="/usr/bin/brave" onChange={(e) => set((d) => void (d.browser.path = e.target.value))} />
          </label>
          <label className="check">
            <input type="checkbox" disabled={(s.browser.driver || drivers.data?.active) === "shared"} checked={s.browser.headless} onChange={(e) => set((d) => void (d.browser.headless = e.target.checked))} />
            Run Brave hidden (headless)
          </label>
          <div className="row">
            <label className="field">
              <span>Viewport width</span>
              <input className="input" type="number" min={320} max={3840} value={s.browser.width} onChange={(e) => set((d) => void (d.browser.width = Number(e.target.value)))} />
            </label>
            <label className="field">
              <span>Viewport height</span>
              <input className="input" type="number" min={240} max={4000} value={s.browser.height} onChange={(e) => set((d) => void (d.browser.height = Number(e.target.value)))} />
            </label>
          </div>
        </fieldset>

        <fieldset className="group">
          <legend>URL approvals</legend>
          <label className="field">
            <span>Seconds the AI waits for your answer</span>
            <input
              className="input input-narrow"
              type="number"
              min={10}
              max={600}
              value={s.url_policy.approval_timeout_sec}
              onChange={(e) => set((d) => void (d.url_policy.approval_timeout_sec = Number(e.target.value)))}
            />
          </label>
          <label className="check">
            <input type="checkbox" checked={s.url_policy.allow_localhost} onChange={(e) => set((d) => void (d.url_policy.allow_localhost = e.target.checked))} />
            Let the AI open localhost (your dev servers), after you approve each host as usual
          </label>
          <label className="check">
            <input type="checkbox" checked={s.url_policy.allow_private_networks} onChange={(e) => set((d) => void (d.url_policy.allow_private_networks = e.target.checked))} />
            Let allow rules include LAN addresses (192.168.x, 10.x)
          </label>
        </fieldset>

        <fieldset className="group">
          <legend>Notifications</legend>
          <p className="muted small">How Smart Control gets your attention when the AI is waiting for you to approve a URL.</p>
          <label className="check">
            <input type="checkbox" checked={s.notify.desktop} onChange={(e) => set((d) => void (d.notify.desktop = e.target.checked))} />
            Desktop notification with Allow / Deny buttons
          </label>
          <label className="check">
            <input type="checkbox" checked={s.notify.popup} onChange={(e) => set((d) => void (d.notify.popup = e.target.checked))} />
            Pop up a small Accept / Decline window on the workspace you're on
          </label>
        </fieldset>

        <fieldset className="group">
          <legend>Voice</legend>
          <p className="muted small">The text-to-speech engine brag videos use for voiceovers. The AI can pick another voice for a single video.</p>
          <ErrorNote error={voices.error} onRetry={voices.reload} />
          <div className="drivers" role="radiogroup" aria-label="Voice engine">
            {(voices.data?.providers ?? []).map((p) => {
              const checked = s.voice.provider === p.name;
              return (
                <label key={p.name} className={`driver ${checked ? "driver-on" : ""} ${p.available ? "" : "driver-off"}`}>
                  <input
                    type="radio"
                    name="voice-provider"
                    value={p.name}
                    checked={checked}
                    onChange={() =>
                      set((x) => {
                        x.voice.provider = p.name;
                        x.voice.voice = p.default_voice;
                      })
                    }
                  />
                  <span className="driver-body">
                    <span className="driver-title">
                      {p.title}
                      <span className={`avail ${p.available ? "avail-yes" : "avail-no"}`}>{p.available ? "Ready" : "Not installed"}</span>
                    </span>
                    <span className="driver-desc">{p.description}</span>
                    {p.detail && <span className="driver-detail">{p.detail}</span>}
                  </span>
                </label>
              );
            })}
          </div>
          <div className="row">
            <label className="field">
              <span>Default voice</span>
              <select className="input" value={s.voice.voice} onChange={(e) => set((d) => void (d.voice.voice = e.target.value))}>
                {(voiceProvider?.voices ?? []).map((v) => (
                  <option key={v.id} value={v.id}>
                    {v.name} ({v.lang}){v.note ? `, ${v.note}` : ""}
                  </option>
                ))}
              </select>
            </label>
            <label className="field">
              <span>Speaking rate (0.7–1.3)</span>
              <input
                className="input input-narrow"
                type="number"
                min={0.7}
                max={1.3}
                step={0.05}
                value={s.voice.speed}
                onChange={(e) => set((d) => void (d.voice.speed = Number(e.target.value)))}
              />
            </label>
          </div>
        </fieldset>

        <fieldset className="group">
          <legend>Activity log</legend>
          <label className="field">
            <span>Keep tool calls for (days, 0 keeps everything)</span>
            <input
              className="input input-narrow"
              type="number"
              min={0}
              max={3650}
              value={s.activity.retention_days}
              onChange={(e) => set((d) => void (d.activity.retention_days = Math.max(0, Math.floor(Number(e.target.value) || 0))))}
            />
          </label>
          <p className="muted small">Older calls are deleted when you save and then once an hour.</p>
        </fieldset>

        {saveError && <div className="error-note">{saveError}</div>}
        <div className="actionbar">
          <button className="btn btn-go" type="submit" disabled={!dirty || saving}>
            Save settings
          </button>
          <button type="button" className="btn btn-quiet" disabled={!dirty} onClick={() => data && setS(structuredClone(data))}>
            Discard changes
          </button>
        </div>
      </form>
    </div>
  );
}
