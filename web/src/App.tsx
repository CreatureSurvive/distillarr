// SPDX-License-Identifier: GPL-3.0-or-later

import { useEffect, useState } from "react";
import { NavLink, Route, Routes, useLocation } from "react-router-dom";
import { Film, ListOrdered, Settings, TriangleAlert, Tv } from "lucide-react";
import { api, subscribe, type Job, type JfStatus, type Progress, type ScanStats } from "./api";
import { Toaster } from "./components";
import { bytes, dur } from "./format";
import Library from "./views/Library";
import Shows from "./views/Shows";
import ShowView from "./views/Show";
import FileDetail from "./views/FileDetail";
import Queue from "./views/Queue";
import Issues from "./views/Issues";
import PreviewView from "./views/Preview";
import UpscaleView from "./views/Upscale";
import SettingsView from "./views/Settings";
import SetupWizard from "./views/Setup";

export type LiveState = {
  progress: Record<number, Progress>;
  activeJobs: Job[];
  scan: ScanStats | null;
  queueVersion: number;
  hwVersion: number;
  previewVersion: number;
  jfVersion: number;
  plexVersion: number;
  arrVersion: number;
  intakeVersion: number;
  notes: Record<number, string>;
  diskPressure: boolean;
};

const NAV = [
  { to: "/", label: "Movies", icon: Film, end: true },
  { to: "/shows", label: "Shows", icon: Tv, end: false },
  { to: "/issues", label: "Issues", icon: TriangleAlert, end: false },
  { to: "/queue", label: "Queue", icon: ListOrdered, end: false },
  { to: "/settings", label: "Settings", icon: Settings, end: false },
];

