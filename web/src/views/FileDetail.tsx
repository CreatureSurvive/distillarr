import { useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api, hardlinkedFiles, type AutopilotDecision, type FileItem, type ForcesTranscodeDetail, type HwReport, type Plan, type Settings, type Stream } from "../api";
import { Art, Copyable, Empty, FileChips, IssueChips, REASON_LABEL, SavingsGauge, Toggle, hasQuickFix, toast, useHardlinkedConfirm } from "../components";
import EncodeOptions, { hasBars } from "../options";
import { backendLabel, bitrate, bytes, channelsLabel, codecLabel, dur, resClass, se } from "../format";
import type { LiveState } from "../App";

export default function FileDetail({ live }: { live: LiveState }) {
  const id = Number(useParams<{ id: string }>().id);
  const nav = useNavigate();
  const [file, setFile] = useState<FileItem | null>(null);
  const [streams, setStreams] = useState<Stream[]>([]);
  const [err, setErr] = useState("");
  const [hw, setHw] = useState<HwReport | null>(null);
  const [auto, setAuto] = useState<Record<string, string>>({});
  const [devices, setDevices] = useState<Record<string, string>>({});
  const [plan, setPlan] = useState<Plan | null>(null);
  const [settings, setSettings] = useState<Settings | null>(null);
  const [custom, setCustom] = useState(false);
  const [busy, setBusy] = useState("");
  const [autopilot, setAutopilot] = useState<AutopilotDecision | null>(null);
  const [codecPenaltyWarning, setCodecPenaltyWarning] = useState("");
  const [forcesTranscode, setForcesTranscode] = useState<ForcesTranscodeDetail | null>(null);
  const { ask: askHardlinked, dialog: hardlinkedDialog } = useHardlinkedConfirm();
  const planReq = useRef(0);

  useEffect(() => {
    setFile(null);
    setSettings(null);
    setCustom(false);
    api.file(id).then((r) => {
      setFile(r.file);
      setStreams(r.streams);
      setAutopilot(r.autopilot || null);
      setCodecPenaltyWarning(r.codec_penalty_warning || "");
      setForcesTranscode(r.forces_transcode || null);
    }).catch((e) => setErr(e.message));
    api.hw().then((h) => { setHw(h.report); setAuto(h.auto); setDevices(h.devices || {}); }).catch(() => {});
  }, [id]);

  useEffect(() => {
    if (!file) return;
    api.file(id).then((r) => setFile(r.file)).catch(() => {});
  }, [live.queueVersion]);

  // Fetch the plan: recommendation first, then live estimates as options change.
  useEffect(() => {
    if (!file) return;
    const n = ++planReq.current;
    const t = setTimeout(() => {
      api.plan(id, custom && settings ? settings : undefined).then((p) => {
        if (n !== planReq.current) return;
        setPlan(p);
        if (!custom) setSettings(p.rec.settings);
      }).catch(() => {});
    }, custom ? 350 : 0);
    return () => clearTimeout(t);
  }, [file?.id, custom, JSON.stringify(settings)]);

  if (err) return <Empty title="File not found">{err}</Empty>;
  if (!file || !settings) return <div className="result-count mono dim">Loading…</div>;

  const rec = plan?.rec;
  const autoRec = plan?.auto;
  const isTV = file.library === "tvshows";
  const title = isTV ? file.jf_name || file.ep_title || se(file.season, file.episode) : file.title;

  const update = (s: Settings) => {
    setSettings(s);
    setCustom(true);
  };
  const reset = () => {
    setCustom(false);
    if (autoRec) setSettings(autoRec.settings);
  };

  const act = async (kind: "queue" | "now" | "preview") => {
    setBusy(kind);
    const body = { settings: custom ? settings : undefined };
    try {
      if (kind === "preview") {
        const p = await api.previewFile(file.id, body);
        nav(`/preview/${p.id}`);
        return;
      }
      try {
        await api.queueFile(file.id, { ...body, run_now: kind === "now" });
      } catch (e) {
        const linked = hardlinkedFiles(e);
        if (!linked) throw e;
        if ((await askHardlinked(linked)) !== "confirm") return;
        await api.queueFile(file.id, { ...body, run_now: kind === "now", confirm_hardlinked: true });
      }
      toast(kind === "now" ? "Encoding started. Progress shows at the bottom." : "Added to the queue.");
      setFile({ ...file, queued: true });
    } catch (e: any) {
      toast(e.message, "err");
    } finally {
      setBusy("");
    }
  };

  const savedBytes = rec ? file.size - rec.est_out_bytes : 0;

  return (
    <div className="detail">
      {hardlinkedDialog}
      <div className="crumbs">
        {isTV ? (
          <><Link to="/shows">Shows</Link> / <Link to={`/show/${encodeURIComponent(file.title)}`}>{file.title}</Link> / <span>{se(file.season, file.episode)}</span></>
        ) : (
          <><Link to="/">Movies</Link> / <span>{file.title}</span></>
        )}
      </div>

      <section className="hero" style={file.backdrop ? { ["--bd" as any]: `url(${file.backdrop})` } : undefined}>
        <div className={`hero-poster${isTV ? " wide" : ""}`}>
          <Art src={file.image} title={isTV ? se(file.season, file.episode) : file.title} ratio={isTV ? "16 / 9" : "2 / 3"} />
        </div>
        <div className="hero-body">
          {isTV && <div className="eyebrow">{file.title} · {se(file.season, file.episode)}</div>}
          <h1 className="page-title">{title}{!isTV && file.year > 0 && <span className="title-year"> {file.year}</span>}</h1>
          <FileChips f={file} />
          <div className="hero-meta mono">
            <span>{bytes(file.size)}</span>
            <span>{bitrate(file.video_bitrate)}</span>
            <span>{file.width}×{file.height}</span>
            {hasBars(file) && <span className="warm" title="Black bars are encoded into the frame">picture {file.crop_w}×{file.crop_h}</span>}
            <span>{file.bit_depth}-bit</span>
            <span>{dur(file.duration)}</span>
            {file.interlaced && <span className="warm">interlaced</span>}
          </div>
          {file.arr && (
            <div className="dim small" style={{ marginTop: 4 }}>
              {file.arr.instance_name}: {file.arr.monitored ? "monitored" : "unmonitored"}
              {file.arr.cutoff_not_met && <span className="warm"> · upgrade pending</span>}
              {" · CF score "}{file.arr.cf_score}
              {file.arr.original_language && <> · {file.arr.original_language}</>}
              {file.arr.tag_names && file.arr.tag_names.length > 0 && <> · tags: {file.arr.tag_names.join(", ")}</>}
            </div>
          )}
          {forcesTranscode && (
            <div className="dim small" style={{ marginTop: 4 }}>
              Forces transcode: {Object.entries(forcesTranscode.reasons)
                .map(([k, n]) => `${REASON_LABEL[k] || k} ×${n}`).join(", ")}
              {" "}({Object.entries(forcesTranscode.servers).map(([s, n]) => `${s} ×${n}`).join(", ")}
              {", last "}{forcesTranscode.last.slice(0, 10)})
            </div>
          )}
          {(file.issues || "").replace(/,/g, "") !== "" && (
            <div className="hero-issues">
              <IssueChips f={file} />
              {(file.issues || "").includes(",missing_subs,") && file.arr && (
                <button className="btn mini" disabled={busy !== ""} title="Ask Bazarr to search for missing subtitles on this series/movie"
                  onClick={async () => {
                    setBusy("bazarr");
                    try {
                      await api.bazarrSearch(file.id);
                      toast("Bazarr is searching. New subtitles show up after the next scan.");
                    } catch (e: any) {
                      toast(e.message, "err");
                    } finally {
                      setBusy("");
                    }
                  }}>Search in Bazarr</button>
              )}
              {hasQuickFix(file) && !file.queued && (
                <button className="btn mini" disabled={busy !== ""} title="Remux with the video copied bit-exact: fixes hvc1 tagging, faststart, PCM audio and legacy containers in minutes"
                  onClick={async () => {
                    setBusy("fix");
                    try {
                      try {
                        await api.fixFile(file.id, true);
                      } catch (e) {
                        const linked = hardlinkedFiles(e);
                        if (!linked) throw e;
                        if ((await askHardlinked(linked)) !== "confirm") return;
                        await api.fixFile(file.id, true, true);
                      }
                      toast("Quick fix started. Video is copied, not re-encoded.");
                      setFile({ ...file, queued: true });
                    } catch (e: any) {
                      toast(e.message, "err");
                    } finally {
                      setBusy("");
                    }
                  }}>Quick fix</button>
              )}
            </div>
          )}
          {file.overview && <p className="hero-overview">{file.overview}</p>}
          <Toggle on={!!file.lang_prune_exempt} label="Don't prune languages on this file" hint="Opts out of language pruning regardless of the global/scoped policy."
            onChange={async (v) => {
              try {
                await api.setLangExempt(file.id, v);
                setFile({ ...file, lang_prune_exempt: v });
                toast(v ? "Exempted from language pruning" : "No longer exempt");
              } catch (e: any) {
                toast(e.message, "err");
              }
            }} />
          <div className="opt-row" style={{ marginTop: 6 }}>
            <div className="opt-label">Subtitle sidecars for this file</div>
            <div className="opt-ctl">
              <select className="input" value={file.sidecar_mode || ""}
                onChange={async (e) => {
                  const mode = e.target.value;
                  try {
                    await api.setSidecarMode(file.id, mode);
                    setFile({ ...file, sidecar_mode: mode });
                    toast(mode ? "Sidecar override set" : "Back to the rule/global default");
                  } catch (err: any) {
                    toast(err.message, "err");
                  }
                }}>
                <option value="">Inherit rule/global setting</option>
                <option value="off">Off</option>
                <option value="extract_keep">Extract + keep embedded</option>
                <option value="extract_remove">Extract + remove embedded</option>
              </select>
            </div>
          </div>
          <div className="opt-row" style={{ marginTop: 6, alignItems: "flex-start" }}>
            <div className="opt-label">Image subtitles for this file</div>
            <div className="opt-ctl" style={{ display: "block" }}>
              <select className="input" value={file.image_subs_mode || ""}
                onChange={async (e) => {
                  const mode = e.target.value;
                  if (mode === "sidecar" && !confirm(
                    "External PGS/VobSub sidecars aren't known to be selectable subtitle tracks in Jellyfin or Plex " +
                    ". Turn on anyway for this file?"
                  )) return;
                  try {
                    await api.setImageSubsMode(file.id, mode);
                    setFile({ ...file, image_subs_mode: mode });
                    toast(mode ? "Image-subtitle override set" : "Back to the rule/global default");
                  } catch (err: any) {
                    toast(err.message, "err");
                  }
                }}>
                <option value="">Inherit rule/global setting</option>
                <option value="off">Off (keep)</option>
                <option value="sidecar">Sidecar</option>
                <option value="ocr">OCR to text</option>
              </select>
              {file.ocr && (
                <div className="dim small" style={{ marginTop: 4 }}>
                  {file.ocr.failed
                    ? `Last OCR attempt scored ${file.ocr.confidence.toFixed(0)}, below threshold — track left untouched.`
                    : `Last OCR wrote ${file.ocr.srt_name} at confidence ${file.ocr.confidence.toFixed(0)}.`}
                </div>
              )}
              <div style={{ marginTop: 6 }}>
                <button type="button" className="btn mini" onClick={async () => {
                  try {
                    await api.ocrFile(file.id);
                    toast("OCR queued");
                  } catch (err: any) {
                    toast(err.message, "err");
                  }
                }}>Run OCR now</button>
              </div>
            </div>
          </div>
        </div>
      </section>

      <div className="detail-cols">
        <aside className="detail-side">
          <section className={`panel verdict v-${rec?.action || "skip"}`}>
            <div className="eyebrow">
              {custom ? "Your settings" : autoRec?.action === "transcode" ? "Recommended" : autoRec?.action === "caution" ? "Caution" : "No change recommended"}
            </div>
            {rec && (
              <>
                <div className="verdict-sizes">
                  <span className="mono">{bytes(file.size)}</span>
                  <span className="arrow">→</span>
                  <span className="mono teal">{bytes(rec.est_low_bytes)}–{bytes(rec.est_high_bytes)}</span>
                </div>
                <div className="verdict-gauge">
                  <SavingsGauge pct={rec.savings_pct} height={8} />
                  <span className="verdict-pct">−{rec.savings_pct.toFixed(0)}%</span>
                </div>
                <div className="dim small">
                  {savedBytes > 0 ? `about ${bytes(savedBytes)} saved · ` : ""}
                  {rec.measured
                    ? "measured on this file's own samples"
                    : settings.vmaf_target
                      ? `estimate; the real quality is measured before encoding (target VMAF ${settings.vmaf_target})`
                      : rec.calibration_samples > 0
                        ? `estimate tuned by ${rec.calibration_samples} real measurement${rec.calibration_samples === 1 ? "" : "s"}`
                        : "estimate; a preview measures it"}
                </div>
              </>
            )}
            {!custom && autoRec?.action !== "transcode" && <p className="verdict-reason">{autoRec?.reason}</p>}
            {autoRec?.why && autoRec.why.length > 0 && (
              <div className="why">
                <div className="why-h">{autoRec.measured ? `Quality ${autoRec.settings.quality}, measured` : `Why quality ${autoRec.settings.quality}`}</div>
                <ul>{autoRec.why.map((w) => <li key={w}>{w}</li>)}</ul>
              </div>
            )}
            {rec?.notes && rec.notes.length > 0 && (
              <ul className="notes">{rec.notes.map((n) => <li key={n}>{n}</li>)}</ul>
            )}
            {autopilot && (
              <div className="dim small" style={{ marginTop: 6 }}>
                Autopilot: {autopilot.rule ? <>rule "{autopilot.rule}"</> : "default"} → {autopilot.action}
                {autopilot.action === "queue_override" && autopilot.codec ? ` (${autopilot.codec}${autopilot.quality ? ` q${autopilot.quality}` : ""})` : ""}
              </div>
            )}
            {codecPenaltyWarning && (
              <div className="alert" style={{ marginTop: 8 }}>
                <div className="small">
                  <b>{codecPenaltyWarning}</b> scores re-encoded files as worth replacing again (an unacknowledged
                  codec-penalty warning). Queueing this is still allowed — autopilot and webhook auto-queue are the
                  only things this blocks. See its card in <a href="#/settings" onClick={(e) => { e.preventDefault(); location.hash = "#/settings/connections"; setTimeout(() => document.getElementById("s-arr")?.scrollIntoView({ behavior: "smooth" }), 50); }}>Settings → Sonarr / Radarr</a>.
                </div>
              </div>
            )}
            <div className="actions">
              <button className="btn" disabled={!!busy} onClick={() => act("preview")}>
                {busy === "preview" ? "Starting…" : settings.vmaf_target ? "Measure & compare" : "Preview & compare"}
              </button>
              {resClass(file.width, file.height) < 2160 && (
                <Link className="btn" to={`/upscale/${file.id}`} title="Tune an upscale against real frames">Upscale…</Link>
              )}
              <button className="btn" disabled={!!busy || file.queued} onClick={() => act("queue")}>
                {file.queued ? "In queue" : busy === "queue" ? "Adding…" : "Add to queue"}
              </button>
              <button className="btn btn-primary" disabled={!!busy || file.queued} onClick={() => act("now")}>
                {busy === "now" ? "Starting…" : "Encode now"}
              </button>
            </div>
            {custom && <button className="btn mini linkish" onClick={reset}>Reset to recommended</button>}
          </section>

          {plan?.warnings && plan.warnings.length > 0 && (
            <div className="alert">
              {plan.warnings.map((w, i) => <div key={i} className="small">{w}</div>)}
            </div>
          )}

          <details className="panel cmd-panel">
            <summary>
              ffmpeg command <span className="dim small">· {backendLabel(plan?.backend || "")}
              {plan?.render_node ? ` on ${devices[plan.render_node] ? `${devices[plan.render_node]} (${plan.render_node.split("/").pop()})` : plan.render_node.split("/").pop()}` : ""} · {plan?.container?.toUpperCase()}</span>
            </summary>
            {plan?.command ? <Copyable text={plan.command} /> : <div className="dim">{plan?.command_error || "…"}</div>}
            {plan?.fallback_command && (
              <details className="sub-details">
                <summary className="dim small">If hardware decoding fails, this runs instead</summary>
                <Copyable text={plan.fallback_command} />
              </details>
            )}
          </details>

          <details className="panel">
            <summary>Source streams</summary>
            <ul className="stream-list">
              {streams.map((s) => (
                <li key={s.id}>
                  <span className="mono dim">#{s.stream_index}</span>
                  <span className="stream-kind">{s.kind}</span>
                  <span className="mono">{s.codec}</span>
                  <span className="dim">
                    {s.lang && s.lang.toUpperCase()}
                    {s.kind === "audio" && ` ${channelsLabel(s.channels)}${s.bit_rate ? ` · ${bitrate(s.bit_rate)}` : ""}`}
                    {s.kind === "subtitle" && (s.is_text ? " · text" : " · image")}
                    {s.forced && " · forced"}
                  </span>
                </li>
              ))}
            </ul>
            <div className="mono faint small path">{file.path}</div>
          </details>
        </aside>

        <section className="panel detail-main">
          <div className="opts-head">
            <h2 className="panel-title">Encode options</h2>
            <span className="dim small">{custom ? "Edited" : `Recommended · ${codecLabel(settings.codec)}`}</span>
          </div>
          <EncodeOptions value={settings} onChange={update} file={file} streams={streams} hw={hw} autoBackend={auto[settings.codec]} />
        </section>
      </div>
    </div>
  );
}
