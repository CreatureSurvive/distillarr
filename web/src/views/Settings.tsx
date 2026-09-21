import { useEffect, useState } from "react";
import { api, type Config, type HwReport } from "../api";
import { bytes } from "../format";
import type { LiveState } from "../App";
import "../components.css";

export default function SettingsView({ live }: { live: LiveState }) {
  const [cfg, setCfg] = useState<Config | null>(null);
  const [hw, setHw] = useState<{ report: HwReport | null; health: Record<string, boolean> } | null>(null);
  const [jf, setJf] = useState<any>(null);
  const [save, setSave] = useState("");
  const [jfKey, setJfKey] = useState("");

  const loadAll = async () => {
    try {
      setCfg(await api.config());
      setHw(await api.hw());
      setJf(await api.jellyfinStatus());
    } catch {}
  };
  useEffect(() => { loadAll(); /* eslint-disable-next-line */ }, [live.hwVersion]);

  if (!cfg) return <div className="empty"><div className="big">Loading…</div></div>;

  const put = async (patch: Partial<Config>) => {
    setSave("saving…");
    try {
      const next = await api.saveConfig({ ...cfg, ...patch, jellyfin_api_key: jfKey || undefined } as any);
      setCfg({ ...next, jellyfin_key_set: (next as any).jellyfin_key_set ?? cfg.jellyfin_key_set });
      setSave("saved");
      setTimeout(() => setSave(""), 1800);
    } catch (e: any) {
      setSave(e.message);
    }
  };

  const rep = hw?.report;

  return (
    <div>
      <div className="page-head">
        <div>
          <h1 className="page-title">Settings</h1>
          <div className="page-sub">Encoding defaults, hardware, schedule safety</div>
        </div>
        <span className="mono dim">{save}</span>
      </div>

      {/* Hardware */}
      <div className="card" style={{ marginBottom: 18 }}>
        <div style={{ display: "flex", justifyContent: "space-between", marginBottom: 12 }}>
          <div className="stat-label">Hardware encoders</div>
          <button className="btn mini" onClick={() => api.reprobe()}>Re-test hardware</button>
        </div>
        {!rep ? (
          <div className="dim">Probing… runs real test encodes per encoder.</div>
        ) : (
          <>
            <div className="mono dim" style={{ fontSize: 11, marginBottom: 10 }}>{rep.ffmpeg_version}</div>
            <div className="chips" style={{ marginBottom: 8 }}>
              {rep.render_nodes.map((n) => (
                <span key={n} className="chip">{n}</span>
              ))}
              {rep.has_nvenc && <span className="chip c-av1">NVIDIA GPU</span>}
            </div>
            <table className="table">
              <thead>
                <tr><th>Backend</th><th>Codec</th><th>Device</th><th>Test</th><th /></tr>
              </thead>
              <tbody>
                {rep.results.map((r, i) => (
                  <tr key={i}>
                    <td className="mono">{r.backend}</td>
                    <td className="mono">{r.codec.toUpperCase()}</td>
                    <td className="mono dim">{r.node?.split("/").pop() || "—"}</td>
                    <td>
                      {r.ok ? (
                        <span className="badge b-done">ok · {r.ms}ms</span>
                      ) : (
                        <span className="badge b-failed" title={r.error}>failed</span>
                      )}
                    </td>
                    <td>
                      {hw?.health[r.backend] && (
                        <>
                          <span className="badge b-failed">degraded</span>{" "}
                          <button className="btn mini" onClick={() => api.resetBackend(r.backend)}>reset</button>
                        </>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </>
        )}
      </div>

      {/* Defaults */}
      <div className="card" style={{ marginBottom: 18 }}>
        <div className="stat-label" style={{ marginBottom: 12 }}>Encoding defaults</div>
        <div className="settings-grid">
          <div className="field">
            <label>Default codec</label>
            <select className="input" value={cfg.default_codec} onChange={(e) => put({ default_codec: e.target.value })}>
              <option value="hevc">HEVC 10-bit (recommended)</option>
              <option value="av1">AV1 10-bit</option>
            </select>
          </div>
          <div className="field">
            <label>Preferred encoder</label>
            <select className="input" value={cfg.preferred_backend} onChange={(e) => put({ preferred_backend: e.target.value })}>
              <option value="auto">Auto (best available)</option>
              <option value="qsv">Intel QSV</option>
              <option value="vaapi">VA-API</option>
              <option value="nvenc">NVENC</option>
              <option value="sw">CPU (software)</option>
            </select>
          </div>
          <div className="field">
            <label>Quality ({cfg.default_quality})</label>
            <input
              type="range" min={20} max={90} value={cfg.default_quality}
              onChange={(e) => setCfg({ ...cfg, default_quality: Number(e.target.value) })}
              onMouseUp={() => put({})}
              onTouchEnd={() => put({})}
            />
          </div>
          <div className="field">
            <label>Min savings to recommend (%)</label>
            <input
              className="input" type="number" min={5} max={90} value={cfg.min_savings_pct}
              onChange={(e) => setCfg({ ...cfg, min_savings_pct: Number(e.target.value) })}
              onBlur={() => put({})}
            />
          </div>
          <div className="field">
            <label>PCM audio →</label>
            <select className="input" value={cfg.audio_pcm_target} onChange={(e) => put({ audio_pcm_target: e.target.value })}>
              <option value="flac">FLAC (lossless)</option>
              <option value="aac">AAC</option>
              <option value="eac3">E-AC3</option>
            </select>
          </div>
          <div className="field">
            <label>Parallel workers</label>
            <select className="input" value={cfg.workers} onChange={(e) => put({ workers: Number(e.target.value) })}>
              {[1, 2, 3, 4].map((n) => <option key={n} value={n}>{n}</option>)}
            </select>
          </div>
          <div className="field">
            <label>Cap output height</label>
            <select className="input" value={cfg.max_height} onChange={(e) => put({ max_height: Number(e.target.value) })}>
              <option value={0}>Keep source resolution</option>
              <option value={1080}>Max 1080p (downscale 4K)</option>
              <option value={720}>Max 720p</option>
            </select>
          </div>
          <div className="field">
            <label>Retry attempts per job</label>
            <input
              className="input" type="number" min={1} max={6} value={cfg.max_attempts}
              onChange={(e) => setCfg({ ...cfg, max_attempts: Number(e.target.value) })}
              onBlur={() => put({})}
            />
          </div>
        </div>
        <div className="chips" style={{ marginTop: 6 }}>
          <Toggle label="Re-compress existing HEVC" on={cfg.recompress_hevc} onClick={() => put({ recompress_hevc: !cfg.recompress_hevc })} />
          <Toggle label="Tone-map HDR10 → SDR (CPU only)" on={cfg.tonemap_hdr} onClick={() => put({ tonemap_hdr: !cfg.tonemap_hdr })} />
        </div>
      </div>

      {/* Safety */}
      <div className="card" style={{ marginBottom: 18 }}>
        <div className="stat-label" style={{ marginBottom: 12 }}>Replacement safety</div>
        <p className="dim" style={{ fontSize: 13, lineHeight: 1.55, marginTop: 0 }}>
          Encodes write to a hidden temp file next to the original, get verified (duration, streams,
          mid-file decode), then replace the original — preserving its modified timestamp. The
          creation timestamp is the one thing Linux won't let us rewrite; when Jellyfin is connected
          its “date added” is patched back instead. Originals are kept in trash before replacement.
        </p>
        <div className="chips">
          <Toggle
            label={`Keep originals in trash (${cfg.trash_days} days)`}
            on={cfg.trash_enabled}
            onClick={() => put({ trash_enabled: !cfg.trash_enabled })}
          />
        </div>
      </div>

      {/* Jellyfin */}
      <div className="card">
        <div className="stat-label" style={{ marginBottom: 12 }}>Jellyfin (optional)</div>
        <div className="settings-grid">
          <div className="field">
            <label>Server URL</label>
            <input
              className="input" value={cfg.jellyfin_url} placeholder="http://host.docker.internal:8096"
              onChange={(e) => setCfg({ ...cfg, jellyfin_url: e.target.value })}
              onBlur={() => put({})}
            />
          </div>
          <div className="field">
            <label>API key {cfg.jellyfin_key_set && <span className="teal" style={{ color: "var(--teal)" }}>· set</span>}</label>
            <input
              className="input" type="password" value={jfKey} placeholder={cfg.jellyfin_key_set ? "unchanged" : "paste key"}
              onChange={(e) => setJfKey(e.target.value)}
            />
          </div>
        </div>
        <div className="toolbar">
          <button className="btn" onClick={() => put({})}>Save</button>
          <button className="btn" onClick={() => api.jellyfinSync()}>Sync library metadata</button>
          {jf?.connected && (
            <span className="chip c-hevc">
              connected · {jf.server_name} {jf.version}
            </span>
          )}
          {jf && !jf.connected && jf.configured && (
            <span className="chip" style={{ color: "var(--red)" }}>{jf.error || "not reachable"}</span>
          )}
        </div>
      </div>
    </div>
  );
}

function Toggle({ label, on, onClick }: { label: string; on: boolean; onClick: () => void }) {
  return (
    <button className={`chip${on ? " c-hevc" : ""}`} style={{ cursor: "pointer" }} onClick={onClick} role="switch" aria-checked={on}>
      <span aria-hidden>{on ? "◉" : "○"}</span> {label}
    </button>
  );
}
