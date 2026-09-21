// API client + SSE subscription.

export type FileItem = {
  id: number;
  path: string;
  library: string;
  title: string;
  year: number;
  season: number;
  episode: number;
  ep_title: string;
  quality_tag: string;
  size: number;
  container: string;
  duration: number;
  video_codec: string;
  width: number;
  height: number;
  bit_depth: number;
  fps: number;
  hdr: string;
  video_bitrate: number;
  total_bitrate: number;
  audio: AudioStream[];
  sub_count: number;
  sidecars: { name: string; lang: string; kind: string }[];
  transcode_score: number;
  missing: boolean;
  poster?: string;
  jf_name?: string;
  overview?: string;
  queued?: boolean;
};

export type AudioStream = {
  index: number;
  codec: string;
  lang: string;
  title: string;
  channels: number;
  bit_rate: number;
  default?: boolean;
  forced?: boolean;
};

export type Stream = {
  id: number;
  kind: string;
  stream_index: number;
  codec: string;
  lang: string;
  title: string;
  channels: number;
  bit_rate: number;
  default: boolean;
  forced: boolean;
  is_text: boolean;
};

export type Settings = {
  codec: string;
  backend?: string;
  quality: number;
  preset?: string;
  max_height?: number;
  tonemap_hdr?: boolean;
  film_grain?: number;
  audio_pcm_target?: string;
  container?: string;
  render_node?: string;
};

export type Recommendation = {
  action: "transcode" | "skip" | "caution";
  reason: string;
  settings: Settings;
  est_out_bytes: number;
  est_low_bytes: number;
  est_high_bytes: number;
  savings_pct: number;
  worth: boolean;
  score: number;
  notes: string[];
};

export type Series = {
  title: string;
  year: number;
  episodes: number;
  total_size: number;
  avg_bitrate: number;
  codecs: string;
  avg_score: number;
};

export type SeasonStat = {
  season: number;
  episodes: number;
  total_size: number;
  avg_bitrate: number;
  avg_score: number;
  height: number;
};

export type SeasonRec = {
  episodes: number;
  total_size: number;
  est_out_total: number;
  savings_pct: number;
  settings: Settings;
  worth_count: number;
  skip_count: number;
  caution_count: number;
  avg_bitrate: number;
  height: number;
  notes: string[];
};

export type Job = {
  id: number;
  file_id: number;
  src_path: string;
  status: string;
  priority: number;
  run_now: boolean;
  backend: string;
  codec: string;
  quality: number;
  settings_json: string;
  attempts: number;
  max_attempts: number;
  error?: string;
  error_tail?: string;
  progress_json?: string;
  src_size: number;
  output_size: number;
  started_at?: string;
  finished_at?: string;
  created_at?: string;
  file_title?: string;
};

export type Progress = {
  frame: number;
  fps: number;
  speed: number;
  out_sec: number;
  total_size: number;
  dur_sec: number;
  job_id?: number;
  pct?: number;
  eta_sec?: number;
  size?: number;
};

export type ScanStats = {
  running: boolean;
  phase: string;
  library?: string;
  seen: number;
  probed: number;
  updated: number;
  errors: number;
};

export type HwReport = {
  tested_at: string;
  ffmpeg_version: string;
  render_nodes: string[];
  has_nvenc: boolean;
  results: {
    backend: string;
    codec: string;
    node?: string;
    ok: boolean;
    ms: number;
    error?: string;
  }[];
};

export type Config = {
  libraries: { name: string; path: string }[];
  workers: number;
  paused: boolean;
  schedules: Schedule[];
  default_codec: string;
  default_quality: number;
  preferred_backend: string;
  min_savings_pct: number;
  audio_pcm_target: string;
  trash_enabled: boolean;
  trash_days: number;
  max_attempts: number;
  recompress_hevc: boolean;
  max_height: number;
  tonemap_hdr: boolean;
  jellyfin_url: string;
  jellyfin_api_key?: string;
  jellyfin_key_set?: boolean;
};

export type Schedule = {
  id: number;
  label?: string;
  days: number;
  start: number;
  end: number;
};

export type Preview = {
  id: string;
  file_id: number;
  path: string;
  created_at: string;
  status: "running" | "ready" | "failed";
  error?: string;
  settings: Settings;
  duration: number;
  segments: {
    index: number;
    start: number;
    len: number;
    src_path: string;
    enc_path: string;
    src_size: number;
    enc_size: number;
    proxy: boolean;
  }[];
};

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, {
    headers: { "Content-Type": "application/json" },
    ...init,
  });
  if (!res.ok) {
    let msg = `${res.status}`;
    try {
      const j = await res.json();
      if (j.error) msg = j.error;
    } catch {}
    throw new Error(msg);
  }
  return res.json();
}

