// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"strings"
	"testing"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

func TestBuildSummary(t *testing.T) {
	if buildSummary(store.JobSummary{}, 0) != nil {
		t.Error("an idle day must send nothing")
	}
	ev := buildSummary(store.JobSummary{Done: 4, Failed: 1, Saved: 3 << 30}, 7)
	if ev == nil || ev.Key != "nightly_summary" {
		t.Fatalf("got %+v", ev)
	}
	for _, want := range []string{"4 job(s) done", "3.0 GB saved", "1 failed", "7 waiting"} {
		if !strings.Contains(ev.Body, want) {
			t.Errorf("body %q lacks %q", ev.Body, want)
		}
	}
}

func TestSummaryDue(t *testing.T) {
	all := 0x7f
	// Two windows: 01:00-03:00 and 22:00-06:00 (wraps). 03:00 isn't the
	// day's last close, because the window reopens at 22:00.
	scheds := []config.Schedule{{Days: all, Start: 60, End: 180}, {Days: all, Start: 22 * 60, End: 6 * 60}}
	at := func(h, m int) time.Time { return time.Date(2026, 9, 24, h, m, 0, 0, time.Local) }
	if summaryDue(scheds, at(3, 0)) {
		t.Error("03:00 close isn't the last: 22:00 reopens")
	}
	single := []config.Schedule{{Days: all, Start: 60, End: 360}}
	if !summaryDue(single, at(6, 0)) || summaryDue(single, at(6, 1)) || summaryDue(single, at(5, 59)) {
		t.Error("single 01:00-06:00 window: due exactly at 06:00")
	}
	if !summaryDue(nil, at(8, 0)) || summaryDue(nil, at(9, 0)) {
		t.Error("no schedules: due at 08:00")
	}
}

func TestUpgradeLoopDetection(t *testing.T) {
	s := newTestServer(t)
	old := mustUpsert(t, s.st, &store.File{Path: "/m/Movie (2020) WEB-1080p.mp4", Library: "movies", Title: "Movie"})
	j := &store.Job{FileID: old.ID, SrcPath: old.Path, Priority: 100000, Backend: "sw", Codec: "hevc", MaxAttempts: 1}
	if err := s.st.CreateJob(j); err != nil {
		t.Fatal(err)
	}
	if err := s.st.FinishJob(j.ID, store.StatusDone, 100, "", ""); err != nil {
		t.Fatal(err)
	}
	newer := mustUpsert(t, s.st, &store.File{Path: "/m/Movie (2020) Bluray-1080p.mkv", Library: "movies", Title: "Movie"})
	inst := config.ArrInstance{ID: "radarr", Name: "Radarr", Kind: "radarr"}

	if !s.checkUpgradeLoop(inst, newer, []string{old.Path}, time.Now()) {
		t.Fatal("a re-encode finished just now, then replaced by a download: loop expected")
	}
	if _, ok, _ := s.st.KVGet(upgradeLoopKey(newer.ID)); !ok {
		t.Error("the new file must be put on the skip list")
	}

	// Outside the window: no loop.
	other := mustUpsert(t, s.st, &store.File{Path: "/m/Other.mkv", Library: "movies", Title: "Other"})
	if s.checkUpgradeLoop(inst, other, []string{old.Path}, time.Now().Add(15*24*time.Hour)) {
		t.Error("a re-encode 15 days before the download is outside the default 14-day window")
	}
	// A file never re-encoded: no loop.
	if s.checkUpgradeLoop(inst, other, nil, time.Now()) {
		t.Error("no re-encode history: no loop")
	}
}
