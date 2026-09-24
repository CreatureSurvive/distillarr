import { useEffect, useState, type ReactNode } from "react";
import { api, subscribe, type Config, type IntakeRow, type Job, type MeasureStatus, type Schedule } from "../api";
import { Copyable, Empty, Seg, Toggle, toast } from "../components";
import { HistoryStatsPanel } from "./Stats";
import { TrendsPanel } from "./Trends";
import { ago, backendLabel, bytes, DAY_LABELS, dur, minutesToHM } from "../format";
import type { LiveState } from "../App";

type Tab = "active" | "pending" | "history";
const STATUS: Record<Tab, string> = {
  active: "running,verifying,replacing",
  pending: "queued",
  history: "done,failed,canceled",
};

export default function Queue({ live }: { live: LiveState }) {
  const [tab, setTab] = useState<Tab>("pending");
  const [jobs, setJobs] = useState<Job[]>([]);
  const [summary, setSummary] = useState<Awaited<ReturnType<typeof api.queueSummary>> | null>(null);
  const [config, setConfig] = useState<Config | null>(null);
  const [open, setOpen] = useState<number | null>(null);

  const load = () => {
    api.jobs(STATUS[tab], 200).then((r) => setJobs(r.jobs)).catch(() => {});
    api.queueSummary().then(setSummary).catch(() => {});
  };
  useEffect(load, [tab, live.queueVersion]);
  useEffect(() => { api.config().then(setConfig).catch(() => {}); }, []);
  useEffect(() => {
    if (summary && tab === "pending" && (summary.counts.running || 0) > 0 && jobs.length === 0) setTab("active");
  }, [summary]);

  const c = summary?.counts || {};
  const act = async (fn: () => Promise<unknown>, msg?: string) => {
    try {
      await fn();
      if (msg) toast(msg);
      load();
    } catch (e: any) {
      toast(e.message, "err");
    }
  };

  return (
    <div>
      <header className="page-head">
        <div>
          <h1 className="page-title">Queue</h1>
          <div className="page-sub">
            {summary ? <>{bytes(summary.realized_saved)} saved across {summary.jobs_done} finished job{summary.jobs_done === 1 ? "" : "s"}</> : "—"}
          </div>
        </div>
        <div className="toolbar">
          <button className={`btn${summary?.paused ? " btn-primary" : ""}`}
            onClick={() => act(() => (summary?.paused ? api.resumeQueue() : api.pauseQueue()), summary?.paused ? "Queue resumed" : "Queue paused. The current encode will finish.")}>
            {summary?.paused ? "Resume queue" : "Pause queue"}
          </button>
        </div>
      </header>

      <div className="status-line">
        <span className={`dot ${summary?.paused || (summary?.viewers_transcoding || 0) > 0 ? "d-amber" : summary?.window_open ? "d-teal" : "d-dim"}`} aria-hidden />
        {summary?.paused
          ? "Paused. No new encodes will start."
          : (summary?.viewers_transcoding || 0) > 0
            ? `Waiting: ${summary!.viewers_transcoding} viewer${summary!.viewers_transcoding === 1 ? "" : "s"} transcoding.`
            : summary?.window_open
              ? "Processing: new encodes start as workers free up."
              : "Outside your processing window. Queued jobs wait; “Run now” skips the wait."}
      </div>

      <IntakePanel live={live} />

      {config && <ScheduleTimeline config={config} onSaved={setConfig} />}
      {config && <MeasurePanel config={config} onSaved={setConfig} live={live} />}

      <div className="tabs-row">
        <Seg value={tab} onChange={setTab} label="Job list" options={[
          { value: "active", label: <>Encoding <span className="count">{(c.running || 0) + (c.verifying || 0) + (c.replacing || 0)}</span></> },
          { value: "pending", label: <>Waiting <span className="count">{c.queued || 0}</span></> },
          { value: "history", label: <>History <span className="count">{(c.done || 0) + (c.failed || 0) + (c.canceled || 0)}</span></> },
        ]} />
        {tab === "pending" && jobs.length > 0 && (
          <button className="btn mini btn-danger" onClick={() => confirm(`Remove all ${jobs.length} waiting jobs?`) && act(api.clearQueue, "Queue cleared")}>
            Clear all
          </button>
        )}
      </div>

      {tab === "history" && <HistoryStatsPanel version={live.queueVersion} />}
      {tab === "history" && <TrendsPanel version={live.queueVersion} />}

      {jobs.length === 0 ? (
        <Empty title={tab === "active" ? "Nothing encoding" : tab === "pending" ? "Queue is empty" : "No history yet"}>
          {tab === "pending" ? "Add movies or whole seasons from the library." : tab === "active" ? "Encodes appear here while they run." : "Finished and failed jobs show up here."}
        </Empty>
      ) : (
        <ul className="jobs">
          {jobs.map((j, i) => {
            const p = live.progress[j.id];
            const pctv = p ? p.pct * 100 : 0;
            const settings = safeSettings(j.settings_json);
            const saved = j.status === "done" && j.src_size > j.output_size;
            return (
              <li key={j.id} className={`job s-${j.status}`}>
                <div className="job-main" onClick={() => setOpen(open === j.id ? null : j.id)}>
                  <div className="job-title">
                    {tab === "pending" && <span className="job-pos mono">{i + 1}</span>}
                    <a href={`#/file/${j.file_id}`} onClick={(e) => e.stopPropagation()}>{j.file_title || j.src_path.split("/").pop()}</a>
                    {j.run_now && <span className="tag tag-save">run now</span>}
                    {j.origin && j.origin !== "manual" && (
                      <span className="tag" title={j.reason || j.origin}>{j.origin}</span>
                    )}
                  </div>
                  <div className="job-meta mono dim">
                    <span className={`badge b-${j.status}`}>{j.status}</span>
                    {j.backend === "remux"
                      ? <span className="warm">Quick fix · remux · video copied</span>
                      : <span>{j.codec.toUpperCase()} · {backendLabel(settings.backend || j.backend || "auto")} · q{j.quality}</span>}
                    <span>{bytes(j.src_size)}{saved && <span className="teal"> → {bytes(j.output_size)} (−{Math.round((1 - j.output_size / j.src_size) * 100)}%)</span>}</span>
                    {j.attempts > 1 && <span>try {j.attempts}/{j.max_attempts}</span>}
                    {j.finished_at && <span>{ago(j.finished_at)}</span>}
                  </div>
                  {j.status === "running" && (
                    <div className="job-progress">
                      <div className="gauge" style={{ height: 6 }}><div className="gauge-fill" style={{ width: `${pctv}%` }} /></div>
                      <span className="mono small">{pctv.toFixed(1)}%{p?.speed ? ` · ${p.speed.toFixed(1)}×` : ""}{p?.eta_sec ? ` · ${dur(p.eta_sec)} left` : ""}</span>
                    </div>
                  )}
                  {j.status === "failed" && j.error && <div className="job-error">{j.error}</div>}
                </div>
                <div className="job-actions">
                  {tab === "pending" && (
                    <>
                      <button className="btn mini" disabled={i === 0} title="Move to top" onClick={() => act(() => api.moveJob(j.id, jobs[0].id))}>⤒</button>
                      <button className="btn mini" disabled={i === 0} title="Move up" onClick={() => act(() => api.moveJob(j.id, jobs[i - 1].id))}>↑</button>
                      <button className="btn mini" disabled={i === jobs.length - 1} title="Move down" onClick={() => act(() => api.moveJob(j.id, jobs[i + 2]?.id ?? 0))}>↓</button>
                      <button className="btn mini" onClick={() => act(() => api.runNowJob(j.id), "Starting as soon as a worker is free")}>Run now</button>
                    </>
                  )}
                  {(tab === "pending" || tab === "active") && (
                    <button className="btn mini btn-danger" onClick={() => act(() => api.cancelJob(j.id), "Job canceled")}>Cancel</button>
                  )}
                  {tab === "history" && (j.status === "failed" || j.status === "canceled") && (
                    j.error?.startsWith("hardlinked:") ? (
                      <button className="btn mini btn-danger" title="This file still shares its data with another link (usually a seeding torrent). Replacing it frees no space until that link is removed."
                        onClick={() => act(() => api.retryJob(j.id, true), "Queued again")}>Retry and replace anyway</button>
                    ) : (
                      <button className="btn mini" onClick={() => act(() => api.retryJob(j.id), "Queued again")}>Retry</button>
                    )
                  )}
                </div>
                {open === j.id && (
                  <div className="job-detail">
                    <div className="mono small faint path">{j.src_path}{j.dest_path && j.dest_path !== j.src_path && <> → {j.dest_path}</>}</div>
                    {j.cmd && <Copyable text={j.cmd} />}
                    {j.error_tail && <pre className="cmd log">{j.error_tail}</pre>}
                  </div>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}

// holding area: files waiting out a settle delay before an
// unattended queue, or waiting on a human because something needs
// confirming first: "hardlinked" or "codec_penalty" (
// the owning arr instance hasn't acknowledged a codec-penalty warning).
function holdReasonText(r: IntakeRow): ReactNode {
  if (r.hold_reason === "hardlinked") return "shares its data with another link (likely still seeding)";
  if (r.hold_reason === "codec_penalty") {
    return (
      <>
        {r.instance_name || "an instance"} hasn't acknowledged a codec-penalty warning — see{" "}
        <a href="#/settings" onClick={(e) => { e.preventDefault(); location.hash = "#/settings/connections"; setTimeout(() => document.getElementById("s-arr")?.scrollIntoView({ behavior: "smooth" }), 50); }}>
          Settings → Sonarr / Radarr
        </a>
      </>
    );
  }
  return r.hold_reason;
}

function IntakePanel({ live }: { live: LiveState }) {
  const [needsConfirmation, setNeedsConfirmation] = useState<IntakeRow[]>([]);
  const [waiting, setWaiting] = useState<IntakeRow[]>([]);
  const [waitingOpen, setWaitingOpen] = useState(false);
  const [busy, setBusy] = useState<Record<number, boolean>>({});

  const load = () => {
    api.intake("needs_confirmation").then((r) => setNeedsConfirmation(r.rows)).catch(() => {});
    api.intake("waiting").then((r) => setWaiting(r.rows)).catch(() => {});
  };
  useEffect(load, [live.intakeVersion]);
  useEffect(() => {
    const t = setInterval(load, 30000); // waiting countdowns drift otherwise
    return () => clearInterval(t);
  }, []);

  const act = async (id: number, fn: () => Promise<unknown>, msg: string) => {
    setBusy((b) => ({ ...b, [id]: true }));
    try {
      await fn();
      toast(msg);
      load();
    } catch (e: any) {
      toast(e.message, "err");
    } finally {
      setBusy((b) => ({ ...b, [id]: false }));
    }
  };

  if (needsConfirmation.length === 0 && waiting.length === 0) return null;

  return (
    <section className="panel">
      {needsConfirmation.length > 0 && (
        <>
          <div className="panel-head">
            <h2 className="panel-title" style={{ fontSize: 15 }}>Needs confirmation <span className="count">{needsConfirmation.length}</span></h2>
            <button className="btn mini"
              onClick={() => Promise.all(needsConfirmation.map((r) => api.intakeApprove(r.id))).then(() => { toast("Approved all"); load(); }).catch((e) => toast(e.message, "err"))}>
              Approve all
            </button>
          </div>
          <ul className="jobs">
            {needsConfirmation.map((r) => (
              <li key={r.id} className="job">
                <div className="job-main">
                  <div className="job-title">
                    <a href={`#/file/${r.file_id}`}>{r.file_title || r.file_path?.split("/").pop() || `file ${r.file_id}`}</a>
                    <span className="tag" title={r.reason || r.origin}>{r.origin}</span>
                  </div>
                  <div className="job-meta mono dim">
                    <span className="warm">{holdReasonText(r)}</span>
                  </div>
                </div>
                <div className="job-actions">
                  <button className="btn mini" disabled={busy[r.id]} onClick={() => act(r.id, () => api.intakeApprove(r.id), "Queued")}>Approve</button>
                  <button className="btn mini btn-danger" disabled={busy[r.id]} onClick={() => act(r.id, () => api.intakeDismiss(r.id), "Dismissed")}>Dismiss</button>
                </div>
              </li>
            ))}
          </ul>
        </>
      )}
      {waiting.length > 0 && (
        <div style={{ marginTop: needsConfirmation.length > 0 ? 14 : 0 }}>
          <button className="btn mini" onClick={() => setWaitingOpen((o) => !o)}>
            {waitingOpen ? "▾" : "▸"} Waiting <span className="count">{waiting.length}</span>
          </button>
          {waitingOpen && (
            <ul className="jobs" style={{ marginTop: 8 }}>
              {waiting.map((r) => {
                const secLeft = r.not_before ? (Date.parse(r.not_before) - Date.now()) / 1000 : 0;
                return (
                  <li key={r.id} className="job">
                    <div className="job-main">
                      <div className="job-title">
                        <a href={`#/file/${r.file_id}`}>{r.file_title || r.file_path?.split("/").pop() || `file ${r.file_id}`}</a>
                        <span className="tag" title={r.reason || r.origin}>{r.origin}</span>
                      </div>
                      <div className="job-meta mono dim">
                        <span>{secLeft > 0 ? `settling · ${dur(secLeft)} left` : "settling · due any moment"}</span>
                      </div>
                    </div>
                    <div className="job-actions">
                      <button className="btn mini" disabled={busy[r.id]} onClick={() => act(r.id, () => api.intakeApprove(r.id), "Queued")}>Queue now</button>
                      <button className="btn mini btn-danger" disabled={busy[r.id]} onClick={() => act(r.id, () => api.intakeDismiss(r.id), "Dismissed")}>Dismiss</button>
                    </div>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      )}
    </section>
  );
}

function safeSettings(s: string) {
  try {
    return JSON.parse(s) || {};
  } catch {
    return {};
  }
}

// 24-hour window timeline with an editor.
type SchedField = "schedules" | "measure_schedules" | "upscale_schedules";

export function ScheduleTimeline({ config, onSaved, field = "schedules", title = "When the queue runs", anyTime = true, children, accent }: {
  config: Config;
  onSaved: (c: Config) => void;
  field?: SchedField;
  title?: string;
  anyTime?: boolean; // no windows = always (queue) vs never (measuring)
  children?: ReactNode;
  accent?: "violet";
}) {
  const [editing, setEditing] = useState<Schedule | null>(null);
  const now = new Date();
  const nowMin = now.getHours() * 60 + now.getMinutes();
  const dayIdx = (now.getDay() + 6) % 7;
  const list = config[field] || [];
  const today = list.filter((s) => s.days & (1 << dayIdx));

  const save = async (next: Schedule[]) => {
    try {
      onSaved(await api.saveConfig({ [field]: next } as Partial<Config>));
      setEditing(null);
      toast("Schedule saved");
    } catch (e: any) {
      toast(e.message, "err");
    }
  };

  return (
    <section className={`panel sched${accent ? " sched-" + accent : ""}`}>
      <div className="sched-head">
        <div className="eyebrow">{title} · today ({DAY_LABELS[dayIdx]})</div>
        <button className="btn mini" onClick={() => setEditing({ id: 0, days: 0x7f, start: 60, end: 360, label: "Nightly" })}>Add window</button>
      </div>
      {children}
      <div className="tl24">
        <div className="tl24-track">
          {list.length === 0 && <div className="tl24-none">{anyTime ? "Any time: no windows set" : "Never: no windows set"}</div>}
          {list.length > 0 && today.length === 0 && <div className="tl24-none">No window today</div>}
          {list.length === 0 && anyTime && <div className="tl24-span" style={{ left: 0, width: "100%", opacity: 0.35 }} />}
          {today.flatMap((s) => (s.start <= s.end ? [[s.start, s.end]] : [[s.start, 1440], [0, s.end]]).map(([a, b], k) => (
            <div key={`${s.id}-${k}`} className="tl24-span" style={{ left: `${(a / 1440) * 100}%`, width: `${((b - a) / 1440) * 100}%` }} />
          )))}
          <div className="tl24-now" style={{ left: `${(nowMin / 1440) * 100}%` }} />
        </div>
        <div className="tl24-scale mono"><span>00</span><span>06</span><span>12</span><span>18</span><span>24</span></div>
      </div>
      {list.length > 0 && (
        <div className="chips" style={{ marginTop: 10 }}>
          {list.map((s) => (
            <button key={s.id} className="chip chip-btn" onClick={() => setEditing(s)}>
              {s.label || "Window"} · {minutesToHM(s.start)}–{minutesToHM(s.end)} · {s.days === 0x7f ? "daily" : DAY_LABELS.filter((_, i) => s.days & (1 << i)).join(" ")}
            </button>
          ))}
        </div>
      )}
      {editing && (
        <ScheduleEditor sched={editing} onClose={() => setEditing(null)}
          onSave={(s) => save(editing.id === 0 ? [...list, { ...s, id: Date.now() % 1e9 }] : list.map((x) => (x.id === s.id ? s : x)))}
          onDelete={() => save(list.filter((x) => x.id !== editing.id))} />
      )}
    </section>
  );
}

function ScheduleEditor({ sched, onClose, onSave, onDelete }: { sched: Schedule; onClose: () => void; onSave: (s: Schedule) => void; onDelete: () => void }) {
  const [s, setS] = useState(sched);
  const hm = (m: number) => `${String(Math.floor(m / 60)).padStart(2, "0")}:${String(m % 60).padStart(2, "0")}`;
  const parse = (v: string) => { const [h, m] = v.split(":").map(Number); return (h || 0) * 60 + (m || 0); };
  return (
    <div className="modal-backdrop" onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal" role="dialog" aria-label="Processing window">
        <h2 className="panel-title">{sched.id === 0 ? "New window" : "Edit window"}</h2>
        <label className="field"><span>Name</span><input className="input" value={s.label || ""} onChange={(e) => setS({ ...s, label: e.target.value })} /></label>
        <div className="field-pair">
          <label className="field"><span>Start</span><input className="input" type="time" value={hm(s.start)} onChange={(e) => setS({ ...s, start: parse(e.target.value) })} /></label>
          <label className="field"><span>End</span><input className="input" type="time" value={hm(s.end)} onChange={(e) => setS({ ...s, end: parse(e.target.value) })} /></label>
        </div>
        {s.end <= s.start && <div className="dim small">Runs past midnight into the next day.</div>}
        <div className="field"><span>Days</span>
          <div className="chips">
            {DAY_LABELS.map((d, i) => (
              <button key={d} className={`chip chip-btn${s.days & (1 << i) ? " c-hevc" : ""}`} aria-pressed={!!(s.days & (1 << i))}
                onClick={() => setS({ ...s, days: s.days ^ (1 << i) })}>{d}</button>
            ))}
          </div>
        </div>
        <div className="toolbar" style={{ marginTop: 14 }}>
          <button className="btn btn-primary" onClick={() => onSave(s)} disabled={s.days === 0}>Save window</button>
          {sched.id !== 0 && <button className="btn btn-danger" onClick={onDelete}>Delete</button>}
          <button className="btn" onClick={onClose}>Cancel</button>
        </div>
      </div>
    </div>
  );
}

// Overnight measuring: VMAF quality searches on candidates while the GPU
// is otherwise idle, turning estimates into measurements.
function MeasurePanel({ config, onSaved, live }: { config: Config; onSaved: (c: Config) => void; live: LiveState }) {
  const [st, setSt] = useState<MeasureStatus | null>(null);
  useEffect(() => {
    api.measure().then(setSt).catch(() => {});
    const unsub = subscribe((ev, data) => {
      if (ev === "measure") setSt((s) => ({ ...(s || data), ...data, measured: s?.measured ?? 0, remaining: s?.remaining ?? 0 }));
    });
    const t = setInterval(() => api.measure().then(setSt).catch(() => {}), 60000);
    return () => { unsub(); clearInterval(t); };
  }, [live.queueVersion]);
  const enabled = config.measure_enabled !== false;
  const toggle = async (on: boolean) => {
    try {
      onSaved(await api.saveConfig({ measure_enabled: on }));
      api.measure().then(setSt).catch(() => {});
    } catch (e: any) {
      toast(e.message, "err");
    }
  };
  const total = st ? st.measured + st.remaining : 0;
  return (
    <ScheduleTimeline config={config} onSaved={onSaved} field="measure_schedules" title="Overnight measuring" anyTime={false} accent="violet">
      <div className="measure-row">
        <Toggle on={enabled} onChange={toggle} label="Measure quality in the background"
          hint="Runs real VMAF quality searches on candidates, biggest files first, so estimates become measurements. Only inside these windows, and it steps aside the moment an encode or scan needs the machine." />
        {st && (
          <div className="measure-stat mono small">
            {st.running ? (
              <><span className="pulse-dot" aria-hidden /> {st.current}{st.note ? <span className="dim"> · {st.note}</span> : null}</>
            ) : (
              <span className="dim">{!enabled ? "Off" : st.window_open ? "Window open, waiting for idle GPU" : "Waiting for the next window"}</span>
            )}
            <div className="measure-bar" title={`${st.measured} measured of ${total}`}>
              <div style={{ width: `${total ? (st.measured / total) * 100 : 0}%` }} />
            </div>
            <span className="dim">{st.measured.toLocaleString()} measured · {st.remaining.toLocaleString()} to go{st.done_this_window ? ` · ${st.done_this_window} this window` : ""}</span>
            {st.last_error && <div className="faint" title={st.last_error}>last skip: {st.last_error.slice(0, 80)}</div>}
          </div>
        )}
      </div>
    </ScheduleTimeline>
  );
}