export const api = {
  health: () => req<{ ok: boolean }>("/api/v1/health"),
  scanStart: () => req<{ started: boolean }>("/api/v1/scan", { method: "POST" }),
  scanStats: () => req<ScanStats>("/api/v1/scan"),

  config: () => req<Config & { jellyfin_key_set?: boolean }>("/api/v1/config"),
  saveConfig: (c: Partial<Config>) =>
    req<Config>("/api/v1/config", { method: "PUT", body: JSON.stringify(c) }),

  hw: () => req<{ report: HwReport | null; health: Record<string, boolean> }>("/api/v1/hw"),
  reprobe: () => req<{ started: boolean }>("/api/v1/hw", { method: "POST" }),
  resetBackend: (b: string) =>
    req<{ ok: boolean }>(`/api/v1/hw/${b}/reset`, { method: "POST" }),

  files: (params: Record<string, string | number | undefined>) => {
    const q = new URLSearchParams();
    for (const [k, v] of Object.entries(params)) {
      if (v !== undefined && v !== "") q.set(k, String(v));
    }
    return req<{ files: FileItem[]; next_cursor: string }>(`/api/v1/libraries?${q}`);
  },
  libraryStats: (lib: string) =>
    req<{ files: number; total_size: number; transcodable_size: number; projected_saved: number }>(
      `/api/v1/libraries/stats?library=${lib}`),
  file: (id: number) => req<{ file: FileItem; streams: Stream[] }>(`/api/v1/files/${id}`),
  fileRec: (id: number) => req<Recommendation>(`/api/v1/files/${id}/rec`),
  queueFile: (id: number, body: { settings?: Settings; run_now?: boolean }) =>
    req<Job>(`/api/v1/files/${id}/queue`, { method: "POST", body: JSON.stringify(body) }),
  previewFile: (id: number, body: { settings?: Settings; segments?: number }) =>
    req<Preview>(`/api/v1/files/${id}/preview`, { method: "POST", body: JSON.stringify(body) }),

  series: (title?: string) =>
    req<{ series: Series[] }>(`/api/v1/series${title ? `?title=${encodeURIComponent(title)}` : ""}`),
  episodes: (show: string, season: number) =>
    req<{ episodes: FileItem[]; seasons: SeasonStat[] }>(
      `/api/v1/series/episodes?show=${encodeURIComponent(show)}&season=${season}`),
  seriesRec: (show: string, season: number) =>
    req<SeasonRec>(`/api/v1/series/rec?show=${encodeURIComponent(show)}&season=${season}`),
  queueSeries: (body: {
    show: string;
    season?: number;
    settings?: Settings;
    run_now?: boolean;
    only_worth?: boolean;
  }) => req<{ created: number }>("/api/v1/series/queue", { method: "POST", body: JSON.stringify(body) }),

  jobs: (status?: string, before?: number) => {
    const q = new URLSearchParams();
    if (status) q.set("status", status);
    if (before) q.set("before", String(before));
    return req<{ jobs: Job[] }>(`/api/v1/jobs?${q}`);
  },
  job: (id: number) => req<{ job: Job; progress: Progress | null }>(`/api/v1/jobs/${id}`),
  cancelJob: (id: number) => req<{ ok: boolean }>(`/api/v1/jobs/${id}/cancel`, { method: "POST" }),
  retryJob: (id: number) => req<{ ok: boolean }>(`/api/v1/jobs/${id}/retry`, { method: "POST" }),
  runNowJob: (id: number) => req<{ ok: boolean }>(`/api/v1/jobs/${id}/run-now`, { method: "POST" }),
  setPriority: (id: number, priority: number) =>
    req<{ ok: boolean }>(`/api/v1/jobs/${id}/priority`, {
      method: "POST",
      body: JSON.stringify({ priority }),
    }),

  queueSummary: () =>
    req<{
      counts: Record<string, number>;
      realized_saved: number;
      jobs_done: number;
      window_open: boolean;
      paused: boolean;
    }>("/api/v1/queue/summary"),
  pauseQueue: () => req<{ paused: boolean }>("/api/v1/queue/pause", { method: "POST" }),
  resumeQueue: () => req<{ paused: boolean }>("/api/v1/queue/resume", { method: "POST" }),

  jellyfinStatus: () =>
    req<{ configured: boolean; connected?: boolean; server_name?: string; version?: string; error?: string }>(
      "/api/v1/jellyfin/status"),
  jellyfinSync: () => req<{ started: boolean }>("/api/v1/jellyfin/sync", { method: "POST" }),

  previews: () => req<{ previews: Preview[] }>("/api/v1/previews"),
  preview: (id: string) => req<Preview>(`/api/v1/previews/${id}`),
  previewClipURL: (id: string, clip: string) => `/api/v1/previews/${id}/${clip}`,
};

// ---- SSE ----

export type LiveEvent =
  | { event: "job"; data: any }
  | { event: "progress"; data: Progress & { job_id: number; pct: number; eta_sec: number; size: number } }
  | { event: "queue"; data: Record<string, number> }
  | { event: "scan"; data: ScanStats }
  | { event: "hardware"; data: any }
  | { event: "preview"; data: Preview }
  | { event: "jellyfin"; data: any }
  | { event: "message"; data: { event: string; data: any } };

export function subscribe(onEvent: (e: LiveEvent) => void): () => void {
  const es = new EventSource("/api/v1/events");
  es.onmessage = (ev) => {
    try {
      const parsed = JSON.parse(ev.data);
      onEvent({ event: "message", data: parsed } as LiveEvent);
    } catch {}
  };
  return () => es.close();
}
