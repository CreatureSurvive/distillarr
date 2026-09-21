import { useEffect, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api, type FileItem, type Stream, type Recommendation, type Settings, type Preview } from "../api";
import { bytes, bitrate, codecLabel, hdrLabel, channelsLabel, dur, backendLabel } from "../format";
import { Poster, SavingsGauge } from "../components";
import type { LiveState } from "../App";
import "../components.css";

export default function FileDetail({ live }: { live: LiveState }) {
  const { id } = useParams<{ id: string }>();
  const nav = useNavigate();
  const [file, setFile] = useState<FileItem | null>(null);
  const [streams, setStreams] = useState<Stream[]>([]);
  const [rec, setRec] = useState<Recommendation | null>(null);
  const [quality, setQuality] = useState<number | null>(null);
  const [busy, setBusy] = useState("");
  const [toast, setToast] = useState("");

  const load = async () => {
    if (!id) return;
    try {
      const r = await api.file(Number(id));
      setFile(r.file);
      setStreams(r.streams);
      const rr = await api.fileRec(Number(id));
      setRec(rr);
    } catch {}
  };
  useEffect(() => { load(); /* eslint-disable-next-line */ }, [id, live.queueVersion]);

  if (!file) {
    return <div className="empty"><div className="big">Loading…</div></div>;
  }

  const effectiveQuality = quality ?? rec?.settings?.quality ?? 60;
  const codec = rec?.settings?.codec ?? "hevc";

  const queue = async (runNow: boolean) => {
    setBusy(runNow ? "now" : "queue");
    try {
      const settings: Settings = {
        ...(rec?.settings ?? {}),
        quality: effectiveQuality,
        codec,
      };
      await api.queueFile(file!.id, { settings, run_now: runNow });
      setToast(runNow ? "Encoding now — watch the deck." : "Added to queue.");
      load();
    } catch (e: any) {
      setToast(e.message);
    } finally {
      setBusy("");
    }
  };

  const makePreview = async () => {
    setBusy("preview");
    try {
      const settings: Settings = { ...(rec?.settings ?? {}), quality: effectiveQuality, codec };
      const p: Preview = await api.previewFile(file!.id, { settings, segments: 3 });
      nav(`/preview/${p.id}`);
    } catch (e: any) {
      setToast(e.message);
    } finally {
      setBusy("");
    }
  };

  const isTV = file.library === "tvshows";
  const pcmAudio = file.audio.filter((a) => a.codec.startsWith("pcm_") || a.codec === "lpcm");

  return (
    <div>
      <div className="crumbs">
        <Link to="/">library</Link> / {isTV ? <>shows / <Link to={`/show/${encodeURIComponent(file.title)}`}>{file.title}</Link> /</> : null}{" "}
        <span className="dim">{file.title}</span>
      </div>

      <div className="detail-grid">
        {/* left: poster + facts */}
        <div>
          <Poster item={file} ratio="2 / 3" />
          <div className="mono dim file-path">{file.path}</div>
        </div>

        {/* right: header, rec card, streams */}
        <div style={{ minWidth: 0 }}>
          <h1 className="page-title" style={{ marginBottom: 4 }}>
            {isTV ? file.ep_title || `${file.title} — S${file.season}E${file.episode}` : file.title}
          </h1>
          <div className="page-sub" style={{ marginBottom: 16 }}>
            {codecLabel(file.video_codec)} · {file.width}×{file.height} · {file.bit_depth}-bit ·{" "}
            {bitrate(file.video_bitrate)} · {bytes(file.size)} · {dur(file.duration)}
            {file.hdr && ` · ${hdrLabel(file.hdr)}`}
          </div>

          {/* recommendation card */}
          <div className="card" style={{ marginBottom: 18, borderColor: "var(--line)" }}>
            <div className="stat-label" style={{ marginBottom: 10 }}>
              {rec?.action === "transcode" ? "Recommended encode" : rec?.action === "caution" ? "Caution" : "No change recommended"}
            </div>
            {rec?.action === "transcode" ? (
              <>
                <div style={{ display: "flex", gap: 18, flexWrap: "wrap", alignItems: "flex-end" }}>
                  <div>
                    <div className="stat-label">Target</div>
                    <div style={{ marginTop: 4 }}>
                      <span className="chip c-hevc" style={{ fontSize: 13, padding: "3px 10px" }}>
                        {codecLabel(codec)} 10-bit
                      </span>{" "}
                      <span className="chip" style={{ fontSize: 13, padding: "3px 10px" }}>
                        {backendLabel(rec.settings.backend || "auto")}
                      </span>
                    </div>
                  </div>
                  <div>
                    <div className="stat-label">Quality</div>
                    <div style={{ display: "flex", alignItems: "center", gap: 8, marginTop: 6 }}>
                      <input
                        type="range"
                        min={20}
                        max={90}
                        value={effectiveQuality}
                        onChange={(e) => setQuality(Number(e.target.value))}
                        style={{ width: 140 }}
                        aria-label="quality"
                      />
                      <span className="mono">{effectiveQuality}</span>
                    </div>
                  </div>
                  <div>
                    <div className="stat-label">Estimated</div>
                    <div className="mono" style={{ fontSize: 15, marginTop: 4 }}>
                      {bytes(rec.est_low_bytes)} – {bytes(rec.est_high_bytes)}
                    </div>
                  </div>
                  <div style={{ minWidth: 180, flex: 1 }}>
                    <div className="stat-label">Savings</div>
                    <div style={{ display: "flex", alignItems: "center", gap: 8, marginTop: 8 }}>
                      <SavingsGauge pctv={rec.savings_pct} height={7} />
                      <span className="mono" style={{ color: "var(--teal)", fontSize: 15, fontWeight: 600 }}>
                        −{rec.savings_pct.toFixed(0)}%
                      </span>
                    </div>
                  </div>
                </div>
                {pcmAudio.length > 0 && (
                  <div className="note" style={{ marginTop: 10 }}>
                    {pcmAudio.length} PCM track{pcmAudio.length > 1 ? "s" : ""} → FLAC (lossless, ~50% smaller audio)
                  </div>
                )}
                {rec.notes.map((n, i) => (
                  <div key={i} className="note">· {n}</div>
                ))}
                <div className="dim" style={{ fontSize: 12.5, marginTop: 10 }}>{rec.reason}</div>

                <div className="toolbar" style={{ marginTop: 16 }}>
                  <button className="btn" disabled={!!busy || file.queued} onClick={makePreview}>
                    {busy === "preview" ? "cutting samples…" : "Preview & compare"}
                  </button>
                  <button className="btn" disabled={!!busy || file.queued} onClick={() => queue(false)}>
                    {busy === "queue" ? "…" : "Add to queue"}
                  </button>
                  <button className="btn btn-primary" disabled={!!busy || file.queued} onClick={() => queue(true)}>
                    {busy === "now" ? "starting…" : "Encode now"}
                  </button>
                  {file.queued && <span className="chip c-hevc">already queued</span>}
                </div>
              </>
            ) : (
              <div className="dim" style={{ lineHeight: 1.55 }}>
                {rec?.reason}
                {rec?.notes?.map((n, i) => (
                  <div key={i} className="note">· {n}</div>
                ))}
                <div className="toolbar" style={{ marginTop: 14 }}>
                  <button className="btn" onClick={makePreview} disabled={!!busy}>
                    {busy === "preview" ? "cutting samples…" : "Preview anyway"}
                  </button>
                  <button className="btn" onClick={() => queue(false)} disabled={!!busy}>
                    Queue anyway
                  </button>
                </div>
              </div>
            )}
          </div>

          {/* streams */}
          <div className="card">
            <div className="stat-label" style={{ marginBottom: 10 }}>Streams</div>
            <table className="table">
              <thead>
                <tr>
                  <th style={{ width: 40 }}>#</th>
                  <th>Type</th>
                  <th>Codec</th>
                  <th>Language</th>
                  <th>Detail</th>
                  <th>Flags</th>
                </tr>
              </thead>
              <tbody>
                {streams.map((s) => (
                  <tr key={s.id}>
                    <td className="mono dim">{s.stream_index}</td>
                    <td>{s.kind}</td>
                    <td className="mono">{s.codec}</td>
                    <td className="dim">{s.lang || "—"}</td>
                    <td className="dim">
                      {s.kind === "audio"
                        ? `${channelsLabel(s.channels)}${s.bit_rate ? ` · ${bitrate(s.bit_rate)}` : ""}`
                        : s.kind === "video"
                          ? `${s.codec === file.video_codec ? `${file.width}×${file.height}` : ""}`
                          : s.is_text
                            ? "text"
                            : "bitmap"}
                    </td>
                    <td>
                      {s.default && <span className="chip">default</span>}{" "}
                      {s.forced && <span className="chip">forced</span>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>

            {file.sidecars.length > 0 && (
              <div style={{ marginTop: 12 }}>
                <div className="stat-label" style={{ marginBottom: 6 }}>External subtitles (untouched)</div>
                <div className="chips">
                  {file.sidecars.map((sc) => (
                    <span key={sc.name} className="chip">
                      {sc.lang ? `${sc.lang.toUpperCase()} ` : ""}{sc.kind}
                    </span>
                  ))}
                </div>
              </div>
            )}
          </div>
        </div>
      </div>
      {toast && (
        <div className="toast" role="status">{toast}</div>
      )}
    </div>
  );
}
