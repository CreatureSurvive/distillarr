import { useEffect, useState, type ReactNode } from "react";
import { api, subscribe, type ArrInfo, type ArrInstance, type ArrTestResult, type Config, type HwInfo, type JfStatus, type JfTest, type TrashItem } from "../api";
import { Seg, Toggle, toast } from "../components";
import { ago, backendLabel, bytes } from "../format";
import { qualityWord } from "../options";
import type { LiveState } from "../App";
import { ScheduleTimeline } from "./Queue";

function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="opt-row">
      <div className="opt-label">{label}{hint && <div className="opt-hint">{hint}</div>}</div>
      <div className="opt-ctl">{children}</div>
    </div>
  );
}

export default function SettingsView({ live, onJellyfin }: { live: LiveState; onJellyfin: () => void }) {
  const [cfg, setCfg] = useState<Config | null>(null);
  const [hw, setHw] = useState<HwInfo | null>(null);

  useEffect(() => { api.config().then(setCfg).catch(() => {}); }, []);
  useEffect(() => { api.hw().then(setHw).catch(() => {}); }, [live.hwVersion]);

  const save = async (patch: Partial<Config>, msg = "Saved") => {
    try {
      setCfg(await api.saveConfig(patch));
      toast(msg);
    } catch (e: any) {
      toast(e.message, "err");
    }
  };

  if (!cfg) return <div className="result-count mono dim">Loading…</div>;

  return (
    <div className="settings">
      <header className="page-head">
        <div>
          <h1 className="page-title">Settings</h1>
          <div className="page-sub">Changes save as you make them.</div>
        </div>
      </header>
      <nav className="settings-nav">
        <a href="#/settings" onClick={(e) => { e.preventDefault(); document.getElementById("s-jf")?.scrollIntoView({ behavior: "smooth" }); }}>Jellyfin</a>
        <a href="#/settings" onClick={(e) => { e.preventDefault(); document.getElementById("s-arr")?.scrollIntoView({ behavior: "smooth" }); }}>Sonarr / Radarr</a>
        <a href="#/settings" onClick={(e) => { e.preventDefault(); document.getElementById("s-enc")?.scrollIntoView({ behavior: "smooth" }); }}>Encoding</a>
        <a href="#/settings" onClick={(e) => { e.preventDefault(); document.getElementById("s-up")?.scrollIntoView({ behavior: "smooth" }); }}>Upscaling</a>
        <a href="#/settings" onClick={(e) => { e.preventDefault(); document.getElementById("s-hw")?.scrollIntoView({ behavior: "smooth" }); }}>Hardware</a>
        <a href="#/settings" onClick={(e) => { e.preventDefault(); document.getElementById("s-trash")?.scrollIntoView({ behavior: "smooth" }); }}>Storage &amp; trash</a>
        <a href="#/settings" onClick={(e) => { e.preventDefault(); document.getElementById("s-maint")?.scrollIntoView({ behavior: "smooth" }); }}>Maintenance</a>
      </nav>

      <JellyfinSection cfg={cfg} setCfg={setCfg} live={live} onJellyfin={onJellyfin} />

      <ArrSection cfg={cfg} setCfg={setCfg} />

      <section className="panel" id="s-enc">
        <h2 className="panel-title">Encoding defaults</h2>
        <p className="dim small">Recommendations start from these and then tune quality per file. Changing them refreshes every recommendation.</p>
        <div className="opts">
          <Field label="Codec">
            <Seg value={cfg.default_codec} onChange={(v) => save({ default_codec: v })}
              options={[{ value: "hevc", label: "HEVC (plays everywhere)" }, { value: "av1", label: "AV1 (smaller, newer clients)" },
                { value: "h264", label: "H.264 (maximum compatibility)" }]} />
          </Field>
          <Field label="Encoder">
            <Seg value={cfg.preferred_backend} onChange={(v) => save({ preferred_backend: v })}
              options={["auto", "qsv", "vaapi", "nvenc", "sw"].map((b) => ({ value: b, label: b === "auto" ? "Auto" : backendLabel(b) }))} />
          </Field>
          <Field label="Quality target" hint="Every encode first scores short samples against the original (VMAF) and uses the smallest setting that meets this. Adds 1–2 minutes per job.">
            <Seg value={cfg.vmaf_target || 0} onChange={(v) => save({ vmaf_target: v })}
              options={[
                { value: 91, label: "91 smaller" },
                { value: 93, label: "93 transparent" },
                { value: 95, label: "95 archival" },
                { value: 0, label: "Off (fixed quality)" },
              ]} />
          </Field>
          <Field label={cfg.vmaf_target ? "Starting quality" : "Base quality"} hint={cfg.vmaf_target ? "Where the quality search starts, and what size estimates assume before a file is measured" : "Files with little bitrate headroom are raised automatically"}>
            <div className="quality">
              <input type="range" min={35} max={85} value={cfg.default_quality}
                onChange={(e) => setCfg({ ...cfg, default_quality: Number(e.target.value) })}
                onPointerUp={() => save({ default_quality: cfg.default_quality })}
                onKeyUp={() => save({ default_quality: cfg.default_quality })} aria-label="Base quality" />
              <span className="mono quality-val">{cfg.default_quality} <span className="dim">{qualityWord(cfg.default_quality)}</span></span>
            </div>
          </Field>
          <Field label="Speed">
            <Seg value={cfg.default_speed} onChange={(v) => save({ default_speed: v })}
              options={["faster", "fast", "medium", "slow", "slower"].map((v) => ({ value: v, label: v }))} />
          </Field>
          <Field label="Recommend when savings reach" hint="Files below this are marked “keep”">
            <Seg value={cfg.min_savings_pct} onChange={(v) => save({ min_savings_pct: v })}
              options={[15, 20, 30, 40, 50].map((v) => ({ value: v, label: `${v}%` }))} />
          </Field>
          <Field label="Uncompressed PCM audio">
            <Seg value={cfg.audio_pcm_target} onChange={(v) => save({ audio_pcm_target: v })}
              options={[{ value: "flac", label: "FLAC (lossless)" }, { value: "eac3", label: "E-AC-3" }, { value: "aac", label: "AAC" }, { value: "copy", label: "Leave as PCM" }]} />
          </Field>
          <Field label="Resolution cap">
            <Seg value={cfg.max_height} onChange={(v) => save({ max_height: v })}
              options={[{ value: 0, label: "Keep source" }, { value: 1080, label: "Max 1080p" }, { value: 720, label: "Max 720p" }]} />
          </Field>
          <Field label="Parallel encodes" hint={workersHint(hw, cfg.default_codec)}>
            <Seg value={cfg.workers} onChange={(v) => save({ workers: v })} options={[1, 2, 3, 4].map((n) => ({ value: n, label: String(n) }))} />
          </Field>
          <Field label="Retries per job">
            <Seg value={cfg.max_attempts} onChange={(v) => save({ max_attempts: v })} options={[1, 2, 3, 5].map((n) => ({ value: n, label: String(n) }))} />
          </Field>
        </div>
        <div className="toggles">
          <Toggle on={cfg.recompress_hevc} onChange={(v) => save({ recompress_hevc: v })} label="Re-encode existing HEVC" hint="Only when its bitrate is unusually high" />
          <Toggle on={cfg.prefer_mp4} onChange={(v) => save({ prefer_mp4: v })} label="Prefer MP4 for Apple devices"
            hint="HEVC tagged hvc1 with the index at the start. MKV is kept only for image subtitles, styled ASS, or TrueHD/DTS/FLAC audio. Changing an extension makes Sonarr/Radarr rescan the file." />
          <Toggle on={cfg.crop_bars} onChange={(v) => save({ crop_bars: v })} label="Crop black bars by default"
            hint="Bars are always left out of size estimates. Cropping them from the output is off by default: a film that switches aspect ratio could lose picture in scenes the detector didn't sample." />
          <CropProgress />
          <Toggle on={cfg.tonemap_hdr} onChange={(v) => save({ tonemap_hdr: v })} label="Tone-map HDR to SDR by default" hint="Off keeps HDR10/HLG intact" />
        </div>
        <Calibration />
      </section>

      <section className="panel" id="s-up">
        <h2 className="panel-title">Upscaling</h2>
        <p className="dim small">
          Tune an upscale from a file's page (the Upscale button), then queue it. Upscaling only makes sense for
          sources below 4K, and it is slow.{" "}
          {hw?.upscale_device ? <>Upscales run on {hw.upscale_device}. </> : hw ? <>No Vulkan GPU passed the upscaler self-test, so upscaling is unavailable. </> : null}
          For reference, the shader methods run at about 3x realtime to 1080p and roughly realtime to 4K on an Intel Arc A380; other GPUs will differ.
        </p>
        <div className="opts">
          <Field label="When an upscale finishes" hint="Each queued upscale can override this">
            <Seg value={cfg.upscale_output} onChange={(v) => save({ upscale_output: v })}
              options={[
                { value: "copy", label: "Keep the original, add a copy", hint: "Writes “… - 1080p upscale” beside the source; the original is never touched" },
                { value: "replace", label: "Replace the original", hint: "The original is kept in the trash for the retention period" },
              ]} />
          </Field>
        </div>
        <p className="dim small" style={{ marginTop: 10 }}>
          {cfg.upscale_output === "copy"
            ? "The copy is a separate file: Jellyfin will list it beside the original, and Sonarr/Radarr don't know about it."
            : "The upscaled file takes the original's place. Restore the original from the trash if you don't like it."}
        </p>
        <ScheduleTimeline config={cfg} onSaved={setCfg} field="upscale_schedules" title="Neural upscale window" anyTime={false} accent="violet">
          <p className="dim small" style={{ margin: "0 0 8px" }}>
            Neural upscales take hours per file and hold the GPU throughout, so they run only inside this window (overnight by
            default), pause when it closes and pick up where they left off. “Upscale now” ignores it. Shader upscales run on the
            normal queue schedule.
          </p>
        </ScheduleTimeline>
      </section>

      <HardwareSection live={live} />
      <TrashSection cfg={cfg} save={save} />
      <MaintenanceSection />
    </div>
  );
}

