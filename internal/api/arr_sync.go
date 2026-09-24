package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/arr"
	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/pathmap"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

var arrSyncing atomic.Bool

// TriggerArrSync starts a sync of every enabled instance in the
// background if one isn't already running. Used by the manual endpoint,
// the post-scan hook (NewServer), and main.go's hourly ticker, so they
// all share the same single-flight guard.
func (s *Server) TriggerArrSync() bool {
	if !arrSyncing.CompareAndSwap(false, true) {
		return false
	}
	go func() {
		defer arrSyncing.Store(false)
		if err := s.SyncArr(context.Background()); err != nil {
			log.Printf("arr sync: %v", err)
		}
	}()
	return true
}

// POST /api/v1/arr/sync — trigger a sync of every enabled instance now.
func (s *Server) arrSyncNow(w http.ResponseWriter, r *http.Request) {
	if !s.TriggerArrSync() {
		writeJSON(w, http.StatusOK, map[string]any{"started": false, "syncing": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"started": true})
}

// SyncArr pulls every enabled instance's series/episode files or
// movies+files, resolves each to a local file, and updates arr_items.
// Instances are processed in config order: a file already claimed by an
// earlier instance this pass is left alone and counted as a conflict on
// the later one, rather than being reassigned. An instance whose own
// fetch fails is skipped entirely (its existing rows are left as they
// were, not wiped) so a transient network error can't look like every
// one of its files stopped being managed.
func (s *Server) SyncArr(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	cfg := s.cfg.Get()

	claimed := map[int64]string{} // file_id -> instance id, this pass only
	conflicts := map[string]int{}
	synced := 0

	for _, inst := range cfg.ArrInstances {
		if !inst.On() {
			continue
		}
		n, err := s.syncOneArrInstance(ctx, inst, claimed, conflicts)
		if err != nil {
			log.Printf("arr sync %s (%s): %v", inst.Name, inst.ID, err)
			s.hub.Broadcast("arr", map[string]any{"instance": inst.ID, "error": arr.FriendlyError(err)})
			continue
		}
		synced += n
		s.hub.Broadcast("arr", map[string]any{"instance": inst.ID, "synced": n})
	}
	if data, err := json.Marshal(conflicts); err == nil {
		_ = s.st.KVSet("arr_conflicts", string(data))
	}
	_ = s.st.KVSet("arr_last_sync", time.Now().UTC().Format(time.RFC3339))
	s.scan.RefreshRecsSoon() // upgrade-pending/tag policy reads arr_items
	s.hub.Broadcast("arr", map[string]any{"done": true, "synced": synced})
	return nil
}

// syncOneArrInstance syncs a single instance and returns how many of its
// files were matched to something local.
func (s *Server) syncOneArrInstance(ctx context.Context, inst config.ArrInstance, claimed map[int64]string, conflicts map[string]int) (int, error) {
	cl := arr.New(inst.URL, inst.APIKey, arrKind(inst.Kind))
	pm, _ := pathmap.Parse(inst.PathMap)

	claim := func(remotePath string) (fileID int64, ok bool) {
		f, err := s.st.GetFileByPath(pm.ToLocal(remotePath))
		if err != nil || f == nil {
			return 0, false
		}
		if owner, taken := claimed[f.ID]; taken && owner != inst.ID {
			conflicts[inst.ID]++
			return 0, false
		}
		claimed[f.ID] = inst.ID
		return f.ID, true
	}

	var items []store.ArrItem
	switch arrKind(inst.Kind) {
	case arr.Sonarr:
		seriesList, err := cl.Series(ctx)
		if err != nil {
			return 0, err
		}
		for _, sr := range seriesList {
			epFiles, err := cl.EpisodeFiles(ctx, sr.ID)
			if err != nil {
				log.Printf("arr sync %s: episode files for series %d: %v", inst.ID, sr.ID, err)
				continue
			}
			for _, ef := range epFiles {
				fid, ok := claim(ef.Path)
				if !ok {
					continue
				}
				items = append(items, store.ArrItem{
					FileID: fid, InstanceID: inst.ID, Kind: "sonarr",
					ItemID: sr.ID, FileRecID: ef.ID, Monitored: sr.Monitored,
					Tags: store.JoinTagIDs(sr.Tags), QualityProfileID: sr.QualityProfileID,
					CutoffNotMet: ef.QualityCutoffNotMet, CFScore: ef.CustomFormatScore,
					OriginalLanguage: sr.OriginalLanguage.Name, SeriesStatus: sr.Status,
					SceneName: ef.SceneName,
				})
			}
		}
	case arr.Radarr:
		movies, err := cl.Movies(ctx)
		if err != nil {
			return 0, err
		}
		for _, m := range movies {
			if m.MovieFile == nil {
				continue
			}
			fid, ok := claim(m.MovieFile.Path)
			if !ok {
				continue
			}
			items = append(items, store.ArrItem{
				FileID: fid, InstanceID: inst.ID, Kind: "radarr",
				ItemID: m.ID, FileRecID: m.MovieFile.ID, Monitored: m.Monitored,
				Tags: store.JoinTagIDs(m.Tags), QualityProfileID: m.QualityProfileID,
				CutoffNotMet: m.MovieFile.QualityCutoffNotMet, CFScore: m.MovieFile.CustomFormatScore,
				OriginalLanguage: m.OriginalLanguage.Name, SeriesStatus: m.Status,
				SceneName: m.MovieFile.SceneName,
			})
		}
	}

	if err := s.st.UpsertArrItems(items); err != nil {
		return 0, err
	}
	keep := make([]int64, 0, len(items))
	for _, it := range items {
		keep = append(keep, it.FileID)
	}
	if err := s.st.DeleteArrItemsForInstanceExcept(inst.ID, keep); err != nil {
		return len(items), err
	}

	if tags, err := cl.Tags(ctx); err == nil {
		m := make(map[int64]string, len(tags))
		for _, t := range tags {
			m[t.ID] = t.Label
		}
		_ = s.st.SetArrTags(inst.ID, m)
	}

	// Codec-penalty check: read-only against this instance's own
	// setup, never assuming any particular quality-profile or naming
	// convention. A fetch failure here just means a stale (or no)
	// report until the next sync — it must never fail the sync itself.
	if cfs, err := cl.CustomFormats(ctx); err == nil {
		if profiles, err := cl.QualityProfiles(ctx); err == nil {
			naming, _ := cl.GetNaming(ctx) // nil is fine; AnalyzePenalties handles it
			report := arr.AnalyzePenalties(cfs, profiles, naming)
			if b, err := json.Marshal(report); err == nil {
				_ = s.st.KVSet("arr_penalties_"+inst.ID, string(b))
			}
		}
	}

	return len(items), nil
}
