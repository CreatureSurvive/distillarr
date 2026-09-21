import { useEffect, useState, type ReactNode } from "react";
import { type FileItem, type Series } from "./api";
import { bytes, codecLabel, hdrLabel, resLabel, se, titleHue } from "./format";

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
      {f.height > 0 && <span className="chip">{resLabel(f.height)}</span>}
      {f.hdr && <span className="chip c-hdr">{hdrLabel(f.hdr)}</span>}
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

export function MovieCard({ f }: { f: FileItem }) {
  return (
    <a className="card-link" href={`#/file/${f.id}`}>
      <div className="pcard">
        <Art src={f.image} title={f.title} sub={f.year ? String(f.year) : undefined} />
        <div className="pcard-overlay">
          <SavingsTag f={f} />
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
