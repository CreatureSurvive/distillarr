// API client + SSE subscription.

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

export type RecSummary = {
  action: "transcode" | "skip" | "caution";
  savings_pct: number;
  est_out_bytes: number;
  quality: number;
  codec: string;
  reason: string;
};

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
  interlaced: boolean;
  crop_w: number;
  crop_h: number;
  crop_x: number;
  crop_y: number;
  crop_checked: boolean;
  video_bitrate: number;
  total_bitrate: number;
  audio: AudioStream[];
  sub_count: number;
  sidecars: { name: string; lang: string; kind: string }[];
  transcode_score: number;
  mtime_ns: number;
  rec?: RecSummary;
  image?: string;
  backdrop?: string;
  jf_name?: string;
  overview?: string;
  genres?: string;
  queued?: boolean;
  video_tag?: string;
  faststart?: number;
  issues?: string; // ",no_hvc1,pcm_audio,"
};

export type IssueType = {
  key: string;
  label: string;
  fix: "quick" | "reencode" | "info";
  help: string;
  scope: "file" | "season";
  movies: number;
  episodes: number;
  bytes: number;
};

export type MixedSeason = {
  show: string;
  season: number;
  episodes: number;
  codecs: string;
  containers: string;
  classes: string;
  size: number;
};

export type Facet = { value: string; files: number; bytes: number };
export type Composition = { containers: Facet[]; video: Facet[]; audio: Facet[]; res: Facet[] };

export type MeasureStatus = {
  enabled: boolean;
  window_open: boolean;
  running: boolean;
  current?: string;
  note?: string;
  measured: number;
  remaining: number;
  done_this_window: number;
  last_error?: string;
};

