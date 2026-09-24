// First-run setup wizard: shown while no library is configured.
// Libraries (server-side directory browser), hardware probe, encoding
// defaults and window, optional integrations, finish (queue stays paused).
import { useEffect, useState } from "react";
import { api, type Config, type HwInfo } from "../api";
import { Seg, toast } from "../components";
import { backendLabel } from "../format";
import { ScheduleTimeline } from "./Queue";

const STEPS = ["Libraries", "Hardware", "Encoding", "Integrations", "Finish"];

function DirBrowser({ onPick }: { onPick: (path: string) => void }) {
  const [path, setPath] = useState("");
  const [dirs, setDirs] = useState<string[]>([]);
  const [parent, setParent] = useState<string | undefined>();
  const [err, setErr] = useState("");
  const open = (p: string) =>
    api.browse(p).then((r) => { setPath(r.path); setDirs(r.dirs); setParent(r.parent); setErr(""); }).catch((e) => setErr(e.message));
  useEffect(() => { open(""); }, []);
  return (
    <div className="dir-browser">
      <div className="toolbar">
        <span className="mono small">{path || "Mounted volumes"}</span>
        {path && <button className="btn mini" onClick={() => open(parent && parent !== path ? parent : "")}>Up</button>}
        {path && <button className="btn mini" onClick={() => open("")}>Top</button>}
        {path && <button className="btn mini btn-primary" onClick={() => onPick(path)}>Add this folder</button>}
      </div>
      {err && <div className="alert">{err}</div>}
      <ul className="dir-list">
        {dirs.length === 0 && <li className="dim small">{path ? "No subfolders." : "No media volumes are mounted into the container."}</li>}
        {dirs.map((d) => (
          <li key={d}><button className="linkish" onClick={() => open(d)}>{path ? d.split("/").pop() : d}</button></li>
        ))}
      </ul>
    </div>
  );
}

