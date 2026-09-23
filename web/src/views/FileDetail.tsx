import { useEffect, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api, hardlinkedFiles, type FileItem, type HwReport, type Plan, type Settings, type Stream } from "../api";
import { Art, Copyable, Empty, FileChips, IssueChips, SavingsGauge, hasQuickFix, toast, useHardlinkedConfirm } from "../components";
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
  const { ask: askHardlinked, dialog: hardlinkedDialog } = useHardlinkedConfirm();
  const planReq = useRef(0);

  useEffect(() => {
    setFile(null);
    setSettings(null);
    setCustom(false);
    api.file(id).then((r) => { setFile(r.file); setStreams(r.streams); }).catch((e) => setErr(e.message));
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
          {(file.issues || "").replace(/,/g, "") !== "" && (
            <div className="hero-issues">
              <IssueChips f={file} />
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
