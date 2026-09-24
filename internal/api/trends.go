// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"log"
	"net/http"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// statFS is overridable in tests: free bytes and device id of path's
// filesystem.
var statFS = func(path string) (free int64, dev uint64, ok bool) {
	var fs syscall.Statfs_t
	var st syscall.Stat_t
	if syscall.Statfs(path, &fs) != nil || syscall.Stat(path, &st) != nil {
		return 0, 0, false
	}
	return int64(fs.Bavail) * int64(fs.Bsize), uint64(st.Dev), true
}

// freeByFS reports free bytes per filesystem the libraries live on,
// keyed by the library names sharing it ("movies, tvshows").
func freeByFS(libs []config.Library) map[string]int64 {
	type fsInfo struct {
		free  int64
		names []string
	}
	byDev := map[uint64]*fsInfo{}
	var order []uint64
	for _, l := range libs {
		free, dev, ok := statFS(l.Path)
		if !ok {
			continue
		}
		if byDev[dev] == nil {
			byDev[dev] = &fsInfo{free: free}
			order = append(order, dev)
		}
		byDev[dev].names = append(byDev[dev].names, l.Name)
	}
	out := map[string]int64{}
	for _, d := range order {
		out[strings.Join(byDev[d].names, ", ")] = byDev[d].free
	}
	return out
}

// backfillTrends turns per-day savings into cumulative rows for every
// day before today that had savings (the first-run history backfill).
func backfillTrends(savedByDay map[string]int64, today string) []store.TrendSnapshot {
	days := make([]string, 0, len(savedByDay))
	for d := range savedByDay {
		if d < today {
			days = append(days, d)
		}
	}
	sort.Strings(days)
	var cum int64
	out := make([]store.TrendSnapshot, 0, len(days))
	for _, d := range days {
		cum += savedByDay[d]
		out = append(out, store.TrendSnapshot{Day: d, SavedCumulative: cum})
	}
	return out
}

// snapshotTrend records today's snapshot unless it already exists.
func (s *Server) snapshotTrend(now time.Time) {
	today := now.Format("2006-01-02")
	if s.st.HasTrend(today) {
		return
	}
	if rows, err := s.st.ListTrends(); err == nil && len(rows) == 0 {
		byDay, err := s.st.SavedByDay(now.Location())
		if err == nil {
			for _, t := range backfillTrends(byDay, today) {
				_ = s.st.UpsertTrend(t)
			}
		}
	}
	codecs, err := s.st.LibraryBytesByCodec()
	if err != nil {
		log.Printf("trends: %v", err)
		return
	}
	var total int64
	for _, n := range codecs {
		total += n
	}
	saved, _, _ := s.st.RealizedSavings()
	if err := s.st.UpsertTrend(store.TrendSnapshot{Day: today, LibBytes: total, BytesByCodec: codecs,
		FreeBytesByFS: freeByFS(s.cfg.Get().Libraries), SavedCumulative: saved}); err != nil {
		log.Printf("trends: %v", err)
	}
}

// TrendLoop takes the daily snapshot at boot and then checks hourly.
func (s *Server) TrendLoop(stop <-chan struct{}) {
	s.snapshotTrend(time.Now())
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			s.snapshotTrend(now)
		}
	}
}

func (s *Server) trends(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.ListTrends()
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, rows)
}
