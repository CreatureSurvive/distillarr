package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNotifierMergeAndMask(t *testing.T) {
	s := newTestServer(t)
	put := func(body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		s.putConfig(w, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body)))
		return w
	}
	if w := put(`{"notifiers":[{"name":"My Discord","url":"discord://tok@123","enabled":true,"events":["job_failed"]}]}`); w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	ns := s.cfg.Get().Notifiers
	if len(ns) != 1 || ns[0].ID != "my-discord" || ns[0].URL != "discord://tok@123" {
		t.Fatalf("stored %+v", ns)
	}

	w := httptest.NewRecorder()
	s.getConfig(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if body := w.Body.String(); strings.Contains(body, "tok@123") || !strings.Contains(body, `"url_set":true`) || !strings.Contains(body, `"service":"discord"`) {
		t.Errorf("GET leaks or misreports the URL: %s", body)
	}

	// A blank URL on an existing id keeps the saved one; other fields change.
	put(`{"notifiers":[{"id":"my-discord","name":"Renamed","url":"","enabled":false,"events":[]}]}`)
	if n := s.cfg.Get().Notifiers[0]; n.URL != "discord://tok@123" || n.Name != "Renamed" || n.Enabled {
		t.Errorf("after edit %+v", n)
	}

	if w := put(`{"notifiers":[{"id":"my-discord","min_level":"loud"}]}`); w.Code == 200 {
		t.Error("an unknown level must be rejected")
	}

	// Leaving an id out removes it.
	put(`{"notifiers":[]}`)
	if got := s.cfg.Get().Notifiers; len(got) != 0 {
		t.Errorf("after removal %+v", got)
	}
}
