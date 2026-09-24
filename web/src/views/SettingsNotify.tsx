import { useEffect, useState } from "react";
import { api, type Config, type Notifier, type NotifyEventType, type NotifyStatus } from "../api";
import { Seg, Toggle, toast } from "../components";
import { ago } from "../format";

// Shoutrrr services offered by "Add target": URL template and docs path
// (github.com/nicholas-fedor/shoutrrr/tree/main/docs/services/<path>).
const SERVICES: { scheme: string; label: string; template: string; docs: string }[] = [
  { scheme: "discord", label: "Discord", template: "discord://TOKEN@WEBHOOK_ID", docs: "chat/discord" },
  { scheme: "ntfy", label: "ntfy", template: "ntfy://ntfy.sh/TOPIC", docs: "push/ntfy" },
  { scheme: "telegram", label: "Telegram", template: "telegram://TOKEN@telegram?chats=@CHANNEL", docs: "chat/telegram" },
  { scheme: "pushover", label: "Pushover", template: "pushover://shoutrrr:API_TOKEN@USER_KEY", docs: "push/pushover" },
  { scheme: "gotify", label: "Gotify", template: "gotify://HOST/TOKEN", docs: "push/gotify" },
  { scheme: "slack", label: "Slack", template: "slack://TOKEN_A/TOKEN_B/TOKEN_C", docs: "chat/slack" },
  { scheme: "matrix", label: "Matrix", template: "matrix://USER:PASSWORD@HOST", docs: "chat/matrix" },
  { scheme: "smtp", label: "Email (SMTP)", template: "smtp://USER:PASSWORD@HOST:587/?from=FROM@EXAMPLE.COM&to=TO@EXAMPLE.COM", docs: "email/smtp" },
  { scheme: "generic", label: "Webhook (generic)", template: "generic://HOST/PATH", docs: "specialized/generic" },
];

const docsURL = (scheme?: string) => {
  const s = SERVICES.find((x) => x.scheme === scheme);
  return `https://github.com/nicholas-fedor/shoutrrr/tree/main/docs/services${s ? "/" + s.docs : ""}`;
};

const toTime = (m: number) => `${String(Math.floor(m / 60)).padStart(2, "0")}:${String(m % 60).padStart(2, "0")}`;
const fromTime = (v: string) => { const [h, m] = v.split(":").map(Number); return (h || 0) * 60 + (m || 0); };

type Draft = Notifier & { urlInput: string };

