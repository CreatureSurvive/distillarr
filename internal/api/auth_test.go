package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mediatrans/internal/config"
)

type authClient struct {
	t       *testing.T
	h       http.Handler
	remote  string
	cookies []*http.Cookie
}

func (c *authClient) do(method, path, body string, hdr map[string]string) *httptest.ResponseRecorder {
	c.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.RemoteAddr = c.remote
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	for _, ck := range c.cookies {
		r.AddCookie(ck)
	}
	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, r)
	for _, ck := range w.Result().Cookies() {
		c.cookies = append(c.cookies, ck)
	}
	return w
}

func (c *authClient) cookie(name string) string {
	v := ""
	for _, ck := range c.cookies {
		if ck.Name == name {
			v = ck.Value
		}
	}
	return v
}

func authServer(t *testing.T, mode string, extra func(*config.Config)) *Server {
	s := newTestServer(t)
	_ = s.cfg.Update(func(c *config.Config) {
		c.AuthMode = mode
		if extra != nil {
			extra(c)
		}
	})
	return s
}

const lan, wan = "192.168.1.5:5000", "203.0.113.9:5000"

func TestAuthFirstRunSetupLoginCSRF(t *testing.T) {
	s := authServer(t, config.AuthRequired, nil)
	c := &authClient{t: t, h: s.Handler(), remote: wan}

	if w := c.do("GET", "/api/v1/config", "", nil); w.Code != 401 || !strings.Contains(w.Body.String(), `"setup_required":true`) {
		t.Fatalf("no admin yet: %d %s", w.Code, w.Body)
	}
	if w := c.do("POST", "/api/v1/auth/setup", `{"username":"admin","password":"longenough"}`, nil); w.Code != 403 {
		t.Errorf("setup from a public address must be refused, got %d", w.Code)
	}
	c.remote = lan
	if w := c.do("POST", "/api/v1/auth/setup", `{"username":"admin","password":"short"}`, nil); w.Code != 400 {
		t.Errorf("short password: %d", w.Code)
	}
	if w := c.do("POST", "/api/v1/auth/setup", `{"username":"admin","password":"longenough"}`, nil); w.Code != 200 {
		t.Fatalf("setup: %d %s", w.Code, w.Body)
	}
	if w := c.do("POST", "/api/v1/auth/setup", `{"username":"x","password":"longenough"}`, nil); w.Code != 409 {
		t.Errorf("second setup must be refused, got %d", w.Code)
	}
	// The setup response logged us in.
	if w := c.do("GET", "/api/v1/config", "", nil); w.Code != 200 {
		t.Fatalf("session GET: %d", w.Code)
	}
	// Mutations need the CSRF header.
	if w := c.do("PUT", "/api/v1/config", `{"workers":2}`, nil); w.Code != 403 {
		t.Errorf("PUT without CSRF: %d", w.Code)
	}
	if w := c.do("PUT", "/api/v1/config", `{"workers":2}`, map[string]string{csrfHeader: c.cookie(csrfCookie)}); w.Code != 200 {
		t.Errorf("PUT with CSRF: %d %s", w.Code, w.Body)
	}
	// Cross-origin writes are refused even with a valid session.
	if w := c.do("PUT", "/api/v1/config", `{}`, map[string]string{csrfHeader: c.cookie(csrfCookie), "Origin": "http://evil.example"}); w.Code != 403 {
		t.Errorf("cross-origin PUT: %d", w.Code)
	}

	// Fresh client: wrong password, then right one.
	d := &authClient{t: t, h: s.Handler(), remote: wan}
	if w := d.do("POST", "/api/v1/auth/login", `{"username":"admin","password":"nope-nope"}`, nil); w.Code != 401 {
		t.Errorf("bad login: %d", w.Code)
	}
	if w := d.do("POST", "/api/v1/auth/login", `{"username":"admin","password":"longenough"}`, nil); w.Code != 200 {
		t.Fatalf("login: %d", w.Code)
	}
	if w := d.do("GET", "/api/v1/events-nope", "", nil); w.Code == 401 {
		t.Error("session should pass the middleware")
	}
	d.do("POST", "/api/v1/auth/logout", "", nil)
	d.cookies = nil
	if w := d.do("GET", "/api/v1/config", "", nil); w.Code != 401 {
		t.Errorf("after logout: %d", w.Code)
	}
}

func TestAuthExemptRoutes(t *testing.T) {
	s := authServer(t, config.AuthRequired, nil)
	c := &authClient{t: t, h: s.Handler(), remote: wan}
	for _, p := range []string{"/api/v1/health", "/api/v1/auth/status"} {
		if w := c.do("GET", p, "", nil); w.Code == 401 {
			t.Errorf("%s must be exempt", p)
		}
	}
	if w := c.do("POST", "/api/v1/hooks/arr/none", "{}", nil); w.Code == 401 {
		t.Error("webhooks carry their own token and must be exempt")
	}
	if w := c.do("GET", "/metrics", "", nil); w.Code != 401 {
		t.Errorf("/metrics without metrics_public: %d", w.Code)
	}
	_ = s.cfg.Update(func(c *config.Config) { c.MetricsPublic = true })
	if w := c.do("GET", "/metrics", "", nil); w.Code != 200 {
		t.Errorf("/metrics with metrics_public: %d", w.Code)
	}
}

