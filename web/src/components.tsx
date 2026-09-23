import { useEffect, useState, type ReactNode } from "react";
import { type FileItem, type HardlinkedFile, type Series } from "./api";
import { bitrate, bytes, codecLabel, hdrLabel, resLabel, se, titleHue } from "./format";

// Artwork with a deterministic typographic fallback (no Jellyfin, or no art).
export function Art({
  src, title, sub, ratio = "2 / 3", className = "",
}: { src?: string; title: string; sub?: string; ratio?: string; className?: string }) {
  const [failed, setFailed] = useState(false);
  useEffect(() => setFailed(false), [src]);
  const hue = titleHue(title);
  if (src && !failed) {
    return (
      <div className={`art ${className}`} style={{ aspectRatio: ratio }}>
        <img src={src} alt="" loading="lazy" decoding="async" onError={() => setFailed(true)} />
      </div>
    );
  }
  return (
    <div
      className={`art art-typo ${className}`}
      style={{
        aspectRatio: ratio,
        background: `linear-gradient(160deg, hsl(${hue} 30% 21%) 0%, hsl(${(hue + 50) % 360} 26% 11%) 65%, hsl(${(hue + 50) % 360} 30% 7%) 100%)`,
      }}
    >
      <span className="art-bar" style={{ background: `hsl(${hue} 60% 58%)` }} aria-hidden />
      <div className="art-title">{title}</div>
      {sub && <div className="art-sub mono">{sub}</div>}
    </div>
  );
}

// Split gauge: the filled part is the space that would be saved.
export function SavingsGauge({ pct, height = 5 }: { pct: number; height?: number }) {
  const v = Math.max(0, Math.min(100, pct || 0));
  return (
    <div className="gauge" style={{ height }} role="img" aria-label={`${v.toFixed(0)}% smaller`}>
      <div className="gauge-fill" style={{ width: `${v}%` }} />
    </div>
  );
}

export function CodecChip({ codec }: { codec: string }) {
  const cls = codec === "hevc" ? "c-hevc" : codec === "av1" ? "c-av1" : codec === "h264" ? "c-h264" : "";
  return <span className={`chip ${cls}`}>{codecLabel(codec)}</span>;
}

export function FileChips({ f }: { f: FileItem }) {
  return (
    <div className="chips">
      <CodecChip codec={f.video_codec} />
      {f.height > 0 && <span className="chip" title={`${f.width}×${f.height}`}>{resLabel(f.width, f.height)}</span>}
      {f.hdr && <span className="chip c-hdr">{hdrLabel(f.hdr)}</span>}
      {f.nlink > 1 && (
        <span className="chip c-warn" title="Shares its data with another file (often a seeding torrent). Replacing it frees no space until the other link is removed.">
          hardlinked
        </span>
      )}
      {f.arr && (
        <span className="chip" title={`Managed by ${f.arr.instance_name}${f.arr.monitored ? "" : " (unmonitored)"}`}>
          {f.arr.instance_name}
        </span>
      )}
      {f.upscaled && (
        <span className="chip c-up" title={`Upscaled to ${f.upscaled.to === 2160 ? "4K" : f.upscaled.to + "p"} with ${f.upscaled.preset}${f.upscaled.at ? " on " + f.upscaled.at.slice(0, 10) : ""}`}>
          upscaled
        </span>
      )}
    </div>
  );
}

// The savings callout shown on cards and rows.
export function SavingsTag({ f }: { f: FileItem }) {
  const r = f.rec;
  if (f.queued) return <span className="tag tag-queued">queued</span>;
  if (!r) return null;
  if (r.action === "transcode") return <span className="tag tag-save">−{bytes(f.size - r.est_out_bytes)}</span>;
  if (r.action === "caution") return <span className="tag tag-warn">DV</span>;
  return null;
}