function JellyfinSection({ cfg, setCfg, live, onJellyfin }: { cfg: Config; setCfg: (c: Config) => void; live: LiveState; onJellyfin: () => void }) {
  const [status, setStatus] = useState<JfStatus | null>(null);
  const [url, setUrl] = useState(cfg.jellyfin_url);
  const [key, setKey] = useState("");
  const [pathMap, setPathMap] = useState(cfg.jellyfin_path_map);
  const [test, setTest] = useState<JfTest | null>(null);
  const [testing, setTesting] = useState(false);
  const [sync, setSync] = useState<string>("");

  const refresh = () => api.jellyfinStatus().then(setStatus).catch(() => {});
  useEffect(() => { refresh(); }, [live.jfVersion]);
  useEffect(() => {
    // Live sync progress arrives over the event stream.
    return subscribe((event, data) => {
      if (event !== "jellyfin") return;
      if (data.error) setSync(`Sync failed: ${data.error}`);
      else if (data.done) { setSync(`Synced ${data.synced.toLocaleString()} items`); refresh(); }
      else setSync(`Syncing… ${data.synced.toLocaleString()} items`);
    });
  }, []);

  const runTest = async () => {
    setTesting(true);
    setTest(null);
    try {
      setTest(await api.jellyfinTest({ url, key, path_map: pathMap }));
    } catch (e: any) {
      setTest({ ok: false, error: e.message });
    } finally {
      setTesting(false);
    }
  };

  const saveJf = async () => {
    try {
      const c = await api.saveConfig({ jellyfin_url: url, jellyfin_api_key: key || undefined, jellyfin_path_map: pathMap });
      setCfg(c);
      setKey("");
      toast("Jellyfin settings saved");
      onJellyfin();
      refresh();
    } catch (e: any) {
      toast(e.message, "err");
    }
  };

  const state = status?.connected ? "ok" : status?.configured ? "bad" : "off";
  return (
    <section className="panel" id="s-jf">
      <div className="panel-head">
        <h2 className="panel-title">Jellyfin</h2>
        <span className={`pill pill-${state}`}>
          <span className="dot" aria-hidden />
          {state === "ok" ? `Connected · ${status?.server_name} ${status?.version}` : state === "bad" ? "Can't reach Jellyfin" : "Not connected"}
        </span>
      </div>
      <p className="dim small">
        Optional. Adds posters, episode titles and genres (animation gets tuned differently), refreshes Jellyfin after a file is replaced, and keeps its “date added” unchanged.
      </p>
      {state === "bad" && status?.error && <div className="alert">{status.error}</div>}
      <div className="form-grid">
        <label className="field"><span>Server URL</span>
          <input className="input" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://host.docker.internal:8096" spellCheck={false} />
        </label>
        <label className="field"><span>API key {cfg.jellyfin_key_set && <span className="teal">· saved</span>}</span>
          <input className="input" type="password" value={key} onChange={(e) => setKey(e.target.value)}
            placeholder={cfg.jellyfin_key_set ? "Leave blank to keep the saved key" : "Dashboard → API Keys → +"} autoComplete="off" />
        </label>
        <label className="field"><span>Path mapping <span className="dim">(Jellyfin=here)</span></span>
          <input className="input mono" value={pathMap} onChange={(e) => setPathMap(e.target.value)} placeholder="/data=/srv/media" spellCheck={false} />
        </label>
      </div>
      <div className="toolbar">
        <button className="btn" onClick={runTest} disabled={testing}>{testing ? "Testing…" : "Test connection"}</button>
        <button className="btn btn-primary" onClick={saveJf}>Save</button>
        <button className="btn" onClick={() => api.jellyfinSync().then(() => setSync("Syncing…")).catch((e) => toast(e.message, "err"))}
          disabled={!status?.connected || status?.syncing}>Sync library now</button>
      </div>
      {test && (
        <div className={`test-result ${test.ok ? "ok" : "bad"}`}>
          {test.ok ? (
            <>
              <div><b>Connected</b> to {test.server_name} ({test.version}).</div>
              {test.libraries && test.libraries.length > 0 && (
                <ul className="lib-check">
                  {test.libraries.map((l) => (
                    <li key={l.location}>
                      <span className={l.reachable ? "teal" : "warm"}>{l.reachable ? "✓" : "✗"}</span>
                      <span>{l.name}</span>
                      <span className="mono dim small">{l.location} → {l.mapped}</span>
                    </li>
                  ))}
                </ul>
              )}
              {test.libraries?.some((l) => !l.reachable) && (
                <div className="dim small">Folders marked ✗ aren't visible here, so their items won't match. Adjust the path mapping, then test again.</div>
              )}
            </>
          ) : (
            <div><b>Not connected.</b> {test.error}</div>
          )}
        </div>
      )}
      <div className="dim small" style={{ marginTop: 10 }}>
        {sync || (status?.cached ? `${status.cached.toLocaleString()} items cached · last sync ${ago(status.last_sync)}` : "Nothing synced yet.")}
      </div>
    </section>
  );
}

