import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, type Preview } from "../api";
import { bytes, codecLabel, dur } from "../format";
import type { LiveState } from "../App";
import "../components.css";

// A/B compare: dual synced players over the same timeline.
export default function PreviewView({ live }: { live: LiveState }) {
  const { id } = useParams<{ id: string }>();
  const [p, setP] = useState<Preview | null>(null);
  const [segIdx, setSegIdx] = useState(0);
  const [sync, setSync] = useState(true);
  const srcRef = useRef<HTMLVideoElement>(null);
  const encRef = useRef<HTMLVideoElement>(null);
  const [swapping, setSwapping] = useState(false);

  useEffect(() => {
    if (id) api.preview(id).then(setP).catch(() => setP(null));
  }, [id, live.previewVersion]);

  if (!p) {
    return (
      <div className="empty">
        <div className="big">Cutting samples…</div>
        <div className="dim">Encoding short A/B clips with the proposed settings. This takes under a minute on QSV.</div>
      </div>
    );
  }
  if (p.status === "failed") {
    return (
      <div className="empty">
        <div className="big">Preview failed</div>
        <div className="dim mono">{p.error}</div>
      </div>
    );
  }

  const seg = p.segments[segIdx];
  const srcURL = api.previewClipURL(p.id, seg.src_path);
  const encURL = api.previewClipURL(p.id, seg.enc_path);

  const keepSync = (who: "src" | "enc") => {
    if (!sync) return;
    const a = srcRef.current, b = encRef.current;
    if (!a || !b) return;
    const from = who === "src" ? a : b;
    const to = who === "src" ? b : a;
    if (Math.abs(from.currentTime - to.currentTime) > 0.12) to.currentTime = from.currentTime;
  };

  const togglePlay = () => {
    const a = srcRef.current, b = encRef.current;
    if (!a || !b) return;
    if (a.paused) { a.play(); b.play(); } else { a.pause(); b.pause(); }
  };

  const seekRel = (d: number) => {
    const a = srcRef.current, b = encRef.current;
    if (!a || !b) return;
    const t = Math.max(0, Math.min(seg.len, a.currentTime + d));
    a.currentTime = t; b.currentTime = t;
  };

  return (
    <div>
      <div className="crumbs">
        <Link to="/">library</Link> / <Link to={`/file/${p.file_id}`}>file</Link> / <span className="dim">preview</span>
      </div>

      <div className="page-head">
        <div>
          <h1 className="page-title">A / B Compare</h1>
          <div className="page-sub">
            {codecLabel(p.settings.codec)} 10-bit · quality {p.settings.quality} · {(p.settings.backend || "auto").toUpperCase()}
          </div>
        </div>
        <div className="toolbar">
          <button className="btn" onClick={() => setSync(!sync)}>
            {sync ? "🔒 synced" : "🔓 independent"}
          </button>
        </div>
      </div>

      {p.status === "running" && (
        <div className="empty" style={{ padding: 30 }}>
          <div className="big">Encoding samples…</div>
        </div>
      )}

      {p.status === "ready" && (
        <>
          <div className={`ab-wrap${swapping ? " swap" : ""}`}>
            <figure className="ab-side">
              <video
                ref={srcRef}
                src={srcURL}
                controls={false}
                playsInline
                onTimeUpdate={() => keepSync("src")}
                onPlay={() => sync && encRef.current?.play()}
                onPause={() => sync && encRef.current?.pause()}
              />
              <figcaption>
                <span className="ab-tag">A · source</span>
                <span className="mono dim">{bytes(seg.src_size)}</span>
                {seg.proxy && <span className="chip" title="source codec can't direct-play in browsers; this side is a visually lossless proxy">proxy</span>}
              </figcaption>
            </figure>
            <figure className="ab-side">
              <video
                ref={encRef}
                src={encURL}
                controls={false}
                playsInline
                onTimeUpdate={() => keepSync("enc")}
                onPlay={() => sync && srcRef.current?.play()}
                onPause={() => sync && srcRef.current?.pause()}
              />
              <figcaption>
                <span className="ab-tag teal">B · {codecLabel(p.settings.codec)}</span>
                <span className="mono dim">
                  {bytes(seg.enc_size)}
                  {seg.src_size > 0 && (
                    <span style={{ color: "var(--teal)" }}>
                      {" "}−{Math.max(0, Math.round((1 - seg.enc_size / seg.src_size) * 100))}%
                    </span>
                  )}
                </span>
              </figcaption>
            </figure>
          </div>

          <div className="toolbar" style={{ justifyContent: "center", marginTop: 14 }}>
            <button className="btn" onClick={() => seekRel(-2)}>−2s</button>
            <button className="btn btn-primary" onClick={togglePlay}>play / pause</button>
            <button className="btn" onClick={() => seekRel(2)}>+2s</button>
            <button className="btn" onClick={() => setSwapping(!swapping)}>
              {swapping ? "stacked" : "side by side"}
            </button>
          </div>

          <div className="toolbar" style={{ marginTop: 18 }}>
            <span className="stat-label" style={{ alignSelf: "center" }}>Samples:</span>
            {p.segments.map((s, i) => (
              <button key={i} className={`chip${i === segIdx ? " c-hevc" : ""}`} style={{ cursor: "pointer" }} onClick={() => setSegIdx(i)}>
                #{i + 1} · {dur(s.start)} in
              </button>
            ))}
            <span className="dim" style={{ fontSize: 12, marginLeft: 6 }}>
              20s each, spread across the file. Compare fine grain and dark scenes.
            </span>
          </div>
        </>
      )}
    </div>
  );
}
