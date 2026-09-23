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

export type UpscaledInfo = { to: number; preset: string; tier: string; at: string };

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
  nlink: number; // >1 means another link shares this file's data (usually a seeding torrent)
  arr?: ArrFileInfo; // set when a connected Sonarr/Radarr instance manages this file
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
  upscaled?: UpscaledInfo; // set when this file is the output of a finished upscale job
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
  content: "film" | "anime" | "any";
  params: UpscaleParam[];
  sec_per_frame?: number; // neural presets: measured cost at ref_pixels of input
};
export type UpscaleInfo = {
  source: { w: number; h: number; class: number };
  targets: UpscaleTarget[];
  suggested: { to: number; preset: string; content: "film" | "anime"; why: string };
  presets: UpscalePreset[];
  vulkan: { index: number; name: string; ok: boolean; discrete: boolean; error?: string }[];
  available: boolean;
  device: string; // Vulkan GPU upscales run on
  neural: boolean;
  ref_pixels: number;
  max_neural_hours: number;
};
export type Still = { key: string; w: number; h: number; at: number; ms: number; cached: boolean; a_url: string; b_url: string };

// cambi: banding severity (0 = none), only present when the sample was
// animation or HDR-tonemap content - not scored on everything.
export type VMAF = { mean: number; p5: number; min: number; cambi?: number };

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
  origin?: string; // "manual" | "issue-fix" | "upscale" | (later) "webhook" | "autopilot" | "playback"
  reason?: string;
};

// A candidate waiting for a settle delay or a human decision before
// it's queued unattended.
export type IntakeRow = {
  id: number;
  file_id: number;
  origin: string;
  reason?: string;
  settings_json?: string;
  state: "waiting" | "needs_confirmation" | "queued" | "dismissed";
  not_before?: string;
  hold_reason?: string;
  job_id?: number;
  created_at: string;
  updated_at: string;
  file_path?: string;
  file_title?: string;
  instance_id?: string; // set when a connected Sonarr/Radarr instance manages the file
  instance_name?: string;
};

