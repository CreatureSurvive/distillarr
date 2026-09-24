package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// Authentication. One middleware in front of every route decides
// who the caller is: nobody needs to be anyone in "disabled" mode or from
// a lan_bypass range; otherwise an X-Api-Key, a trusted proxy's username
// header, or a session cookie. Browser sessions also need the CSRF
// double-submit header on mutating requests.

const (
	sessionCookie = "distillarr_session"
	csrfCookie    = "distillarr_csrf"
	csrfHeader    = "X-CSRF-Token"
	sessionTTL    = 30 * 24 * time.Hour
)

// identity is who a request is acting as.
type identity struct {
	User string // "" when not a named user (bypass/disabled/key)
	Via  string // disabled | bypass | proxy | key | session
	sess *store.Session
	tok  string
}

func clientIP(r *http.Request) net.IP {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	return net.ParseIP(host)
}

func ipIn(ip net.IP, cidrs []string) bool {
	if ip == nil {
		return false
	}
	for _, c := range cidrs {
		if !strings.Contains(c, "/") {
			if other := net.ParseIP(c); other != nil && other.Equal(ip) {
				return true
			}
			continue
		}
		if _, n, err := net.ParseCIDR(c); err == nil && n.Contains(ip) {
			return true
		}
	}
	return false
}

// identify resolves the caller, or nil when unauthenticated.
func (s *Server) identify(r *http.Request, cfg config.Config) *identity {
	mode := cfg.AuthModeOn()
	if mode == config.AuthDisabled {
		return &identity{Via: "disabled"}
	}
	ip := clientIP(r)
	if key := r.Header.Get("X-Api-Key"); key != "" && s.st.CheckAPIKey(key) {
		return &identity{Via: "key"}
	}
	if mode == config.AuthProxyHeader && ipIn(ip, cfg.TrustedProxies) {
		if u := strings.TrimSpace(r.Header.Get(cfg.ProxyHeaderOn())); u != "" {
			return &identity{User: u, Via: "proxy"}
		}
	}
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if ss, err := s.st.SessionByToken(c.Value); err == nil && ss != nil {
			return &identity{User: ss.Username, Via: "session", sess: ss, tok: c.Value}
		}
	}
	if mode == config.AuthLANBypass && ipIn(ip, cfg.AuthCIDRsOn()) {
		return &identity{Via: "bypass"}
	}
	return nil
}

// authExempt lists what needs no identity: the SPA's static files, the
// health check, webhooks (own token), the auth routes themselves,
// and /metrics when metrics_public is set.
func authExempt(r *http.Request, cfg config.Config) bool {
	p := r.URL.Path
	switch {
	case p == "/metrics":
		return cfg.MetricsPublic
	case !strings.HasPrefix(p, "/api/"):
		return true
	case p == "/api/v1/health", strings.HasPrefix(p, "/api/v1/hooks/"):
		return true
	case p == "/api/v1/auth/status", p == "/api/v1/auth/login", p == "/api/v1/auth/logout", p == "/api/v1/auth/setup":
		return true
	}
	return false
}

func mutating(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return true
}

// sameOrigin rejects cross-site browser writes: when an Origin header is
// present it must name this host.
func sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	i := strings.Index(o, "://")
	return i >= 0 && strings.EqualFold(o[i+3:], r.Host)
}