export type StatGroup = { key: string; jobs: number; src_bytes: number; out_bytes: number; saved: number; hours: number };
export type HistoryStats = {
  totals: { done: number; failed: number; canceled: number; src_bytes: number; out_bytes: number; saved: number; encode_hours: number };
  kinds: StatGroup[];
  codecs: StatGroup[];
  library: StatGroup[];
  weeks: StatGroup[];
  top: { job_id: number; path: string; src_bytes: number; out_bytes: number; finished_at: string }[];
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

export type AudioTrack = {
  index: number;
  action: "copy" | "convert" | "drop";
  codec?: string;
  bitrate?: number;
  channels?: number;
};

export type SubTrack = { index: number; action: "keep" | "drop" };

export type Settings = {
  codec: string;
  backend?: string;
  quality: number;
  speed?: string;
  bit_depth?: number;
  max_height?: number;
  deinterlace?: string;
  tonemap_hdr?: boolean;
  film_grain?: number;
  tune?: string;
  container?: string;
  render_node?: string;
  audio_pcm_target?: string;
  audio?: AudioTrack[];
  subs?: SubTrack[];
  extra_args?: string;
  crop?: string;
  vmaf_target?: number;
  upscale_to?: number;
  upscale_tier?: string;
  upscale_preset?: string;
  upscale_params?: Record<string, number>;
  upscale_output?: string;
  vulkan_device?: number;
};

export type UpscaleTarget = { class: number; w: number; h: number };
export type UpscaleParam = { key: string; label: string; min: number; max: number; step: number; def: number };
export type UpscalePreset = {
  id: string;
  label: string;
  desc: string;
  tier: string;
  content: "film" | "anime";
  params: UpscaleParam[];
};
export type UpscaleInfo = {
  source: { w: number; h: number; class: number };
  targets: UpscaleTarget[];
  suggested: { to: number; preset: string; content: "film" | "anime"; why: string };
  presets: UpscalePreset[];
  vulkan: { index: number; name: string; ok: boolean; discrete: boolean; error?: string }[];
  available: boolean;
};
export type Still = { key: string; w: number; h: number; at: number; ms: number; cached: boolean; a_url: string; b_url: string };

export type VMAF = { mean: number; p5: number; min: number };

export type TuneResult = {
  target: number;
  quality: number;
  vmaf: VMAF;
  ratio: number;
  met: boolean;
  steps: { quality: number; crf: number; vmaf: VMAF; ratio: number; pass: boolean }[];
};

export type AudioPlan = {
  index: number;
  codec: string;
  channels: number;
  lang?: string;
  action: string;
  target?: string;
  note?: string;
};

export type Recommendation = {
  action: "transcode" | "skip" | "caution" | "custom";
  reason: string;
  settings: Settings;
  why?: string[];
  notes?: string[];
  audio?: AudioPlan[];
  est_out_bytes: number;
  est_low_bytes: number;
  est_high_bytes: number;
  savings_pct: number;
  worth: boolean;
  score: number;
  src_bpp: number;
  calibration_samples: number;
  measured?: boolean;
};

export type Plan = {
  rec: Recommendation;
  auto: Recommendation;
  command?: string;
  fallback_command?: string;
  command_error?: string;
  container?: string;
  backend: string;
  render_node?: string;
};

export type Series = {
  title: string;
  year: number;
  episodes: number;
  seasons: number;
  total_size: number;
  avg_bitrate: number;
  codecs: string;
  height: number;
  worth_count: number;
  reclaimable: number;
  image?: string;
};

export type SeasonStat = {
  season: number;
  episodes: number;
  total_size: number;
  avg_bitrate: number;
  height: number;
  codecs: string;
  worth_count: number;
  reclaimable: number;
  image?: string;
};

export type ShowDetail = {
  title: string;
  seasons: SeasonStat[];
  overview?: string;
  genres?: string;
  image?: string;
  backdrop?: string;
};

export type ShowOverrides = {
  codec?: string;
  backend?: string;
  speed?: string;
  bit_depth?: number;
  max_height?: number;
  quality_delta?: number;
  quality?: number;
  container?: string;
  audio_pcm_target?: string;
  deinterlace?: string;
  tonemap_hdr?: boolean;
  film_grain?: number;
};

export type ShowPlan = {
  summary: {
    episodes: number;
    total_size: number;
    worth_size: number;
    est_out_total: number;
    savings_pct: number;
    worth_count: number;
    skip_count: number;
    caution_count: number;
    avg_bitrate: number;
    height: number;
    quality_min: number;
    quality_max: number;
    why?: string[];
    notes?: string[];
    settings: Settings;
  };
  episodes: {
    file_id: number;
    action: string;
    worth: boolean;
    size: number;
    est_out_bytes: number;
    savings_pct: number;
    settings: Settings;
  }[];
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
  cmd?: string;
  dest_path?: string;
};

export type Progress = {
  job_id: number;
  pct: number;
  fps: number;
  speed: number;
  eta_sec: number;
  size: number;
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

export type HwResult = {
  backend: string;
  codec: string;
  node?: string;
  ok: boolean;
  ms: number;
  error?: string;
};

export type HwReport = {
  tested_at: string;
  ffmpeg_version: string;
  render_nodes: string[];
  has_nvenc: boolean;
  results: HwResult[];
};

export type Schedule = {
  id: number;
  label?: string;
  days: number;
  start: number;
  end: number;
};

export type Config = {
  libraries: { name: string; path: string }[];
  workers: number;
  paused: boolean;
  schedules: Schedule[];
  measure_enabled: boolean | null;
  measure_schedules: Schedule[];
  default_codec: string;
  default_quality: number;
  default_speed: string;
  preferred_backend: string;
  min_savings_pct: number;
  audio_pcm_target: string;
  trash_enabled: boolean;
  trash_days: number;
  trash_dir: string;
  max_attempts: number;
  recompress_hevc: boolean;
  max_height: number;
  tonemap_hdr: boolean;
  prefer_mp4: boolean;
  crop_bars: boolean;
  vmaf_target: number;
  jellyfin_url: string;
  jellyfin_api_key?: string;
  jellyfin_key_set?: boolean;
  jellyfin_path_map: string;
};

export type JfStatus = {
  configured: boolean;
  connected?: boolean;
  server_name?: string;
  version?: string;
  error?: string;
  url?: string;
  cached: number;
  syncing: boolean;
  last_sync?: string;
};

export type JfTest = {
  ok: boolean;
  error?: string;
  server_name?: string;
  version?: string;
  libraries?: { name: string; type: string; location: string; mapped: string; reachable: boolean }[];
};

export type TrashItem = {
  id: number;
  orig_path: string;
  trash_path: string;
  current_path: string;
  size: number;
  job_id: number;
  created_at: string;
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
  command?: string;
  measured_ratio?: number;
  tune?: TuneResult;
  stage?: string;
  segments: {
    index: number;
    start: number;
    len: number;
    src_path: string;
    enc_path: string;
    src_size: number;
    enc_size: number;
    proxy: boolean;
    vmaf?: VMAF;
  }[];
};

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, { headers: { "Content-Type": "application/json" }, ...init });
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    try {
      const j = await res.json();
      if (j.error) msg = j.error;
    } catch {}
    throw new Error(msg);
  }
  return res.json();
}