type ArrDraft = ArrInstance & { keyInput: string };

// Any number of Sonarr/Radarr connections. Each card edits its own
// fields locally; Save/Remove always sends the whole list, since the
// server's PUT replaces arr_instances wholesale (an id left out is
// removed) — a blank key on an existing id keeps what's already saved.
function ArrSection({ cfg, setCfg }: { cfg: Config; setCfg: (c: Config) => void }) {
  const [drafts, setDrafts] = useState<ArrDraft[]>(() => cfg.arr_instances.map((a) => ({ ...a, keyInput: "" })));
  const [results, setResults] = useState<Record<number, ArrTestResult | null>>({});
  const [testing, setTesting] = useState<Record<number, boolean>>({});
  const [saving, setSaving] = useState(false);
  const [info, setInfo] = useState<Record<string, ArrInfo>>({});
  const [syncing, setSyncing] = useState(false);

  useEffect(() => {
    setDrafts(cfg.arr_instances.map((a) => ({ ...a, keyInput: "" })));
    // Codec-penalty reports come from each saved instance's own
    // last sync, not this form's draft state.
    cfg.arr_instances.forEach((a) => {
      if (!a.id) return;
      api.arrGet(a.id).then((r) => setInfo((m) => ({ ...m, [a.id!]: r }))).catch(() => {});
    });
  }, [cfg.arr_instances]);

  const ack = async (id: string) => {
    try {
      await api.arrAck(id);
      const r = await api.arrGet(id);
      setInfo((m) => ({ ...m, [id]: r }));
    } catch (e: any) {
      toast(e.message, "err");
    }
  };

  const update = (i: number, patch: Partial<ArrDraft>) =>
    setDrafts((d) => d.map((x, j) => (j === i ? { ...x, ...patch } : x)));

  const toPayload = (d: ArrDraft) => ({
    id: d.id || undefined, name: d.name, kind: d.kind, url: d.url,
    api_key: d.keyInput || undefined, path_map: d.path_map, enabled: d.enabled,
  });

  const saveAll = async (list: ArrDraft[], msg: string) => {
    setSaving(true);
    try {
      const c = await api.saveConfig({ arr_instances: list.map(toPayload) });
      setCfg(c);
      toast(msg);
    } catch (e: any) {
      toast(e.message, "err");
    } finally {
      setSaving(false);
    }
  };

  const addInstance = (kind: "sonarr" | "radarr") => {
    setDrafts((d) => [...d, {
      id: "", name: kind === "sonarr" ? "Sonarr" : "Radarr", kind, url: "",
      api_key_set: false, path_map: "", enabled: true, keyInput: "",
    }]);
  };

  const removeInstance = (i: number) => {
    const next = drafts.filter((_, j) => j !== i);
    saveAll(next, "Removed");
  };

  const testOne = async (i: number) => {
    const d = drafts[i];
    setTesting((t) => ({ ...t, [i]: true }));
    setResults((r) => ({ ...r, [i]: null }));
    try {
      const r = await api.arrTest(d.id || "new", {
        url: d.url, api_key: d.keyInput || undefined, kind: d.kind, path_map: d.path_map,
      });
      setResults((res) => ({ ...res, [i]: r }));
    } catch (e: any) {
      setResults((res) => ({ ...res, [i]: { ok: false, error: e.message } }));
    } finally {
      setTesting((t) => ({ ...t, [i]: false }));
    }
  };

  return (
    <section className="panel" id="s-arr">
      <div className="panel-head">
        <h2 className="panel-title">Sonarr / Radarr</h2>
        {drafts.some((d) => d.id) && (
          <button className="btn mini" disabled={syncing}
            onClick={() => { setSyncing(true); api.arrSync().then((r) => { if (!r.started) toast("Already syncing"); }).catch((e) => toast(e.message, "err")).finally(() => setTimeout(() => setSyncing(false), 2000)); }}>
            {syncing ? "Syncing…" : "Sync now"}
          </button>
        )}
      </div>
      <p className="dim small">
        Optional. Connect any number of Sonarr and Radarr instances (a 4K
        Radarr fits fine alongside a regular one) to see what they know
        about a file, and later — nothing yet — to tell them about
        replaced files.
      </p>
      {drafts.map((d, i) => {
        const test = results[i];
        const inst = d.id ? info[d.id] : undefined;
        const penalties = inst?.penalties.penalties || [];
        const namingWarnings = inst?.penalties.naming_warnings || [];
        const showWarning = (penalties.length > 0 || namingWarnings.length > 0) && !inst?.penalty_ack;
        return (
          <div key={i} className="panel" style={{ marginTop: 12, marginBottom: 0 }}>
            <div className="panel-head">
              <h3 className="panel-title" style={{ fontSize: 14 }}>{d.name || (d.kind === "sonarr" ? "Sonarr" : "Radarr")}</h3>
              <Toggle on={d.enabled !== false} onChange={(v) => update(i, { enabled: v })} label="Enabled" />
            </div>
            {showWarning && (
              <div className="alert">
                <div><b>Re-encoded files may look worse to {d.name || "this instance"}.</b></div>
                {penalties.map((p, pi) => (
                  <div key={pi} className="small">
                    Profile "{p.profile}" scores custom format "{p.custom_format}" at {p.score} (matches: {p.terms.join(", ")}
                    {" via "}{p.matches}). A file Distillarr re-encodes could be treated as worth replacing again.
                  </div>
                ))}
                {namingWarnings.map((n, ni) => (
                  <div key={ni} className="small">
                    Naming format "{n.field}" includes the codec ({n.template}) — a future scoring rule would apply to
                    every renamed file immediately, even though nothing is scored today.
                  </div>
                ))}
                <div className="small dim" style={{ marginTop: 6 }}>
                  Fix: exclude a tag from that custom format in {d.kind === "sonarr" ? "Sonarr" : "Radarr"} → Settings → Custom
                  Formats, or in Recyclarr's config under that format's <code>quality_profiles.score</code> override. See{" "}
                  <a href="https://github.com/TRaSH-Guides/Guides" target="_blank" rel="noreferrer">TRaSH-Guides</a> for the exclusion syntax.
                </div>
                <div className="toolbar" style={{ marginTop: 8 }}>
                  <button className="btn mini" onClick={() => d.id && ack(d.id)}>I understand</button>
                </div>
              </div>
            )}
            <div className="form-grid">
              <label className="field"><span>Name</span>
                <input className="input" value={d.name} onChange={(e) => update(i, { name: e.target.value })} placeholder={d.kind === "sonarr" ? "Sonarr" : "Radarr"} />
              </label>
              <label className="field"><span>Kind</span>
                <Seg value={d.kind} onChange={(v) => update(i, { kind: v as "sonarr" | "radarr" })}
                  options={[{ value: "sonarr", label: "Sonarr" }, { value: "radarr", label: "Radarr" }]} />
              </label>
              <label className="field"><span>Server URL <span className="dim">(container name if on the same Docker network, otherwise host.docker.internal or the LAN IP)</span></span>
                <input className="input" value={d.url} onChange={(e) => update(i, { url: e.target.value })} placeholder="http://host.docker.internal:8989" spellCheck={false} />
              </label>
              <label className="field"><span>API key {d.api_key_set && <span className="teal">· saved</span>}</span>
                <input className="input" type="password" value={d.keyInput} onChange={(e) => update(i, { keyInput: e.target.value })}
                  placeholder={d.api_key_set ? "Leave blank to keep the saved key" : "Settings → General → Security"} autoComplete="off" />
              </label>
              <label className="field"><span>Path mapping <span className="dim">(their view=here)</span></span>
                <input className="input mono" value={d.path_map || ""} onChange={(e) => update(i, { path_map: e.target.value })} placeholder="/data=/media" spellCheck={false} />
              </label>
            </div>
            <div className="toolbar">
              <button className="btn" onClick={() => testOne(i)} disabled={testing[i]}>{testing[i] ? "Testing…" : "Test connection"}</button>
              <button className="btn btn-primary" onClick={() => saveAll(drafts, "Saved")} disabled={saving}>Save</button>
              <button className="btn btn-danger" onClick={() => removeInstance(i)} disabled={saving}>Remove</button>
            </div>
            {test && (
              <div className={`test-result ${test.ok ? "ok" : "bad"}`}>
                {test.ok ? (
                  <>
                    <div><b>Connected</b> to {test.app_name} ({test.version}).</div>
                    {test.root_folders && test.root_folders.length > 0 && (
                      <ul className="lib-check">
                        {test.root_folders.map((f) => (
                          <li key={f.path}>
                            <span className={f.reachable ? "teal" : "warm"}>{f.reachable ? "✓" : "✗"}</span>
                            <span className="mono dim small">{f.path} → {f.mapped}</span>
                          </li>
                        ))}
                      </ul>
                    )}
                    {test.root_folders?.some((f) => !f.reachable) && (
                      <div className="dim small">Folders marked ✗ aren't visible here. Adjust the path mapping, then test again.</div>
                    )}
                  </>
                ) : (
                  <div><b>Not connected.</b> {test.error}</div>
                )}
              </div>
            )}
          </div>
        );
      })}
      <div className="toolbar" style={{ marginTop: 14 }}>
        <button className="btn" onClick={() => addInstance("sonarr")}>+ Add Sonarr</button>
        <button className="btn" onClick={() => addInstance("radarr")}>+ Add Radarr</button>
      </div>
    </section>
  );
}

