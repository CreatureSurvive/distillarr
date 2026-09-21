// Formatting helpers + small utilities.

export function bytes(b: number | undefined | null): string {
  if (b === undefined || b === null || isNaN(b)) return "—";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let v = b;
  let u = 0;
  while (v >= 1024 && u < units.length - 1) {
    v /= 1024;
    u++;
  }
  return `${v >= 100 || u === 0 ? Math.round(v) : v.toFixed(1)} ${units[u]}`;
}

export function bitrate(bps: number | undefined | null): string {
  if (!bps) return "—";
  if (bps >= 1e6) return `${(bps / 1e6).toFixed(1)} Mb/s`;
  return `${Math.round(bps / 1e3)} kb/s`;
}

export function pct(p: number | undefined | null, digits = 0): string {
  if (p === undefined || p === null || isNaN(p)) return "—";
  return `${p.toFixed(digits)}%`;
}

export function dur(sec: number | undefined | null): string {
  if (!sec || sec < 0) return "—";
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = Math.floor(sec % 60);
  if (h > 0) return `${h}h ${String(m).padStart(2, "0")}m`;
  if (m > 0) return `${m}m ${String(s).padStart(2, "0")}s`;
  return `${s}s`;
}

export function codecLabel(c: string): string {
  const map: Record<string, string> = {
    h264: "H.264",
    hevc: "HEVC",
    av1: "AV1",
    mpeg2video: "MPEG-2",
    vc1: "VC-1",
    mpeg4: "MPEG-4",
    vp9: "VP9",
  };
  return map[c] || c.toUpperCase();
}

export function hdrLabel(h: string): string {
  const map: Record<string, string> = {
    hdr10: "HDR10",
    hlg: "HLG",
    dolby_vision: "Dolby Vision",
  };
  return map[h] || "";
}

export function channelsLabel(c: number): string {
  if (c === 8) return "7.1";
  if (c === 6) return "5.1";
  if (c === 2) return "2.0";
  if (c === 1) return "1.0";
  return `${c}ch`;
}

export function backendLabel(b: string): string {
  const map: Record<string, string> = {
    qsv: "QSV",
    vaapi: "VAAPI",
    nvenc: "NVENC",
    sw: "CPU",
    auto: "Auto",
  };
  return map[b] || b.toUpperCase();
}

export function minutesToHM(m: number): string {
  const h = Math.floor(m / 60);
  const mm = m % 60;
  return `${String(h).padStart(2, "0")}:${String(mm).padStart(2, "0")}`;
}

export const DAY_LABELS = ["Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"];

export function daysBitmap(days: number): string {
  return DAY_LABELS.map((_, i) => (days & (1 << i) ? DAY_LABELS[i] : null))
    .filter(Boolean)
    .join(" ") || "no days";
}

// hue from a title string — deterministic typographic poster tint.
export function titleHue(s: string): number {
  let h = 0;
  for (let i = 0; i < s.length; i++) {
    h = (h * 31 + s.charCodeAt(i)) % 360;
  }
  return h;
}
