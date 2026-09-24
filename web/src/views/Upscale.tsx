// SPDX-License-Identifier: GPL-3.0-or-later

import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { api, hardlinkedFiles, type Config, type FileItem, type Plan, type Settings, type Still, type UpscaleInfo } from "../api";
import { Empty, Seg, Toggle, toast, useHardlinkedConfirm } from "../components";
import { dur, minutesToHM, resLabel } from "../format";

type View = "split" | "a" | "b";
type Zoom = "fit" | 1 | 2;

const classLabel = (c: number) => (c === 2160 ? "4K" : `${c}p`);
const fmtHours = (h: number) => (h < 1 ? `${Math.max(1, Math.round(h * 60))} min` : h < 48 ? `${h.toFixed(1)} h` : `${(h / 24).toFixed(1)} days`);

// Tune an upscale against real frames: a standard Lanczos resize (A) and the
// same frame through the chosen upscaler (B), with a wipe divider. Frames
// render on the server in about a second and are cached, so the sliders feel live.
export default function UpscaleView() {
  const id = Number(useParams<{ id: string }>().id);
  const nav = useNavigate();
  const [file, setFile] = useState<FileItem | null>(null);
  const [info, setInfo] = useState<UpscaleInfo | null>(null);
  const [loadErr, setLoadErr] = useState("");

  const [to, setTo] = useState(0);
  const [presetId, setPresetId] = useState("");
  const [params, setParams] = useState<Record<string, number>>({});
  const [at, setAt] = useState(0);

  const [still, setStill] = useState<Still | null>(null);
  const [busy, setBusy] = useState(false);
  const [stillErr, setStillErr] = useState("");
  const [split, setSplit] = useState(0.5);
  const [view, setView] = useState<View>("split");
  // 1:1 by default: "Fit" scales a 1920px frame down to whatever the screen is
  // (on a phone, often 5x+), which flattens exactly the fine-detail differences
  // this page exists to show. Panning at 1:1 needs a real handle to drag (see
  // wrap below) rather than the whole image, or a touch swipe can't scroll.
  const [zoom, setZoom] = useState<Zoom>(1);
  const [clipBusy, setClipBusy] = useState(false);
  const [base, setBase] = useState<Settings | null>(null);      // the file's own recommended settings
  const [output, setOutput] = useState<"copy" | "replace">("copy");
  const [qBusy, setQBusy] = useState<"" | "queue" | "now">("");
  const [queued, setQueued] = useState(false);
  const [window_, setWindow] = useState("");                    // the neural window, as text
  const { ask: askHardlinked, dialog: hardlinkedDialog } = useHardlinkedConfirm();
  const seq = useRef(0);
  const wrap = useRef<HTMLDivElement>(null);
  const viewport = useRef<HTMLDivElement>(null);

  useEffect(() => {
    Promise.all([api.file(id), api.upscaleInfo(id), api.plan(id), api.config()])
      .then(([f, u, p, c]: [{ file: FileItem }, UpscaleInfo, Plan, Config]) => {
        setBase(p.auto.settings);
        setOutput(c.upscale_output === "replace" ? "replace" : "copy");
        setWindow(
          (c.upscale_schedules ?? []).length === 0
            ? "no window is set, so only “Upscale now” will run it"
            : c.upscale_schedules.map((s) => `${minutesToHM(s.start)}–${minutesToHM(s.end)}`).join(", "),
        );
        setQueued(!!f.file.queued);
        setFile(f.file);
        setInfo(u);
        const t = u.targets.find((x) => x.class === u.suggested.to) ?? u.targets[0];
        setTo(t ? t.class : 0);
        setPresetId(u.suggested.preset);
        setAt(Math.floor(f.file.duration * 0.3)); // mid-film: avoids titles and credits
      })
      .catch((e) => setLoadErr(e.message));
  }, [id]);

  const preset = info?.presets.find((p) => p.id === presetId);

  // The file's own recommendation (codec, quality, crop, tone-map…) plus the
  // upscale choices, so the preview, stills and queued job all agree.
  const settings = useMemo<Settings>(
    () => ({
      ...(base ?? { codec: "hevc", quality: 60 }),
      upscale_to: to, upscale_preset: presetId, upscale_params: params, upscale_output: output,
    }),
    [base, to, presetId, params, output],
  );

  // Debounced render. A response that arrives after a newer request started is
  // dropped, so fast slider drags never flash an older frame.
  useEffect(() => {
    if (!info?.available || !to || !presetId) return;
    const n = ++seq.current;
    const t = setTimeout(async () => {
      setBusy(true);
      try {
        const r = await api.still(id, settings, at);
        if (n === seq.current) { setStill(r); setStillErr(""); }
      } catch (e: any) {
        if (n === seq.current) setStillErr(e.message);
      } finally {
        if (n === seq.current) setBusy(false);
      }
    }, 250);
    return () => clearTimeout(t);
  }, [id, info, settings, at, to, presetId]);

  // At 1:1/2:1 the frame is wider than the screen; start centred rather than
  // showing the left edge, since detail worth comparing is usually mid-frame.
  useEffect(() => {
    const el = viewport.current;
    if (!el || zoom === "fit") return;
    el.scrollLeft = (el.scrollWidth - el.clientWidth) / 2;
  }, [zoom, still?.key]);

  if (loadErr) return <Empty title="Couldn't load this file">{loadErr}</Empty>;
  if (!file || !info) return <div className="dim">Loading…</div>;

  const back = <div className="crumbs"><Link to={`/file/${file.id}`}>← Back to file</Link></div>;

  if (!info.available) {
    return (
      <div>
        {back}
        <Empty title="Upscaling isn't available on this host">
          <div className="dim">No Vulkan GPU passed the upscaler self-test.</div>
          {info.vulkan.map((d) => (
            <div key={d.index} className="mono small dim" style={{ marginTop: 6 }}>{d.name}: {d.error || "not usable"}</div>
          ))}
        </Empty>
      </div>
    );
  }
  if (info.targets.length === 0) {
    return <div>{back}<Empty title="Nothing to upscale to">This file is already {classLabel(info.source.class)}.</Empty></div>;
  }

  // A neural preset's run time comes from its measured cost per frame, scaled by
  // this file's own size; past the limit the server refuses it, so say so here.
  const estHours = (p?: { sec_per_frame?: number }) =>
    p?.sec_per_frame && info.ref_pixels
      ? (file.duration * (file.fps || 24) * p.sec_per_frame * ((file.width * file.height) / info.ref_pixels)) / 3600
      : 0;
  const isNeural = preset?.tier === "neural";
  const tooLong = isNeural && estHours(preset) > info.max_neural_hours;

  const setParam = (k: string, v: number) => setParams((p) => ({ ...p, [k]: v }));
  // Always measured against the outer wrap (the full-image-width box), not
  // whichever element captured the pointer, so the hit-strip below still maps
  // to the right 0..1 position across the whole picture.
  const pick = (e: React.PointerEvent) => {
    const r = wrap.current?.getBoundingClientRect();
    if (r && r.width > 0) setSplit(Math.min(1, Math.max(0, (e.clientX - r.left) / r.width)));
  };
  // setPointerCapture can throw if the browser doesn't consider the pointer
  // active (seen with synthetic/automated touch input; real touches are fine).
  // It's just an optimization that keeps drag events flowing outside the
  // element's bounds, so a failure here must never block the tap-to-jump below.
  const grab = (e: React.PointerEvent) => {
    try { e.currentTarget.setPointerCapture(e.pointerId); } catch { /* see above */ }
    pick(e);
  };
  const drag = (e: React.PointerEvent) => { if (e.buttons) pick(e); };

  const enqueue = async (kind: "queue" | "now") => {
    setQBusy(kind);
    try {
      try {
        await api.queueFile(id, { settings, run_now: kind === "now" });
      } catch (e) {
        const linked = hardlinkedFiles(e);
        if (!linked) throw e;
        if ((await askHardlinked(linked)) !== "confirm") return;
        await api.queueFile(id, { settings, run_now: kind === "now", confirm_hardlinked: true });
      }
      setQueued(true);
      toast(kind === "now" ? "Upscaling started. Progress shows at the bottom." : "Added to the queue.");
    } catch (e: any) {
      toast(e.message, "err");
    } finally {
      setQBusy("");
    }
  };

  const renderClip = async () => {
    setClipBusy(true);
    try {
      const p = await api.previewFile(id, { settings, starts: [Math.max(0, at - 2)] });
      nav(`/preview/${p.id}`);
    } catch (e: any) {
      toast(e.message, "err");
    } finally {
      setClipBusy(false);
    }
  };

  const dim = zoom === "fit" ? undefined : { width: (still?.w ?? 0) * zoom };
  const sharp = zoom === "fit" ? undefined : ({ imageRendering: "pixelated" } as const);
  const suggested = info.suggested;
  const maxT = Math.max(1, Math.floor(file.duration));

  return (
    <div>
      {hardlinkedDialog}
      {back}
      <div className="page-head">
        <div>
          <h1 className="page-title">Upscale</h1>
          <div className="page-sub">
            {file.title} · {file.width}×{file.height} ({resLabel(file.width, file.height)}) → {classLabel(to)}
          </div>
        </div>
      </div>

      <div className="up-layout">
        <div>
          <div className="toolbar" style={{ marginBottom: 10 }}>
            <Seg<View> value={view} onChange={setView} label="Compare"
              options={[{ value: "split", label: "Split" }, { value: "a", label: "A only" }, { value: "b", label: "B only" }]} />
            <Seg<Zoom> value={zoom} onChange={setZoom} label="Zoom"
              options={[{ value: "fit", label: "Fit" }, { value: 1, label: "1:1" }, { value: 2, label: "2:1" }]} />
            <span className="mono dim small" style={{ marginLeft: "auto" }}>
              {busy ? "rendering…" : still ? `${still.w}×${still.h} · ${still.cached ? "cached" : `${still.ms} ms`}` : ""}
            </span>
          </div>

          <div className={`up-stage${busy ? " busy" : ""}`}>
            {still ? (
              <div ref={viewport} className="up-viewport" style={zoom === "fit" ? undefined : { overflow: "auto", maxHeight: "72vh" }}>
                <div
                  ref={wrap}
                  className={`up-wrap${zoom !== "fit" ? " up-wrap-zoomed" : ""}`}
                  style={dim}
                  // At "fit" the image is never wider than its box, so dragging
                  // anywhere is unambiguous. Zoomed, the box is wider than the
                  // screen and a touch swipe has to scroll it — dragging there is
                  // handled by the narrow .up-handle-hit strip below instead, so
                  // this whole area keeps native pan/scroll (see .up-wrap-zoomed).
                  onPointerDown={zoom === "fit" ? grab : undefined}
                  onPointerMove={zoom === "fit" ? drag : undefined}
                >
                  <img className="up-img" style={sharp} src={still.a_url} alt="Standard Lanczos resize" draggable={false} />
                  {view !== "a" && (
                    <img
                      className="up-img up-b"
                      style={{ ...sharp, clipPath: view === "split" ? `inset(0 0 0 ${split * 100}%)` : "none" }}
                      src={still.b_url}
                      alt="Upscaled"
                      draggable={false}
                    />
                  )}
                  {view === "split" && <div className="up-handle" style={{ left: `${split * 100}%` }} />}
                  {view === "split" && zoom !== "fit" && (
                    <div className="up-handle-hit" style={{ left: `calc(${split * 100}% - 24px)` }}
                      onPointerDown={grab} onPointerMove={drag} />
                  )}
                  {view !== "b" && <span className="up-tag up-tag-a">A · standard Lanczos</span>}
                  {view !== "a" && <span className="up-tag up-tag-b">B · {preset?.label}</span>}
                </div>
              </div>
            ) : (
              <div className="up-empty dim">{stillErr ? "" : "Rendering the first frame…"}</div>
            )}
            {stillErr && <div className="up-err mono small">{stillErr}</div>}
          </div>

          <div className="up-time">
            <span className="stat-label">Frame</span>
            <input type="range" min={0} max={maxT} step={1} value={Math.round(at)}
              onChange={(e) => setAt(Number(e.target.value))} aria-label="Position in the file" />
            <span className="mono small">{dur(at)}</span>
          </div>
          <div className="toolbar" style={{ marginTop: 8 }}>
            {[0.1, 0.3, 0.5, 0.7, 0.9].map((f) => (
              <button key={f} className="chip chip-btn" onClick={() => setAt(Math.floor(file.duration * f))}>
                {Math.round(f * 100)}% · {dur(file.duration * f)}
              </button>
            ))}
          </div>
          <p className="dim small" style={{ marginTop: 12 }}>
            Showing real pixels (1:1) — scroll or swipe to see the rest of the frame, and drag the {"↔"} handle to
            compare. Switch to Fit for the whole picture at once, though small differences disappear at that size.
            Lanczos (EWA), Jinc and Spline64 are deliberately close to a plain resize; for an obvious difference try
            FSR, NIS or RAVU with sharpening, Anime4K on animation, or a neural method. Stills are 8-bit SDR: they
            judge sharpness and artefacts, not colour grading.
          </p>
        </div>

        <aside className="up-controls">
          <section className="panel">
            <div className="eyebrow">Target</div>
            <Seg<number> value={to} onChange={setTo} label="Target resolution"
              options={info.targets.map((t) => ({ value: t.class, label: classLabel(t.class), hint: `${t.w}×${t.h}` }))} />
            <div className="dim small" style={{ marginTop: 8 }}>
              {info.targets.find((t) => t.class === to)?.w}×{info.targets.find((t) => t.class === to)?.h}, from{" "}
              {info.source.w}×{info.source.h}
            </div>
          </section>

          <section className="panel">
            <div className="eyebrow">Method</div>
            <div className="up-presets">
              {info.presets.map((p, i, all) => (
                <Fragment key={p.id}>
                  {p.tier === "neural" && all[i - 1]?.tier !== "neural" && (
                    <div className="up-group dim small">
                      Neural: far more detail, but hours per file. Runs overnight{info.device ? ` on ${info.device}` : ""}.{" "}
                      {all.some((x) => x.tier === "neural" && x.measured)
                        ? "Estimates marked “measured” come from runs on this GPU."
                        : `Estimates come from benchmarks (reference: ${info.reference_device}) until this GPU has finished a few runs.`}
                    </div>
                  )}
                <button className={`up-preset${p.id === presetId ? " on" : ""}`}
                  onClick={() => { setPresetId(p.id); setParams({}); }}>
                  <span className="up-preset-head">
                    <b>{p.label}</b>
                    <span className={`chip ${p.content === "anime" ? "c-av1" : p.content === "any" ? "" : "c-hevc"}`}>
                      {p.content === "anime" ? "Animation" : p.content === "any" ? "Any content" : "Live action"}
                    </span>
                    {p.id === suggested.preset && <span className="tag tag-save">suggested</span>}
                  </span>
                  <span className="dim small">{p.desc}</span>
                  {p.tier === "neural" && (
                    <span className={`mono small ${estHours(p) > info.max_neural_hours ? "warm" : "teal"}`}>
                      ≈ {fmtHours(estHours(p))} for this file{estHours(p) > info.max_neural_hours ? ": too long to queue" : ""}
                      <span className="dim"> · {p.measured ? `measured (${p.samples} runs)` : `reference: ${info.reference_device}`}</span>
                    </span>
                  )}
                  {p.tier !== "neural" && p.measured && p.realtime && (
                    <span className="mono small dim">≈ {p.realtime.toFixed(1)}x realtime here · measured ({p.samples} runs)</span>
                  )}
                </button>
                </Fragment>
              ))}
            </div>
            <div className="dim small" style={{ marginTop: 8 }}>Suggested: {suggested.why}.</div>
          </section>

          {preset && (preset.params ?? []).length > 0 && (
            <section className="panel">
              <div className="eyebrow">Tuning</div>
              <div className="up-params">
              {(preset.params ?? []).map((p) => {
                const v = params[p.key] ?? p.def;
                return p.max === 1 && p.step === 1 ? (
                  <Toggle key={p.key} on={v >= 1} onChange={(on) => setParam(p.key, on ? 1 : 0)} label={p.label} />
                ) : (
                  <label key={p.key} className="field">
                    <span>{p.label} <span className="mono dim">{v}</span></span>
                    <input type="range" min={p.min} max={p.max} step={p.step} value={v}
                      onChange={(e) => setParam(p.key, Number(e.target.value))} />
                  </label>
                );
              })}
              </div>
            </section>
          )}

          <section className="panel">
            <div className="eyebrow">Queue it</div>
            <Seg<"copy" | "replace"> value={output} onChange={setOutput} label="When done"
              options={[
                { value: "copy", label: "Add a copy", hint: "Writes “… - " + classLabel(to) + " upscale” beside the source; the original is never touched" },
                { value: "replace", label: "Replace original", hint: "The original is kept in the trash for the retention period" },
              ]} />
            <p className="dim small" style={{ margin: "8px 0 10px" }}>
              {output === "copy"
                ? "The original stays as it is. Jellyfin will list the upscale beside it."
                : "The upscale takes the original's place; the original goes to the trash."}
            </p>
            {isNeural && (
              <p className={`small ${tooLong ? "warm" : "dim"}`} style={{ margin: "0 0 10px" }}>
                {tooLong
                  ? `This would take about ${fmtHours(estHours(preset))}, over the ${info.max_neural_hours}-hour limit. Pick a shader method, or a shorter file.`
                  : `About ${fmtHours(estHours(preset))} of GPU time. “Add to queue” runs it in the upscale window (${window_}); “Upscale now” starts immediately.`}
              </p>
            )}
            <div className="actions">
              <button className="btn" disabled={!!qBusy || queued || tooLong} onClick={() => enqueue("queue")}>
                {queued ? "In queue" : qBusy === "queue" ? "Adding…" : "Add to queue"}
              </button>
              <button className="btn btn-primary" disabled={!!qBusy || queued || tooLong} onClick={() => enqueue("now")}>
                {qBusy === "now" ? "Starting…" : "Upscale now"}
              </button>
            </div>
          </section>

          <section className="panel">
            <div className="eyebrow">Confirm motion</div>
            <p className="dim small" style={{ marginTop: 0 }}>
              {isNeural
                ? "Clip previews aren't available for neural methods: they need the whole chunked pipeline. Judge the detail on the frames above; a frame renders in a few seconds."
                : "Encode a 20-second clip with these settings and compare it against the source, playing side by side."}
            </p>
            <button className="btn btn-primary" disabled={clipBusy || isNeural} onClick={renderClip}>
              {clipBusy ? "Starting…" : "Render 20s clip"}
            </button>
          </section>
        </aside>
      </div>
    </div>
  );
}
