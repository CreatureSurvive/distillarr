import { useEffect, useRef, useState } from "react";
import { NavLink, Route, Routes, useLocation } from "react-router-dom";
import { api, subscribe, type LiveEvent, type Progress, type Job, type ScanStats } from "./api";
import Library from "./views/Library";
import SeriesDetail from "./views/SeriesDetail";
import FileDetail from "./views/FileDetail";
import Queue from "./views/Queue";
import PreviewView from "./views/Preview";
import SettingsView from "./views/Settings";
import { bytes, dur } from "./format";

// live encode telemetry shared app-wide
export type LiveState = {
  progress: Record<number, Progress & { pct: number; eta_sec: number; size: number }>;
  activeJobs: Job[];
  scan: ScanStats | null;
  queueVersion: number;
  hwVersion: number;
  previewVersion: number;
};

const emptyLive: LiveState = {
  progress: {},
  activeJobs: [],
  scan: null,
  queueVersion: 0,
  hwVersion: 0,
  previewVersion: 0,
};

export default function App() {
  const [live, setLive] = useState<LiveState>(emptyLive);
  const loc = useLocation();
  const liveRef = useRef(live);
  liveRef.current = live;

  useEffect(() => {
    const refreshActive = async () => {
      try {
        const { jobs } = await api.jobs("running,verifying,replacing");
        setLive((l) => ({ ...l, activeJobs: jobs }));
      } catch {}
    };
    refreshActive();
    const t = setInterval(refreshActive, 10000);
    return () => clearInterval(t);
  }, [loc.pathname]);

  useEffect(() => {
    const unsub = subscribe((ev) => {
      const msg = ev.event === "message" ? ev.data : null;
      if (!msg) return;
      const { event, data } = msg;
      if (event === "progress" && data?.job_id !== undefined) {
        setLive((l) => ({ ...l, progress: { ...l.progress, [data.job_id]: data } }));
      } else if (event === "job") {
        if (["done", "failed", "canceled"].includes(data?.status)) {
          setLive((l) => {
            const p = { ...l.progress };
            delete p[data.id];
            return { ...l, progress: p };
          });
        }
        setLive((l) => ({
          ...l,
          queueVersion: l.queueVersion + 1,
          activeJobs: [
            ...l.activeJobs.filter((j) => j.id !== data.id),
            ...(["running", "verifying", "replacing"].includes(data?.status)
              ? [{ ...l.activeJobs.find((j) => j.id === data.id)!, ...data, file_title: l.activeJobs.find((j) => j.id === data.id)?.file_title ?? "" } as Job]
              : []),
          ].filter((j) => j && j.id),
        }));
      } else if (event === "queue") {
        setLive((l) => ({ ...l, queueVersion: l.queueVersion + 1 }));
      } else if (event === "scan") {
        setLive((l) => ({ ...l, scan: data }));
      } else if (event === "hardware") {
        setLive((l) => ({ ...l, hwVersion: l.hwVersion + 1 }));
      } else if (event === "preview") {
        setLive((l) => ({ ...l, previewVersion: l.previewVersion + 1 }));
      }
    });
    return unsub;
  }, []);

  return (
    <div className="shell">
      <aside className="sidebar">
        <div className="brand">
          <span className="brand-mark" aria-hidden />
          <div className="brand-text">
            <div className="brand-name">mediatrans</div>
            <div className="brand-sub">encode bay</div>
          </div>
        </div>
        <nav className="nav">
          <NavLink to="/" end className={({ isActive }) => `nav-link${isActive ? " active" : ""}`}>
            <span className="nav-glyph" aria-hidden>▤</span> Library
          </NavLink>
          <NavLink to="/queue" className={({ isActive }) => `nav-link${isActive ? " active" : ""}`}>
            <span className="nav-glyph" aria-hidden>≡</span> Queue
            {live.activeJobs.length > 0 && (
              <span className="nav-badge">{live.activeJobs.length}</span>
            )}
          </NavLink>
          <NavLink to="/settings" className={({ isActive }) => `nav-link${isActive ? " active" : ""}`}>
            <span className="nav-glyph" aria-hidden>⚙</span> Settings
          </NavLink>
        </nav>
        <div className="sidebar-foot">
          <ScanIndicator scan={live.scan} />
        </div>
      </aside>

      <main className="main">
        <Routes>
          <Route path="/" element={<Library live={live} />} />
          <Route path="/show/:title" element={<SeriesDetail live={live} />} />
          <Route path="/file/:id" element={<FileDetail live={live} />} />
          <Route path="/queue" element={<Queue live={live} />} />
          <Route path="/preview/:id" element={<PreviewView live={live} />} />
          <Route path="/settings" element={<SettingsView live={live} />} />
        </Routes>
      </main>

      <EncodeDeck live={live} />
    </div>
  );
}

function ScanIndicator({ scan }: { scan: ScanStats | null }) {
  if (!scan?.running) return null;
  return (
    <div className="scan-indicator">
      <span className="pulse-dot" aria-hidden />
      scanning · {scan.phase}
      {scan.probed > 0 && <div className="mono dim">{scan.probed} probed</div>}
    </div>
  );
}

// Signature: persistent transport bar with live encode telemetry.
function EncodeDeck({ live }: { live: LiveState }) {
  const active = live.activeJobs;
  if (active.length === 0 && Object.keys(live.progress).length === 0) return null;
  return (
    <footer className="deck" role="status" aria-label="live encode status">
      {active.length === 0 ? (
        <span className="deck-idle">queue idle</span>
      ) : (
        active.map((j) => {
          const p = live.progress[j.id];
          const pctv = p ? Math.round(p.pct * 100) : 0;
          return (
            <div key={j.id} className="deck-job">
              <div className="deck-title">
                <span className={`deck-status s-${j.status}`} />
                {j.file_title || j.src_path.split("/").pop()}
                <span className="mono dim"> · {j.codec.toUpperCase()} · {j.backend}</span>
              </div>
              <div className="deck-meter">
                <div className="deck-meter-fill" style={{ width: `${pctv}%` }} />
              </div>
              <div className="deck-tele mono">
                {p ? (
                  <>
                    <b>{pctv}%</b>
                    {p.eta_sec > 0 ? <> · eta {dur(p.eta_sec)}</> : null}
                    {p.speed > 0 ? <> · {p.speed.toFixed(2)}×</> : null}
                    {p.size > 0 ? <> · {bytes(p.size)}</> : null}
                  </>
                ) : (
                  <span className="dim">{j.status}…</span>
                )}
              </div>
            </div>
          );
        })
      )}
    </footer>
  );
}
