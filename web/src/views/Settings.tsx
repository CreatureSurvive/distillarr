import { useEffect, useState, type ReactNode } from "react";
import { api, subscribe, type ArrInfo, type ArrInstance, type ArrTestResult, type AutopilotPreviewGroup, type AutoRule, type Config, type HwInfo, type JfStatus, type JfTest, type PlexStatus, type PlexTest, type RuleAction, type RuleMatch, type TrashItem } from "../api";
import { Copyable, ISSUE_SHORT, Seg, Toggle, toast } from "../components";
import { ago, backendLabel, bytes, codecLabel } from "../format";
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

export default function SettingsView({ live, onJellyfin, onPlex }: { live: LiveState; onJellyfin: () => void; onPlex: () => void }) {
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
        <a href="#/settings" onClick={(e) => { e.preventDefault(); document.getElementById("s-plex")?.scrollIntoView({ behavior: "smooth" }); }}>Plex</a>
        <a href="#/settings" onClick={(e) => { e.preventDefault(); document.getElementById("s-arr")?.scrollIntoView({ behavior: "smooth" }); }}>Sonarr / Radarr</a>
        <a href="#/settings" onClick={(e) => { e.preventDefault(); document.getElementById("s-auto")?.scrollIntoView({ behavior: "smooth" }); }}>Autopilot</a>
        <a href="#/settings" onClick={(e) => { e.preventDefault(); document.getElementById("s-enc")?.scrollIntoView({ behavior: "smooth" }); }}>Encoding</a>
        <a href="#/settings" onClick={(e) => { e.preventDefault(); document.getElementById("s-up")?.scrollIntoView({ behavior: "smooth" }); }}>Upscaling</a>
        <a href="#/settings" onClick={(e) => { e.preventDefault(); document.getElementById("s-hw")?.scrollIntoView({ behavior: "smooth" }); }}>Hardware</a>
        <a href="#/settings" onClick={(e) => { e.preventDefault(); document.getElementById("s-trash")?.scrollIntoView({ behavior: "smooth" }); }}>Storage &amp; trash</a>
        <a href="#/settings" onClick={(e) => { e.preventDefault(); document.getElementById("s-maint")?.scrollIntoView({ behavior: "smooth" }); }}>Maintenance</a>
      </nav>

      <JellyfinSection cfg={cfg} setCfg={setCfg} live={live} onJellyfin={onJellyfin} />

      <PlexSection cfg={cfg} setCfg={setCfg} live={live} onPlex={onPlex} />

      <ArrSection cfg={cfg} setCfg={setCfg} />

      <AutopilotSection cfg={cfg} setCfg={setCfg} />

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
          <Toggle on={cfg.defer_while_transcoding} onChange={(v) => save({ defer_while_transcoding: v })}
            label="Don't start new encodes while someone is transcoding"
            hint="Applies once Jellyfin and/or Plex is connected below. Encodes already running keep going." />
          <Toggle on={cfg.hold_replace_while_playing} onChange={(v) => save({ hold_replace_while_playing: v })}
            label="Don't swap a file in while it's being watched"
            hint="Holds the finished encode and waits for playback to stop (up to a few hours) before replacing it." />
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

function PlexSection({ cfg, setCfg, live, onPlex }: { cfg: Config; setCfg: (c: Config) => void; live: LiveState; onPlex: () => void }) {
  const [status, setStatus] = useState<PlexStatus | null>(null);
  const [url, setUrl] = useState(cfg.plex_url);
  const [token, setToken] = useState("");
  const [pathMap, setPathMap] = useState(cfg.plex_path_map);
  const [test, setTest] = useState<PlexTest | null>(null);
  const [testing, setTesting] = useState(false);
  const [sync, setSync] = useState<string>("");

  const refresh = () => api.plexStatus().then(setStatus).catch(() => {});
  useEffect(() => { refresh(); }, [live.plexVersion]);
  useEffect(() => {
    // Live sync progress arrives over the event stream.
    return subscribe((event, data) => {
      if (event !== "plex") return;
      if (data.error) setSync(`Sync failed: ${data.error}`);
      else if (data.done) { setSync(`Synced ${data.synced.toLocaleString()} items`); refresh(); }
      else setSync(`Syncing… ${data.synced.toLocaleString()} items`);
    });
  }, []);

  const runTest = async () => {
    setTesting(true);
    setTest(null);
    try {
      setTest(await api.plexTest({ url, token, path_map: pathMap }));
    } catch (e: any) {
      setTest({ ok: false, error: e.message });
    } finally {
      setTesting(false);
    }
  };

  const savePlex = async () => {
    try {
      const c = await api.saveConfig({ plex_url: url, plex_token: token || undefined, plex_path_map: pathMap });
      setCfg(c);
      setToken("");
      toast("Plex settings saved");
      onPlex();
      refresh();
    } catch (e: any) {
      toast(e.message, "err");
    }
  };

  const saveKeepAddedAt = async (v: boolean) => {
    try {
      setCfg(await api.saveConfig({ plex_keep_added_at: v }));
    } catch (e: any) {
      toast(e.message, "err");
    }
  };

  const state = status?.connected ? "ok" : status?.configured ? "bad" : "off";
  return (
    <section className="panel" id="s-plex">
      <div className="panel-head">
        <h2 className="panel-title">Plex</h2>
        <span className={`pill pill-${state}`}>
          <span className="dot" aria-hidden />
          {state === "ok" ? `Connected · ${status?.server_name} ${status?.version}` : state === "bad" ? "Can't reach Plex" : "Not connected"}
        </span>
      </div>
      <p className="dim small">
        Optional, alongside or instead of Jellyfin. Refreshes the affected library section after a file is replaced.
      </p>
      {state === "bad" && status?.error && <div className="alert">{status.error}</div>}
      <div className="form-grid">
        <label className="field"><span>Server URL</span>
          <input className="input" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="http://host.docker.internal:32400" spellCheck={false} />
        </label>
        <label className="field"><span>Token {cfg.plex_token_set && <span className="teal">· saved</span>}{" "}
          <a href="https://support.plex.tv/articles/204059436-finding-an-authentication-token-x-plex-token/" target="_blank" rel="noreferrer" className="dim small">how to find yours</a></span>
          <input className="input" type="password" value={token} onChange={(e) => setToken(e.target.value)}
            placeholder={cfg.plex_token_set ? "Leave blank to keep the saved token" : "paste your X-Plex-Token"} autoComplete="off" />
        </label>
        <label className="field"><span>Path mapping <span className="dim">(Plex=here)</span></span>
          <input className="input mono" value={pathMap} onChange={(e) => setPathMap(e.target.value)} placeholder="/data=/srv/media" spellCheck={false} />
        </label>
      </div>
      <div className="toolbar">
        <button className="btn" onClick={runTest} disabled={testing}>{testing ? "Testing…" : "Test connection"}</button>
        <button className="btn btn-primary" onClick={savePlex}>Save</button>
        <button className="btn" onClick={() => api.plexSync().then(() => setSync("Syncing…")).catch((e) => toast(e.message, "err"))}
          disabled={!status?.connected || status?.syncing}>Sync library now</button>
      </div>
      <div className="toggles" style={{ marginTop: 10 }}>
        <Toggle on={cfg.plex_keep_added_at} onChange={saveKeepAddedAt} label="Keep the original &quot;date added&quot;"
          hint="If a Plex refresh changes an item's added date, put it back and lock the field so a later refresh doesn't change it again." />
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
                <div className="dim small">Sections marked ✗ aren't visible here, so their items won't match. Adjust the path mapping, then test again.</div>
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
// Lets Settings show the right host:port in front of a webhook path when
// this container's own visible address differs from what the browser is
// loaded from (e.g. Sonarr/Radarr reach it under a Docker-network
// hostname). Blank keeps the default (the browser's own origin).
function WebhookBaseURLField({ cfg, setCfg }: { cfg: Config; setCfg: (c: Config) => void }) {
  const [v, setV] = useState(cfg.webhook_base_url || "");
  const [saving, setSaving] = useState(false);
  useEffect(() => setV(cfg.webhook_base_url || ""), [cfg.webhook_base_url]);
  const save = async () => {
    setSaving(true);
    try {
      setCfg(await api.saveConfig({ webhook_base_url: v }));
      toast("Saved");
    } catch (e: any) {
      toast(e.message, "err");
    } finally {
      setSaving(false);
    }
  };
  return (
    <label className="field" style={{ marginBottom: 14 }}>
      <span>Webhook base URL override <span className="dim">(blank = use this browser's own address)</span></span>
      <div className="toolbar" style={{ marginTop: 0 }}>
        <input className="input mono" value={v} onChange={(e) => setV(e.target.value)}
          placeholder={window.location.origin} spellCheck={false} style={{ flex: 1 }} />
        <button className="btn mini" onClick={save} disabled={saving || v === (cfg.webhook_base_url || "")}>Save</button>
      </div>
    </label>
  );
}

function ArrSection({ cfg, setCfg }: { cfg: Config; setCfg: (c: Config) => void }) {
  const [drafts, setDrafts] = useState<ArrDraft[]>(() => cfg.arr_instances.map((a) => ({ ...a, keyInput: "" })));
  const [results, setResults] = useState<Record<number, ArrTestResult | null>>({});
  const [testing, setTesting] = useState<Record<number, boolean>>({});
  const [saving, setSaving] = useState(false);
  const [info, setInfo] = useState<Record<string, ArrInfo>>({});
  const [syncing, setSyncing] = useState(false);
  // The tag name as last saved, per instance id — used to offer renaming
  // it in Sonarr/Radarr too when the user edits reencode_tag.
  const [savedTags, setSavedTags] = useState<Record<string, string>>({});
  const [renaming, setRenaming] = useState<Record<string, boolean>>({});
  // Revealed webhook tokens, in-memory only — the server never echoes
  // one back except right after (re)generating it.
  const [revealedTokens, setRevealedTokens] = useState<Record<string, string>>({});
  const [generatingToken, setGeneratingToken] = useState<Record<string, boolean>>({});

  useEffect(() => {
    setDrafts(cfg.arr_instances.map((a) => ({ ...a, keyInput: "" })));
    setSavedTags(Object.fromEntries(cfg.arr_instances.filter((a) => a.id).map((a) => [a.id!, a.reencode_tag || ""])));
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
    tag_after_reencode: d.tag_after_reencode, reencode_tag: d.reencode_tag,
    unmonitor_after_reencode: d.unmonitor_after_reencode,
    webhook_intake: d.webhook_intake, webhook_settle_minutes: d.webhook_settle_minutes,
  });

  const generateWebhookToken = async (id: string) => {
    setGeneratingToken((g) => ({ ...g, [id]: true }));
    try {
      const r = await api.arrRegenerateWebhookToken(id);
      setRevealedTokens((m) => ({ ...m, [id]: r.token }));
      const info = await api.arrGet(id);
      setInfo((m) => ({ ...m, [id]: info }));
      toast("New webhook token generated — copy it now, Distillarr won't show it again");
    } catch (e: any) {
      toast(e.message, "err");
    } finally {
      setGeneratingToken((g) => ({ ...g, [id]: false }));
    }
  };

  const renameTag = async (id: string, oldName: string, newName: string) => {
    setRenaming((r) => ({ ...r, [id]: true }));
    try {
      const r = await api.arrRenameTag(id, oldName, newName);
      if (!r.ok) throw new Error(r.error || "Rename failed");
      toast(r.renamed ? `Renamed "${oldName}" to "${newName}"` : `No "${oldName}" tag found there to rename`);
      setSavedTags((m) => ({ ...m, [id]: newName }));
    } catch (e: any) {
      toast(e.message, "err");
    } finally {
      setRenaming((r) => ({ ...r, [id]: false }));
    }
  };

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
        about a file, rescan them after a replace, and optionally tag,
        unmonitor or auto-queue imports through a webhook.
      </p>
      {drafts.some((d) => d.id) && <WebhookBaseURLField cfg={cfg} setCfg={setCfg} />}
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
                  Fix: turn on "Tag after re-encode" below, then exclude that tag from this custom format in{" "}
                  {d.kind === "sonarr" ? "Sonarr" : "Radarr"} → Settings → Custom Formats, or in Recyclarr's config under
                  that format's <code>quality_profiles.score</code> override. See{" "}
                  <a href="https://github.com/TRaSH-Guides/Guides" target="_blank" rel="noreferrer">TRaSH-Guides</a> for the exclusion syntax.
                  This is only a suggestion — nothing here gets tagged unless you turn it on yourself.
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
            <div className="panel" style={{ marginTop: 10, marginBottom: 0 }}>
              <h4 style={{ margin: "0 0 8px", fontSize: 13 }}>Write-back <span className="dim">(optional — off by default)</span></h4>
              <div className="form-grid">
                <div className="field">
                  <Toggle on={!!d.tag_after_reencode} onChange={(v) => update(i, { tag_after_reencode: v })} label="Tag after re-encode" />
                  <input className="input" value={d.reencode_tag || ""} onChange={(e) => update(i, { reencode_tag: e.target.value })}
                    placeholder="distilled" spellCheck={false} disabled={!d.tag_after_reencode} style={{ marginTop: 6 }} />
                </div>
                <div className="field">
                  <Toggle on={!!d.unmonitor_after_reencode} onChange={(v) => update(i, { unmonitor_after_reencode: v })}
                    label={`Unmonitor after re-encode${d.kind === "sonarr" ? " (that episode only)" : ""}`} />
                </div>
              </div>
              {d.id && savedTags[d.id] && d.reencode_tag && d.reencode_tag !== savedTags[d.id] && (
                <div className="small dim" style={{ marginTop: 6 }}>
                  Renamed the tag here?{" "}
                  <button className="btn mini" disabled={renaming[d.id]}
                    onClick={() => d.id && renameTag(d.id, savedTags[d.id], d.reencode_tag!)}>
                    {renaming[d.id] ? "Renaming…" : `Also rename "${savedTags[d.id]}" → "${d.reencode_tag}" in ${d.kind === "sonarr" ? "Sonarr" : "Radarr"}`}
                  </button>
                </div>
              )}
              <div className="small dim" style={{ marginTop: 6 }}>
                Applies after a successful re-encode/remux/upscale-replace of a file this instance manages. Tagging
                creates the tag there if it doesn't exist yet. Unmonitoring targets the specific episode/movie, never
                the whole series.
              </div>
            </div>
            <div className="panel" style={{ marginTop: 10, marginBottom: 0 }}>
              <h4 style={{ margin: "0 0 8px", fontSize: 13 }}>Webhook <span className="dim">(optional — off by default)</span></h4>
              {!d.id ? (
                <div className="small dim">Save this instance first to get its webhook URL and token.</div>
              ) : (
                <>
                  <div className="field">
                    <span>URL to paste into {d.kind === "sonarr" ? "Sonarr" : "Radarr"} → Settings → Connect → Webhook</span>
                    <Copyable text={`${cfg.webhook_base_url || window.location.origin}/api/v1/hooks/arr/${d.id}`} />
                  </div>
                  <div className="small dim">Username: anything. Password: the token below.</div>
                  <div className="field" style={{ marginTop: 8 }}>
                    <span>Token</span>
                    {revealedTokens[d.id] ? (
                      <Copyable text={revealedTokens[d.id]} />
                    ) : (
                      <div className="toolbar" style={{ marginTop: 0 }}>
                        <span className="dim small">{d.webhook_token_set ? "Generated · hidden" : "Not generated yet"}</span>
                        <button className="btn mini" disabled={generatingToken[d.id]} onClick={() => d.id && generateWebhookToken(d.id)}>
                          {generatingToken[d.id] ? "Generating…" : d.webhook_token_set ? "Regenerate" : "Generate"}
                        </button>
                      </div>
                    )}
                  </div>
                  <div className="form-grid" style={{ marginTop: 8 }}>
                    <div className="field">
                      <Toggle on={!!d.webhook_intake} onChange={(v) => update(i, { webhook_intake: v })}
                        label="Auto-queue imports after a settle delay" />
                    </div>
                    <label className="field"><span>Settle delay (minutes)</span>
                      <input className="input" type="number" min={0} value={d.webhook_settle_minutes ?? 30}
                        onChange={(e) => update(i, { webhook_settle_minutes: parseInt(e.target.value) || 0 })}
                        disabled={!d.webhook_intake} style={{ width: 100 }} />
                    </label>
                  </div>
                  <div className="small dim" style={{ marginTop: 6 }}>
                    An import/upgrade always refreshes that file's recommendation. With auto-queue on, it also opens a
                    settling entry in the Queue page's intake panel, which becomes eligible to run once the delay
                    passes (see Queue → Needs confirmation / Waiting). Last webhook received:{" "}
                    {inst?.webhook.last_received ? ago(inst.webhook.last_received) : "never"}.
                    {(inst?.webhook.auth_fails || 0) > 0 && (
                      <span className="warm"> · {inst!.webhook.auth_fails} failed auth attempt{inst!.webhook.auth_fails === 1 ? "" : "s"} since restart</span>
                    )}
                  </div>
                </>
              )}
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

const RULE_LIBRARIES = [{ value: "movies", label: "Movies" }, { value: "tvshows", label: "Shows" }];
const RULE_ORIGINS = [{ value: "webhook", label: "webhook" }, { value: "autopilot", label: "autopilot" }, { value: "playback", label: "playback" }, { value: "manual", label: "manual" }];
const RULE_RES_CLASSES = [480, 576, 720, 1080, 2160];
const RULE_CODECS = ["h264", "hevc", "av1", "mpeg2video", "vc1", "mpeg4", "vp9"];
const RULE_ACTIONS: { value: RuleAction["kind"]; label: string }[] = [
  { value: "queue", label: "Queue (recommended settings)" },
  { value: "queue_override", label: "Queue with an override" },
  { value: "quick_fix", label: "Quick fix (remux)" },
  { value: "ignore", label: "Ignore" },
];

// Chip-style toggle group for a RuleMatch string-array field.
function ChipMultiSelect({ options, value, onChange }: { options: { value: string; label: string }[]; value: string[]; onChange: (v: string[]) => void }) {
  return (
    <div className="chips">
      {options.map((o) => (
        <button key={o.value} type="button" className={`chip chip-btn${value.includes(o.value) ? " c-hevc" : ""}`}
          aria-pressed={value.includes(o.value)}
          onClick={() => onChange(value.includes(o.value) ? value.filter((v) => v !== o.value) : [...value, o.value])}>
          {o.label}
        </button>
      ))}
    </div>
  );
}

function summarizeRule(r: AutoRule): string {
  const parts: string[] = [];
  const w = r.when || {};
  if (w.libraries?.length) parts.push(w.libraries.join("/"));
  if (w.instances?.length) parts.push(`instance: ${w.instances.join(", ")}`);
  if (w.tags?.length) parts.push(`tags: ${w.tags.join(", ")}`);
  if (w.origins?.length) parts.push(`origin: ${w.origins.join(", ")}`);
  if (w.src_codecs?.length) parts.push(w.src_codecs.join("/").toUpperCase());
  if (w.res_classes?.length) parts.push(w.res_classes.map((c) => (c === 2160 ? "4K" : `${c}p`)).join("/"));
  if (w.min_savings_pct) parts.push(`≥${w.min_savings_pct}% savings`);
  if (w.min_age_days) parts.push(`≥${w.min_age_days}d old`);
  if (w.issue_keys?.length) parts.push(w.issue_keys.map((k) => ISSUE_SHORT[k]?.label || k).join(", "));
  if (w.animation === true) parts.push("animation");
  if (w.animation === false) parts.push("not animation");
  const when = parts.length ? parts.join(" · ") : "matches everything";
  const action = RULE_ACTIONS.find((a) => a.value === r.then?.kind)?.label || r.then?.kind;
  return `${when} → ${action}${r.then?.kind === "queue_override" && r.then.codec ? ` (${r.then.codec}${r.then.quality ? ` q${r.then.quality}` : ""})` : ""}`;
}

function csvField(label: string, value: string[] | undefined, onChange: (v: string[]) => void, placeholder = "") {
  return (
    <label className="field"><span>{label}</span>
      <input className="input" value={(value || []).join(", ")} placeholder={placeholder}
        onChange={(e) => onChange(e.target.value.split(",").map((s) => s.trim()).filter(Boolean))} />
    </label>
  );
}

function RuleEditor({ rule, instanceNames, onClose, onSave, onDelete }: {
  rule: AutoRule; instanceNames: string[]; onClose: () => void; onSave: (r: AutoRule) => void; onDelete?: () => void;
}) {
  const [r, setR] = useState<AutoRule>(rule);
  const w = r.when || {};
  const setWhen = (patch: Partial<RuleMatch>) => setR({ ...r, when: { ...w, ...patch } });

  return (
    <div className="modal-backdrop" onClick={(e) => e.target === e.currentTarget && onClose()}>
      <div className="modal" role="dialog" aria-label="Autopilot rule" style={{ maxWidth: 560 }}>
        <h2 className="panel-title">{rule.id ? "Edit rule" : "New rule"}</h2>
        <label className="field"><span>Name</span>
          <input className="input" value={r.name} onChange={(e) => setR({ ...r, name: e.target.value })} placeholder="e.g. Anime, gentle quality" />
        </label>
        <Toggle on={r.enabled} onChange={(v) => setR({ ...r, enabled: v })} label="Enabled" />

        <h3 className="panel-title" style={{ fontSize: 13, marginTop: 14 }}>When (every set condition must match)</h3>
        <div className="field"><span>Library</span>
          <ChipMultiSelect options={RULE_LIBRARIES} value={w.libraries || []} onChange={(v) => setWhen({ libraries: v })} />
        </div>
        {instanceNames.length > 0 && (
          <div className="field"><span>Sonarr/Radarr instance</span>
            <ChipMultiSelect options={instanceNames.map((n) => ({ value: n, label: n }))} value={w.instances || []} onChange={(v) => setWhen({ instances: v })} />
          </div>
        )}
        {csvField("Tags (comma-separated, as set in Sonarr/Radarr)", w.tags, (v) => setWhen({ tags: v }), "distilled, anime")}
        <div className="field"><span>Where the candidate came from</span>
          <ChipMultiSelect options={RULE_ORIGINS} value={w.origins || []} onChange={(v) => setWhen({ origins: v })} />
        </div>
        <div className="field"><span>Source codec</span>
          <ChipMultiSelect options={RULE_CODECS.map((c) => ({ value: c, label: codecLabel(c) }))} value={w.src_codecs || []} onChange={(v) => setWhen({ src_codecs: v })} />
        </div>
        <div className="field"><span>Resolution</span>
          <ChipMultiSelect options={RULE_RES_CLASSES.map((c) => ({ value: String(c), label: c === 2160 ? "4K" : `${c}p` }))}
            value={(w.res_classes || []).map(String)} onChange={(v) => setWhen({ res_classes: v.map(Number) })} />
        </div>
        <div className="field-pair">
          <label className="field"><span>Min savings %</span>
            <input className="input" type="number" min={0} max={100} value={w.min_savings_pct || 0}
              onChange={(e) => setWhen({ min_savings_pct: parseInt(e.target.value) || 0 })} />
          </label>
          <label className="field"><span>Min age (days)</span>
            <input className="input" type="number" min={0} value={w.min_age_days || 0}
              onChange={(e) => setWhen({ min_age_days: parseInt(e.target.value) || 0 })} />
          </label>
        </div>
        <div className="field"><span>Issue</span>
          <ChipMultiSelect options={Object.entries(ISSUE_SHORT).map(([k, v]) => ({ value: k, label: v.label }))}
            value={w.issue_keys || []} onChange={(v) => setWhen({ issue_keys: v })} />
        </div>
        <label className="field"><span>Animation</span>
          <Seg value={w.animation === true ? "yes" : w.animation === false ? "no" : "any"}
            onChange={(v) => setWhen({ animation: v === "any" ? null : v === "yes" })}
            options={[{ value: "any", label: "Any" }, { value: "yes", label: "Animation only" }, { value: "no", label: "Not animation" }]} />
        </label>

        <h3 className="panel-title" style={{ fontSize: 13, marginTop: 14 }}>Then</h3>
        <label className="field"><span>Action</span>
          <Seg value={r.then?.kind || "queue"} onChange={(v) => setR({ ...r, then: { ...r.then, kind: v as RuleAction["kind"] } })} options={RULE_ACTIONS} />
        </label>
        {r.then?.kind === "queue_override" && (
          <div className="field-pair">
            <label className="field"><span>Codec</span>
              <Seg value={r.then.codec || "hevc"} onChange={(v) => setR({ ...r, then: { ...r.then, codec: v } })}
                options={[{ value: "hevc", label: "HEVC" }, { value: "av1", label: "AV1" }, { value: "h264", label: "H.264" }]} />
            </label>
            <label className="field"><span>Quality (0 = recommended)</span>
              <input className="input" type="number" min={0} max={100} value={r.then.quality || 0}
                onChange={(e) => setR({ ...r, then: { ...r.then, quality: parseInt(e.target.value) || 0 } })} />
            </label>
          </div>
        )}

        <div className="toolbar" style={{ marginTop: 14 }}>
          <button className="btn btn-primary" onClick={() => onSave(r)} disabled={!r.name.trim()}>Save rule</button>
          {onDelete && <button className="btn btn-danger" onClick={onDelete}>Delete</button>}
          <button className="btn" onClick={onClose}>Cancel</button>
        </div>
      </div>
    </div>
  );
}

function AutopilotPreviewResults({ groups }: { groups: AutopilotPreviewGroup[] }) {
  if (groups.length === 0) return <div className="dim small" style={{ marginTop: 10 }}>No candidates found right now.</div>;
  const totalGB = groups.reduce((a, g) => a + g.est_saved_gb, 0);
  return (
    <div className="panel" style={{ marginTop: 10, marginBottom: 0 }}>
      <div className="dim small" style={{ marginBottom: 8 }}>
        {groups.reduce((a, g) => a + g.count, 0).toLocaleString()} candidate{groups.reduce((a, g) => a + g.count, 0) === 1 ? "" : "s"} · about {totalGB.toFixed(1)} GB estimated
      </div>
      {groups.map((g, i) => (
        <div key={i} style={{ marginBottom: 10 }}>
          <div className="small">
            <b>{g.rule || "Default"}</b> → {g.action} · {g.count.toLocaleString()} file{g.count === 1 ? "" : "s"} · ~{g.est_saved_gb.toFixed(1)} GB
          </div>
          <ul className="mono small dim" style={{ margin: "4px 0 0 16px" }}>
            {g.sample.map((f) => <li key={f.id}>{f.title || f.path.split("/").pop()}</li>)}
            {g.count > g.sample.length && <li>…and {(g.count - g.sample.length).toLocaleString()} more</li>}
          </ul>
        </div>
      ))}
    </div>
  );
}

function AutopilotSection({ cfg, setCfg }: { cfg: Config; setCfg: (c: Config) => void }) {
  const [rules, setRules] = useState<AutoRule[]>(cfg.auto_rules || []);
  const [editing, setEditing] = useState<AutoRule | null>(null);
  const [preview, setPreview] = useState<AutopilotPreviewGroup[] | null>(null);
  const [previewing, setPreviewing] = useState(false);

  useEffect(() => setRules(cfg.auto_rules || []), [cfg.auto_rules]);

  const saveRules = async (next: AutoRule[]) => {
    try {
      setCfg(await api.saveConfig({ auto_rules: next }));
    } catch (e: any) {
      toast(e.message, "err");
      setRules(cfg.auto_rules || []); // roll back the optimistic reorder/remove
    }
  };

  const toggleAutopilot = async (on: boolean) => {
    try {
      setCfg(await api.saveConfig({ autopilot_enabled: on }));
      toast(on ? "Autopilot is on" : "Autopilot is off");
    } catch (e: any) {
      toast(e.message, "err");
    }
  };

  const [pressurePct, setPressurePct] = useState(String(cfg.disk_pressure_pct ?? 0));
  const [pressureX, setPressureX] = useState(String(cfg.disk_pressure_budget_x ?? 2));
  useEffect(() => setPressurePct(String(cfg.disk_pressure_pct ?? 0)), [cfg.disk_pressure_pct]);
  useEffect(() => setPressureX(String(cfg.disk_pressure_budget_x ?? 2)), [cfg.disk_pressure_budget_x]);
  const savePressurePct = async () => {
    const v = Math.max(0, Math.min(100, parseInt(pressurePct, 10) || 0));
    setPressurePct(String(v));
    if (v === (cfg.disk_pressure_pct ?? 0)) return;
    try {
      setCfg(await api.saveConfig({ disk_pressure_pct: v }));
      toast(v === 0 ? "Disk pressure mode is off" : `Disk pressure mode triggers at ${v}% free`);
    } catch (e: any) {
      toast(e.message, "err");
    }
  };
  const savePressureX = async () => {
    const v = Math.max(1, parseFloat(pressureX) || 2);
    setPressureX(String(v));
    if (v === (cfg.disk_pressure_budget_x ?? 2)) return;
    try {
      setCfg(await api.saveConfig({ disk_pressure_budget_x: v }));
      toast(`Budget multiplier under pressure is now ${v}x`);
    } catch (e: any) {
      toast(e.message, "err");
    }
  };

  const move = (i: number, dir: -1 | 1) => {
    const j = i + dir;
    if (j < 0 || j >= rules.length) return;
    const next = [...rules];
    [next[i], next[j]] = [next[j], next[i]];
    setRules(next);
    saveRules(next);
  };

  const removeRule = (i: number) => {
    const next = rules.filter((_, j) => j !== i);
    setRules(next);
    saveRules(next);
  };

  const saveRule = (r: AutoRule) => {
    const id = r.id || `rule-${Date.now().toString(36)}`;
    const withID = { ...r, id };
    const exists = rules.some((x) => x.id === id);
    const next = exists ? rules.map((x) => (x.id === id ? withID : x)) : [...rules, withID];
    setRules(next);
    saveRules(next);
    setEditing(null);
  };

  const runPreview = async () => {
    setPreviewing(true);
    try {
      const r = await api.autopilotPreview();
      setPreview(r.groups);
    } catch (e: any) {
      toast(e.message, "err");
    } finally {
      setPreviewing(false);
    }
  };

  const instanceNames = (cfg.arr_instances || []).map((a) => a.name).filter(Boolean);

  return (
    <section className="panel" id="s-auto">
      <div className="panel-head">
        <h2 className="panel-title">Autopilot</h2>
        <Toggle on={!!cfg.autopilot_enabled} onChange={toggleAutopilot} label={cfg.autopilot_enabled ? "On" : "Off"} />
      </div>
      <p className="dim small">
        Optional, off by default. Ordered rules decide what an unattended candidate (a webhook import that opted into
        auto-queue) does: queue with the recommendation, queue with an override, quick-fix, or ignore. The first
        matching rule wins; with no match, the built-in default queues anything recommended that clears your savings
        floor below. Manual queueing from the library is never affected by any of this.
        {" "}An instance with an unacknowledged codec-penalty warning never gets an autopilot-queued HEVC/AV1
        re-encode — those go to Queue → Needs confirmation instead.
      </p>

      {rules.length === 0 ? (
        <p className="dim small">No rules yet — every candidate falls through to the built-in default.</p>
      ) : (
        <ul className="jobs">
          {rules.map((r, i) => (
            <li key={r.id} className="job">
              <div className="job-main">
                <div className="job-title">
                  <span className="mono dim">{i + 1}</span>
                  <span>{r.name || "Unnamed rule"}</span>
                  {!r.enabled && <span className="tag dim">disabled</span>}
                </div>
                <div className="job-meta mono dim small">{summarizeRule(r)}</div>
              </div>
              <div className="job-actions">
                <button className="btn mini" disabled={i === 0} title="Move up" onClick={() => move(i, -1)}>↑</button>
                <button className="btn mini" disabled={i === rules.length - 1} title="Move down" onClick={() => move(i, 1)}>↓</button>
                <button className="btn mini" onClick={() => setEditing(r)}>Edit</button>
                <button className="btn mini btn-danger" onClick={() => removeRule(i)}>Remove</button>
              </div>
            </li>
          ))}
        </ul>
      )}

      <div className="toolbar" style={{ marginTop: 12 }}>
        <button className="btn" onClick={() => setEditing({ id: "", name: "", enabled: true, when: {}, then: { kind: "queue" } })}>+ Add rule</button>
        <button className="btn" disabled={previewing} onClick={runPreview}>{previewing ? "Running…" : "Dry run"}</button>
      </div>

      {preview && <AutopilotPreviewResults groups={preview} />}

      <div className="opt-row" style={{ marginTop: 12 }}>
        <div className="opt-label">
          Disk pressure mode
          <div className="opt-hint">
            0 = off. Below this free-space percentage on any library's filesystem, autopilot favors quick fixes
            (remuxes) and its budget is multiplied so it clears space faster.
          </div>
        </div>
        <div className="opt-ctl">
          <input className="input" type="number" min={0} max={100} style={{ width: 80 }}
            value={pressurePct} onChange={(e) => setPressurePct(e.target.value)} onBlur={savePressurePct} />
          <span className="dim small">% free</span>
          <input className="input" type="number" min={1} step={0.5} style={{ width: 70, marginLeft: 12 }}
            value={pressureX} onChange={(e) => setPressureX(e.target.value)} onBlur={savePressureX} />
          <span className="dim small">x budget while under pressure</span>
        </div>
      </div>

      {editing && (
        <RuleEditor rule={editing} instanceNames={instanceNames} onClose={() => setEditing(null)} onSave={saveRule}
          onDelete={editing.id ? () => { removeRule(rules.findIndex((x) => x.id === editing.id)); setEditing(null); } : undefined} />
      )}
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