// Short issue names for chips (full text comes from /api/v1/issues).
export const ISSUE_SHORT: Record<string, { label: string; fix: "quick" | "reencode" | "info" }> = {
  no_hvc1: { label: "not hvc1", fix: "quick" },
  no_faststart: { label: "no faststart", fix: "quick" },
  pcm_audio: { label: "PCM audio", fix: "quick" },
  legacy_container: { label: "legacy container", fix: "quick" },
  legacy_codec: { label: "legacy codec", fix: "reencode" },
  interlaced: { label: "interlaced", fix: "reencode" },
  worth_reencoding: { label: "worth re-encoding", fix: "reencode" },
  quality_limited: { label: "can't reach target", fix: "info" },
  upgrade_pending: { label: "upgrade pending", fix: "info" },
};

export function issueKeys(f: FileItem): string[] {
  return (f.issues || "").split(",").filter(Boolean);
}

export function hasQuickFix(f: FileItem): boolean {
  return issueKeys(f).some((k) => ISSUE_SHORT[k]?.fix === "quick");
}

export function IssueChips({ f, skip = [] }: { f: FileItem; skip?: string[] }) {
  const keys = issueKeys(f).filter((k) => !skip.includes(k));
  if (keys.length === 0) return null;
  return (
    <div className="chips">
      {keys.map((k) => (
        <span key={k} className={`chip issue-${ISSUE_SHORT[k]?.fix || "info"}`}>{ISSUE_SHORT[k]?.label || k}</span>
      ))}
    </div>
  );
}

export function MovieCard({ f }: { f: FileItem }) {
  return (
    <a className="card-link" href={`#/file/${f.id}`}>
      <div className="pcard">
        <Art src={f.image} title={f.title} sub={f.year ? String(f.year) : undefined} />
        <div className="pcard-overlay">
          <SavingsTag f={f} />
          {hasQuickFix(f) && !f.queued && <span className="tag tag-fix" title="Has issues a quick remux fixes">fix</span>}
        </div>
      </div>
      <div className="pcard-body">
        <div className="pcard-title">{f.title}</div>
        <div className="pcard-meta mono">
          {f.year > 0 && <span>{f.year}</span>}
          <span>{bytes(f.size)}</span>
          <span className={f.video_codec === "hevc" || f.video_codec === "av1" ? "ok" : "warm"}>
            {codecLabel(f.video_codec)}
          </span>
          {f.arr ? (
            <span title={`Managed by ${f.arr.instance_name}${f.arr.monitored ? "" : " (unmonitored)"}`}>{f.arr.instance_name}</span>
          ) : (
            <span className="dim" title="No connected Sonarr/Radarr instance manages this file">unmanaged</span>
          )}
        </div>
      </div>
    </a>
  );
}

export function ShowCard({ s }: { s: Series }) {
  return (
    <a className="card-link" href={`#/show/${encodeURIComponent(s.title)}`}>
      <div className="pcard">
        <Art src={s.image} title={s.title} sub={s.year ? String(s.year) : undefined} />
        <div className="pcard-overlay">
          {s.reclaimable > 0 && <span className="tag tag-save">−{bytes(s.reclaimable)}</span>}
        </div>
      </div>
      <div className="pcard-body">
        <div className="pcard-title">{s.title}</div>
        <div className="pcard-meta mono">
          <span>{s.seasons} season{s.seasons === 1 ? "" : "s"}</span>
          <span>{bytes(s.total_size)}</span>
          {s.avg_bitrate > 0 && <span>{bitrate(s.avg_bitrate)}</span>}
        </div>
      </div>
    </a>
  );
}

export function EpisodeLabel({ f }: { f: FileItem }) {
  return (
    <>
      <span className="mono dim">{se(f.season, f.episode)}</span> {f.jf_name || f.ep_title || `Episode ${f.episode}`}
    </>
  );
}

// Segmented control (wraps on narrow screens).
export function Seg<T extends string | number>({
  value, options, onChange, label,
}: { value: T; options: { value: T; label: ReactNode; hint?: string }[]; onChange: (v: T) => void; label?: string }) {
  return (
    <div className="seg" role="radiogroup" aria-label={label}>
      {options.map((o) => (
        <button
          key={String(o.value)}
          role="radio"
          aria-checked={o.value === value}
          title={o.hint}
          className={`seg-btn${o.value === value ? " on" : ""}`}
          onClick={() => onChange(o.value)}
        >
          {o.label}
        </button>
      ))}
    </div>
  );
}

