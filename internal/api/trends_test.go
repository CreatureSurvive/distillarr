package api

import (
	"testing"
	"time"

	"mediatrans/internal/config"
	"mediatrans/internal/store"
)

func TestBackfillTrends(t *testing.T) {
	got := backfillTrends(map[string]int64{"2026-09-01": 10, "2026-08-30": 5, "2026-09-24": 99}, "2026-09-24")
	if len(got) != 2 || got[0].Day != "2026-08-30" || got[0].SavedCumulative != 5 || got[1].SavedCumulative != 15 {
		t.Errorf("got %+v (today must be left to the live snapshot)", got)
	}
}

func TestSnapshotTrend(t *testing.T) {
	old := statFS
	statFS = func(path string) (int64, uint64, bool) {
		if path == "/a" || path == "/b" {
			return 1000, 7, true // same filesystem
		}
		return 50, 9, true
	}
	t.Cleanup(func() { statFS = old })

	s := newTestServer(t)
	_ = s.cfg.Update(func(c *config.Config) {
		c.Libraries = []config.Library{{Name: "movies", Path: "/a"}, {Name: "tv", Path: "/b"}, {Name: "x", Path: "/c"}}
	})
	mustUpsert(t, s.st, &store.File{Path: "/a/1.mkv", Library: "movies", Title: "1", Size: 300, VideoCodec: "h264"})
	mustUpsert(t, s.st, &store.File{Path: "/a/2.mkv", Library: "movies", Title: "2", Size: 200, VideoCodec: "hevc"})

	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.Local)
	s.snapshotTrend(now)
	s.snapshotTrend(now) // idempotent per day
	rows, err := s.st.ListTrends()
	if err != nil || len(rows) != 1 {
		t.Fatalf("rows %+v err %v", rows, err)
	}
	r := rows[0]
	if r.Day != "2026-09-24" || r.LibBytes != 500 || r.BytesByCodec["h264"] != 300 || r.BytesByCodec["hevc"] != 200 {
		t.Errorf("snapshot %+v", r)
	}
	if r.FreeBytesByFS["movies, tv"] != 1000 || r.FreeBytesByFS["x"] != 50 || len(r.FreeBytesByFS) != 2 {
		t.Errorf("free by fs %+v", r.FreeBytesByFS)
	}
}
