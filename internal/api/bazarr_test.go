package api

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/jobs"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// fakeBazarr records every PATCH as "path?query" and checks the API key.
type fakeBazarr struct {
	mu    sync.Mutex
	calls []string
}

func (f *fakeBazarr) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-KEY") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPatch {
			f.mu.Lock()
			f.calls = append(f.calls, r.URL.Path+"?"+r.URL.RawQuery)
			f.mu.Unlock()
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (f *fakeBazarr) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func setupBazarr(t *testing.T, url string) (*Server, *store.File, *store.File) {
	t.Helper()
	s := newTestServer(t)
	ep := mustUpsert(t, s.st, &store.File{Path: "/m/show/e1.mkv", Library: "tv", Title: "Show"})
	mv := mustUpsert(t, s.st, &store.File{Path: "/m/movie.mkv", Library: "movies", Title: "Movie"})
	if err := s.cfg.Update(func(c *config.Config) { c.BazarrURL, c.BazarrKey = url, "k" }); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{
		{FileID: ep.ID, InstanceID: "sonarr", Kind: "sonarr", ItemID: 42},
		{FileID: mv.ID, InstanceID: "radarr", Kind: "radarr", ItemID: 7},
	}); err != nil {
		t.Fatal(err)
	}
	return s, ep, mv
}

func TestBazarrRescanSubscriber(t *testing.T) {
	fastSeasonDebounce(t, 20*time.Millisecond)
	fb := &fakeBazarr{}
	fake := httptest.NewServer(fb.handler(t))
	defer fake.Close()
	s, ep, mv := setupBazarr(t, fake.URL)

	sub := s.BazarrRescanSubscriber()
	sub(jobs.ReplacedEvent{FileID: mv.ID, Kind: "encode", OldPath: mv.Path, NewPath: mv.Path})
	// Two episode finishes of one series coalesce into one scan.
	sub(jobs.ReplacedEvent{FileID: ep.ID, Kind: "remux", OldPath: ep.Path, NewPath: "/m/show/e1.mp4"})
	sub(jobs.ReplacedEvent{FileID: ep.ID, Kind: "remux", OldPath: ep.Path, NewPath: "/m/show/e1.mp4"})
	// Not a replace: ignored.
	sub(jobs.ReplacedEvent{FileID: mv.ID, Kind: "preview"})

	waitFor(t, time.Second, func() bool { return len(fb.got()) == 2 })
	time.Sleep(50 * time.Millisecond)
	got := fb.got()
	if len(got) != 2 {
		t.Fatalf("calls = %v", got)
	}
	want := map[string]bool{
		"/api/movies?action=scan-disk&radarrid=7":  true,
		"/api/series?action=scan-disk&seriesid=42": true,
	}
	for _, c := range got {
		if !want[c] {
			t.Errorf("unexpected call %q", c)
		}
	}
}

func TestBazarrRescanSubscriberNoopWhenUnconfigured(t *testing.T) {
	fb := &fakeBazarr{}
	fake := httptest.NewServer(fb.handler(t))
	defer fake.Close()
	s, _, mv := setupBazarr(t, fake.URL)
	if err := s.cfg.Update(func(c *config.Config) { c.BazarrURL = "" }); err != nil {
		t.Fatal(err)
	}
	s.BazarrRescanSubscriber()(jobs.ReplacedEvent{FileID: mv.ID, Kind: "encode"})
	time.Sleep(30 * time.Millisecond)
	if len(fb.got()) != 0 {
		t.Errorf("calls = %v", fb.got())
	}
}

func TestBazarrSearch(t *testing.T) {
	fb := &fakeBazarr{}
	fake := httptest.NewServer(fb.handler(t))
	defer fake.Close()
	s, ep, _ := setupBazarr(t, fake.URL)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetPathValue("id", itoa64(ep.ID))
	w := httptest.NewRecorder()
	s.bazarrSearch(w, req)
	if w.Code != 200 {
		t.Fatalf("code %d: %s", w.Code, w.Body)
	}
	if got := fb.got(); len(got) != 1 || got[0] != "/api/series?action=search-missing&seriesid=42" {
		t.Errorf("calls = %v", got)
	}

	// An unmanaged file can't be addressed in Bazarr.
	other := mustUpsert(t, s.st, &store.File{Path: "/m/x.mkv", Library: "movies", Title: "X"})
	req = httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetPathValue("id", itoa64(other.ID))
	w = httptest.NewRecorder()
	s.bazarrSearch(w, req)
	if w.Code != 400 {
		t.Errorf("unmanaged: code %d", w.Code)
	}
}

func TestBazarrKeyMasked(t *testing.T) {
	s, _, _ := setupBazarr(t, "http://bazarr")
	w := httptest.NewRecorder()
	s.getConfig(w, httptest.NewRequest(http.MethodGet, "/", nil))
	body := w.Body.String()
	if !contains(body, `"bazarr_key_set":true`) || contains(body, `"bazarr_key":"k"`) {
		t.Errorf("config body leaks or misreports the key: %s", body)
	}
}
