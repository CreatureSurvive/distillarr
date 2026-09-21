import { type FileItem } from "./api";
import { bytes, bitrate, codecLabel, hdrLabel, titleHue } from "./format";

// Deterministic typographic poster when no Jellyfin art exists.
export function Poster({ item, ratio = "2 / 3" }: { item: FileItem; ratio?: string }) {
  const hue = titleHue(item.title);
  const hue2 = (hue + 50) % 360;
  if (item.poster) {
    return (
      <div className="poster" style={{ aspectRatio: ratio }}>
        <img src={item.poster} alt="" loading="lazy" />
      </div>
    );
  }
  return (
    <div
      className="poster poster-typo"
      style={{
        aspectRatio: ratio,
        background: `linear-gradient(160deg,
          hsl(${hue} 32% 20%) 0%,
          hsl(${hue2} 26% 11%) 62%,
          hsl(${hue2} 30% 7%) 100%)`,
      }}
    >
      <div className="poster-title">{item.title}</div>
      <div className="poster-meta mono">
        {item.year > 0 && <span>{item.year}</span>}
        <span>{item.height > 0 ? `${item.height}p` : ""}</span>
      </div>
      <div
        className="poster-glyph"
        style={{ background: `hsl(${hue} 60% 55%)` }}
        aria-hidden
      />
    </div>
  );
}

// VU-style split gauge: kept vs saved.
export function SavingsGauge({ pctv, height = 5 }: { pctv: number; height?: number }) {
  const clamped = Math.max(0, Math.min(100, pctv));
  return (
    <div className="gauge" style={{ height }} role="img" aria-label={`${clamped.toFixed(0)}% estimated savings`}>
      <div className="gauge-saved" style={{ width: `${clamped}%` }} />
      <div className="gauge-tick" style={{ left: `${clamped}%` }} />
    </div>
  );
}

export function CodecChips({ item }: { item: FileItem }) {
  const codec = item.video_codec;
  const cls =
    codec === "hevc" ? "c-hevc" : codec === "av1" ? "c-av1" : codec === "h264" ? "c-h264" : "c-old";
  return (
    <div className="chips">
      <span className={`chip ${cls}`}>{codecLabel(codec)}</span>
      {item.height > 0 && <span className="chip">{item.height}p</span>}
      {item.bit_depth >= 10 && <span className="chip">10-bit</span>}
      {item.hdr && <span className="chip c-hdr">{hdrLabel(item.hdr)}</span>}
    </div>
  );
}

// Grid card for movies / shows.
export function FileCard({ item }: { item: FileItem }) {
  return (
    <div className={`fcard${item.queued ? " queued" : ""}`}>
      <a className="fcard-link" href={`#/file/${item.id}`} aria-label={item.title}>
        <Poster item={item} />
        <div className="fcard-body">
          <div className="fcard-title">
            {item.library === "tvshows"
              ? `S${String(item.season).padStart(2, "0")}E${String(item.episode).padStart(2, "0")} · ${item.ep_title || item.title}`
              : item.title}
          </div>
          <div className="fcard-meta mono dim">
            {bytes(item.size)} · {bitrate(item.video_bitrate)}
          </div>
          <CodecChips item={item} />
        </div>
      </a>
    </div>
  );
}
