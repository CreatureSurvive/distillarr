import { useEffect, useState } from "react";
import { api, type Job, type Config, type Schedule } from "../api";
import { bytes, dur, minutesToHM, DAY_LABELS } from "../format";
import type { LiveState } from "../App";
import "../components.css";

export default function Queue({ live }: { live: LiveState }) {
  const [jobs, setJobs] = useState<Job[]>([]);
  const [summary, setSummary] = useState<any>(null);
  const [config, setConfig] = useState<Config | null>(null);
  const [tab, setTab] = useState<"pending" | "active" | "history">("active");

  const load = async () => {
    try {
      const statusByTab: Record<string, string> = {
        active: "running,verifying,replacing",
        pending: "queued",
        history: "done,failed,canceled",
      };
      const r = await api.jobs(statusByTab[tab]);
      setJobs(r.jobs);
      setSummary(await api.queueSummary());
      setConfig(await api.config());
    } catch {}
  };

  useEffect(() => { load(); /* eslint-disable-next-line */ }, [tab, live.queueVersion]);
  useEffect(() => {
    const t = setInterval(load, 5000);
    return () => clearInterval(t);
  });

  const pauseToggle = async () => {
    if (summary?.paused) await api.resumeQueue();
    else await api.pauseQueue();
    load();
  };

  return (
    <div>
      <div className="page-head">
        <div>
          <h1 className="page-title">Queue</h1>
          <div className="page-sub">
            {summary
              ? `${summary.counts.queued || 0} pending · ${summary.counts.running || 0} running · ${bytes(summary.realized_saved)} saved so far`
              : "—"}
          </div>
        </div>
        <div className="toolbar">
          <button className={`btn${summary?.paused ? " btn-danger" : ""}`} onClick={pauseToggle}>
            {summary?.paused ? "Resume queue" : "Pause queue"}
          </button>
        </div>
      </div>

      <div className="stat-strip">
        <div className={`stat ${summary?.window_open ? "teal" : ""}`}>
          <div className="stat-label">Window</div>
          <div className="stat-value">{summary?.window_open ? "open" : "closed"}</div>
        </div>
        <div className="stat">
          <div className="stat-label">Pending</div>
          <div className="stat-value">{summary?.counts.queued ?? 0}</div>
        </div>
        <div className="stat amber">
          <div className="stat-label">Failed</div>
          <div className="stat-value">{summary?.counts.failed ?? 0}</div>
        </div>
        <div className="stat teal">
          <div className="stat-label">
            Saved <span className="unit">realized</span>
          </div>
          <div className="stat-value">{bytes(summary?.realized_saved ?? 0)}</div>
        </div>
        <div className="stat">
          <div className="stat-label">Jobs done</div>
          <div className="stat-value">{summary?.jobs_done ?? 0}</div>
        </div>
      </div>

      <ScheduleTimeline config={config} />

      <div className="tabs" style={{ margin: "22px 0 14px" }}>
        <button className={`tab${tab === "active" ? " active" : ""}`} onClick={() => setTab("active")}>
          Active
        </button>
        <button className={`tab${tab === "pending" ? " active" : ""}`} onClick={() => setTab("pending")}>
          Pending
        </button>
        <button className={`tab${tab === "history" ? " active" : ""}`} onClick={() => setTab("history")}>
          History
        </button>
      </div>

      <table className="table">
        <thead>
          <tr>
            <th style={{ width: 60 }}>Job</th>
            <th>File</th>
            <th>Codec</th>
            <th>Status</th>
            {tab === "active" && <th>Progress</th>}
            {tab === "history" && <th>Result</th>}
            <th />
          </tr>
        </thead>
        <tbody>
          {jobs.map((j) => {
            const p = live.progress[j.id];
            const pctv = p ? Math.round(p.pct * 100) : null;
            const title = j.file_title || j.src_path.split("/").slice(-1)[0];
            return (
              <tr key={j.id}>
                <td className="mono dim">#{j.id}</td>
                <td style={{ maxWidth: 380, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                  <span title={j.src_path}>{title}</span>
                </td>
                <td>
                  <span className={`chip ${j.codec === "av1" ? "c-av1" : "c-hevc"}`}>
                    {j.codec.toUpperCase()}
                  </span>
                </td>
                <td>
                  <span className={`badge b-${j.status}`}>{j.status}</span>
                  {j.attempts > 1 && <span className="mono faint" style={{ marginLeft: 6 }}>×{j.attempts}</span>}
                </td>
                {tab === "active" && (
                  <td style={{ minWidth: 160 }}>
                    {pctv !== null ? (
                      <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
                        <div className="gauge" style={{ height: 5 }}>
                          <div className="gauge-saved" style={{ width: `${pctv}%` }} />
                        </div>
                        <span className="mono dim" style={{ whiteSpace: "nowrap" }}>
                          {pctv}%{p.eta_sec > 0 ? ` · ${dur(p.eta_sec)}` : ""}
                        </span>
                      </div>
                    ) : (
                      <span className="dim">—</span>
                    )}
                  </td>
                )}
                {tab === "history" && (
                  <td className="mono dim">
                    {j.status === "done" && j.src_size > j.output_size
                      ? `${bytes(j.src_size)} → ${bytes(j.output_size)} (−${Math.round((1 - j.output_size / j.src_size) * 100)}%)`
                      : j.error
                        ? <span title={j.error_tail || j.error} style={{ color: "var(--red)" }}>{j.error.slice(0, 60)}</span>
                        : "—"}
                  </td>
                )}
                <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                  {tab === "pending" && (
                    <>
                      <button className="btn mini" onClick={() => api.runNowJob(j.id).then(load)}>run now</button>{" "}
                    </>
                  )}
                  {(tab === "pending" || tab === "active") && (
                    <button className="btn mini btn-danger" onClick={() => api.cancelJob(j.id).then(load)}>
                      cancel
                    </button>
                  )}
                  {(tab === "history" && (j.status === "failed" || j.status === "canceled")) && (
                    <button className="btn mini" onClick={() => api.retryJob(j.id).then(load)}>retry</button>
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      {jobs.length === 0 && (
        <div className="empty">
          <div className="big">
            {tab === "active" ? "Encoder idle" : tab === "pending" ? "Queue empty" : "No history yet"}
          </div>
          <div>
            {tab === "pending"
              ? "Add files from the library, or queue a whole season from a show."
              : tab === "active"
                ? "Jobs appear here while encoding. Outside the schedule window, no new job starts."
                : "Finished jobs and their realized savings show up here."}
          </div>
        </div>
      )}
    </div>
  );
}

// Signature element: 24h broadcast-style window timeline.
function ScheduleTimeline({ config }: { config: Config | null }) {
  const [editing, setEditing] = useState<Schedule | null>(null);
  if (!config) return null;

  const now = new Date();
  const nowMin = now.getHours() * 60 + now.getMinutes();
  const dayBit = 1 << ((now.getDay() + 6) % 7);

  const todays = config.schedules.filter((s) => (s.days & dayBit) !== 0);

  return (
    <div className="card">
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 12 }}>
        <div className="stat-label">Processing windows — today ({DAY_LABELS[(now.getDay() + 6) % 7]})</div>
        <button className="btn mini" onClick={() => setEditing({ id: 0, days: 0x7f, start: 60, end: 360, label: "nightly" })}>
          + window
        </button>
      </div>
      <div className="tl24">
        <div className="tl24-track">
          {todays.length === 0 && (
            <div className="tl24-none">no windows today — queue runs only via “run now”</div>
          )}
          {todays.map((s, i) => {
            const spans = s.start <= s.end ? [[s.start, s.end]] : [[s.start, 1440], [0, s.end]];
            return spans.map(([a, b], j) => (
              <div
                key={`${i}-${j}`}
                className="tl24-span"
                style={{ left: `${(a / 1440) * 100}%`, width: `${((b - a) / 1440) * 100}%` }}
                title={`${s.label || "window"} ${minutesToHM(s.start)}–${minutesToHM(s.end)}`}
              />
            ));
          })}
          <div className="tl24-now" style={{ left: `${(nowMin / 1440) * 100}%` }} title="now" />
        </div>
        <div className="tl24-scale mono">
          <span>00</span><span>06</span><span>12</span><span>18</span><span>24</span>
        </div>
      </div>
      <div className="tl24-list">
        {config.schedules.map((s) => (
          <span key={s.id} className="chip" style={{ cursor: "pointer" }} onClick={() => setEditing(s)}>
            {s.label || "window"} · {minutesToHM(s.start)}–{minutesToHM(s.end)} ·{" "}
            {DAY_LABELS.filter((_, i) => s.days & (1 << i)).join("") || "no days"}
          </span>
        ))}
      </div>
      {editing && <ScheduleEditor config={config} sched={editing} onClose={() => { setEditing(null); window.location.reload(); }} />}
    </div>
  );
}

function ScheduleEditor({ config, sched, onClose }: { config: Config; sched: Schedule; onClose: () => void }) {
  const [s, setS] = useState<Schedule>(sched);
  const save = async () => {
    const schedules = sched.id === 0
      ? [...config.schedules, { ...s, id: Date.now() }]
      : config.schedules.map((x) => (x.id === sched.id ? s : x));
    await api.saveConfig({ schedules });
    onClose();
  };
  const del = async () => {
    await api.saveConfig({ schedules: config.schedules.filter((x) => x.id !== sched.id) });
    onClose();
  };
  const toStr = (m: number) => `${String(Math.floor(m / 60)).padStart(2, "0")}:${String(m % 60).padStart(2, "0")}`;
  const fromStr = (v: string) => {
    const [h, m] = v.split(":").map(Number);
    return (h || 0) * 60 + (m || 0);
  };
  return (
    <div className="modal-backdrop" onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal" role="dialog" aria-label="edit schedule window">
        <div className="stat-label" style={{ marginBottom: 12 }}>
          {sched.id === 0 ? "New window" : "Edit window"}
        </div>
        <div className="field">
          <label>Label</label>
          <input className="input" value={s.label || ""} onChange={(e) => setS({ ...s, label: e.target.value })} />
        </div>
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 12 }}>
          <div className="field">
            <label>Start</label>
            <input
              className="input"
              type="time"
              value={toStr(s.start)}
              onChange={(e) => setS({ ...s, start: fromStr(e.target.value) })}
            />
          </div>
          <div className="field">
            <label>End (may wrap past midnight)</label>
            <input
              className="input"
              type="time"
              value={toStr(s.end)}
              onChange={(e) => setS({ ...s, end: fromStr(e.target.value) })}
            />
          </div>
        </div>
        <div className="field">
          <label>Days</label>
          <div className="chips">
            {DAY_LABELS.map((d, i) => (
              <button
                key={d}
                className={`chip${s.days & (1 << i) ? " c-hevc" : ""}`}
                style={{ cursor: "pointer" }}
                onClick={() => setS({ ...s, days: s.days ^ (1 << i) })}
              >
                {d}
              </button>
            ))}
          </div>
        </div>
        <div className="toolbar" style={{ marginTop: 8 }}>
          <button className="btn btn-primary" onClick={save}>Save window</button>
          {sched.id !== 0 && <button className="btn btn-danger" onClick={del}>Delete</button>}
        </div>
      </div>
    </div>
  );
}