const post = <T,>(path: string, body?: unknown) =>
  req<T>(path, { method: "POST", body: body === undefined ? undefined : JSON.stringify(body) });

function qs(params: Record<string, string | number | boolean | undefined>) {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === "" || v === false) continue;
    q.set(k, v === true ? "1" : String(v));
  }
  return q.toString();
}

export const api = {
  scanStart: () => post<{ started: boolean }>("/api/v1/scan"),
  scanStats: () => req<ScanStats>("/api/v1/scan"),
  system: () =>
    req<{
      media?: { total: number; free: number };
      config?: { total: number; free: number };
      trash_bytes: number;
      trash_count: number;
      crop?: { checked: number; total: number; with_bars: number };
    }>("/api/v1/system"),

  config: () => req<Config>("/api/v1/config"),
  saveConfig: (c: Partial<Config>) => req<Config>("/api/v1/config", { method: "PUT", body: JSON.stringify(c) }),

  hw: () =>
    req<{ report: HwReport | null; health: Record<string, boolean>; auto: Record<string, string> }>("/api/v1/hw"),
  reprobe: () => post<{ started: boolean }>("/api/v1/hw"),
  resetBackend: (b: string) => post<{ ok: boolean }>(`/api/v1/hw/${b}/reset`),
  calibration: () => req<Record<string, { samples: number; factor: number }>>("/api/v1/calibration"),

  files: (p: Record<string, string | number | boolean | undefined>) =>
    req<{ files: FileItem[]; total: number }>(`/api/v1/libraries?${qs(p)}`),
  libraryStats: (lib: string) =>
    req<{ files: number; total_size: number; transcodable_size: number; projected_saved: number }>(
      `/api/v1/libraries/stats?library=${lib}`),
  series: (p: { title?: string; sort?: string; candidates?: boolean }) =>
    req<{ series: Series[] }>(`/api/v1/series?${qs(p)}`),
  show: (title: string) => req<ShowDetail>(`/api/v1/show?title=${encodeURIComponent(title)}`),
  showEpisodes: (title: string, season?: number) =>
    req<{ episodes: FileItem[] }>(`/api/v1/show/episodes?${qs({ title, season })}`),
  showPlan: (body: { title: string; season?: number; overrides: ShowOverrides; file_ids?: number[] }) =>
    post<ShowPlan>("/api/v1/show/plan", body),
  queueShow: (body: {
    title: string;
    season?: number;
    overrides: ShowOverrides;
    file_ids?: number[];
    run_now?: boolean;
    only_worth?: boolean;
  }) => post<{ created: number; skipped: number }>("/api/v1/show/queue", body),

  file: (id: number) => req<{ file: FileItem; streams: Stream[] }>(`/api/v1/files/${id}`),
  plan: (id: number, settings?: Settings) => post<Plan>(`/api/v1/files/${id}/plan`, { settings }),
  queueFile: (id: number, body: { settings?: Settings; run_now?: boolean }) =>
    post<Job>(`/api/v1/files/${id}/queue`, body),
  previewFile: (id: number, body: { settings?: Settings; segments?: number; starts?: number[] }) =>
    post<Preview>(`/api/v1/files/${id}/preview`, body),
  upscaleInfo: (id: number) => req<UpscaleInfo>(`/api/v1/files/${id}/upscale`),
  still: (id: number, settings: Settings, at: number) =>
    post<Still>(`/api/v1/files/${id}/still`, { settings, at }),

  fixFile: (id: number, run_now = false) => post<Job>(`/api/v1/files/${id}/fix`, { run_now }),
  issues: () => req<{ types: IssueType[] }>("/api/v1/issues"),
  mixedSeasons: (show?: string) => req<{ seasons: MixedSeason[] }>(`/api/v1/issues/mixed?${qs({ show })}`),
  fixIssue: (key: string, body: { library?: string; show?: string; run_now?: boolean }) =>
    post<{ queued: number; skipped: number }>(`/api/v1/issues/${key}/fix`, body),
  composition: (library?: string) => req<Composition>(`/api/v1/libraries/composition?${qs({ library })}`),
  measure: () => req<MeasureStatus>("/api/v1/measure"),
  stats: () => req<HistoryStats>("/api/v1/stats"),

  jobs: (status?: string, limit = 100) => req<{ jobs: Job[] }>(`/api/v1/jobs?${qs({ status, limit })}`),
  job: (id: number) => req<{ job: Job; progress: Progress | null }>(`/api/v1/jobs/${id}`),
  cancelJob: (id: number) => post<{ ok: boolean }>(`/api/v1/jobs/${id}/cancel`),
  retryJob: (id: number) => post<{ ok: boolean }>(`/api/v1/jobs/${id}/retry`),
  runNowJob: (id: number) => post<{ ok: boolean }>(`/api/v1/jobs/${id}/run-now`),
  moveJob: (id: number, before: number) => post<{ ok: boolean }>(`/api/v1/jobs/${id}/move`, { before }),
  queueSummary: () =>
    req<{ counts: Record<string, number>; realized_saved: number; jobs_done: number; window_open: boolean; paused: boolean }>(
      "/api/v1/queue/summary"),
  pauseQueue: () => post<{ paused: boolean }>("/api/v1/queue/pause"),
  resumeQueue: () => post<{ paused: boolean }>("/api/v1/queue/resume"),
  clearQueue: () => post<{ canceled: number }>("/api/v1/queue/clear"),

  trash: () => req<{ items: TrashItem[]; retention_days: number }>("/api/v1/trash"),
  restoreTrash: (id: number) => post<{ ok: boolean }>(`/api/v1/trash/${id}/restore`),
  deleteTrash: (id: number) => req<{ ok: boolean }>(`/api/v1/trash/${id}`, { method: "DELETE" }),
  purgeTrash: (all: boolean) => post<{ freed: number; count: number }>(`/api/v1/trash/purge${all ? "?all=1" : ""}`),

  jellyfinStatus: () => req<JfStatus>("/api/v1/jellyfin/status"),
  jellyfinTest: (body: { url?: string; key?: string; path_map?: string }) => post<JfTest>("/api/v1/jellyfin/test", body),
  jellyfinSync: () => post<{ started: boolean }>("/api/v1/jellyfin/sync"),

  preview: (id: string) => req<Preview>(`/api/v1/previews/${id}`),
  previews: () => req<{ previews: Preview[] }>("/api/v1/previews"),
  previewClipURL: (id: string, clip: string) => `/api/v1/previews/${id}/${clip}`,
};

// ---- SSE: one shared EventSource, many listeners ----

type Listener = (event: string, data: any) => void;
const listeners = new Set<Listener>();
let es: EventSource | null = null;

export function subscribe(fn: Listener): () => void {
  listeners.add(fn);
  if (!es) {
    es = new EventSource("/api/v1/events");
    es.addEventListener("message", (ev) => {
      try {
        const { event, data } = JSON.parse((ev as MessageEvent).data);
        listeners.forEach((l) => l(event, data));
      } catch {}
    });
  }
  return () => {
    listeners.delete(fn);
  };
}