export function Toggle({ on, onChange, label, hint }: { on: boolean; onChange: (v: boolean) => void; label: string; hint?: string }) {
  return (
    <label className="toggle">
      <input type="checkbox" checked={on} onChange={(e) => onChange(e.target.checked)} />
      <span className="toggle-track" aria-hidden />
      <span>
        {label}
        {hint && <span className="toggle-hint">{hint}</span>}
      </span>
    </label>
  );
}

// Tiny global toast.
let pushToast: (m: string, kind?: "ok" | "err") => void = () => {};
export const toast = (m: string, kind: "ok" | "err" = "ok") => pushToast(m, kind);

export function Toaster() {
  const [t, setT] = useState<{ m: string; kind: string; id: number } | null>(null);
  useEffect(() => {
    pushToast = (m, kind = "ok") => setT({ m, kind, id: Date.now() });
  }, []);
  useEffect(() => {
    if (!t) return;
    const h = setTimeout(() => setT(null), 3800);
    return () => clearTimeout(h);
  }, [t]);
  if (!t) return null;
  return (
    <div className={`toast ${t.kind === "err" ? "toast-err" : ""}`} role="status" key={t.id}>
      {t.m}
    </div>
  );
}

// Shared confirmation for queueing a hardlinked file (one that shares its
// data with another link, usually a seeding torrent): replacing it frees
// no space until the other link is removed. ask() resolves to "confirm"
// (queue it anyway), "skip" (bulk only: drop just these and continue), or
// "cancel". Render {dialog} once near the top of the view that calls ask().
export function useHardlinkedConfirm() {
  const [pending, setPending] = useState<{ files: HardlinkedFile[]; allowSkip: boolean; resolve: (v: "confirm" | "skip" | "cancel") => void } | null>(null);

  const ask = (files: HardlinkedFile[], allowSkip = false) =>
    new Promise<"confirm" | "skip" | "cancel">((resolve) => setPending({ files, allowSkip, resolve }));

  const settle = (v: "confirm" | "skip" | "cancel") => {
    pending?.resolve(v);
    setPending(null);
  };

  const dialog = pending && (
    <div className="modal-backdrop" onClick={(e) => e.target === e.currentTarget && settle("cancel")}>
      <div className="modal" role="dialog" aria-label="Shares data with another file">
        <h2 className="panel-title">{pending.files.length === 1 ? "Shares data with another file" : `${pending.files.length} files share data with another file`}</h2>
        <p className="dim small">
          {pending.files.length === 1 ? "This file" : "These files"} still {pending.files.length === 1 ? "has" : "have"} another
          hardlink somewhere — usually a torrent still seeding it. Replacing {pending.files.length === 1 ? "it" : "them"} frees no
          disk space until that other link is removed.
        </p>
        <ul className="mono small" style={{ maxHeight: 180, overflowY: "auto", margin: "10px 0" }}>
          {pending.files.slice(0, 12).map((f) => <li key={f.id}>{f.path.split("/").pop()}</li>)}
          {pending.files.length > 12 && <li className="dim">…and {pending.files.length - 12} more</li>}
        </ul>
        <div className="toolbar" style={{ marginTop: 14 }}>
          <button className="btn btn-primary" onClick={() => settle("confirm")}>Queue anyway</button>
          {pending.allowSkip && <button className="btn" onClick={() => settle("skip")}>Skip these</button>}
          <button className="btn" onClick={() => settle("cancel")}>Cancel</button>
        </div>
      </div>
    </div>
  );

  return { ask, dialog };
}

export function Empty({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="empty">
      <div className="big">{title}</div>
      {children && <div className="dim">{children}</div>}
    </div>
  );
}

export function Copyable({ text }: { text: string }) {
  const [done, setDone] = useState(false);
  return (
    <div className="copyable">
      <pre className="cmd">{text}</pre>
      <button
        className="btn mini copy-btn"
        onClick={() => {
          navigator.clipboard?.writeText(text).then(() => {
            setDone(true);
            setTimeout(() => setDone(false), 1500);
          });
        }}
      >
        {done ? "Copied" : "Copy"}
      </button>
    </div>
  );
}