// the ordered rule engine that decides what an unattended
// candidate does. RuleMatch fields are all AND'd; an empty/absent field
// means "don't care" except animation, a real tri-state.
export type RuleMatch = {
  libraries?: string[]; // "movies" | "tvshows"
  instances?: string[]; // arr instance display names
  tags?: string[]; // resolved arr tag names
  origins?: string[]; // intake origin: webhook | autopilot | playback | manual
  src_codecs?: string[];
  res_classes?: number[]; // 480/576/720/1080/2160
  min_savings_pct?: number;
  min_age_days?: number;
  issue_keys?: string[];
  animation?: boolean | null;
};
export type RuleAction = {
  kind: "queue" | "queue_override" | "quick_fix" | "ignore";
  codec?: string;
  quality?: number;
};
export type AutoRule = {
  id: string;
  name: string;
  enabled: boolean;
  when: RuleMatch;
  then: RuleAction;
};
export type AutopilotDecision = {
  rule_id?: string;
  rule?: string;
  action: "queue" | "queue_override" | "quick_fix" | "ignore";
  codec?: string;
  quality?: number;
  reason: string;
};
export type AutopilotPreviewGroup = {
  rule_id?: string;
  rule?: string;
  action: string;
  count: number;
  est_saved_gb: number;
  sample: { id: number; title: string; path: string }[];
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

// HwDevice is the device an encode runs on, named from the hardware probe
// (the driver's model name), never hardcoded.
export type HwDevice = { backend: string; node?: string; name: string };
export type HwInfo = {
  report: HwReport | null;
  health: Record<string, boolean>;
  auto: Record<string, string>;
  devices: Record<string, string>; // render node → display name
  active: Record<string, HwDevice>; // codec → device an encode would use now
  upscale_device: string; // Vulkan GPU upscales run on ("" = none)
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
  upscale_output: string;
  upscale_schedules: Schedule[];
  jellyfin_url: string;
  jellyfin_api_key?: string;
  jellyfin_key_set?: boolean;
  jellyfin_path_map: string;
  plex_url: string;
  plex_token?: string;
  plex_token_set?: boolean;
  plex_path_map: string;
  arr_instances: ArrInstance[];
  // Override for the host:port Settings shows in front of each webhook
  // path. Blank = use the browser's own origin.
  webhook_base_url?: string;
  // off by default. Rules are evaluated in order, first
  // match wins.
  autopilot_enabled?: boolean;
  auto_rules?: AutoRule[];
  // off (0) unless the user sets a free-space threshold.
  disk_pressure_pct?: number;
  disk_pressure_budget_x?: number;
};

// One connected Sonarr or Radarr. api_key is never sent by the server
// (api_key_set says whether one is stored); a blank api_key on save
// keeps the stored one. id is a stable slug: never edit it after
// creation, since webhook URLs (later phases) key on it.
export type ArrInstance = {
  id?: string; // omit when creating a new one; the server assigns it
  name: string;
  kind: "sonarr" | "radarr";
  url: string;
  api_key?: string;
  api_key_set?: boolean;
  path_map?: string;
  enabled?: boolean;
  penalty_ack?: boolean; // set only via api.arrAck(); a plain save never changes it

  // Write-back, off by default. tag_after_reencode requires a
  // non-empty reencode_tag: the server rejects a save that enables it
  // with a blank name.
  tag_after_reencode?: boolean;
  reencode_tag?: string;
  unmonitor_after_reencode?: boolean;

  // Webhook. webhook_token is never sent by a plain save (see
  // webhook_token_set) — it's revealed only once, in
  // api.arrRegenerateWebhookToken()'s response. webhook_intake is off by
  // default: connecting the webhook and letting it queue unattended are
  // two separate opt-ins.
  webhook_token_set?: boolean;
  webhook_intake?: boolean;
  webhook_settle_minutes?: number;
};

export type ArrPenalty = { profile: string; custom_format: string; score: number; matches: string; terms: string[] };
export type ArrNamingWarning = { field: string; template: string };
export type ArrPenaltyReport = { penalties: ArrPenalty[] | null; naming_warnings: ArrNamingWarning[] | null };
export type ArrWebhookInfo = { last_received?: string; auth_fails: number };
export type ArrInfo = { id: string; penalties: ArrPenaltyReport; penalty_ack: boolean; webhook: ArrWebhookInfo };

export type ArrFileInfo = {
  instance_id: string;
  instance_name: string;
  kind: "sonarr" | "radarr";
  monitored: boolean;
  cutoff_not_met: boolean;
  cf_score: number;
  tag_names?: string[];
  original_language?: string;
  series_status?: string;
};

export type ArrTestResult = {
  ok: boolean;
  error?: string;
  version?: string;
  app_name?: string;
  root_folders?: { path: string; mapped: string; reachable: boolean }[];
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

export type PlexStatus = {
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

export type PlexTest = {
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

// ApiError carries the parsed JSON error body, when there was one, so
// callers that need structured data (e.g. the "hardlinked" conflict's
// file list) don't have to re-parse the message string.
export class ApiError extends Error {
  data?: any;
  constructor(message: string, data?: any) {
    super(message);
    this.data = data;
  }
}

async function req<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(path, { headers: { "Content-Type": "application/json" }, ...init });
  if (!res.ok) {
    let msg = `${res.status} ${res.statusText}`;
    let data: any;
    try {
      data = await res.json();
      if (data.error) msg = data.error;
    } catch {}
    throw new ApiError(msg, data);
  }
  return res.json();
}

// One file in a "hardlinked" 409 conflict: it shares its data with
// another link (usually a seeding torrent), so replacing it frees no
// space until that link is removed.
export type HardlinkedFile = { id: number; path: string; nlink: number };

// hardlinkedFiles reads the file list out of a caught error, if it was a
// "hardlinked" conflict; otherwise returns null so the caller re-throws.
export function hardlinkedFiles(err: unknown): HardlinkedFile[] | null {
  if (err instanceof ApiError && err.data?.error === "hardlinked" && Array.isArray(err.data.files)) {
    return err.data.files;
  }
  return null;
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
      disk_pressure?: boolean;
    }>("/api/v1/system"),

  config: () => req<Config>("/api/v1/config"),
  saveConfig: (c: Partial<Config>) => req<Config>("/api/v1/config", { method: "PUT", body: JSON.stringify(c) }),

  hw: () =>
    req<HwInfo>("/api/v1/hw"),
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
    confirm_hardlinked?: boolean;
    skip_hardlinked?: boolean;
  }) => post<{ created: number; skipped: number }>("/api/v1/show/queue", body),

  file: (id: number) =>
    req<{ file: FileItem; streams: Stream[]; autopilot?: AutopilotDecision; codec_penalty_warning?: string }>(`/api/v1/files/${id}`),
  autopilotPreview: () => req<{ groups: AutopilotPreviewGroup[] }>("/api/v1/autopilot/preview"),
  plan: (id: number, settings?: Settings) => post<Plan>(`/api/v1/files/${id}/plan`, { settings }),
  queueFile: (id: number, body: { settings?: Settings; run_now?: boolean; confirm_hardlinked?: boolean }) =>
    post<Job>(`/api/v1/files/${id}/queue`, body),
  previewFile: (id: number, body: { settings?: Settings; segments?: number; starts?: number[] }) =>
    post<Preview>(`/api/v1/files/${id}/preview`, body),
  upscaleInfo: (id: number) => req<UpscaleInfo>(`/api/v1/files/${id}/upscale`),
  still: (id: number, settings: Settings, at: number) =>
    post<Still>(`/api/v1/files/${id}/still`, { settings, at }),

  fixFile: (id: number, run_now = false, confirm_hardlinked = false) =>
    post<Job>(`/api/v1/files/${id}/fix`, { run_now, confirm_hardlinked }),
  issues: () => req<{ types: IssueType[] }>("/api/v1/issues"),
  mixedSeasons: (show?: string) => req<{ seasons: MixedSeason[] }>(`/api/v1/issues/mixed?${qs({ show })}`),
  fixIssue: (key: string, body: { library?: string; show?: string; run_now?: boolean; confirm_hardlinked?: boolean; skip_hardlinked?: boolean }) =>
    post<{ queued: number; skipped: number }>(`/api/v1/issues/${key}/fix`, body),
  composition: (library?: string) => req<Composition>(`/api/v1/libraries/composition?${qs({ library })}`),
  measure: () => req<MeasureStatus>("/api/v1/measure"),
  stats: () => req<HistoryStats>("/api/v1/stats"),

  jobs: (status?: string, limit = 100) => req<{ jobs: Job[] }>(`/api/v1/jobs?${qs({ status, limit })}`),
  job: (id: number) => req<{ job: Job; progress: Progress | null }>(`/api/v1/jobs/${id}`),
  cancelJob: (id: number) => post<{ ok: boolean }>(`/api/v1/jobs/${id}/cancel`),
  retryJob: (id: number, confirm_hardlinked = false) =>
    post<{ ok: boolean }>(`/api/v1/jobs/${id}/retry`, confirm_hardlinked ? { confirm_hardlinked } : undefined),
  runNowJob: (id: number) => post<{ ok: boolean }>(`/api/v1/jobs/${id}/run-now`),

  intake: (state?: string) => req<{ rows: IntakeRow[] }>(`/api/v1/intake?${qs({ state })}`),
  intakeApprove: (id: number) => post<{ ok: boolean }>(`/api/v1/intake/${id}/approve`),
  intakeDismiss: (id: number) => post<{ ok: boolean }>(`/api/v1/intake/${id}/dismiss`),
  intakeApproveBulk: (ids: number[]) => post<{ approved: number; failed: number }>("/api/v1/intake/approve", { ids }),
  intakeDismissBulk: (ids: number[]) => post<{ dismissed: number }>("/api/v1/intake/dismiss", { ids }),
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

  plexStatus: () => req<PlexStatus>("/api/v1/plex/status"),
  plexTest: (body: { url?: string; token?: string; path_map?: string }) => post<PlexTest>("/api/v1/plex/test", body),
  plexSync: () => post<{ started: boolean }>("/api/v1/plex/sync"),

  // id is the stored instance's id, or "new" for one not yet saved.
  arrTest: (id: string, body: { url?: string; api_key?: string; kind?: string; path_map?: string }) =>
    post<ArrTestResult>(`/api/v1/arr/${id}/test`, body),
  arrGet: (id: string) => req<ArrInfo>(`/api/v1/arr/${id}`),
  arrAck: (id: string) => post<{ ok: boolean }>(`/api/v1/arr/${id}/ack`),
  arrSync: () => post<{ started: boolean; syncing?: boolean }>("/api/v1/arr/sync"),
  arrRenameTag: (id: string, oldName: string, newName: string) =>
    post<{ ok: boolean; renamed?: boolean; error?: string }>(`/api/v1/arr/${id}/rename-tag`, { old_name: oldName, new_name: newName }),
  arrRegenerateWebhookToken: (id: string) => post<{ ok: boolean; token: string }>(`/api/v1/arr/${id}/regenerate-webhook-token`),

  preview: (id: string) => req<Preview>(`/api/v1/previews/${id}`),
  previews: () => req<{ previews: Preview[] }>("/api/v1/previews"),
  previewClipURL: (id: string, clip: string) => `/api/v1/previews/${id}/${clip}`,

  // Maintenance: reset caches/derived state without touching media files.
  clearMeasurements: () => post<{ cleared: number }>("/api/v1/maintenance/measurements/clear"),
  clearHistory: () => post<{ cleared: number }>("/api/v1/maintenance/history/clear"),
  clearCalibration: () => post<{ ok: boolean }>("/api/v1/maintenance/calibration/clear"),
  clearCrop: () => post<{ cleared: number }>("/api/v1/maintenance/crop/clear"),
  clearIssueTags: () => post<{ cleared: number }>("/api/v1/maintenance/issues/clear"),
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