// Notification targets. Like the arr cards, Save/Remove send the
// whole list: the server's PUT replaces notifiers wholesale, keeping a
// saved URL when the draft's is blank.
export function NotificationsSection({ cfg, setCfg }: { cfg: Config; setCfg: (c: Config) => void }) {
  const [drafts, setDrafts] = useState<Draft[]>(() => (cfg.notifiers || []).map((n) => ({ ...n, urlInput: "" })));
  const [types, setTypes] = useState<NotifyEventType[]>([]);
  const [status, setStatus] = useState<NotifyStatus>({});
  const [adding, setAdding] = useState(false);

  useEffect(() => { api.notifyEvents().then(setTypes).catch(() => {}); }, []);
  const refreshStatus = () => api.notifyStatus().then(setStatus).catch(() => {});
  useEffect(() => { refreshStatus(); }, []);

  const saveAll = async (list: Draft[], msg: string) => {
    try {
      const c = await api.saveConfig({ notifiers: list.map(({ urlInput, url_set, service, ...n }) => ({ ...n, url: urlInput })) });
      setCfg(c);
      setDrafts((c.notifiers || []).map((n) => ({ ...n, urlInput: "" })));
      toast(msg);
    } catch (e: any) {
      toast(e.message, "err");
    }
  };

  const patch = (i: number, p: Partial<Draft>) => setDrafts(drafts.map((d, j) => (j === i ? { ...d, ...p } : d)));

  const add = (scheme: string) => {
    const s = SERVICES.find((x) => x.scheme === scheme)!;
    setDrafts([...drafts, {
      id: "", name: s.label, urlInput: s.template, service: s.scheme, enabled: true,
      events: types.filter((t) => t.default_on).map((t) => t.key), min_level: "", quiet_start: 0, quiet_end: 0,
    }]);
    setAdding(false);
  };

  const test = async (d: Draft) => {
    try {
      const r = await api.notifyTest(d.id, d.urlInput || undefined);
      if (r.ok) toast(`Test sent to ${d.name}`);
      else toast(r.error || "Test failed", "err");
    } catch (e: any) {
      toast(e.message, "err");
    }
    refreshStatus();
  };

  return (
    <section className="panel" id="s-notify">
      <div className="panel-head">
        <h2 className="panel-title">Notifications</h2>
      </div>
      <p className="dim small">
        Send events to Discord, ntfy, Telegram, email and more. Bursts of the same event within a minute are
        combined into one message. Info events wait out a target's quiet hours; warnings and errors don't.
        Links in messages use the webhook base URL under Connections when it's set.
      </p>
      {drafts.length === 0 && <div className="dim small" style={{ margin: "10px 0" }}>No targets yet.</div>}
      {drafts.map((d, i) => {
        const st = d.id ? status[d.id] : undefined;
        const scheme = d.urlInput ? d.urlInput.split(":")[0].toLowerCase() : d.service;
        return (
          <div className="panel" style={{ marginTop: 12, marginBottom: 0 }} key={d.id || `new-${i}`}>
            <div className="panel-head">
              <input className="input" style={{ maxWidth: 260 }} value={d.name} onChange={(e) => patch(i, { name: e.target.value })} aria-label="Target name" />
              <Toggle on={d.enabled} onChange={(v) => patch(i, { enabled: v })} label="Enabled" />
            </div>
            <label className="field"><span>URL {d.url_set && <span className="teal">· saved</span>}{" "}
              <a href={docsURL(scheme)} target="_blank" rel="noreferrer" className="dim small">{scheme ? `${scheme} URL format` : "URL formats"}</a></span>
              <input className="input mono" type={d.url_set ? "password" : "text"} value={d.urlInput} onChange={(e) => patch(i, { urlInput: e.target.value })}
                placeholder={d.url_set ? "Leave blank to keep the saved URL" : "service://…"} autoComplete="off" spellCheck={false} />
            </label>
            <div className="opts">
              <label className="field"><span>Minimum level</span>
                <Seg value={d.min_level || "info"} onChange={(v) => patch(i, { min_level: v === "info" ? "" : v })}
                  options={[{ value: "info", label: "Info" }, { value: "warning", label: "Warning" }, { value: "error", label: "Error" }]} />
              </label>
              <label className="field"><span>Quiet hours <span className="dim">(same time = none)</span></span>
                <span style={{ display: "flex", gap: 8, alignItems: "center" }}>
                  <input className="input" type="time" value={toTime(d.quiet_start)} onChange={(e) => patch(i, { quiet_start: fromTime(e.target.value) })} aria-label="Quiet hours start" />
                  to
                  <input className="input" type="time" value={toTime(d.quiet_end)} onChange={(e) => patch(i, { quiet_end: fromTime(e.target.value) })} aria-label="Quiet hours end" />
                </span>
              </label>
            </div>
            <div className="field"><span>Events</span>
              <div className="toggles">
                {types.map((t) => (
                  <Toggle key={t.key} on={d.events.includes(t.key)} label={`${t.label} · ${t.level}`} hint={t.help}
                    onChange={(v) => patch(i, { events: v ? [...d.events, t.key] : d.events.filter((k) => k !== t.key) })} />
                ))}
              </div>
            </div>
            {st?.last_error && <div className="alert">Last delivery failed {ago(st.last_error_at)}: {st.last_error}</div>}
            {st?.last_sent_at && !st.last_error && <div className="dim small">Last delivered {ago(st.last_sent_at)}.</div>}
            <div className="toolbar">
              <button className="btn" onClick={() => test(d)}>Test</button>
              <button className="btn btn-primary" onClick={() => saveAll(drafts, `${d.name} saved`)}>Save</button>
              <button className="btn btn-danger" onClick={() => saveAll(drafts.filter((_, j) => j !== i), `${d.name} removed`)}>Remove</button>
            </div>
          </div>
        );
      })}
      <div className="toolbar">
        {adding ? (
          <>
            {SERVICES.map((s) => <button key={s.scheme} className="btn" onClick={() => add(s.scheme)}>{s.label}</button>)}
            <button className="btn" onClick={() => setAdding(false)}>Cancel</button>
          </>
        ) : (
          <button className="btn" onClick={() => setAdding(true)}>+ Add target</button>
        )}
      </div>
    </section>
  );
}
