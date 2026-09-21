import { useEffect, useState } from "react";
import { api, type Config, type Job, type Schedule } from "../api";
import { Copyable, Empty, Seg, toast } from "../components";
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
        <span className={`dot ${summary?.paused ? "d-amber" : summary?.window_open ? "d-teal" : "d-dim"}`} aria-hidden />
        {summary?.paused
          ? "Paused. No new encodes will start."
          : summary?.window_open
            ? "Processing: new encodes start as workers free up."
            : "Outside your processing window. Queued jobs wait; “Run now” skips the wait."}
      </div>

      {config && <ScheduleTimeline config={config} onSaved={setConfig} />}

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
                  </div>
                  <div className="job-meta mono dim">
                    <span className={`badge b-${j.status}`}>{j.status}</span>
                    <span>{j.codec.toUpperCase()} · {backendLabel(settings.backend || j.backend || "auto")} · q{j.quality}</span>
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
                    <button className="btn mini" onClick={() => act(() => api.retryJob(j.id), "Queued again")}>Retry</button>
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

function safeSettings(s: string) {
  try {
    return JSON.parse(s) || {};
  } catch {
    return {};
  }
}

// 24-hour window timeline with an editor.
function ScheduleTimeline({ config, onSaved }: { config: Config; onSaved: (c: Config) => void }) {
  const [editing, setEditing] = useState<Schedule | null>(null);
  const now = new Date();
  const nowMin = now.getHours() * 60 + now.getMinutes();
  const dayIdx = (now.getDay() + 6) % 7;
  const today = config.schedules.filter((s) => s.days & (1 << dayIdx));

  const save = async (schedules: Schedule[]) => {
    try {
      onSaved(await api.saveConfig({ schedules }));
      setEditing(null);
      toast("Schedule saved");
    } catch (e: any) {
      toast(e.message, "err");
    }
  };

  return (
    <section className="panel sched">
      <div className="sched-head">
        <div className="eyebrow">When the queue runs · today ({DAY_LABELS[dayIdx]})</div>
        <button className="btn mini" onClick={() => setEditing({ id: 0, days: 0x7f, start: 60, end: 360, label: "Nightly" })}>Add window</button>
      </div>
      <div className="tl24">
        <div className="tl24-track">
          {config.schedules.length === 0 && <div className="tl24-none">Any time: no windows set</div>}
          {config.schedules.length > 0 && today.length === 0 && <div className="tl24-none">No window today</div>}
          {config.schedules.length === 0 && <div className="tl24-span" style={{ left: 0, width: "100%", opacity: 0.35 }} />}
          {today.flatMap((s) => (s.start <= s.end ? [[s.start, s.end]] : [[s.start, 1440], [0, s.end]]).map(([a, b], k) => (
            <div key={`${s.id}-${k}`} className="tl24-span" style={{ left: `${(a / 1440) * 100}%`, width: `${((b - a) / 1440) * 100}%` }} />
          )))}
          <div className="tl24-now" style={{ left: `${(nowMin / 1440) * 100}%` }} />
        </div>
        <div className="tl24-scale mono"><span>00</span><span>06</span><span>12</span><span>18</span><span>24</span></div>
      </div>
      {config.schedules.length > 0 && (
        <div className="chips" style={{ marginTop: 10 }}>
          {config.schedules.map((s) => (
            <button key={s.id} className="chip chip-btn" onClick={() => setEditing(s)}>
              {s.label || "Window"} · {minutesToHM(s.start)}–{minutesToHM(s.end)} · {s.days === 0x7f ? "daily" : DAY_LABELS.filter((_, i) => s.days & (1 << i)).join(" ")}
            </button>
          ))}
        </div>
      )}
      {editing && (
        <ScheduleEditor sched={editing} onClose={() => setEditing(null)}
          onSave={(s) => save(editing.id === 0 ? [...config.schedules, { ...s, id: Date.now() % 1e9 }] : config.schedules.map((x) => (x.id === s.id ? s : x)))}
          onDelete={() => save(config.schedules.filter((x) => x.id !== editing.id))} />
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
