// SPDX-License-Identifier: GPL-3.0-or-later

// Savings dashboard shown above the job history.
import { useEffect, useState } from "react";
import { api, type HistoryStats, type StatGroup } from "../api";
import { bytes } from "../format";

function mondayOf(d: Date): Date {
  const x = new Date(d.getFullYear(), d.getMonth(), d.getDate());
  x.setDate(x.getDate() - ((x.getDay() + 6) % 7));
  return x;
}
const ymd = (d: Date) => `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;

// Last n weeks, oldest first, zero-filled.
function weekSeries(weeks: StatGroup[], n = 12) {
  const by = new Map(weeks.map((w) => [w.key, w]));
  const out: { key: string; label: string; saved: number; jobs: number }[] = [];
  const start = mondayOf(new Date());
  for (let i = n - 1; i >= 0; i--) {
    const d = new Date(start);
    d.setDate(d.getDate() - 7 * i);
    const g = by.get(ymd(d));
    out.push({ key: ymd(d), label: d.toLocaleDateString(undefined, { month: "short", day: "numeric" }), saved: g?.saved || 0, jobs: g?.jobs || 0 });
  }
  return out;
}

const KIND: Record<string, string> = { encode: "Re-encodes", remux: "Quick fixes" };
const LIB: Record<string, string> = { movies: "Movies", tvshows: "TV" };

function Breakdown({ title, groups, label }: { title: string; groups: StatGroup[]; label: (k: string) => string }) {
  const max = Math.max(1, ...groups.map((g) => g.saved));
  if (groups.length === 0) return null;
  return (
    <div className="bd">
      <div className="eyebrow">{title}</div>
      {groups.map((g) => (
        <div key={g.key} className="bd-row">
          <span className="bd-key">{label(g.key)}</span>
          <div className="bd-bar"><div style={{ width: `${(Math.max(0, g.saved) / max) * 100}%` }} /></div>
          <span className="mono small bd-val">{bytes(g.saved)}</span>
          <span className="mono small faint bd-sub">{g.jobs} job{g.jobs === 1 ? "" : "s"}{g.src_bytes > 0 ? ` · −${Math.round((1 - g.out_bytes / g.src_bytes) * 100)}%` : ""}</span>
        </div>
      ))}
    </div>
  );
}

export function HistoryStatsPanel({ version }: { version: number }) {
  const [s, setS] = useState<HistoryStats | null>(null);
  const [left, setLeft] = useState<number | null>(null);
  useEffect(() => {
    api.stats().then(setS).catch(() => {});
    Promise.all([api.libraryStats("movies"), api.libraryStats("tvshows")])
      .then(([m, t]) => setLeft(m.projected_saved + t.projected_saved)).catch(() => {});
  }, [version]);
  if (!s) return null;
  const t = s.totals;
  const weeks = weekSeries(s.weeks);
  const wmax = Math.max(1, ...weeks.map((w) => w.saved));
  const reduction = t.src_bytes > 0 ? (1 - t.out_bytes / t.src_bytes) * 100 : 0;
  const perHour = t.encode_hours > 0 ? t.saved / t.encode_hours : 0;

  return (
    <section className="panel stats">
      <div className="kpis">
        <div className="kpi"><div className="kpi-val teal">{bytes(t.saved)}</div><div className="kpi-label">space saved</div></div>
        <div className="kpi"><div className="kpi-val">{reduction.toFixed(0)}%</div><div className="kpi-label">average reduction</div></div>
        <div className="kpi"><div className="kpi-val">{t.done.toLocaleString()}</div><div className="kpi-label">jobs done{t.failed ? <span className="bad"> · {t.failed} failed</span> : null}</div></div>
        <div className="kpi"><div className="kpi-val">{t.encode_hours < 10 ? t.encode_hours.toFixed(1) : Math.round(t.encode_hours)}h</div><div className="kpi-label">encoding{perHour > 0 ? ` · ${bytes(perHour)}/h` : ""}</div></div>
        {left !== null && <div className="kpi"><div className="kpi-val warm">{bytes(left)}</div><div className="kpi-label">still reclaimable</div></div>}
      </div>

      <div className="stats-grid">
        <div>
          <div className="eyebrow">Saved per week</div>
          <div className="wk-chart" role="img" aria-label="Space saved per week, last 12 weeks">
            {weeks.map((w) => (
              <div key={w.key} className="wk-col" title={`Week of ${w.label}: ${bytes(w.saved)} saved, ${w.jobs} jobs`}>
                <div className="wk-bar" style={{ height: `${(w.saved / wmax) * 100}%` }} />
              </div>
            ))}
          </div>
          <div className="wk-scale mono faint"><span>{weeks[0].label}</span><span>this week</span></div>
        </div>
        <div>
          <Breakdown title="By library" groups={s.library} label={(k) => LIB[k] || k} />
          <Breakdown title="By kind" groups={s.kinds} label={(k) => KIND[k] || k} />
          <Breakdown title="By output codec" groups={s.codecs} label={(k) => k.toUpperCase()} />
        </div>
      </div>

      {s.top.length > 0 && (
        <div className="top-savers">
          <div className="eyebrow">Biggest wins</div>
          <ol>
            {s.top.map((x) => (
              <li key={x.job_id}>
                <span className="ts-name" title={x.path}>{x.path.split("/").pop()}</span>
                <span className="mono small teal">−{bytes(x.src_bytes - x.out_bytes)}</span>
                <span className="mono small faint">{bytes(x.src_bytes)} → {bytes(x.out_bytes)}</span>
              </li>
            ))}
          </ol>
        </div>
      )}
    </section>
  );
}
