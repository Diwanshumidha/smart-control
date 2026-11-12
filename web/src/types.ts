export interface ToolInfo {
  toolset: string;
  name: string;
  description: string;
}

export interface Status {
  version: string;
  mcp_local_url: string;
  public_url: string;
  connector_url: string;
  tunnel: { mode: string; state: "starting" | "up" | "error" | "off" | string; error: string };
  roots: string[];
  brave_path: string;
  browser_driver?: string;
  project_index?: IndexStatus;
  data_dir: string;
  tools: ToolInfo[];
  media_playback?: { supported: boolean; missing?: string[]; hint?: string };
  can_restart?: boolean;
  pid?: number;
}

export interface ActivityEntry {
  id: string;
  toolset: string;
  tool: string;
  args: unknown;
  status: "running" | "ok" | "error";
  result: string;
  started_at: number;
  finished_at?: number;
  duration_ms?: number;
}

export type PostStatus = "pending_review" | "approved" | "posted" | "discarded";

export interface Post {
  id: string;
  platform: string;
  title: string;
  content: string;
  media: string[];
  hashtags: string[];
  notes: string;
  status: PostStatus;
  external_url: string;
  created_at: number;
  updated_at: number;
  posted_at?: number;
}

export interface Resource {
  id: string;
  kind: "image" | "video" | "audio" | "document" | "other";
  name: string;
  path: string;
  mime: string;
  size: number;
  sha256: string;
  origin: string;
  source: string;
  note: string;
  meta: Record<string, string>;
  created_at: number;
}

export interface PublishResult {
  share_url: string;
  text: string;
  media: Resource[];
}

export type Capability = "*" | "browse" | "screenshot" | "download" | "interact";

export interface UrlRule {
  id: string;
  pattern: string;
  action: "allow" | "deny";
  capabilities: Capability[];
  note: string;
  created_at: number;
}

export interface Approval {
  id: string;
  url: string;
  host: string;
  capability: string;
  reason: string;
  status: "pending" | "approved" | "denied" | "expired";
  decision: string;
  created_at: number;
  decided_at?: number;
}

export type Decision = "allow_once" | "allow_host" | "deny" | "deny_host";

export interface BragRun {
  id: string;
  dir: string;
  project: string;
  url: string;
  created: string;
  stage: string;
  plan_url: string;
  video: string;
  poster?: string;
}

export interface Settings {
  roots: string[];
  tunnel: { mode: string; hostname: string; name: string; public_url: string };
  browser: { driver: string; path: string; headless: boolean; width: number; height: number; debug_port?: number };
  url_policy: { approval_timeout_sec: number; allow_localhost: boolean; allow_private_networks: boolean };
  activity: { retention_days: number };
  notify: { desktop: boolean; popup: boolean };
  voice: { provider: string; voice: string; speed: number };
}

export interface BusEvent {
  topic: string;
  at: string;
  data: any;
}

export interface IndexStatus {
  count: number;
  running: boolean;
  last_run_at: number;
  duration_ms: number;
  error: string;
}

export interface Project {
  path: string;
  name: string;
  root: string;
  kinds: string[];
  description: string;
  git_remote: string;
  git_branch: string;
  has_readme: boolean;
  modified_at: number;
  indexed_at: number;
}

export interface VoiceProvider {
  name: string;
  title: string;
  description: string;
  default_voice: string;
  voices: { id: string; name: string; lang: string; note?: string }[];
  available: boolean;
  detail?: string;
}

export interface BrowserDriver {
  name: string;
  title: string;
  description: string;
  available: boolean;
  detail: string;
}