// workersHint names the device encodes run on now, so the advice fits
// whatever hardware the probe found.
function workersHint(hw: HwInfo | null, codec: string): string {
  const dev = hw?.active[codec] || hw?.active.hevc;
  if (!dev) return "Most GPUs handle 2 at once; lower it if other apps share the GPU";
  if (dev.backend === "sw") return "Encodes run on the CPU (software), and each one already uses every core: 1 is usually best";
  return `Encodes run on ${dev.name}. Most GPUs handle 2 at once; lower it if other apps (a media server, other transcoders) share the GPU`;
}

function HardwareSection({ live }: { live: LiveState }) {
  const [hw, setHw] = useState<HwInfo | null>(null);
  const [probing, setProbing] = useState(false);
  useEffect(() => {
    api.hw().then(setHw).catch(() => {});
    setProbing(false);
  }, [live.hwVersion]);
  const rep = hw?.report;
  const nodes = rep ? [...new Set(rep.results.map((r) => r.node || "cpu"))] : [];
  return (
    <section className="panel" id="s-hw">
      <div className="panel-head">
        <h2 className="panel-title">Hardware</h2>
        <button className="btn mini" disabled={probing} onClick={() => { setProbing(true); api.reprobe(); }}>{probing ? "Testing…" : "Test again"}</button>
      </div>
      <p className="dim small">Each encoder runs a real test encode. Only the ones that pass are used. Auto picks {hw ? Object.entries(hw.auto).map(([c, b]) => {
        const dev = hw.active[c];
        const on = dev && dev.backend === b && b !== "sw" ? ` on ${dev.name}` : "";
        return `${backendLabel(b)}${on} for ${c.toUpperCase()}`;
      }).join(", ") : "…"}.</p>
      {rep && (
        <div className="hw-grid">
          {nodes.map((n) => (
            <div key={n} className="hw-dev">
              <div className="hw-dev-name">
                {n === "cpu" ? "CPU (software)" : hw?.devices[n] || n.replace("/dev/dri/", "")}
                {n !== "cpu" && <span className="mono dim small"> · {n.replace("/dev/dri/", "")}</span>}
              </div>
              {rep.results.filter((r) => (r.node || "cpu") === n).map((r) => (
                <div key={`${r.backend}${r.codec}`} className={`hw-cell ${r.ok ? "ok" : "no"}`} title={r.error || `${r.ms} ms`}>
                  <span>{backendLabel(r.backend)} {r.codec.toUpperCase()}</span>
                  <span className="mono">{r.ok ? "✓" : "—"}</span>
                </div>
              ))}
            </div>
          ))}
        </div>
      )}
      {hw && Object.entries(hw.health).filter(([, bad]) => bad).map(([b]) => (
        <div key={b} className="alert">
          {backendLabel(b)} failed twice recently and is being skipped.{" "}
          <button className="btn mini" onClick={() => api.resetBackend(b).then(() => api.hw().then(setHw))}>Use it again</button>
        </div>
      ))}
      {rep && <div className="mono faint small" style={{ marginTop: 8 }}>{rep.ffmpeg_version} · tested {ago(rep.tested_at)}</div>}
    </section>
  );
}

