// SPDX-License-Identifier: GPL-3.0-or-later

// Settings → Security: auth mode, trusted ranges/proxies, the
// admin password and API keys.
import { useEffect, useState } from "react";
import { api, type APIKey, type AuthStatus, type Config } from "../api";
import { Seg, Toggle, toast } from "../components";
import { ago } from "../format";

const MODES = [
  { value: "required", label: "Always log in" },
  { value: "lan_bypass", label: "LAN bypass" },
  { value: "proxy_header", label: "Proxy header" },
  { value: "disabled", label: "Off" },
];

const lines = (v: string) => v.split(/[\s,]+/).map((x) => x.trim()).filter(Boolean);

export function SecuritySection({ cfg, setCfg }: { cfg: Config; setCfg: (c: Config) => void }) {
  const [status, setStatus] = useState<AuthStatus | null>(null);
  const [mode, setMode] = useState(cfg.auth_mode || "required");
  const [cidrs, setCidrs] = useState((cfg.auth_cidrs || []).join("\n"));
  const [proxies, setProxies] = useState((cfg.trusted_proxies || []).join("\n"));
  const [header, setHeader] = useState(cfg.proxy_header || "");
  const [confirmOff, setConfirmOff] = useState(false);
  const [pw, setPw] = useState({ current: "", next: "", again: "" });
  const [keys, setKeys] = useState<APIKey[]>([]);
  const [keyName, setKeyName] = useState("");
  const [newKey, setNewKey] = useState("");

  useEffect(() => { api.authStatus().then(setStatus).catch(() => {}); }, []);
  const loadKeys = () => api.authKeys().then(setKeys).catch(() => {});
  useEffect(() => { loadKeys(); }, []);

  const saveMode = async () => {
    if (mode === "disabled" && !confirmOff) {
      setConfirmOff(true);
      return;
    }
    try {
      setCfg(await api.saveConfig({ auth_mode: mode, auth_cidrs: lines(cidrs), trusted_proxies: lines(proxies), proxy_header: header }));
      setConfirmOff(false);
      toast("Security settings saved");
    } catch (e: any) {
      toast(e.message, "err");
    }
  };

  const changePassword = async () => {
    if (pw.next !== pw.again) return toast("The new passwords don't match", "err");
    try {
      await api.authPassword({ username: status?.user, current: pw.current, new: pw.next });
      setPw({ current: "", next: "", again: "" });
      toast("Password changed. Other sessions were logged out.");
    } catch (e: any) {
      toast(e.message, "err");
    }
  };

  const createKey = async () => {
    try {
      const r = await api.authCreateKey(keyName);
      setNewKey(r.key);
      setKeyName("");
      loadKeys();
    } catch (e: any) {
      toast(e.message, "err");
    }
  };

  const logout = async () => {
    await api.authLogout().catch(() => {});
    location.reload();
  };

  return (
    <section className="panel" id="s-auth">
      <div className="panel-head">
        <h2 className="panel-title">Security</h2>
        {status?.via === "session" && <button className="btn mini" onClick={logout}>Log out {status.user}</button>}
      </div>
      <p className="dim small">
        {status?.via === "bypass" ? "You're on a trusted network, so no login was needed for this page. " : ""}
        Webhooks keep their own tokens, and the health check is always open.
      </p>
      <div className="opts">
        <label className="field"><span>Login</span>
          <Seg value={mode} onChange={(v) => { setMode(v); setConfirmOff(false); }} options={MODES} />
        </label>
      </div>
      <p className="dim small">
        {mode === "required" && "Everyone logs in, including on your own network."}
        {mode === "lan_bypass" && "No login from the addresses below (default: private ranges such as 192.168.x.x); everyone else logs in."}
        {mode === "proxy_header" && "A reverse proxy such as Authelia or Authentik logs people in and passes the username in a header. Only requests from the trusted proxy addresses below may set it; anyone else logs in normally."}
        {mode === "disabled" && "No login at all: anyone who can reach this port can change settings and replace files. Only use this behind something else that controls access."}
      </p>
      {mode === "lan_bypass" && (
        <label className="field"><span>Trusted ranges <span className="dim">(one per line; blank = private ranges)</span></span>
          <textarea className="input mono" rows={3} value={cidrs} onChange={(e) => setCidrs(e.target.value)} placeholder={"192.168.0.0/16\n10.0.0.0/8"} spellCheck={false} />
        </label>
      )}
      {mode === "proxy_header" && (
        <div className="form-grid">
          <label className="field"><span>Trusted proxy addresses</span>
            <textarea className="input mono" rows={3} value={proxies} onChange={(e) => setProxies(e.target.value)} placeholder="172.18.0.2" spellCheck={false} />
          </label>
          <label className="field"><span>Username header</span>
            <input className="input mono" value={header} onChange={(e) => setHeader(e.target.value)} placeholder="Remote-User" spellCheck={false} />
          </label>
        </div>
      )}
      {confirmOff && (
        <div className="alert">Turning login off lets anyone who can reach Distillarr change settings, queue jobs and replace files. Press Save again to confirm.</div>
      )}
      <div className="toggles">
        <Toggle on={!!cfg.metrics_public} label="Leave /metrics open for Prometheus"
          hint="Otherwise scrape it with an API key in the X-Api-Key header."
          onChange={(v) => api.saveConfig({ metrics_public: v }).then(setCfg).catch((e) => toast(e.message, "err"))} />
      </div>
      <div className="toolbar">
        <button className="btn btn-primary" onClick={saveMode}>{confirmOff ? "Save: turn login off" : "Save"}</button>
      </div>

      <h3 className="panel-title" style={{ fontSize: 14, marginTop: 18 }}>Admin password</h3>
      {status?.has_admin ? (
        <div className="form-grid">
          <label className="field"><span>Current</span>
            <input className="input" type="password" value={pw.current} onChange={(e) => setPw({ ...pw, current: e.target.value })} autoComplete="current-password" />
          </label>
          <label className="field"><span>New</span>
            <input className="input" type="password" value={pw.next} onChange={(e) => setPw({ ...pw, next: e.target.value })} autoComplete="new-password" />
          </label>
          <label className="field"><span>Repeat new</span>
            <input className="input" type="password" value={pw.again} onChange={(e) => setPw({ ...pw, again: e.target.value })} autoComplete="new-password" />
          </label>
          <div className="toolbar"><button className="btn" onClick={changePassword} disabled={!pw.current || !pw.next}>Change password</button></div>
        </div>
      ) : (
        <p className="dim small">No admin account yet.</p>
      )}

      <h3 className="panel-title" style={{ fontSize: 14, marginTop: 18 }}>API keys</h3>
      <p className="dim small">For scripts and Prometheus: send the key in an <span className="mono">X-Api-Key</span> header. A key is shown once, when it's created.</p>
      {keys.length > 0 && (
        <ul className="lib-check">
          {keys.map((k) => (
            <li key={k.id}>
              <span>{k.name}</span>
              <span className="mono dim small">{k.prefix}… · created {ago(k.created_at)} · {k.last_used_at ? `used ${ago(k.last_used_at)}` : "never used"}</span>
              <button className="btn mini btn-danger" onClick={() => api.authDeleteKey(k.id).then(loadKeys).catch((e) => toast(e.message, "err"))}>Revoke</button>
            </li>
          ))}
        </ul>
      )}
      {newKey && <div className="test-result ok"><b>New key:</b> <span className="mono">{newKey}</span> <span className="dim small">Copy it now; it won't be shown again.</span></div>}
      <div className="toolbar">
        <input className="input" style={{ maxWidth: 260 }} value={keyName} onChange={(e) => setKeyName(e.target.value)} placeholder="What uses it, e.g. Prometheus" />
        <button className="btn" onClick={createKey} disabled={!keyName.trim()}>Create key</button>
      </div>
    </section>
  );
}