func TestAuthModes(t *testing.T) {
	// lan_bypass: private addresses pass, public ones don't.
	s := authServer(t, config.AuthLANBypass, nil)
	if w := (&authClient{t: t, h: s.Handler(), remote: lan}).do("GET", "/api/v1/config", "", nil); w.Code != 200 {
		t.Errorf("lan_bypass from LAN: %d", w.Code)
	}
	if w := (&authClient{t: t, h: s.Handler(), remote: wan}).do("GET", "/api/v1/config", "", nil); w.Code != 401 {
		t.Errorf("lan_bypass from WAN: %d", w.Code)
	}
	// Custom CIDRs replace the defaults.
	_ = s.cfg.Update(func(c *config.Config) { c.AuthCIDRs = []string{"10.9.0.0/16"} })
	if w := (&authClient{t: t, h: s.Handler(), remote: lan}).do("GET", "/api/v1/config", "", nil); w.Code != 401 {
		t.Errorf("lan_bypass outside custom CIDRs: %d", w.Code)
	}

	// proxy_header: only from a trusted proxy, only with the header.
	p := authServer(t, config.AuthProxyHeader, func(c *config.Config) { c.TrustedProxies = []string{"10.0.0.1"} })
	hdr := map[string]string{"Remote-User": "dana"}
	if w := (&authClient{t: t, h: p.Handler(), remote: "10.0.0.1:1"}).do("GET", "/api/v1/config", "", hdr); w.Code != 200 {
		t.Errorf("trusted proxy with header: %d", w.Code)
	}
	if w := (&authClient{t: t, h: p.Handler(), remote: "10.0.0.1:1"}).do("GET", "/api/v1/config", "", nil); w.Code != 401 {
		t.Errorf("trusted proxy without header: %d", w.Code)
	}
	if w := (&authClient{t: t, h: p.Handler(), remote: wan}).do("GET", "/api/v1/config", "", hdr); w.Code != 401 {
		t.Errorf("header from an untrusted address must be ignored: %d", w.Code)
	}

	// disabled: everyone.
	d := authServer(t, config.AuthDisabled, nil)
	if w := (&authClient{t: t, h: d.Handler(), remote: wan}).do("PUT", "/api/v1/config", `{"workers":2}`, nil); w.Code != 200 {
		t.Errorf("disabled: %d", w.Code)
	}

	// An unknown mode is rejected by PUT /config.
	if w := (&authClient{t: t, h: d.Handler(), remote: wan}).do("PUT", "/api/v1/config", `{"auth_mode":"open"}`, nil); w.Code == 200 {
		t.Error("unknown mode accepted")
	}
}

func TestAPIKeys(t *testing.T) {
	s := authServer(t, config.AuthRequired, nil)
	if _, err := s.st.CreateUser("admin", "longenough"); err != nil {
		t.Fatal(err)
	}
	key, info, err := s.st.CreateAPIKey("script")
	if err != nil {
		t.Fatal(err)
	}
	c := &authClient{t: t, h: s.Handler(), remote: wan}
	if w := c.do("PUT", "/api/v1/config", `{"workers":2}`, map[string]string{"X-Api-Key": key}); w.Code != 200 {
		t.Errorf("API key write (no CSRF needed): %d %s", w.Code, w.Body)
	}
	if w := c.do("GET", "/api/v1/auth/keys", "", map[string]string{"X-Api-Key": key}); !strings.Contains(w.Body.String(), info.Prefix) || strings.Contains(w.Body.String(), key) {
		t.Errorf("list must show the prefix and never the key: %s", w.Body)
	}
	if err := s.st.DeleteAPIKey(info.ID); err != nil {
		t.Fatal(err)
	}
	if w := c.do("GET", "/api/v1/config", "", map[string]string{"X-Api-Key": key}); w.Code != 401 {
		t.Errorf("revoked key: %d", w.Code)
	}
}

func TestPasswordHashRoundTrip(t *testing.T) {
	s := newTestServer(t)
	if _, err := s.st.CreateUser("admin", "correct horse"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.CheckPassword("admin", "correct horse"); err != nil {
		t.Errorf("right password: %v", err)
	}
	if _, err := s.st.CheckPassword("admin", "wrong horse"); err == nil {
		t.Error("wrong password accepted")
	}
	if _, err := s.st.CheckPassword("nobody", "correct horse"); err == nil {
		t.Error("unknown user accepted")
	}
	var status map[string]any
	w := httptest.NewRecorder()
	s.authStatus(w, httptest.NewRequest("GET", "/", nil))
	_ = json.Unmarshal(w.Body.Bytes(), &status)
	if status["has_admin"] != true {
		t.Errorf("status %v", status)
	}
}