function TrashSection({ cfg, save }: { cfg: Config; save: (p: Partial<Config>, m?: string) => void }) {
  const [items, setItems] = useState<TrashItem[]>([]);
  const [sys, setSys] = useState<Awaited<ReturnType<typeof api.system>> | null>(null);
  const load = () => {
    api.trash().then((r) => setItems(r.items)).catch(() => {});
    api.system().then(setSys).catch(() => {});
  };
  useEffect(load, []);
  const total = items.reduce((a, t) => a + t.size, 0);
  const act = async (fn: () => Promise<unknown>, msg: string) => {
    try { await fn(); toast(msg); load(); } catch (e: any) { toast(e.message, "err"); }
  };
  const disk = (d?: { total: number; free: number }) => d ? `${bytes(d.free)} free of ${bytes(d.total)}` : "—";
  return (
    <section className="panel" id="s-trash">
      <h2 className="panel-title">Storage &amp; trash</h2>
      <div className="stat-row">
        <div className="stat"><div className="stat-label">Media pool</div><div className="stat-val">{disk(sys?.media)}</div></div>
        <div className="stat"><div className="stat-label">App data (NVMe)</div><div className="stat-val">{disk(sys?.config)}</div></div>
        <div className="stat"><div className="stat-label">In trash</div><div className="stat-val">{bytes(total)} · {items.length} file{items.length === 1 ? "" : "s"}</div></div>
      </div>
      <p className="dim small">
        Before an original is replaced it is moved to <span className="mono">{cfg.trash_dir}</span> on the media pool (instant, no copying).
        It's deleted after the retention period. Restore puts it back and removes the encoded file.
      </p>
      <div className="opts">
        <Field label="Keep originals">
          <Toggle on={cfg.trash_enabled} onChange={(v) => save({ trash_enabled: v })} label={cfg.trash_enabled ? "On" : "Off: originals are deleted immediately"} />
        </Field>
        <Field label="Retention">
          <Seg value={cfg.trash_days} onChange={(v) => save({ trash_days: v })} options={[1, 3, 7, 14, 30].map((d) => ({ value: d, label: `${d} day${d === 1 ? "" : "s"}` }))} />
        </Field>
      </div>
      {items.length > 0 && (
        <>
          <ul className="trash-list">
            {items.map((t) => (
              <li key={t.id}>
                <div className="trash-name">
                  <div>{t.orig_path.split("/").pop()}</div>
                  <div className="mono faint small">{bytes(t.size)} · {ago(t.created_at)}</div>
                </div>
                <div className="toolbar">
                  <button className="btn mini" onClick={() => confirm("Put the original back and delete the encoded version?") && act(() => api.restoreTrash(t.id), "Original restored")}>Restore</button>
                  <button className="btn mini btn-danger" onClick={() => confirm("Delete this original permanently?") && act(() => api.deleteTrash(t.id), "Deleted")}>Delete</button>
                </div>
              </li>
            ))}
          </ul>
          <button className="btn btn-danger" onClick={() => confirm(`Permanently delete all ${items.length} originals (${bytes(total)})?`) && act(() => api.purgeTrash(true), "Trash emptied")}>
            Empty trash
          </button>
        </>
      )}
    </section>
  );
}

