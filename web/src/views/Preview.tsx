import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, type Preview } from "../api";
import { backendLabel, bytes, codecLabel, dur } from "../format";
import { Copyable } from "../components";
import type { LiveState } from "../App";

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
  const up = p.settings.upscale_to ?? 0; // an upscale grows the file on purpose: "% smaller" and VMAF don't apply
  const upLabel = up === 2160 ? "4K" : `${up}p`;
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
        <Link to={`/file/${p.file_id}`}>← Back to file</Link>
      </div>

      <div className="page-head">
        <div>
          <h1 className="page-title">A / B Compare</h1>
          <div className="page-sub">
            {codecLabel(p.settings.codec)} {p.settings.bit_depth === 8 ? "8" : "10"}-bit · quality {p.settings.quality} · {p.settings.speed || "medium"} · {backendLabel(p.settings.backend || "auto")}
            {up > 0
              ? <> · <span className="teal">{upLabel} upscale{p.measured_ratio ? `, video ${p.measured_ratio.toFixed(1)}× the source's size` : ""}</span></>
              : p.measured_ratio ? <> · <span className="teal">video {Math.round((1 - p.measured_ratio) * 100)}% smaller across all samples</span></> : null}
          </div>
        </div>
        <div className="toolbar">
          <button className="btn" onClick={() => setSync(!sync)}>
            {sync ? "Players linked" : "Players independent"}
          </button>
        </div>
      </div>

      {p.status === "running" && (
        <div className="empty" style={{ padding: 30 }}>
          <div className="big">{p.settings.vmaf_target ? "Measuring quality…" : "Encoding samples…"}</div>
          {p.stage && <div className="dim mono">{p.stage}</div>}
          {p.settings.vmaf_target ? (
            <div className="dim small" style={{ marginTop: 8 }}>
              Encoding three 15-second samples at a few quality levels and scoring each against the original.
              About a minute on the Arc.
            </div>
          ) : null}
        </div>
      )}

      {p.tune && (
        <section className="panel tune-panel">
          <div className="eyebrow">Quality search · target VMAF {p.tune.target}</div>
          <p className="verdict-reason" style={{ marginTop: 0 }}>
            {p.tune.met
              ? <>Quality <b>{p.tune.quality}</b> is the smallest setting that reaches the target: VMAF <b>{p.tune.vmaf.mean.toFixed(1)}</b> (worst moments {p.tune.vmaf.p5.toFixed(1)}), video at <b>{Math.round(p.tune.ratio * 100)}%</b> of the source's size.</>
              : <>No tested setting reached VMAF {p.tune.target}; the best was {p.tune.vmaf.mean.toFixed(1)} at quality {p.tune.quality}. The source's own compression limits how close a re-encode can get. This file may not be worth re-encoding.</>}
          </p>
          <ul className="tune-steps">
            {p.tune.steps.map((st) => (
              <li key={st.quality} className={st.quality === p.tune!.quality ? "chosen" : ""}>
                <span className="mono">q{st.quality}</span>
                <span className={`mono ${st.pass ? "teal" : "warm"}`}>VMAF {st.vmaf.mean.toFixed(1)}</span>
                <span className="mono dim">low {st.vmaf.p5.toFixed(1)}</span>
                <span className="mono dim">{Math.round(st.ratio * 100)}% size</span>
                {st.quality === p.tune!.quality && <span className="tag tag-save">chosen</span>}
              </li>
            ))}
          </ul>
        </section>
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
                <span className="ab-tag teal">B · {up > 0 ? `${upLabel} upscale` : `${codecLabel(p.settings.codec)} q${p.settings.quality}`}</span>
                {!up && seg.vmaf && <span className={`mono ${seg.vmaf.mean >= 93 ? "teal" : seg.vmaf.mean >= 90 ? "" : "warm"}`}>VMAF {seg.vmaf.mean.toFixed(1)}</span>}
                <span className="mono dim">
                  {bytes(seg.enc_size)}
                  {seg.src_size > 0 && (up > 0
                    ? <span className="dim"> {(seg.enc_size / seg.src_size).toFixed(1)}×</span>
                    : <span style={{ color: "var(--teal)" }}>
                        {" "}−{Math.max(0, Math.round((1 - seg.enc_size / seg.src_size) * 100))}%
                      </span>)}
                </span>
              </figcaption>
            </figure>
          </div>

          <div className="toolbar" style={{ justifyContent: "center", marginTop: 14 }}>
            <button className="btn" onClick={() => seekRel(-2)}>−2s</button>
            <button className="btn btn-primary" onClick={togglePlay}>Play / pause</button>
            <button className="btn" onClick={() => seekRel(2)}>+2s</button>
            <button className="btn" onClick={() => setSwapping(!swapping)}>
              {swapping ? "Side by side" : "Stacked"}
            </button>
          </div>

          <div className="toolbar" style={{ marginTop: 18 }}>
            <span className="stat-label" style={{ alignSelf: "center" }}>Samples:</span>
            {p.segments.map((s, i) => (
              <button key={i} className={`chip${i === segIdx ? " c-hevc" : ""}`} style={{ cursor: "pointer" }} onClick={() => setSegIdx(i)}>
                #{i + 1} · {dur(s.start)} in
              </button>
            ))}
            <span className="dim small" style={{ marginLeft: 6 }}>
              20s each, spread across the file. Compare fine grain and dark scenes.
            </span>
          </div>
          {p.command && (
            <details className="panel cmd-panel" style={{ marginTop: 18 }}>
              <summary>ffmpeg command for the B side</summary>
              <Copyable text={p.command} />
            </details>
          )}
        </>
      )}
    </div>
  );
}