export default function App() {
  const [live, setLive] = useState<LiveState>({
    progress: {}, activeJobs: [], scan: null, queueVersion: 0, hwVersion: 0, previewVersion: 0, jfVersion: 0, plexVersion: 0, arrVersion: 0, intakeVersion: 0, notes: {}, diskPressure: false,
  });
  const [jf, setJf] = useState<JfStatus | null>(null);
  const [needsConfirmation, setNeedsConfirmation] = useState(0);

  useEffect(() => {
    const refresh = () => api.jobs("running,verifying,replacing").then(({ jobs }) => setLive((l) => ({ ...l, activeJobs: jobs }))).catch(() => {});
    refresh();
    const t = setInterval(refresh, 8000);
    const unsub = subscribe((event, data) => {
      if (event === "progress" && data?.job_id !== undefined) {
        setLive((l) => ({ ...l, progress: { ...l.progress, [data.job_id]: data } }));
      } else if (event === "job" && data?.note) {
        setLive((l) => ({ ...l, notes: { ...l.notes, [data.id]: data.note } }));
      } else if (event === "job" || event === "queue") {
        if (event === "job" && ["done", "failed", "canceled"].includes(data?.status)) {
          setLive((l) => {
            const p = { ...l.progress };
            delete p[data.id];
            return { ...l, progress: p };
          });
        }
        setLive((l) => ({ ...l, queueVersion: l.queueVersion + 1 }));
        refresh();
      } else if (event === "scan") {
        setLive((l) => ({ ...l, scan: data }));
      } else if (event === "hardware") {
        setLive((l) => ({ ...l, hwVersion: l.hwVersion + 1 }));
      } else if (event === "preview") {
        setLive((l) => ({ ...l, previewVersion: l.previewVersion + 1 }));
      } else if (event === "jellyfin") {
        if (data?.done || data?.error) setLive((l) => ({ ...l, jfVersion: l.jfVersion + 1 }));
      } else if (event === "plex") {
        if (data?.done || data?.error) setLive((l) => ({ ...l, plexVersion: l.plexVersion + 1 }));
      } else if (event === "arr") {
        if (data?.done) setLive((l) => ({ ...l, arrVersion: l.arrVersion + 1 }));
      } else if (event === "intake") {
        setLive((l) => ({ ...l, intakeVersion: l.intakeVersion + 1 }));
      } else if (event === "system" && data?.pressure !== undefined) {
        setLive((l) => ({ ...l, diskPressure: !!data.pressure }));
      }
    });
    return () => {
      clearInterval(t);
      unsub();
    };
  }, []);

  useEffect(() => {
    api.jellyfinStatus().then(setJf).catch(() => setJf(null));
  }, [live.jfVersion]);

  useEffect(() => {
    // SSE "system" only fires on a change; fetch the state as of page
    // load once so a reload mid-pressure still shows the banner.
    api.system().then((s) => setLive((l) => ({ ...l, diskPressure: !!s.disk_pressure }))).catch(() => {});
  }, []);

  useEffect(() => {
    api.intake("needs_confirmation").then((r) => setNeedsConfirmation(r.rows.length)).catch(() => {});
  }, [live.intakeVersion]);

  // no library yet means a fresh install; show the setup wizard
  // (Settings stays reachable for the wizard's integration links).
  const [needsSetup, setNeedsSetup] = useState(false);
  useEffect(() => { api.config().then((c) => setNeedsSetup(c.libraries.length === 0)).catch(() => {}); }, []);
  const loc = useLocation();
  // New pages open at the top; list views restore their own position.
  useEffect(() => { window.scrollTo(0, 0); }, [loc.pathname]);

  const running = live.activeJobs.length;

  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="brand">
          <span className="brand-mark" aria-hidden />
          <div>
            <div className="brand-name">distillarr</div>
            <div className="brand-sub">encode bay</div>
          </div>
        </div>
        <nav className="nav" aria-label="Main">
          {NAV.map((n) => (
            <NavLink key={n.to} to={n.to} end={n.end} className={({ isActive }) => `nav-link${isActive ? " active" : ""}`}>
              <n.icon className="nav-glyph" aria-hidden strokeWidth={1.8} />
              <span className="nav-text">{n.label}</span>
              {n.to === "/queue" && needsConfirmation > 0 && (
                <span className="nav-badge nav-badge-warn" title="Needs confirmation">{needsConfirmation}</span>
              )}
              {n.to === "/queue" && needsConfirmation === 0 && running > 0 && <span className="nav-badge">{running}</span>}
            </NavLink>
          ))}
        </nav>
        <div className="sidebar-foot">
          {live.diskPressure && (
            <div className="side-status jf-bad" title="A library's filesystem is low on free space: autopilot favors quick wins and its budget is multiplied until this clears">
              <span className="dot" aria-hidden /> Disk pressure — quick wins first
            </div>
          )}
          {live.scan?.running && (
            <div className="side-status">
              <span className="pulse-dot" aria-hidden /> Scanning · {live.scan.probed} probed
            </div>
          )}
          <a href="#/settings/connections" className={`side-status jf-${jf?.connected ? "ok" : jf?.configured ? "bad" : "off"}`}>
            <span className="dot" aria-hidden />
            {jf?.connected ? `Jellyfin connected` : jf?.configured ? "Jellyfin unreachable" : "Jellyfin not set up"}
          </a>
        </div>
      </aside>

      <main className="main">
        {needsSetup && !loc.pathname.startsWith("/settings") ? <SetupWizard onDone={() => setNeedsSetup(false)} /> : (
        <Routes>
          <Route path="/" element={<Library live={live} />} />
          <Route path="/shows" element={<Shows live={live} />} />
          <Route path="/show/:title" element={<ShowView live={live} />} />
          <Route path="/file/:id" element={<FileDetail live={live} />} />
          <Route path="/issues" element={<Issues live={live} />} />
          <Route path="/queue" element={<Queue live={live} />} />
          <Route path="/preview/:id" element={<PreviewView live={live} />} />
          <Route path="/upscale/:id" element={<UpscaleView />} />
          <Route path="/settings/:tab?" element={<SettingsView live={live}
            onJellyfin={() => setLive((l) => ({ ...l, jfVersion: l.jfVersion + 1 }))}
            onPlex={() => setLive((l) => ({ ...l, plexVersion: l.plexVersion + 1 }))} />} />
        </Routes>
        )}
      </main>

      <EncodeDeck live={live} />
      <Toaster />
    </div>
  );
}

// Persistent transport bar with live encode telemetry.
function EncodeDeck({ live }: { live: LiveState }) {
  if (live.activeJobs.length === 0) return null;
  return (
    <footer className="deck" role="status" aria-label="Encoding now">
      {live.activeJobs.map((j) => {
        const p = live.progress[j.id];
        const pctv = p ? Math.round(p.pct * 1000) / 10 : 0;
        return (
          <a key={j.id} href="#/queue" className="deck-job">
            <div className="deck-title">
              <span className={`deck-status s-${j.status}`} />
              {j.file_title || j.src_path.split("/").pop()}
            </div>
            <div className="deck-meter"><div className="deck-meter-fill" style={{ width: `${pctv}%` }} /></div>
            <div className="deck-tele mono">
              {j.status !== "running" ? (
                <span className="dim">{j.status}…</span>
              ) : p ? (
                <>
                  <b>{pctv.toFixed(1)}%</b>
                  {p.speed > 0 && <> · {p.speed.toFixed(1)}×</>}
                  {p.eta_sec > 0 && <> · {dur(p.eta_sec)} left</>}
                  <span className="hide-sm">{p.size > 0 && <> · {bytes(p.size)}</>}</span>
                </>
              ) : (
                <span className="dim">{live.notes[j.id] || "starting…"}</span>
              )}
            </div>
          </a>
        );
      })}
    </footer>
  );
}
