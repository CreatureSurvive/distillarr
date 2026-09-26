// SPDX-License-Identifier: GPL-3.0-or-later

import { useEffect, useMemo, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { api, hardlinkedFiles, type FileItem, type HwReport, type ShowDetail, type ShowOverrides, type ShowPlan } from "../api";
import { Art, CodecChip, Empty, SavingsGauge, Seg, toast, useHardlinkedConfirm } from "../components";
import { backendLabel, bitrate, bytes, codecLabel, hdrLabel, resLabel, se, seasonName } from "../format";
import { workingBackends } from "../options";
import type { LiveState } from "../App";

export default function ShowView({ live }: { live: LiveState }) {
  const title = decodeURIComponent(useParams<{ title: string }>().title ?? "");
  const [show, setShow] = useState<ShowDetail | null>(null);
  const [err, setErr] = useState("");
  const [params] = useSearchParams();
  const [season, setSeason] = useState<number | null>(params.get("season") ? Number(params.get("season")) : null);
  const [eps, setEps] = useState<FileItem[]>([]);
  const [hw, setHw] = useState<HwReport | null>(null);
  const [auto, setAuto] = useState<Record<string, string>>({});
  const [ov, setOv] = useState<ShowOverrides>({});
  const [plan, setPlan] = useState<ShowPlan | null>(null);
  const [sel, setSel] = useState<Set<number>>(new Set());
  const [busy, setBusy] = useState(false);
  const { ask: askHardlinked, dialog: hardlinkedDialog } = useHardlinkedConfirm();

  useEffect(() => {
    api.show(title).then((s) => {
      setShow(s);
      setSeason((cur) => cur ?? (s.seasons.find((x) => x.season > 0)?.season ?? s.seasons[0]?.season ?? 1));
    }).catch((e) => setErr(e.message));
    api.hw().then((h) => { setHw(h.report); setAuto(h.auto); }).catch(() => {});
  }, [title, live.queueVersion]);

  // A new season starts with nothing selected; queue updates (a job
  // finishing or starting) only refresh the list and keep the selection.
  useEffect(() => { setSel(new Set()); }, [title, season]);

  useEffect(() => {
    if (season === null) return;
    api.showEpisodes(title, season).then((r) => {
      setEps(r.episodes);
      const ids = new Set(r.episodes.map((e) => e.id));
      setSel((cur) => {
        const kept = [...cur].filter((id) => ids.has(id));
        return kept.length === cur.size ? cur : new Set(kept);
      });
    }).catch(() => setEps([]));
  }, [title, season, live.queueVersion]);

  const fileIds = sel.size > 0 ? [...sel].sort((a, b) => a - b) : undefined;
  useEffect(() => {
    if (season === null) return;
    const t = setTimeout(() => {
      api.showPlan({ title, season, overrides: ov, file_ids: fileIds }).then(setPlan).catch(() => setPlan(null));
    }, 250);
    return () => clearTimeout(t);
  }, [title, season, JSON.stringify(ov), JSON.stringify(fileIds), live.queueVersion]);

  const planByFile = useMemo(() => {
    const m = new Map<number, ShowPlan["episodes"][number]>();
    plan?.episodes.forEach((p) => m.set(p.file_id, p));
    return m;
  }, [plan]);

  if (err) return <Empty title="Show not found">{err}</Empty>;
  if (!show) return <div className="result-count mono dim">Loading…</div>;

  const cur = show.seasons.find((s) => s.season === season);
  const codec = ov.codec || plan?.summary.settings.codec || "hevc";
  const backends = workingBackends(hw, codec);
  const sum = plan?.summary;

  const queue = async (runNow: boolean) => {
    setBusy(true);
    try {
      const body = { title, season: season ?? undefined, overrides: ov, file_ids: fileIds, run_now: runNow, only_worth: !fileIds };
      let r;
      try {
        r = await api.queueShow(body);
      } catch (e) {
        const linked = hardlinkedFiles(e);
        if (!linked) throw e;
        const choice = await askHardlinked(linked, true);
        if (choice === "cancel") return;
        r = await api.queueShow({ ...body, confirm_hardlinked: choice === "confirm", skip_hardlinked: choice === "skip" });
      }
      toast(r.created > 0
        ? `${runNow ? "Encoding" : "Queued"} ${r.created} episode${r.created === 1 ? "" : "s"}`
          + (r.for_uniform_season ? ` (${r.for_uniform_season} to keep the season uniform)` : "")
          + (r.skipped ? ` · ${r.skipped} skipped` : "")
          + (r.left_different ? ` · ${r.left_different} already re-encoded, left as is` : "")
        : "Nothing new to queue: episodes are already queued or not worth it.", r.created > 0 ? "ok" : "err");
      setSel(new Set());
    } catch (e: any) {
      toast(e.message, "err");
    } finally {
      setBusy(false);
    }
  };

  const toggle = (id: number) => {
    const n = new Set(sel);
    n.has(id) ? n.delete(id) : n.add(id);
    setSel(n);
  };

  return (
    <div>
      {hardlinkedDialog}
      <div className="crumbs"><Link to="/shows">Shows</Link> / <span>{title}</span></div>

      <section className="hero" style={show.backdrop ? { ["--bd" as any]: `url(${show.backdrop})` } : undefined}>
        <div className="hero-poster"><Art src={show.image} title={title} /></div>
        <div className="hero-body">
          <h1 className="page-title">{title}</h1>
          <div className="hero-meta mono">
            <span>{show.seasons.length} season{show.seasons.length === 1 ? "" : "s"}</span>
            <span>{show.seasons.reduce((a, s) => a + s.episodes, 0)} episodes</span>
            <span>{bytes(show.seasons.reduce((a, s) => a + s.total_size, 0))}</span>
            {show.seasons.reduce((a, s) => a + (s.encoded_saved || 0), 0) > 0 && (
              <span className="teal">−{bytes(show.seasons.reduce((a, s) => a + (s.encoded_saved || 0), 0))} saved</span>
            )}
            {show.seasons.reduce((a, s) => a + s.reclaimable, 0) > 0 && (
              <span className="teal">−{bytes(show.seasons.reduce((a, s) => a + s.reclaimable, 0))} possible</span>
            )}
          </div>
          {show.genres && <div className="chips" style={{ marginTop: 8 }}>{show.genres.split(",").slice(0, 4).map((g) => <span key={g} className="chip">{g}</span>)}</div>}
          {show.overview && <p className="hero-overview">{show.overview}</p>}
        </div>
      </section>

      <div className="season-strip" role="tablist" aria-label="Seasons">
        {show.seasons.map((s) => (
          <button key={s.season} role="tab" aria-selected={s.season === season}
            className={`season-card${s.season === season ? " on" : ""}`} onClick={() => setSeason(s.season)}>
            <Art src={s.image} title={seasonName(s.season)} ratio="2 / 3" />
            <div className="season-name">{seasonName(s.season)}</div>
            <div className="season-meta mono">{s.episodes} ep · {bytes(s.total_size)}</div>
            {(s.encoded_saved || 0) > 0 && <div className="season-meta mono teal">−{bytes(s.encoded_saved || 0)} saved</div>}
            {s.reclaimable > 0 && <div className="season-meta mono teal">−{bytes(s.reclaimable)}</div>}
          </button>
        ))}
      </div>

      {cur && (
        <section className="panel plan-panel">
          <div className="plan-head">
            <div>
              <div className="eyebrow">{sel.size > 0 ? `${sel.size} selected episode${sel.size === 1 ? "" : "s"}` : `${seasonName(cur.season)} plan`}</div>
              {sum && sum.worth_count > 0 ? (
                <div className="plan-total">
                  <span className="mono">{bytes(sum.worth_size)}</span> → <span className="mono teal">{bytes(sum.est_out_total)}</span>
                  <span className="plan-pct">−{sum.savings_pct.toFixed(0)}%</span>
                </div>
              ) : (
                <div className="plan-total dim">{sum ? "Nothing here clears your savings threshold. Select episodes to encode them anyway." : "Calculating…"}</div>
              )}
              {sum && sum.worth_count > 0 && <SavingsGauge pct={sum.savings_pct} height={6} />}
              {sum && (
                <div className="dim small" style={{ marginTop: 8 }}>
                  {sum.worth_count} of {sum.episodes} worth re-encoding · median source {bitrate(sum.avg_bitrate)} · {resLabel(sum.height)}
                  {sum.quality_max > 0 && ` · quality ${sum.quality_min === sum.quality_max ? sum.quality_min : `${sum.quality_min}–${sum.quality_max}`}`}
                </div>
              )}
              {sum?.notes?.map((n) => <div key={n} className="dim small">{n}</div>)}
            </div>
            <div className="plan-actions">
              <button className="btn" disabled={busy || (!sel.size && !sum?.worth_count)} onClick={() => queue(false)}>{sel.size ? "Queue selected" : "Add season to queue"}</button>
              <button className="btn btn-primary" disabled={busy || (!sel.size && !sum?.worth_count)} onClick={() => queue(true)}>Encode now</button>
            </div>
          </div>

          <details className="plan-opts">
            <summary>Adjust settings for these episodes</summary>
            <div className="opts">
              <div className="opt-row">
                <div className="opt-label">Codec</div>
                <div className="opt-ctl">
                  <Seg value={codec} onChange={(c) => setOv({ ...ov, codec: c, backend: undefined })}
                    options={[{ value: "hevc", label: "HEVC" }, { value: "av1", label: "AV1" }, { value: "h264", label: "H.264" }]} />
                </div>
              </div>
              <div className="opt-row">
                <div className="opt-label">Encoder</div>
                <div className="opt-ctl">
                  <Seg value={ov.backend || "auto"} onChange={(b) => setOv({ ...ov, backend: b === "auto" ? undefined : b })}
                    options={[{ value: "auto", label: `Auto (${backendLabel(auto[codec] || backends[0])})` }, ...backends.map((b) => ({ value: b, label: backendLabel(b) }))]} />
                </div>
              </div>
              <div className="opt-row">
                <div className="opt-label">Quality<div className="opt-hint">Each episode is tuned from its own source; nudge them all up or down</div></div>
                <div className="opt-ctl">
                  <Seg value={ov.quality_delta || 0} onChange={(d) => setOv({ ...ov, quality_delta: d })}
                    options={[-10, -5, 0, 5, 10].map((d) => ({ value: d, label: d === 0 ? "As tuned" : d > 0 ? `+${d}` : `${d}` }))} />
                </div>
              </div>
              <div className="opt-row">
                <div className="opt-label">Speed</div>
                <div className="opt-ctl">
                  <Seg value={ov.speed || "default"} onChange={(v) => setOv({ ...ov, speed: v === "default" ? undefined : v })}
                    options={["default", "fast", "medium", "slow"].map((v) => ({ value: v, label: v === "default" ? "Default" : v }))} />
                </div>
              </div>
              {(cur.height ?? 0) > 740 && (
                <div className="opt-row">
                  <div className="opt-label">Resolution</div>
                  <div className="opt-ctl">
                    <Seg value={ov.max_height ?? -1} onChange={(h) => setOv({ ...ov, max_height: h === -1 ? undefined : h })}
                      options={[{ value: -1, label: "Default" }, { value: 0, label: "Keep" },
                        ...(cur.height > 1100 ? [{ value: 1080, label: "1080p" }] : []), { value: 720, label: "720p" }]} />
                  </div>
                </div>
              )}
              <div className="opt-row">
                <div className="opt-label">Bit depth</div>
                <div className="opt-ctl">
                  <Seg value={ov.bit_depth || 10} onChange={(b) => setOv({ ...ov, bit_depth: b })}
                    options={[{ value: 10, label: "10-bit" }, { value: 8, label: "8-bit" }]} />
                </div>
              </div>
            </div>
            {Object.keys(ov).length > 0 && <button className="btn mini" onClick={() => setOv({})}>Reset to recommended</button>}
          </details>
        </section>
      )}

      <div className="list-head">
        <span className="eyebrow">{cur ? seasonName(cur.season) : ""} · {eps.length} episodes</span>
        {eps.length > 0 && (
          <button className="btn mini" onClick={() => setSel(sel.size === eps.length ? new Set() : new Set(eps.map((e) => e.id)))}>
            {sel.size === eps.length ? "Select none" : "Select all"}
          </button>
        )}
      </div>
      <ul className="ep-list">
        {eps.map((f) => {
          const p = planByFile.get(f.id);
          return (
            <li key={f.id} className={`ep${sel.has(f.id) ? " sel" : ""}`}>
              <label className="ep-check">
                <input type="checkbox" checked={sel.has(f.id)} onChange={() => toggle(f.id)} aria-label={`Select ${se(f.season, f.episode)}`} />
              </label>
              <a href={`#/file/${f.id}`} className="ep-link">
                <div className="ep-still">
                  <Art src={f.image} title={se(f.season, f.episode)} ratio="16 / 9" />
                </div>
                <div className="ep-body">
                  <div className="ep-title">
                    <span className="mono dim">{f.episode}.</span> {f.jf_name || f.ep_title || `Episode ${f.episode}`}
                  </div>
                  <div className="ep-meta">
                    <CodecChip codec={f.video_codec} />
                    <span className="chip" title={`${f.width}×${f.height}`}>{resLabel(f.width, f.height)}</span>
                    {f.hdr && <span className="chip c-hdr">{hdrLabel(f.hdr)}</span>}
                    <span className="mono dim">{bitrate(f.video_bitrate)}</span>
                  </div>
                </div>
                <div className="ep-size mono">
                  {f.queued ? (
                    <span className="tag tag-queued">queued</span>
                  ) : p?.worth ? (
                    <>
                      <span className="dim">{bytes(f.size)}</span>
                      <span className="teal">→ {bytes(p.est_out_bytes)}</span>
                    </>
                  ) : f.encoded && f.encoded.saved > 0 ? (
                    <>
                      <span className="dim">{bytes(f.size + f.encoded.saved)}</span>
                      <span className="teal">→ {bytes(f.size)}</span>
                    </>
                  ) : (
                    <>
                      <span className="dim">{bytes(f.size)}</span>
                      <span className="faint small">{p?.action === "caution" ? "Dolby Vision" : codecLabel(f.video_codec) === "HEVC" ? "already HEVC" : "keep"}</span>
                    </>
                  )}
                </div>
              </a>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
