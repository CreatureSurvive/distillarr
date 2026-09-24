package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/plex"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// fakePlex serves one movie section (single Part) and one show section
// (an episode with two Parts, exercising the multi-Part case), reachable
// only through /library/sections/{key}/all with the expected type filter.
func fakePlex() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/library/sections":
			w.Write([]byte(`{"MediaContainer":{"Directory":[
				{"key":"1","type":"movie","title":"Movies","Location":[{"id":1,"path":"/data/movies"}]},
				{"key":"2","type":"show","title":"TV Shows","Location":[{"id":2,"path":"/data/tvshows"}]}
			]}}`))
		case r.URL.Path == "/library/sections/1/all":
			w.Write([]byte(`{"MediaContainer":{"totalSize":1,"Metadata":[
				{"ratingKey":"100","addedAt":1700000000,"Media":[{"Part":[{"file":"/data/movies/Movie/Movie.mp4"}]}]}
			]}}`))
		case r.URL.Path == "/library/sections/2/all":
			if r.URL.Query().Get("type") != "4" {
				http.Error(w, "want type=4", 400)
				return
			}
			w.Write([]byte(`{"MediaContainer":{"totalSize":1,"Metadata":[
				{"ratingKey":"200","addedAt":1700000001,"Media":[{"Part":[
					{"file":"/data/tvshows/Show/Show S01E01.mkv"},
					{"file":"/data/tvshows/Show/Show S01E01.cd2.mkv"}
				]}]}
			]}}`))
		default:
			w.WriteHeader(404)
		}
	}
}

func TestSyncPlexMapsPartsToFiles(t *testing.T) {
	srv := httptest.NewServer(fakePlex())
	defer srv.Close()

	s := newTestServer(t)
	movieFile := mustUpsert(t, s.st, &store.File{Path: "/srv/media/movies/Movie/Movie.mp4", Library: "movies", Title: "Movie"})
	epFile := mustUpsert(t, s.st, &store.File{Path: "/srv/media/tvshows/Show/Show S01E01.mkv", Library: "tvshows", Title: "Show"})
	// The second Part's file isn't a known file (e.g. not yet scanned):
	// SyncPlex must skip it rather than fail the whole sync.

	if err := s.cfg.Update(func(c *config.Config) {
		c.PlexURL, c.PlexToken, c.PlexPathMap = srv.URL, "tok", "/data=/srv/media"
	}); err != nil {
		t.Fatal(err)
	}

	n, err := s.SyncPlex(context.Background(), plex.New(srv.URL, "tok"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("synced %d rows, want 2", n)
	}

	row, err := s.st.PlexByFileID(movieFile.ID)
	if err != nil || row == nil {
		t.Fatalf("expected a plex_items row for the movie, got %v, %v", row, err)
	}
	if row.RatingKey != "100" || row.SectionID != "1" || row.AddedAt != 1700000000 || row.ItemType != 1 {
		t.Errorf("movie row = %+v", row)
	}

	epRow, err := s.st.PlexByFileID(epFile.ID)
	if err != nil || epRow == nil {
		t.Fatalf("expected a plex_items row for the episode, got %v, %v", epRow, err)
	}
	if epRow.RatingKey != "200" || epRow.SectionID != "2" || epRow.ItemType != 4 {
		t.Errorf("episode row = %+v", epRow)
	}

	if v, ok, _ := s.st.KVGet("plex_last_sync"); !ok || v == "" {
		t.Error("expected plex_last_sync to be recorded")
	}
}
