import { useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, type FileItem, type SeasonStat, type SeasonRec, type Settings } from "../api";
import { bytes, bitrate, codecLabel, dur } from "../format";
import type { LiveState } from "../App";
import { SavingsGauge } from "../components";
import "../components.css";

export default function SeriesDetail({ live }: { live: LiveState }) {
  const { title } = useParams<{ title: string }>();
  const show = decodeURIComponent(title ?? "");
  const [seasons, setSeasons] = useState<SeasonStat[]>([]);
  const [episodes, setEpisodes] = useState<FileItem[]>([]);
  const [season, setSeason] = useState(1);
  const [rec, setRec] = useState<SeasonRec | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<string>("");

  useEffect(() => {
    setSeason(1);
  }, [show]);

  useEffect(() => {
    if (!show) return;
    api.episodes(show, -1).then((r) => {
      setSeasons(r.seasons);
      setEpisodes(r.episodes);
      // default to first season with episodes
      if (r.seasons.length > 0) {
        setSeason(r.seasons[0].season);
      }
    }).catch(() => {});
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [show, live.queueVersion]);

  useEffect(() => {
    if (!show) return;
    api.episodes(show, season).then((r) => setEpisodes(r.episodes)).catch(() => {});
    api.seriesRec(show, season).then(setRec).catch(() => setRec(null));
    setResult("");
  }, [show, season, live.queueVersion]);

  const queueSeason = async (runNow: boolean) => {
    setBusy(true);
    setResult("");
    try {
      const r = await api.queueSeries({ show, season, run_now: runNow, only_worth: true });
      setResult(r.created > 0 ? `Queued ${r.created} episode${r.created === 1 ? "" : "s"}.` : "Nothing new to queue.");
    } catch (e: any) {
      setResult(e.message);
    } finally {
      setBusy(false);
    }
  };

  const settingsHint: Settings | undefined = rec?.settings;

  return (
    <div>
      <div className="crumbs">
        <Link to="/">library</Link> / shows / <span className="dim">{show}</span>
      </div>
      <div className="page-head">
        <div>
          <h1 className="page-title">{show}</h1>
          <div className="page-sub">
            {seasons.length} season{seasons.length === 1 ? "" : "s"} ·{" "}
            {bytes(seasons.reduce((a, s) => a + s.total_size, 0))} total
          </div>
        </div>
        <div className="toolbar">
          <button className="btn" disabled={busy} onClick={() => queueSeason(false)}>
            Queue season
          </button>
          <button className="btn btn-primary" disabled={busy} onClick={() => queueSeason(true)}>
            Encode now
          </button>
        </div>
      </div>

      {rec && (
        <div className="card" style={{ marginBottom: 20 }}>
          <div style={{ display: "flex", justifyContent: "space-between", gap: 14, flexWrap: "wrap" }}>
            <div>
              <div className="stat-label">Season {season} plan</div>
              <div style={{ marginTop: 6, fontSize: 14, lineHeight: 1.5 }}>
                {rec.worth_count > 0 ? (
                  <>
                    <b>{rec.worth_count}</b> of {rec.episodes} episodes worth re-encoding to{" "}
                    <b>{codecLabel(rec.settings?.codec || "hevc")}</b> 10-bit at quality {rec.settings?.quality} ·{" "}
                    median source {bitrate(rec.avg_bitrate)} · {rec.height}p
                    {settingsHint?.max_height ? ` (capped at ${settingsHint.max_height}p)` : ""}
                  </>
                ) : (
                  <span className="dim">
                    No episodes clear the savings threshold this season.
                    {rec.caution_count > 0 && ` ${rec.caution_count} Dolby Vision episode(s) excluded.`}
                  </span>
                )}
              </div>
              {rec.notes?.map((n, i) => (
                <div key={i} className="dim" style={{ fontSize: 12.5, marginTop: 4 }}>
                  · {n}
                </div>
              ))}
            </div>
            {rec.worth_count > 0 && rec.total_size > 0 && (
              <div style={{ minWidth: 210 }}>
                <div className="stat-label">Projected</div>
                <div className="mono" style={{ fontSize: 15, marginTop: 4 }}>
                  {bytes(rec.total_size)} → <b style={{ color: "var(--teal)" }}>{bytes(rec.est_out_total)}</b>
                </div>
                <div style={{ display: "flex", alignItems: "center", gap: 8, marginTop: 8 }}>
                  <SavingsGauge pctv={rec.savings_pct} />
                  <span className="mono" style={{ color: "var(--teal)", fontSize: 13 }}>
                    −{rec.savings_pct.toFixed(0)}%
                  </span>
                </div>
              </div>
            )}
          </div>
          {result && <div className="mono dim" style={{ marginTop: 10 }}>{result}</div>}
        </div>
      )}

      <div className="toolbar" style={{ marginBottom: 14 }}>
        <div className="tabs">
          {seasons.map((s) => (
            <button key={s.season} className={`tab${season === s.season ? " active" : ""}`} onClick={() => setSeason(s.season)}>
              {s.season === 0 ? "Specials" : `S${s.season}`}
            </button>
          ))}
        </div>
      </div>

      <table className="table">
        <thead>
          <tr>
            <th style={{ width: 52 }}>#</th>
            <th>Episode</th>
            <th>Codec</th>
            <th>Bitrate</th>
            <th>Size</th>
            <th style={{ width: 90 }}>Savings</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {episodes.map((ep) => {
            const saved = ep.transcode_score > 0;
            return (
              <tr key={ep.id}>
                <td className="mono dim">{ep.episode}</td>
                <td>
                  <a href={`#/file/${ep.id}`} style={{ fontWeight: 600 }}>
                    {ep.ep_title || `Episode ${ep.episode}`}
                  </a>
                  {ep.queued && <span className="badge b-running" style={{ marginLeft: 8 }}>queued</span>}
                </td>
                <td>
                  <span className="chip">{codecLabel(ep.video_codec)}</span>
                  {ep.hdr && <span className="chip c-hdr" style={{ marginLeft: 4 }}>{ep.hdr === "dolby_vision" ? "DV" : "HDR"}</span>}
                </td>
                <td className="mono dim">{bitrate(ep.video_bitrate)}</td>
                <td className="mono dim">{bytes(ep.size)}</td>
                <td>
                  {saved ? (
                    <div style={{ display: "flex", alignItems: "center", gap: 6 }}>
                      <SavingsGauge pctv={epScoreToPct(ep.transcode_score)} height={4} />
                    </div>
                  ) : (
                    <span className="faint">—</span>
                  )}
                </td>
                <td className="mono dim">{dur(ep.duration)}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {episodes.length === 0 && (
        <div className="empty">
          <div className="big">No episodes</div>
          <div>Season {season} has no indexed files yet.</div>
        </div>
      )}
    </div>
  );
}

// score ≈ 0..100 blends savings% and size impact; display as approx %.
function epScoreToPct(score: number): number {
  return Math.max(0, Math.min(100, score * 0.75));
}
