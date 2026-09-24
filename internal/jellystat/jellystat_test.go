// SPDX-License-Identifier: GPL-3.0-or-later

package jellystat

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHistoryPagesAndFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-token") != "k" {
			w.WriteHeader(403)
			return
		}
		page := r.URL.Query().Get("page")
		act := `{"NowPlayingItemId":"movie1","EpisodeId":null,"PlayMethod":"Transcode","ActivityDateInserted":"2026-09-01T20:00:00Z","TranscodingInfo":{"TranscodeReasons":["ContainerNotSupported"]}}`
		if page == "2" {
			act = `{"NowPlayingItemId":"series1","EpisodeId":"ep1","PlayMethod":"DirectPlay","ActivityDateInserted":"2026-09-02T20:00:00Z","TranscodingInfo":null}`
		}
		fmt.Fprintf(w, `{"pages":2,"results":[{"results":[%s]}]}`, act)
	}))
	defer srv.Close()

	var got []Activity
	if err := New(srv.URL, "k").History(context.Background(), time.Time{}, func(a []Activity) error { got = append(got, a...); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d", len(got))
	}
	if got[0].ItemID() != "movie1" || got[0].Direct() || len(got[0].Reasons()) != 1 {
		t.Errorf("movie activity %+v", got[0])
	}
	if got[1].ItemID() != "ep1" || !got[1].Direct() || got[1].Reasons() != nil {
		t.Errorf("episode activity %+v", got[1])
	}
	if _, err := New(srv.URL, "bad").Test(context.Background()); err == nil {
		t.Error("bad key accepted")
	}
}
