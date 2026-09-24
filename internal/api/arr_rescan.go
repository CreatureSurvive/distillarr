// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/arr"
	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/jobs"
	"github.com/CreatureSurvive/distillarr/internal/notify"
	"github.com/CreatureSurvive/distillarr/internal/pathmap"
)

// Polling tunables for arrPollNewPath, overridable in tests so they don't
// need to run for real minutes. Backoff doubles from arrPollInitial up
// to arrPollMax, for a total wall time of arrPollTotal.
var (
	arrPollTotal   = 10 * time.Minute
	arrPollInitial = 5 * time.Second
	arrPollMax     = 60 * time.Second
	// arrSeasonDebounce batches Sonarr rescans by series: re-encoding a
	// whole season sends one RescanSeries call after the burst of
	// episode finishes settles, instead of one call per episode. Movies
	// (and Radarr generally) always rescan immediately — a movie's one
	// file is already "the whole item", nothing to wait for.
	arrSeasonDebounce = 2 * time.Minute
)

// arrPollRequest is one "confirm the instance picked up this new path"
// job, queued until its batch's rescan actually runs.
type arrPollRequest struct {
	inst       config.ArrInstance
	itemID     int64
	wantRemote string
}

// arrRescanBatcher coalesces per-series rescans: each ArrRescanSubscriber
// closure owns one, living for the process lifetime. schedule resets the
// debounce timer for key on every call, so a steady stream of episode
// finishes keeps pushing the rescan out until the stream stops.
type arrRescanBatcher struct {
	mu     sync.Mutex
	timers map[string]*time.Timer
	polls  map[string][]arrPollRequest
}

func newArrRescanBatcher() *arrRescanBatcher {
	return &arrRescanBatcher{timers: map[string]*time.Timer{}, polls: map[string][]arrPollRequest{}}
}

// schedule queues req (if non-nil) against key and (re)starts key's
// debounce timer; fire runs once the timer finally elapses, with every
// poll request accumulated under key across the whole burst.
func (b *arrRescanBatcher) schedule(key string, req *arrPollRequest, fire func([]arrPollRequest)) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if req != nil {
		b.polls[key] = append(b.polls[key], *req)
	}
	if t, ok := b.timers[key]; ok {
		t.Stop()
	}
	b.timers[key] = time.AfterFunc(arrSeasonDebounce, func() {
		b.mu.Lock()
		delete(b.timers, key)
		reqs := b.polls[key]
		delete(b.polls, key)
		b.mu.Unlock()
		fire(reqs)
	})
}

// ArrRescanSubscriber returns a jobs.OnFinished subscriber: after a
// successful encode/remux/upscale-replace, tell the owning Sonarr/Radarr
// instance to rescan that series/movie, so it notices the change instead
// of waiting for its own schedule. A copy-mode upscale also gets a
// rescan (there's a new file to notice) but is never polled below, since
// nothing was replaced. When the container extension changed
// (OldPath != NewPath), also poll for confirmation that the instance
// now reports the new path, recording a warning if it never does within
// arrPollTotal.
//
// Sonarr rescans are batched per series (see arrRescanBatcher): a whole
// season re-encoding fires one RescanSeries call once the burst of
// episode finishes settles, rather than hammering Sonarr once per
// episode. Radarr's item is already a single movie, so it always
// rescans immediately.
func (s *Server) ArrRescanSubscriber() func(jobs.ReplacedEvent) {
	batch := newArrRescanBatcher()
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

		var pending *arrPollRequest
		if ev.Kind != "upscale-copy" && ev.OldPath != ev.NewPath {
			pm, _ := pathmap.Parse(inst.PathMap)
			pending = &arrPollRequest{inst: *inst, itemID: item.ItemID, wantRemote: pm.ToRemote(ev.NewPath)}
		}

		rescanAndPoll := func(polls []arrPollRequest) {
			s.arrRescanNow(*inst, item.ItemID, polls)
		}

		if arrKind(inst.Kind) != arr.Sonarr {
			var polls []arrPollRequest
			if pending != nil {
				polls = []arrPollRequest{*pending}
			}
			rescanAndPoll(polls)
			return
		}

		key := inst.ID + ":" + strconv.FormatInt(item.ItemID, 10)
		batch.schedule(key, pending, rescanAndPoll)
	}
}

// arrRescanNow sends the RescanSeries/RescanMovie command and, on
// success, kicks off polling for every path confirmation queued against
// this batch.
func (s *Server) arrRescanNow(inst config.ArrInstance, itemID int64, polls []arrPollRequest) {
	cl := arr.New(inst.URL, inst.APIKey, arrKind(inst.Kind))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var rescanErr error
	if arrKind(inst.Kind) == arr.Sonarr {
		rescanErr = cl.RescanSeries(ctx, itemID)
	} else {
		rescanErr = cl.RescanMovie(ctx, itemID)
	}
	if rescanErr != nil {
		log.Printf("arr rescan %s (item %d): %v", inst.ID, itemID, rescanErr)
		return
	}
	for _, r := range polls {
		go s.arrPollNewPath(r.inst, r.itemID, r.wantRemote)
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
	s.Notify.Send(notify.Event{Key: notify.ArrPathNotUpdated, Level: notify.Warning, Title: msg,
		Body: "Expected path: " + wantRemote + ". A manual rescan in " + inst.Name + " usually fixes it.",
		Link: "#/settings/connections", Group: "paths not picked up by Sonarr/Radarr"})
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
