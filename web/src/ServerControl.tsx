import { useEffect, useRef, useState } from "react";
import { Play, RotateCw } from "lucide-react";
import { api, isDesktop } from "./api";
import { Spinner, useToast } from "./ui";

type Busy = "" | "starting" | "restarting";

/**
 * Always in the sidebar: "Start server" while the window can't reach the
 * background server, "Restart server" while it can. Restarting interrupts
 * running tool calls, so it asks for a second click.
 */
export function ServerControl({ live, canRestart }: { live: boolean; canRestart: boolean }) {
  const [busy, setBusy] = useState<Busy>("");
  const [confirm, setConfirm] = useState(false);
  const toast = useToast();
  const sawDown = useRef(false);

  // A restart is over once the connection drops and comes back.
  useEffect(() => {
    if (busy !== "restarting") return;
    if (!live) sawDown.current = true;
    else if (sawDown.current) {
      setBusy("");
      toast("Server restarted", "ok");
    }
  }, [live, busy, toast]);
  useEffect(() => {
    if (busy !== "restarting") return;
    const t = setTimeout(() => setBusy(""), 30000);
    return () => clearTimeout(t);
  }, [busy]);
  useEffect(() => {
    if (!confirm) return;
    const t = setTimeout(() => setConfirm(false), 4000);
    return () => clearTimeout(t);
  }, [confirm]);

  const start = async () => {
    setBusy("starting");
    try {
      await api.startServer();
      toast("Server started", "ok");
    } catch (e) {
      toast(e instanceof Error ? e.message : String(e), "error");
    } finally {
      setBusy("");
    }
  };

  const restart = async () => {
    if (!confirm) {
      setConfirm(true);
      return;
    }
    setConfirm(false);
    sawDown.current = false;
    setBusy("restarting");
    try {
      await api.restartServer();
    } catch (e) {
      setBusy("");
      toast(e instanceof Error ? e.message : String(e), "error");
    }
  };

  if (busy) {
    return (
      <button className="btn server-btn" disabled>
        <Spinner /> {busy === "starting" ? "Starting server…" : "Restarting server…"}
      </button>
    );
  }
  if (!live) {
    if (!isDesktop()) {
      return (
        <p className="muted small server-hint">
          Start the server with <code>smart-control serve</code>, then reload.
        </p>
      );
    }
    return (
      <button className="btn btn-go server-btn" onClick={start}>
        <Play size={14} /> Start server
      </button>
    );
  }
  return (
    <button
      className={`btn server-btn ${confirm ? "server-confirm" : "btn-quiet"}`}
      onClick={restart}
      disabled={!canRestart}
      title={canRestart ? "Restart MCP, tunnel and dashboard. Running tool calls are interrupted." : "This server can't restart itself"}
    >
      <RotateCw size={14} /> {confirm ? "Click again to restart" : "Restart server"}
    </button>
  );
}
