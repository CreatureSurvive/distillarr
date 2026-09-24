// SPDX-License-Identifier: GPL-3.0-or-later

// Library trend charts: daily snapshots from /api/v1/trends.
// Hand-drawn SVG like the rest of Stats; hover shows a crosshair and the
// day's values. Colours: the dataviz reference palette's dark steps,
// validated against --surface (slots 1-5), in fixed order; "Other" is gray.
import { useEffect, useMemo, useState } from "react";
import { api, type TrendSnapshot } from "../api";
import { bytes } from "../format";

const SLOTS = ["#3987e5", "#d95926", "#199e70", "#c98500", "#d55181"];
const OTHER = "#7a7f87";

type Series = { key: string; label: string; color: string };
type Point = { t: number; day: string; v: Record<string, number> };

const W = 640, H = 180, PAD_L = 56, PAD_R = 12, PAD_T = 10, PAD_B = 22;

function TimeChart({ title, points, series, stacked, label }: {
  title: string; points: Point[]; series: Series[]; stacked?: boolean; label: string;
}) {
  const [hover, setHover] = useState<number | null>(null);
  if (points.length < 2) return null;
  const t0 = points[0].t, t1 = points[points.length - 1].t;
  const x = (t: number) => PAD_L + ((t - t0) / Math.max(1, t1 - t0)) * (W - PAD_L - PAD_R);
  const tops = points.map((p) => {
    let acc = 0;
    return series.map((s) => (stacked ? (acc += p.v[s.key] || 0) : p.v[s.key] || 0));
  });
  const max = Math.max(1, ...tops.flat());
  const y = (v: number) => PAD_T + (1 - v / max) * (H - PAD_T - PAD_B);
  const path = (si: number) => points.map((p, i) => `${i ? "L" : "M"}${x(p.t).toFixed(1)},${y(tops[i][si]).toFixed(1)}`).join("");
  const area = (si: number) => {
    const top = path(si);
    const bottom = points.slice().reverse().map((p, ri) => {
      const i = points.length - 1 - ri;
      return `L${x(p.t).toFixed(1)},${y(si ? tops[i][si - 1] : 0).toFixed(1)}`;
    }).join("");
    return top + bottom + "Z";
  };
  const onMove = (e: React.PointerEvent<SVGSVGElement>) => {
    const r = e.currentTarget.getBoundingClientRect();
    const px = ((e.clientX - r.left) / r.width) * W;
    let best = 0;
    points.forEach((p, i) => { if (Math.abs(x(p.t) - px) < Math.abs(x(points[best].t) - px)) best = i; });
    setHover(best);
  };
  const hp = hover !== null ? points[hover] : null;
  return (
    <figure className="trend">
      <figcaption className="eyebrow">{title}</figcaption>
      {series.length > 1 && (
        <div className="trend-legend">
          {series.map((s) => <span key={s.key}><i style={{ background: s.color }} />{s.label}</span>)}
        </div>
      )}
      <div className="trend-plot">
        <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label={label} onPointerMove={onMove} onPointerLeave={() => setHover(null)}>
          {[0, 0.5, 1].map((f) => (
            <g key={f}>
              <line x1={PAD_L} x2={W - PAD_R} y1={y(max * f)} y2={y(max * f)} className="trend-grid" />
              <text x={PAD_L - 6} y={y(max * f) + 4} textAnchor="end" className="trend-axis">{bytes(max * f)}</text>
            </g>
          ))}
          <text x={PAD_L} y={H - 6} className="trend-axis">{points[0].day}</text>
          <text x={W - PAD_R} y={H - 6} textAnchor="end" className="trend-axis">{points[points.length - 1].day}</text>
          {stacked
            ? series.map((s, si) => <path key={s.key} d={area(si)} fill={s.color} fillOpacity={0.85} stroke="var(--surface)" strokeWidth={1} />)
            : series.map((s, si) => <path key={s.key} d={path(si)} fill="none" stroke={s.color} strokeWidth={2} strokeLinejoin="round" />)}
          {hp && <line x1={x(hp.t)} x2={x(hp.t)} y1={PAD_T} y2={H - PAD_B} className="trend-cross" />}
        </svg>
        {hp && (
          <div className="trend-tip" style={{ left: `${(x(hp.t) / W) * 100}%` }}>
            <div className="mono small">{hp.day}</div>
            {series.slice().reverse().map((s) => (
              <div key={s.key} className="small"><i style={{ background: s.color }} />{s.label} <b className="mono">{bytes(hp.v[s.key] || 0)}</b></div>
            ))}
          </div>
        )}
      </div>
      <details className="trend-table">
        <summary className="dim small">Table</summary>
        <div className="table-wrap">
          <table className="small mono">
            <thead><tr><th>Day</th>{series.map((s) => <th key={s.key}>{s.label}</th>)}</tr></thead>
            <tbody>{points.slice().reverse().map((p) => (
              <tr key={p.day}><td>{p.day}</td>{series.map((s) => <td key={s.key}>{bytes(p.v[s.key] || 0)}</td>)}</tr>
            ))}</tbody>
          </table>
        </div>
      </details>
    </figure>
  );
}

