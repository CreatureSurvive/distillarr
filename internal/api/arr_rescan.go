package api

import (
	"context"
	"fmt"
	"log"
	"time"

	"mediatrans/internal/arr"
	"mediatrans/internal/config"
	"mediatrans/internal/jobs"
	"mediatrans/internal/pathmap"
)

// Polling tunables for arrPollNewPath, overridable in tests so they don't
// need to run for real minutes. Backoff doubles from arrPollInitial up
// to arrPollMax, for a total wall time of arrPollTotal.
var (
	arrPollTotal   = 10 * time.Minute
	arrPollInitial = 5 * time.Second
	arrPollMax     = 60 * time.Second
)

// ArrRescanSubscriber returns a jobs.OnFinished subscriber: after a
// successful encode/remux/upscale-replace, tell the owning Sonarr/Radarr
// instance to rescan that series/movie, so it notices the change instead
// of waiting for its own schedule. A copy-mode upscale also gets a
// rescan (there's a new file to notice) but is never polled below, since
// nothing was replaced. When the container extension changed
// (OldPath != NewPath), also poll for confirmation that the instance
// now reports the new path, recording a warning if it never does within
// arrPollTotal.
func (s *Server) ArrRescanSubscriber() func(jobs.ReplacedEvent) {
	return func(ev jobs.ReplacedEvent) {
		switch ev.Kind {
		case "encode", "remux", "upscale-replace", "upscale-copy":
		default:
			return
		}
		item, err := s.st.ArrItemByFileID(ev.FileID)
		if err != nil || item == nil {
			return
		}
		inst := s.arrInstanceByID(item.InstanceID)
		if inst == nil || !inst.RescanAfterReplaceOn() {
			return
		}

		cl := arr.New(inst.URL, inst.APIKey, arrKind(inst.Kind))
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var rescanErr error
		if arrKind(inst.Kind) == arr.Sonarr {
			rescanErr = cl.RescanSeries(ctx, item.ItemID)
		} else {
			rescanErr = cl.RescanMovie(ctx, item.ItemID)
		}
		if rescanErr != nil {
			log.Printf("arr rescan %s (item %d): %v", inst.ID, item.ItemID, rescanErr)
			return
		}

		if ev.Kind == "upscale-copy" || ev.OldPath == ev.NewPath {
			return // nothing to confirm: a copy beside the source, or the path didn't change
		}
		pm, _ := pathmap.Parse(inst.PathMap)
		wantRemote := pm.ToRemote(ev.NewPath)
		go s.arrPollNewPath(*inst, item.ItemID, wantRemote)
	}
}

func (s *Server) arrInstanceByID(id string) *config.ArrInstance {
	for _, x := range s.cfg.Get().ArrInstances {
		if x.ID == id {
			x := x
			return &x
		}
	}
	return nil
}

// arrPollNewPath waits, with backoff, for the instance to report
// wantRemote for itemID, and records a warning (kv + SSE) if it times
// out. Never returns an error — this runs in its own goroutine after the
// job that triggered it has already finished.
func (s *Server) arrPollNewPath(inst config.ArrInstance, itemID int64, wantRemote string) {
	cl := arr.New(inst.URL, inst.APIKey, arrKind(inst.Kind))
	deadline := time.Now().Add(arrPollTotal)
	delay := arrPollInitial
	for time.Now().Before(deadline) {
		time.Sleep(delay)
		if delay < arrPollMax {
			delay *= 2
			if delay > arrPollMax {
				delay = arrPollMax
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		found := arrReportsPath(ctx, cl, arrKind(inst.Kind), itemID, wantRemote)
		cancel()
		if found {
			return
		}
	}
	msg := fmt.Sprintf("%s didn't pick up the new path within %s", inst.Name, arrPollTotal)
	_ = s.st.KVSet("arr_warning_"+inst.ID, msg)
	s.hub.Broadcast("arr", map[string]any{"instance": inst.ID, "warning": msg})
	log.Printf("arr rescan: %s", msg)
}

// arrReportsPath checks whether the instance currently reports itemID's
// file at wantRemote. Sonarr's episode files are scoped per series
// (cheap to re-fetch); Radarr's single-movie GET is used instead of the
// full movie list for the same reason.
func arrReportsPath(ctx context.Context, cl *arr.Client, kind arr.Kind, itemID int64, wantRemote string) bool {
	if kind == arr.Sonarr {
		files, err := cl.EpisodeFiles(ctx, itemID)
		if err != nil {
			return false
		}
		for _, f := range files {
			if f.Path == wantRemote {
				return true
			}
		}
		return false
	}
	m, err := cl.Movie(ctx, itemID)
	if err != nil || m == nil || m.MovieFile == nil {
		return false
	}
	return m.MovieFile.Path == wantRemote
}
