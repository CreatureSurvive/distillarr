package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/jobs"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// fakePlexRefresh serves /library/metadata/{ratingKey} (returning
// metaAddedAt's current value, so a caller can flip it between the
// before/after read to simulate Plex changing the date on refresh),
// /library/sections/{id}/refresh, and /library/sections/{id}/all (the
// SetAddedAt write). Each call is recorded on the given channels/counter
// when non-nil.
type fakePlexRefresh struct {
	addedAt    atomic.Int64
	refreshed  chan string
	setAddedAt chan string
}

func newFakePlexRefresh(initialAddedAt int64) *fakePlexRefresh {
	f := &fakePlexRefresh{refreshed: make(chan string, 1), setAddedAt: make(chan string, 1)}
	f.addedAt.Store(initialAddedAt)
	return f
}

func (f *fakePlexRefresh) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/library/metadata/100":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"MediaContainer":{"Metadata":[{"ratingKey":"100","addedAt":%d}]}}`, f.addedAt.Load())
		case r.URL.Path == "/library/sections/1/refresh":
			select {
			case f.refreshed <- r.URL.Path + "?" + r.URL.RawQuery:
			default:
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut && r.URL.Path == "/library/sections/1/all":
			select {
			case f.setAddedAt <- r.URL.Path + "?" + r.URL.RawQuery:
			default:
			}
			w.WriteHeader(http.StatusOK)
		default:
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]any{})
		}
	}
}

