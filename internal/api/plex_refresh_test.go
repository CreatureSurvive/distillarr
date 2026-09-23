package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mediatrans/internal/config"
	"mediatrans/internal/jobs"
	"mediatrans/internal/store"
)

func TestPlexRefreshSubscriberSendsRefresh(t *testing.T) {
	gotPath := make(chan string, 1)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath <- r.URL.Path + "?" + r.URL.RawQuery
	}))
	defer fake.Close()

	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/srv/media/movies/Movie/Movie.mkv", Library: "movies", Title: "Movie"})
	if err := s.st.UpsertPlex([]store.PlexRow{{FileID: f.ID, RatingKey: "100", SectionID: "1"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.cfg.Update(func(c *config.Config) {
		c.PlexURL, c.PlexToken, c.PlexPathMap = fake.URL, "tok", "/data=/srv/media"
	}); err != nil {
		t.Fatal(err)
	}

	sub := s.PlexRefreshSubscriber()
	sub(jobs.ReplacedEvent{FileID: f.ID, Kind: "encode",
		OldPath: "/srv/media/movies/Movie/Movie.mkv", NewPath: "/srv/media/movies/Movie/Movie.mkv"})

	select {
	case p := <-gotPath:
		if p != "/library/sections/1/refresh?path=%2Fdata%2Fmovies%2FMovie" {
			t.Errorf("wrong refresh call: %s", p)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("expected a refresh call")
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
