// gate the app behind the auth status; show first-run admin
// creation or the login form when needed.
import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import { api, AUTH_EVENT, type AuthStatus } from "./api";

export function AuthGate({ children }: { children: ReactNode }) {
  const [status, setStatus] = useState<AuthStatus | null>(null);
  const [error, setError] = useState("");
  const refresh = () => api.authStatus().then(setStatus).catch((e) => setError(e.message));
  useEffect(() => {
    refresh();
    const on = () => refresh();
    window.addEventListener(AUTH_EVENT, on);
    return () => window.removeEventListener(AUTH_EVENT, on);
  }, []);

  if (!status) return <div className="login-shell"><div className="dim small">{error || "Loading…"}</div></div>;
  if (status.authenticated && !status.setup_required) return <>{children}</>;
  return <LoginForm status={status} onDone={refresh} />;
}

function LoginForm({ status, onDone }: { status: AuthStatus; onDone: () => void }) {
  const setup = status.setup_required;
  const [user, setUser] = useState(setup ? "admin" : "");
  const [pw, setPw] = useState("");
  const [pw2, setPw2] = useState("");
  const [err, setErr] = useState("");
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setErr("");
    if (setup && pw !== pw2) return setErr("The passwords don't match.");
    setBusy(true);
    try {
      if (setup) await api.authSetup(user, pw);
      else await api.authLogin(user, pw);
      onDone();
    } catch (e: any) {
      setErr(e.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login-shell">
      <form className="panel login-card" onSubmit={submit}>
        <div className="brand">
          <span className="brand-mark" aria-hidden />
          <div><div className="brand-name">distillarr</div><div className="brand-sub">encode bay</div></div>
        </div>
        {setup ? (
          status.setup_allowed ? (
            <>
              <h1 className="panel-title">Create the admin account</h1>
              <p className="dim small">No account exists yet. This page only works from a private network address.
                You can switch to LAN bypass, proxy-header login or no login afterwards in Settings → Security.</p>
            </>
          ) : (
            <>
              <h1 className="panel-title">Setup needed</h1>
              <p className="dim small">No admin account exists yet. Open Distillarr from a device on your local network to create one.</p>
            </>
          )
        ) : (
          <h1 className="panel-title">Log in</h1>
        )}
        {(!setup || status.setup_allowed) && (
          <>
            <label className="field"><span>Username</span>
              <input className="input" value={user} onChange={(e) => setUser(e.target.value)} autoComplete="username" autoFocus={!setup} required />
            </label>
            <label className="field"><span>Password{setup && <span className="dim"> (8+ characters)</span>}</span>
              <input className="input" type="password" value={pw} onChange={(e) => setPw(e.target.value)} autoComplete={setup ? "new-password" : "current-password"} autoFocus={setup} required />
            </label>
            {setup && (
              <label className="field"><span>Repeat password</span>
                <input className="input" type="password" value={pw2} onChange={(e) => setPw2(e.target.value)} autoComplete="new-password" required />
              </label>
            )}
            {err && <div className="alert">{err}</div>}
            <button className="btn btn-primary" type="submit" disabled={busy}>{setup ? "Create account" : "Log in"}</button>
          </>
        )}
      </form>
    </div>
  );
}