func TestPlexRefreshSubscriberSendsRefresh(t *testing.T) {
	f := newFakePlexRefresh(1700000000)
	fake := httptest.NewServer(f.handler())
	defer fake.Close()

	s := newTestServer(t)
	file := mustUpsert(t, s.st, &store.File{Path: "/srv/media/movies/Movie/Movie.mkv", Library: "movies", Title: "Movie"})
	if err := s.st.UpsertPlex([]store.PlexRow{{FileID: file.ID, RatingKey: "100", SectionID: "1", AddedAt: 1700000000, ItemType: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.cfg.Update(func(c *config.Config) {
		c.PlexURL, c.PlexToken, c.PlexPathMap = fake.URL, "tok", "/data=/srv/media"
	}); err != nil {
		t.Fatal(err)
	}

	sub := s.PlexRefreshSubscriber()
	sub(jobs.ReplacedEvent{FileID: file.ID, Kind: "encode",
		OldPath: "/srv/media/movies/Movie/Movie.mkv", NewPath: "/srv/media/movies/Movie/Movie.mkv"})

	select {
	case p := <-f.refreshed:
		if p != "/library/sections/1/refresh?path=%2Fdata%2Fmovies%2FMovie" {
			t.Errorf("wrong refresh call: %s", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected a refresh call")
	}
	select {
	case p := <-f.setAddedAt:
		t.Errorf("addedAt unchanged: must not restore, got %s", p)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestPlexRefreshSubscriberSkipsUnsyncedFile(t *testing.T) {
	called := false
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
	defer fake.Close()

	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/srv/media/movies/Movie/Movie.mkv", Library: "movies", Title: "Movie"})
	if err := s.cfg.Update(func(c *config.Config) {
		c.PlexURL, c.PlexToken = fake.URL, "tok"
	}); err != nil {
		t.Fatal(err)
	}

	sub := s.PlexRefreshSubscriber()
	sub(jobs.ReplacedEvent{FileID: f.ID, Kind: "encode", NewPath: "/srv/media/movies/Movie/Movie.mkv"})
	time.Sleep(100 * time.Millisecond)
	if called {
		t.Error("a file never synced from Plex must not trigger a refresh call")
	}
}

func TestPlexRefreshSubscriberNoopWhenUnconfigured(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/srv/media/movies/Movie/Movie.mkv", Library: "movies", Title: "Movie"})
	if err := s.st.UpsertPlex([]store.PlexRow{{FileID: f.ID, RatingKey: "100", SectionID: "1"}}); err != nil {
		t.Fatal(err)
	}
	// Must not panic or block with no Plex URL/token set.
	s.PlexRefreshSubscriber()(jobs.ReplacedEvent{FileID: f.ID, Kind: "encode", NewPath: "/srv/media/movies/Movie/Movie.mkv"})
}

// TestPlexRefreshSubscriberRestoresAddedAt: when the refresh
// changes addedAt, the subscriber writes the original value back locked.
func TestPlexRefreshSubscriberRestoresAddedAt(t *testing.T) {
	f := newFakePlexRefresh(1700000000)
	mux := http.NewServeMux()
	mux.HandleFunc("/library/sections/1/refresh", func(w http.ResponseWriter, r *http.Request) {
		f.addedAt.Store(1800000000) // simulate Plex bumping the date on refresh
		f.handler()(w, r)
	})
	mux.HandleFunc("/", f.handler())
	fake := httptest.NewServer(mux)
	defer fake.Close()

	s := newTestServer(t)
	file := mustUpsert(t, s.st, &store.File{Path: "/srv/media/movies/Movie/Movie.mkv", Library: "movies", Title: "Movie"})
	if err := s.st.UpsertPlex([]store.PlexRow{{FileID: file.ID, RatingKey: "100", SectionID: "1", AddedAt: 1700000000, ItemType: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.cfg.Update(func(c *config.Config) {
		c.PlexURL, c.PlexToken, c.PlexPathMap = fake.URL, "tok", "/data=/srv/media"
	}); err != nil {
		t.Fatal(err)
	}

	sub := s.PlexRefreshSubscriber()
	sub(jobs.ReplacedEvent{FileID: file.ID, Kind: "encode", NewPath: "/srv/media/movies/Movie/Movie.mkv"})

	select {
	case p := <-f.setAddedAt:
		if p != "/library/sections/1/all?addedAt.locked=1&addedAt.value=1700000000&id=100&type=1" {
			t.Errorf("wrong restore call: %s", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected an addedAt restore call")
	}

	row, err := s.st.PlexByFileID(file.ID)
	if err != nil || row == nil || row.AddedAt != 1700000000 {
		t.Errorf("cache not updated after restore: %+v, %v", row, err)
	}
}

// TestPlexRefreshSubscriberRespectsKeepAddedAtOff: the toggle off
// must skip both addedAt reads and never call SetAddedAt.
func TestPlexRefreshSubscriberRespectsKeepAddedAtOff(t *testing.T) {
	f := newFakePlexRefresh(1700000000)
	mux := http.NewServeMux()
	mux.HandleFunc("/library/sections/1/refresh", func(w http.ResponseWriter, r *http.Request) {
		f.addedAt.Store(1800000000)
		f.handler()(w, r)
	})
	mux.HandleFunc("/", f.handler())
	fake := httptest.NewServer(mux)
	defer fake.Close()

	s := newTestServer(t)
	file := mustUpsert(t, s.st, &store.File{Path: "/srv/media/movies/Movie/Movie.mkv", Library: "movies", Title: "Movie"})
	if err := s.st.UpsertPlex([]store.PlexRow{{FileID: file.ID, RatingKey: "100", SectionID: "1", AddedAt: 1700000000, ItemType: 1}}); err != nil {
		t.Fatal(err)
	}
	off := false
	if err := s.cfg.Update(func(c *config.Config) {
		c.PlexURL, c.PlexToken, c.PlexPathMap, c.PlexKeepAddedAt = fake.URL, "tok", "/data=/srv/media", &off
	}); err != nil {
		t.Fatal(err)
	}

	sub := s.PlexRefreshSubscriber()
	sub(jobs.ReplacedEvent{FileID: file.ID, Kind: "encode", NewPath: "/srv/media/movies/Movie/Movie.mkv"})

	select {
	case <-f.refreshed:
	case <-time.After(2 * time.Second):
		t.Fatal("expected a refresh call")
	}
	select {
	case p := <-f.setAddedAt:
		t.Errorf("toggle off: must not restore addedAt, got %s", p)
	case <-time.After(200 * time.Millisecond):
	}
}
