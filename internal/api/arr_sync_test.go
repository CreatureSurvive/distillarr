package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// fakeSonarr serves a fixed series + episode file list, keyed by whether
// removeEpisode has been called (simulating the file being deleted in
// Sonarr between two sync passes).
type fakeSonarr struct {
	removed bool
}

func (f *fakeSonarr) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/series":
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1, "title": "Show", "path": "/data/tvshows/Show", "qualityProfileId": 7,
					"tags": []int{3}, "status": "continuing", "monitored": true,
					"originalLanguage": map[string]string{"name": "English"}},
			})
		case r.URL.Path == "/api/v3/episodefile":
			if f.removed {
				json.NewEncoder(w).Encode([]map[string]any{})
				return
			}
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 10, "seriesId": 1, "path": "/data/tvshows/Show/Show S01E01.mkv",
					"qualityCutoffNotMet": true, "customFormatScore": 5, "sceneName": "Show.S01E01"},
			})
		case r.URL.Path == "/api/v3/tag":
			json.NewEncoder(w).Encode([]map[string]any{{"id": 3, "label": "keep-quality"}})
		default:
			w.WriteHeader(404)
		}
	}
}

func fakeRadarrHandler(moviePath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/movie":
			json.NewEncoder(w).Encode([]map[string]any{
				{"id": 1, "title": "Movie", "path": "/data/movies/Movie", "qualityProfileId": 4,
					"tags": []int{}, "status": "released", "monitored": true, "hasFile": true,
					"originalLanguage": map[string]string{"name": "English"},
					"movieFile": map[string]any{
						"id": 20, "movieId": 1, "path": moviePath,
						"qualityCutoffNotMet": false, "customFormatScore": 0, "sceneName": "Movie.2020",
					}},
			})
		case "/api/v3/tag":
			json.NewEncoder(w).Encode([]map[string]any{})
		default:
			w.WriteHeader(404)
		}
	}
}

func TestSyncArrOwnershipAndTags(t *testing.T) {
	sonarr := &fakeSonarr{}
	sonarrSrv := httptest.NewServer(sonarr.handler())
	defer sonarrSrv.Close()
	radarrSrv := httptest.NewServer(fakeRadarrHandler("/data/movies/Movie/Movie.mp4"))
	defer radarrSrv.Close()

	s := newTestServer(t)
	tvFile := mustUpsert(t, s.st, &store.File{Path: "/srv/media/tvshows/Show/Show S01E01.mkv", Library: "tvshows", Title: "Show"})
	movieFile := mustUpsert(t, s.st, &store.File{Path: "/srv/media/movies/Movie/Movie.mp4", Library: "movies", Title: "Movie"})

	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{
			{ID: "sonarr", Name: "Sonarr", Kind: "sonarr", URL: sonarrSrv.URL, APIKey: "k", PathMap: "/data=/srv/media"},
			{ID: "radarr", Name: "Radarr", Kind: "radarr", URL: radarrSrv.URL, APIKey: "k", PathMap: "/data=/srv/media"},
		}
	}); err != nil {
		t.Fatal(err)
	}

	if err := s.SyncArr(context.Background()); err != nil {
		t.Fatal(err)
	}

	tvItem, err := s.st.ArrItemByFileID(tvFile.ID)
	if err != nil || tvItem == nil {
		t.Fatalf("expected a synced arr_items row for the tv file, got %v, %v", tvItem, err)
	}
	if tvItem.InstanceID != "sonarr" || tvItem.ItemID != 1 || tvItem.FileRecID != 10 ||
		!tvItem.CutoffNotMet || tvItem.CFScore != 5 || tvItem.SceneName != "Show.S01E01" {
		t.Errorf("tv item = %+v", tvItem)
	}
	if got := tvItem.TagIDs(); len(got) != 1 || got[0] != 3 {
		t.Errorf("tag ids = %v, want [3]", got)
	}
	tagNames := s.st.ArrTags("sonarr")
	if tagNames[3] != "keep-quality" {
		t.Errorf("cached tag names = %v", tagNames)
	}

	movieItem, err := s.st.ArrItemByFileID(movieFile.ID)
	if err != nil || movieItem == nil {
		t.Fatalf("expected a synced arr_items row for the movie file, got %v, %v", movieItem, err)
	}
	if movieItem.InstanceID != "radarr" || movieItem.Kind != "radarr" || movieItem.FileRecID != 20 {
		t.Errorf("movie item = %+v", movieItem)
	}

	// Now simulate the episode file being deleted in Sonarr: the next
	// sync must remove the stale arr_items row.
	sonarr.removed = true
	if err := s.SyncArr(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got, err := s.st.ArrItemByFileID(tvFile.ID); err != nil || got != nil {
		t.Errorf("expected the tv item's row to be removed after Sonarr stopped reporting it, got %+v", got)
	}
	// The movie, still reported by Radarr, must be untouched.
	if got, err := s.st.ArrItemByFileID(movieFile.ID); err != nil || got == nil {
		t.Errorf("radarr's item should survive a sonarr-only resync, got %v, %v", got, err)
	}
}

// Two instances whose path maps both resolve to the same local file: the
// first one in config order keeps it, the second is recorded as a
// conflict and does not overwrite the row.
func TestSyncArrConflictFirstInstanceWins(t *testing.T) {
	radarrA := httptest.NewServer(fakeRadarrHandler("/data/movies/Movie/Movie.mp4"))
	defer radarrA.Close()
	radarrB := httptest.NewServer(fakeRadarrHandler("/data2/movies/Movie/Movie.mp4"))
	defer radarrB.Close()

	s := newTestServer(t)
	movieFile := mustUpsert(t, s.st, &store.File{Path: "/srv/media/movies/Movie/Movie.mp4", Library: "movies", Title: "Movie"})

	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{
			{ID: "radarr-a", Name: "Radarr A", Kind: "radarr", URL: radarrA.URL, APIKey: "k", PathMap: "/data=/srv/media"},
			{ID: "radarr-b", Name: "Radarr B", Kind: "radarr", URL: radarrB.URL, APIKey: "k", PathMap: "/data2=/srv/media"},
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncArr(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := s.st.ArrItemByFileID(movieFile.ID)
	if err != nil || got == nil {
		t.Fatalf("expected exactly one owner, got %v, %v", got, err)
	}
	if got.InstanceID != "radarr-a" {
		t.Errorf("owner = %q, want radarr-a (first in config order)", got.InstanceID)
	}
	// radarr-b must not have removed radarr-a's claim by "cleaning up"
	// what it thinks are its own stale rows.
	stillThere, _ := s.st.ArrItemByFileID(movieFile.ID)
	if stillThere == nil {
		t.Error("the conflict-losing instance's cleanup must not delete the winner's row")
	}
}

// A disabled instance is skipped entirely by a sync pass.
func TestSyncArrSkipsDisabledInstance(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	}))
	defer srv.Close()

	s := newTestServer(t)
	off := false
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{
			{ID: "radarr", Name: "Radarr", Kind: "radarr", URL: srv.URL, APIKey: "k", Enabled: &off},
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.SyncArr(context.Background()); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Error("a disabled instance must not be contacted during sync")
	}
}
