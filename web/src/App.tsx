import { useEffect, useState } from "react";
import { NavLink, Route, Routes } from "react-router-dom";
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

export type LiveState = {
  progress: Record<number, Progress>;
  activeJobs: Job[];
  scan: ScanStats | null;
  queueVersion: number;
  hwVersion: number;
  previewVersion: number;
  jfVersion: number;
  notes: Record<number, string>;
};

const NAV = [
  { to: "/", label: "Movies", icon: "▦", end: true },
  { to: "/shows", label: "Shows", icon: "▤", end: false },
  { to: "/issues", label: "Issues", icon: "!", end: false },
  { to: "/queue", label: "Queue", icon: "≡", end: false },
  { to: "/settings", label: "Settings", icon: "⚙", end: false },
];

export default function App() {
  const [live, setLive] = useState<LiveState>({
    progress: {}, activeJobs: [], scan: null, queueVersion: 0, hwVersion: 0, previewVersion: 0, jfVersion: 0, notes: {},
  });
  const [jf, setJf] = useState<JfStatus | null>(null);

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

  const running = live.activeJobs.length;

  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="brand">
          <span className="brand-mark" aria-hidden />
          <div>
            <div className="brand-name">mediatrans</div>
            <div className="brand-sub">encode bay</div>
          </div>
        </div>
        <nav className="nav" aria-label="Main">
          {NAV.map((n) => (
            <NavLink key={n.to} to={n.to} end={n.end} className={({ isActive }) => `nav-link${isActive ? " active" : ""}`}>
              <span className="nav-glyph" aria-hidden>{n.icon}</span>
              <span className="nav-text">{n.label}</span>
              {n.to === "/queue" && running > 0 && <span className="nav-badge">{running}</span>}
            </NavLink>
          ))}
        </nav>
        <div className="sidebar-foot">
          {live.scan?.running && (
            <div className="side-status">
              <span className="pulse-dot" aria-hidden /> Scanning · {live.scan.probed} probed
            </div>
          )}
          <a href="#/settings" className={`side-status jf-${jf?.connected ? "ok" : jf?.configured ? "bad" : "off"}`}>
            <span className="dot" aria-hidden />
            {jf?.connected ? `Jellyfin connected` : jf?.configured ? "Jellyfin unreachable" : "Jellyfin not set up"}
          </a>
        </div>
      </aside>

      <main className="main">
        <Routes>
          <Route path="/" element={<Library live={live} />} />
          <Route path="/shows" element={<Shows live={live} />} />
          <Route path="/show/:title" element={<ShowView live={live} />} />
          <Route path="/file/:id" element={<FileDetail live={live} />} />
          <Route path="/issues" element={<Issues live={live} />} />
          <Route path="/queue" element={<Queue live={live} />} />
          <Route path="/preview/:id" element={<PreviewView live={live} />} />
          <Route path="/upscale/:id" element={<UpscaleView />} />
          <Route path="/settings" element={<SettingsView live={live} onJellyfin={() => setLive((l) => ({ ...l, jfVersion: l.jfVersion + 1 }))} />} />
        </Routes>
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
