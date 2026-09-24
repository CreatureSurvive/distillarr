// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

func TestImportJellystatIdempotentAndPlayStats(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"pages":1,"results":[{"results":[
			{"NowPlayingItemId":"aaaa-bbbb","EpisodeId":null,"PlayMethod":"Transcode","ActivityDateInserted":"2026-09-01T20:10:00Z","TranscodingInfo":{"TranscodeReasons":["ContainerNotSupported","AudioCodecNotSupported"]}},
			{"NowPlayingItemId":"aaaabbbb","EpisodeId":null,"PlayMethod":"DirectPlay","ActivityDateInserted":"2026-09-05T20:10:00Z","TranscodingInfo":null},
			{"NowPlayingItemId":"unknown","EpisodeId":null,"PlayMethod":"DirectPlay","ActivityDateInserted":"2026-09-05T20:10:00Z"}
		]}]}`)
	}))
	defer srv.Close()

	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A"})
	if err := s.st.UpsertJF([]store.JellyfinRow{{Path: f.Path, ItemID: "AAAABBBB"}}); err != nil {
		t.Fatal(err)
	}
	_ = s.cfg.Update(func(c *config.Config) { c.JellystatURL, c.JellystatKey = srv.URL, "k" })

	for i := 0; i < 2; i++ { // twice: the second run must not add rows
		n, err := s.ImportJellystat(context.Background(), time.Time{})
		if err != nil || n != 2 {
			t.Fatalf("run %d: n=%d err=%v", i, n, err)
		}
	}
	ps := s.st.PlayStatFor(f.ID)
	if ps.Plays != 2 || ps.LastPlayed.Format("2006-01-02") != "2026-09-05" {
		t.Errorf("play stat %+v", ps)
	}
	if forces, _ := s.st.ForcesTranscode(f.ID, 100000); !forces {
		t.Error("the transcoded session should count for forces_transcode")
	}

	// Plex's own count adds only what the poller didn't already see.
	if err := s.st.UpsertPlex([]store.PlexRow{{FileID: f.ID, RatingKey: "1", SectionID: "1", ViewCount: 3, LastViewedAt: time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC).Unix()}}); err != nil {
		t.Fatal(err)
	}
	all, err := s.st.PlayStats()
	if err != nil {
		t.Fatal(err)
	}
	if got := all[f.ID]; got.Plays != 5 || got.LastPlayed.Format("2006-01-02") != "2026-09-20" {
		t.Errorf("combined %+v", got)
	}
	if one := s.st.PlayStatFor(f.ID); one.Plays != 5 {
		t.Errorf("PlayStatFor disagrees with PlayStats: %+v", one)
	}
}