// Resets for caches/derived state that a pipeline or logic change can make
// stale. None of these touch media files - everything but history rebuilds
// itself automatically (measurements in the overnight queue; crop/issue
// detection the same way it backfilled on first scan).
function MaintenanceSection() {
  const act = async (fn: () => Promise<{ cleared?: number; ok?: boolean }>, label: string) => {
    try {
      const r = await fn();
      toast(r.cleared !== undefined ? `${label}: ${r.cleared.toLocaleString()} file${r.cleared === 1 ? "" : "s"}` : label);
    } catch (e: any) {
      toast(e.message, "err");
    }
  };
  const item = (name: string, hint: string, confirmMsg: string, fn: () => Promise<{ cleared?: number; ok?: boolean }>, doneMsg: string, danger = false) => (
    <li key={name}>
      <div className="trash-name">
        <div>{name}</div>
        <div className="mono faint small">{hint}</div>
      </div>
      <button className={`btn mini ${danger ? "btn-danger" : ""}`} onClick={() => confirm(confirmMsg) && act(fn, doneMsg)}>
        {danger ? "Delete" : "Clear"}
      </button>
    </li>
  );
  return (
    <section className="panel" id="s-maint">
      <h2 className="panel-title">Maintenance</h2>
      <p className="dim small">
        Resets caches that a pipeline change can leave stale. Nothing here touches media files. Job history is the
        one exception below: it's deleted outright, not rebuilt.
      </p>
      <ul className="trash-list">
        {item("Quality measurements", "VMAF/CAMBI results for every file, so the overnight queue re-measures with the current sampling method.",
          "Clear all quality measurements? Every measured file goes back into the overnight queue.",
          api.clearMeasurements, "Measurements cleared")}
        {item("Size-estimate calibration", "The self-correcting size model, back to the raw formula. It relearns from new measurements and finished jobs.",
          "Reset size-estimate calibration to neutral?",
          api.clearCalibration, "Calibration reset")}
        {item("Black-bar (crop) detection", "Re-checks every file for letterboxing/pillarboxing from scratch.",
          "Re-run black-bar detection on every file?",
          api.clearCrop, "Crop detection cleared")}
        {item("Issue detection (hvc1, faststart, …)", "Re-probes every file's container tags and recomputes its issue list.",
          "Re-run issue detection on every file?",
          api.clearIssueTags, "Issue tags cleared")}
        {item("Job history & stats", "Permanently deletes done/failed/canceled job records - what the savings/history dashboard and the “upscaled” badge are built from. Active jobs aren't touched.",
          "Permanently delete all job history? This cannot be undone - it removes the record of past work, not just a cache.",
          api.clearHistory, "Job history cleared", true)}
      </ul>
    </section>
  );
}

