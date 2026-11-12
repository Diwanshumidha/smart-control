import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import App from "./App";
import { ApprovalPopup } from "./ApprovalPopup";
import { api } from "./api";
import { ToastProvider } from "./ui";
import "./styles.css";

// The desktop app says which window this is; the browser dashboard has no
// /api/desktop and is always the main UI.
async function mode(): Promise<string> {
  if (!window.runtime && location.protocol !== "wails:") return "main";
  try {
    return (await api.desktop()).mode;
  } catch {
    return "main";
  }
}

mode().then((m) =>
  createRoot(document.getElementById("root")!).render(
    <StrictMode>
      <ToastProvider>{m === "approvals" ? <ApprovalPopup /> : <App />}</ToastProvider>
    </StrictMode>,
  ),
);