// authMiddleware enforces the auth mode on every route.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.cfg.Get()
		if mutating(r) && !strings.HasPrefix(r.URL.Path, "/api/v1/hooks/") && !sameOrigin(r) {
			http.Error(w, "cross-origin request refused", http.StatusForbidden)
			return
		}
		if authExempt(r, cfg) {
			next.ServeHTTP(w, r)
			return
		}
		id := s.identify(r, cfg)
		if id == nil {
			n, _ := s.st.CountUsers()
			writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "login required", "setup_required": n == 0})
			return
		}
		if id.Via == "session" && mutating(r) && r.Header.Get(csrfHeader) != id.sess.CSRF {
			writeJSON(w, http.StatusForbidden, map[string]any{"error": "missing or wrong CSRF token; reload the page"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// WarnIfNoAdmin logs the first-run state at boot.
func (s *Server) WarnIfNoAdmin() {
	cfg := s.cfg.Get()
	if n, _ := s.st.CountUsers(); n == 0 && cfg.AuthModeOn() != config.AuthDisabled {
		log.Printf("auth: no admin account yet. Open the web UI from a private address to create one (mode %s).", cfg.AuthModeOn())
	}
}

func secureRequest(r *http.Request, cfg config.Config) bool {
	return r.TLS != nil || (ipIn(clientIP(r), cfg.TrustedProxies) && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"))
}

func (s *Server) setSessionCookies(w http.ResponseWriter, r *http.Request, token, csrf string) {
	sec := secureRequest(r, s.cfg.Get())
	exp := time.Now().Add(sessionTTL)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", Expires: exp, HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: sec})
	// Readable by the UI, which echoes it in X-CSRF-Token (double submit).
	http.SetCookie(w, &http.Cookie{Name: csrfCookie, Value: csrf, Path: "/", Expires: exp, SameSite: http.SameSiteLaxMode, Secure: sec})
}

func clearSessionCookies(w http.ResponseWriter) {
	for _, n := range []string{sessionCookie, csrfCookie} {
		http.SetCookie(w, &http.Cookie{Name: n, Value: "", Path: "/", MaxAge: -1})
	}
}

// ---- login throttle ----

var loginFails = struct {
	sync.Mutex
	m map[string][]time.Time
}{m: map[string][]time.Time{}}

// loginBlocked reports whether ip had 5+ failed logins in 10 minutes.
func loginBlocked(ip string) bool {
	loginFails.Lock()
	defer loginFails.Unlock()
	var keep []time.Time
	for _, t := range loginFails.m[ip] {
		if time.Since(t) < 10*time.Minute {
			keep = append(keep, t)
		}
	}
	loginFails.m[ip] = keep
	return len(keep) >= 5
}

func noteLoginFail(ip string) {
	loginFails.Lock()
	loginFails.m[ip] = append(loginFails.m[ip], time.Now())
	loginFails.Unlock()
}

// ---- routes ----

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	n, _ := s.st.CountUsers()
	id := s.identify(r, cfg)
	out := map[string]any{
		"mode":           cfg.AuthModeOn(),
		"authenticated":  id != nil,
		"setup_required": n == 0 && cfg.AuthModeOn() != config.AuthDisabled,
		"setup_allowed":  n == 0 && ipIn(clientIP(r), config.DefaultAuthCIDRs),
		"has_admin":      n > 0,
	}
	if id != nil {
		out["user"], out["via"] = id.User, id.Via
	}
	writeJSON(w, http.StatusOK, out)
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func validCredentials(c credentials) error {
	if strings.TrimSpace(c.Username) == "" {
		return fmt.Errorf("enter a username")
	}
	if len(c.Password) < 8 {
		return fmt.Errorf("use a password of at least 8 characters")
	}
	return nil
}

// authSetup creates the first admin: only while none exists, and only
// from a private address, so an exposed fresh install can't be claimed
// from the internet.
func (s *Server) authSetup(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := readJSON(r, &c); err != nil {
		fail(w, 400, err)
		return
	}
	if !ipIn(clientIP(r), config.DefaultAuthCIDRs) {
		fail(w, 403, fmt.Errorf("the first admin can only be created from a private network address"))
		return
	}
	if n, _ := s.st.CountUsers(); n > 0 {
		fail(w, 409, fmt.Errorf("an admin account already exists"))
		return
	}
	if err := validCredentials(c); err != nil {
		fail(w, 400, err)
		return
	}
	u, err := s.st.CreateUser(strings.TrimSpace(c.Username), c.Password)
	if err != nil {
		fail(w, 500, err)
		return
	}
	tok, csrf, err := s.st.CreateSession(u.ID, sessionTTL)
	if err != nil {
		fail(w, 500, err)
		return
	}
	s.setSessionCookies(w, r, tok, csrf)
	log.Printf("auth: admin account %q created", u.Username)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": u.Username})
}

func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if err := readJSON(r, &c); err != nil {
		fail(w, 400, err)
		return
	}
	ip := clientIP(r).String()
	if loginBlocked(ip) {
		fail(w, 429, fmt.Errorf("too many failed logins; try again in a few minutes"))
		return
	}
	u, err := s.st.CheckPassword(strings.TrimSpace(c.Username), c.Password)
	if err != nil {
		noteLoginFail(ip)
		log.Printf("auth: failed login for %q from %s", c.Username, ip)
		fail(w, 401, store.ErrBadCredentials)
		return
	}
	tok, csrf, err := s.st.CreateSession(u.ID, sessionTTL)
	if err != nil {
		fail(w, 500, err)
		return
	}
	_ = s.st.PruneSessions()
	s.setSessionCookies(w, r, tok, csrf)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "user": u.Username})
}

func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		_ = s.st.DeleteSession(c.Value)
	}
	clearSessionCookies(w)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// authPassword changes the admin password (the current one is always
// required, whatever let the request in) and ends other sessions.
func (s *Server) authPassword(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Current  string `json:"current"`
		New      string `json:"new"`
	}
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err)
		return
	}
	id := s.identify(r, s.cfg.Get())
	user := strings.TrimSpace(req.Username)
	if id != nil && id.sess != nil {
		user = id.sess.Username
	}
	u, err := s.st.CheckPassword(user, req.Current)
	if err != nil {
		fail(w, 400, fmt.Errorf("the current password is wrong"))
		return
	}
	if err := validCredentials(credentials{Username: u.Username, Password: req.New}); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.st.SetPassword(u.ID, req.New); err != nil {
		fail(w, 500, err)
		return
	}
	keep := ""
	if id != nil {
		keep = id.tok
	}
	_ = s.st.DeleteUserSessions(u.ID, keep)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) authKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.st.ListAPIKeys()
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func (s *Server) authCreateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if strings.TrimSpace(req.Name) == "" {
		fail(w, 400, fmt.Errorf("name the key (what uses it)"))
		return
	}
	key, row, err := s.st.CreateAPIKey(strings.TrimSpace(req.Name))
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"key": key, "info": row})
}

func (s *Server) authDeleteKey(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.st.DeleteAPIKey(id); err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// validateAuth rejects an unknown mode or unparsable address lists.
func validateAuth(c config.Config) error {
	switch c.AuthMode {
	case "", config.AuthRequired, config.AuthLANBypass, config.AuthProxyHeader, config.AuthDisabled:
	default:
		return fmt.Errorf("unknown auth mode %q", c.AuthMode)
	}
	for _, list := range [][]string{c.AuthCIDRs, c.TrustedProxies} {
		for _, v := range list {
			if _, _, err := net.ParseCIDR(v); err != nil && net.ParseIP(v) == nil {
				return fmt.Errorf("%q isn't an IP address or CIDR range", v)
			}
		}
	}
	if c.AuthMode == config.AuthProxyHeader && len(c.TrustedProxies) == 0 {
		return fmt.Errorf("proxy header mode needs at least one trusted proxy address")
	}
	return nil
}