export default function SetupWizard({ onDone }: { onDone: () => void }) {
  const [cfg, setCfg] = useState<Config | null>(null);
  const [step, setStep] = useState(0);
  const [libs, setLibs] = useState<{ name: string; path: string }[]>([]);
  const [hw, setHw] = useState<HwInfo | null>(null);
  const [probing, setProbing] = useState(false);

  useEffect(() => { api.config().then((c) => { setCfg(c); setLibs(c.libraries); }).catch(() => {}); }, []);
  useEffect(() => { if (step === 1) api.hw().then(setHw).catch(() => {}); }, [step]);

  const save = async (p: Partial<Config>) => {
    try { setCfg(await api.saveConfig(p)); } catch (e: any) { toast(e.message, "err"); throw e; }
  };

  const next = async () => {
    if (step === 0) {
      if (libs.length === 0) return toast("Add at least one library folder", "err");
      await save({ libraries: libs });
    }
    setStep(step + 1);
  };

  const probe = async () => {
    setProbing(true);
    try {
      await api.reprobe();
      for (let i = 0; i < 30; i++) {
        await new Promise((r) => setTimeout(r, 2000));
        const h = await api.hw();
        setHw(h);
        if (h.report && Date.parse(h.report.tested_at) > Date.now() - 120000) break;
      }
    } finally {
      setProbing(false);
    }
  };

  const finish = async () => {
    await api.scanStart().catch(() => {});
    onDone();
  };

  if (!cfg) return <div className="result-count mono dim">Loading…</div>;
  const ok = (hw?.report?.results || []).filter((r) => r.ok);

  return (
    <div className="settings setup">
      <header className="page-head">
        <div>
          <h1 className="page-title">Set up Distillarr</h1>
          <div className="page-sub">Step {step + 1} of {STEPS.length}: {STEPS[step]}</div>
        </div>
      </header>
      <nav className="settings-nav" aria-label="Setup steps">
        {STEPS.map((s, i) => <a key={s} className={i === step ? "active" : undefined} aria-current={i === step ? "step" : undefined}>{i + 1}. {s}</a>)}
      </nav>

      <section className="panel">
        {step === 0 && (
          <>
            <h2 className="panel-title">Where is your media?</h2>
            <p className="dim small">Pick each library folder as the container sees it (the volumes you mounted). Movies and TV
              in separate folders work best; say which kind each one holds. Nothing is changed on disk until you queue something.</p>
            {libs.length > 0 && (
              <ul className="lib-check">
                {libs.map((l, i) => (
                  <li key={l.path}>
                    <Seg value={l.name} onChange={(v) => setLibs(libs.map((x, j) => (j === i ? { ...x, name: v } : x)))}
                      options={[{ value: "movies", label: "Movies" }, { value: "tvshows", label: "TV shows" }]} />
                    <span className="mono small">{l.path}</span>
                    <button className="btn mini btn-danger" onClick={() => setLibs(libs.filter((_, j) => j !== i))}>Remove</button>
                  </li>
                ))}
              </ul>
            )}
            <DirBrowser onPick={(p) => {
              if (libs.some((l) => l.path === p)) return;
              // The library name is its type everywhere in the app; guess it
              // from the folder name, the Seg above corrects it.
              const tv = /tv|show|series|anime/i.test(p.split("/").pop() || "");
              setLibs([...libs, { name: tv ? "tvshows" : "movies", path: p }]);
            }} />
          </>
        )}

        {step === 1 && (
          <>
            <h2 className="panel-title">Hardware</h2>
            <p className="dim small">Distillarr tests every encoder it can find (GPU and CPU) with a short sample and uses what works.</p>
            {hw?.report ? (
              <ul className="lib-check">
                {ok.length === 0 && <li className="dim small">Only software (CPU) encoding works here. That's fine, just slower.</li>}
                {ok.map((r) => (
                  <li key={`${r.backend}-${r.codec}-${r.node || ""}`}>
                    <span className="teal">✓</span>
                    <span>{backendLabel(r.backend)} {r.codec.toUpperCase()}</span>
                    <span className="mono dim small">{r.node ? hw.devices[r.node] || r.node : ""}</span>
                  </li>
                ))}
              </ul>
            ) : <p className="dim small">Not probed yet.</p>}
            <div className="toolbar"><button className="btn" onClick={probe} disabled={probing}>{probing ? "Probing…" : hw?.report ? "Probe again" : "Run the hardware probe"}</button></div>
          </>
        )}

        {step === 2 && (
          <>
            <h2 className="panel-title">Encoding</h2>
            <div className="opts">
              <label className="field"><span>Codec</span>
                <Seg value={cfg.default_codec} onChange={(v) => save({ default_codec: v })}
                  options={[{ value: "hevc", label: "HEVC (plays everywhere)" }, { value: "av1", label: "AV1 (smaller, newer clients)" }, { value: "h264", label: "H.264" }]} />
              </label>
              <label className="field"><span>Quality target (VMAF)</span>
                <Seg value={cfg.vmaf_target || 0} onChange={(v) => save({ vmaf_target: v })}
                  options={[{ value: 91, label: "91 smaller" }, { value: 93, label: "93 transparent" }, { value: 95, label: "95 archival" }]} />
              </label>
            </div>
            <ScheduleTimeline config={cfg} onSaved={setCfg} />
          </>
        )}

        {step === 3 && (
          <>
            <h2 className="panel-title">Integrations (optional)</h2>
            <p className="dim small">Everything works without these. You can connect them now or later in Settings.</p>
            <ul className="lib-check">
              <li><a href="#/settings/connections">Jellyfin / Plex</a><span className="dim small">posters, refresh after a replace, pause while people watch</span></li>
              <li><a href="#/settings/connections">Sonarr / Radarr</a><span className="dim small">skip files about to be upgraded, tags, webhooks</span></li>
              <li><a href="#/settings/connections">Bazarr</a><span className="dim small">rescan subtitles after a replace</span></li>
              <li><a href="#/settings/notifications">Notifications</a><span className="dim small">Discord, ntfy, email…</span></li>
            </ul>
          </>
        )}

        {step === 4 && (
          <>
            <h2 className="panel-title">Ready</h2>
            <p>Finishing starts a library scan. <b>The queue stays paused</b>: review the recommendations on the Movies and
              Shows pages, try a preview or two, then resume the queue from the Queue page when you're happy.</p>
            <p className="dim small">Originals are kept in the trash for {cfg.trash_days} days after each replace, and every
              output is verified before it replaces anything.</p>
          </>
        )}

        <div className="toolbar" style={{ marginTop: 14 }}>
          {step > 0 && <button className="btn" onClick={() => setStep(step - 1)}>Back</button>}
          {step < STEPS.length - 1
            ? <button className="btn btn-primary" onClick={next}>Next</button>
            : <button className="btn btn-primary" onClick={finish}>Finish and scan</button>}
        </div>
      </section>
    </div>
  );
}