function CropProgress() {
  const [c, setC] = useState<{ checked: number; total: number; with_bars: number } | null>(null);
  useEffect(() => {
    const load = () => api.system().then((s) => setC(s.crop ?? null)).catch(() => {});
    load();
    const t = setInterval(load, 15000);
    return () => clearInterval(t);
  }, []);
  if (!c || !c.total) return null;
  return (
    <div className="dim small" style={{ marginLeft: 44 }}>
      Black-bar check: {c.checked.toLocaleString()} of {c.total.toLocaleString()} files done
      {c.with_bars > 0 && `, ${c.with_bars.toLocaleString()} have bars`}
      {c.checked < c.total && " (re-encode candidates first)"}.
    </div>
  );
}

function Calibration() {
  const [c, setC] = useState<Record<string, { samples: number; factor: number }>>({});
  useEffect(() => { api.calibration().then(setC).catch(() => {}); }, []);
  const entries = Object.entries(c);
  return (
    <details className="sub-details">
      <summary className="dim small">Size estimate accuracy ({entries.reduce((a, [, v]) => a + v.samples, 0)} real measurements)</summary>
      {entries.length === 0 ? (
        <div className="dim small">No measurements yet. Every preview and finished encode corrects future estimates.</div>
      ) : (
        <ul className="calib">
          {entries.map(([k, v]) => {
            const [b, codec, src, res, content] = k.split("|");
            return (
              <li key={k} className="mono small">
                {backendLabel(b)} {codec.toUpperCase()} from {src} {res}{content === "anim" ? " animation" : ""}: {v.samples} sample{v.samples === 1 ? "" : "s"}, files come out {v.factor >= 1 ? `${Math.round((v.factor - 1) * 100)}% larger` : `${Math.round((1 - v.factor) * 100)}% smaller`} than the base model
              </li>
            );
          })}
        </ul>
      )}
    </details>
  );
}