export function TrendsPanel({ version }: { version: number }) {
  const [rows, setRows] = useState<TrendSnapshot[] | null>(null);
  useEffect(() => { api.trends().then(setRows).catch(() => {}); }, [version]);

  const charts = useMemo(() => {
    if (!rows) return null;
    const t = (d: string) => Date.parse(d + "T00:00:00");
    // Library by codec: only full snapshots (backfilled days carry savings only).
    const full = rows.filter((r) => r.lib_bytes > 0);
    const latest = full[full.length - 1]?.bytes_by_codec || {};
    const top = Object.keys(latest).sort((a, b) => latest[b] - latest[a]).slice(0, SLOTS.length);
    const codecSeries: Series[] = top.map((k, i) => ({ key: k, label: k.toUpperCase(), color: SLOTS[i] }));
    const allCodecs = new Set(full.flatMap((r) => Object.keys(r.bytes_by_codec || {})));
    if ([...allCodecs].some((k) => !top.includes(k))) codecSeries.push({ key: "__other", label: "Other", color: OTHER });
    const codecPts = full.map((r) => {
      const v: Record<string, number> = {};
      for (const [k, n] of Object.entries(r.bytes_by_codec || {})) {
        const key = top.includes(k) ? k : "__other";
        v[key] = (v[key] || 0) + n;
      }
      return { t: t(r.day), day: r.day, v };
    });
    const savedPts = rows.map((r) => ({ t: t(r.day), day: r.day, v: { saved: r.saved_cumulative } }));
    const fsNames = [...new Set(full.flatMap((r) => Object.keys(r.free_bytes_by_fs || {})))].sort().slice(0, SLOTS.length);
    const fsSeries = fsNames.map((k, i) => ({ key: k, label: k, color: SLOTS[i] }));
    const fsPts = full.map((r) => ({ t: t(r.day), day: r.day, v: r.free_bytes_by_fs || {} }));
    return { codecSeries, codecPts, savedPts, fsSeries, fsPts };
  }, [rows]);

  if (!charts) return null;
  const enough = charts.codecPts.length >= 2 || charts.savedPts.length >= 2;
  return (
    <section className="panel stats">
      <div className="eyebrow">Library trends</div>
      {!enough && <p className="dim small">A snapshot is taken once a day; charts appear after the second one.</p>}
      <div className="trends-grid">
        <TimeChart title="Library size by codec" points={charts.codecPts} series={charts.codecSeries} stacked label="Library size by video codec over time, stacked" />
        <TimeChart title="Space saved (cumulative)" points={charts.savedPts} series={[{ key: "saved", label: "Saved", color: SLOTS[2] }]} label="Cumulative space saved over time" />
        <TimeChart title="Free space per filesystem" points={charts.fsPts} series={charts.fsSeries} label="Free space per filesystem over time" />
      </div>
    </section>
  );
}
