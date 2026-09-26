// SPDX-License-Identifier: GPL-3.0-or-later

package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/hwprobe"
	"github.com/CreatureSurvive/distillarr/internal/media"
	"github.com/CreatureSurvive/distillarr/internal/recs"
	"github.com/CreatureSurvive/distillarr/internal/store"
	"github.com/CreatureSurvive/distillarr/internal/tune"
)

// Tune-ahead: a quality search is CPU-bound (VMAF) and an encode is
// GPU-bound, so while one job encodes, the queued jobs behind it are
// measured in the order they will run. Results land in files.tune_json,
// where tuneQuality finds them, so a measured job goes straight to encoding.
// A job's own search always wins: the ahead search yields the CPU to it, and
// a job whose file is being measured ahead waits for that result instead of
// measuring twice.

// claimTune marks path as being measured. It returns a release func, or,
// when another search already holds path, a channel closed when it ends.
func (e *Engine) claimTune(path string) (release func(), busy <-chan struct{}) {
	e.tuneMu.Lock()
	defer e.tuneMu.Unlock()
	if ch, ok := e.tuning[path]; ok {
		return nil, ch
	}
	ch := make(chan struct{})
	e.tuning[path] = ch
	return func() {
		e.tuneMu.Lock()
		delete(e.tuning, path)
		e.tuneMu.Unlock()
		close(ch)
	}, nil
}

// preemptAhead cancels a running ahead search so a job's own search gets
// the CPU; the ahead worker picks that file up again later.
func (e *Engine) preemptAhead() {
	e.tuneMu.Lock()
	defer e.tuneMu.Unlock()
	if e.aheadCancel != nil {
		e.aheadCancel()
	}
}

func (e *Engine) tuneAheadLoop() {
	defer e.wg.Done()
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-e.stopCh:
			return
		case <-t.C:
		}
		e.tuneAheadOnce()
	}
}

// aheadSettings resolves a queued job's settings the way runJob will, or
// reports that the job has nothing to measure ahead.
func (e *Engine) aheadSettings(j *store.Job) (encode.Settings, bool) {
	var s encode.Settings
	if j.Backend == "ocr" || json.Unmarshal([]byte(j.SettingsJSON), &s) != nil {
		return s, false
	}
	if s.VMAFTarget <= 0 || s.VideoCopy || s.UpscaleTo > 0 {
		return s, false
	}
	s.Normalize()
	rep := e.Report()
	s.Backend = e.resolveBackend(rep, s, j.Backend)
	if rep != nil && s.Backend != encode.SW {
		s.RenderNode = hwprobe.NodeFor(rep, s.Backend, s.Codec)
	}
	return s, true
}

// tuneAheadOnce measures the first queued job, in dispatch order, that
// has no usable stored result. One search at a time: VMAF already uses
// every core.
func (e *Engine) tuneAheadOnce() {
	if !media.VMAFAvailable() || e.inRunTunes.Load() > 0 {
		return
	}
	if e.cfg.Get().DeferWhileTranscodingOn() && e.Transcoding() > 0 {
		return
	}
	queued, err := e.st.QueuedForTune(e.windowOpen.Load(), 200)
	if err != nil {
		return
	}
	for _, j := range queued {
		e.tuneMu.Lock()
		skip := e.aheadFailed[j.ID]
		e.tuneMu.Unlock()
		if skip {
			continue
		}
		s, ok := e.aheadSettings(j)
		if !ok {
			continue
		}
		f, _ := e.st.GetFileByPath(j.SrcPath)
		if f == nil || recs.TunedFor(f, s) != nil {
			continue
		}
		release, busy := e.claimTune(j.SrcPath)
		if busy != nil {
			continue
		}
		e.measureAhead(j, f, s)
		release()
		return
	}
}

func (e *Engine) measureAhead(j *store.Job, f *store.File, s encode.Settings) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.tuneMu.Lock()
	e.aheadCancel = cancel
	e.tuneMu.Unlock()
	defer func() {
		e.tuneMu.Lock()
		e.aheadCancel = nil
		e.tuneMu.Unlock()
	}()
	go func() {
		select {
		case <-e.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()
	// A job may have started its own search between the check and the claim.
	if e.inRunTunes.Load() > 0 {
		return
	}

	note := func(msg string) {
		e.notify(EvJob, map[string]any{"id": j.ID, "status": store.StatusQueued, "note": msg})
	}
	src, err := media.ProbeFile(ctx, j.SrcPath)
	if err != nil {
		e.markAheadFailed(j.ID)
		return
	}
	note(fmt.Sprintf("measuring quality ahead (target VMAF %.0f)", s.VMAFTarget))
	work := filepath.Join("/config/tune", fmt.Sprintf("ahead-%d", j.ID))
	res, _, err := tune.Search(ctx, tune.Options{
		Settings: s, Probe: src, Target: s.VMAFTarget, WorkDir: work,
		Acquire:  e.AcquireSem,
		Cambi:    recs.IsAnimation(f) || s.TonemapHDR,
		Nice:     true, // the running encode comes first
		Progress: func(msg string) { note("ahead: " + msg) },
	})
	_ = os.RemoveAll(work)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("jobs: %d: quality search ahead skipped: %v", j.ID, err)
			e.markAheadFailed(j.ID)
			note("")
		}
		return
	}
	rec := recs.TuneRecord{Target: s.VMAFTarget, Codec: string(s.Codec), Backend: string(s.Backend),
		Crop: s.Crop, MaxHeight: s.MaxHeight, Quality: res.Quality, Ratio: res.Ratio, VMAF: res.VMAF,
		Met: res.Met, At: time.Now().UTC().Format(time.RFC3339)}
	b, _ := json.Marshal(rec)
	_ = e.st.SetTune(f.ID, string(b))
	if res.Ratio > 0 {
		recs.RecordObservation(f, s, res.Ratio)
	}
	// Show the measured quality on the queued job; runJob sets it again
	// from the stored result when it starts.
	var raw map[string]any
	if json.Unmarshal([]byte(j.SettingsJSON), &raw) == nil {
		raw["quality"] = res.Quality
		nb, _ := json.Marshal(raw)
		_ = e.st.UpdateJobSettings(j.ID, res.Quality, string(nb))
	}
	log.Printf("jobs: %d: measured ahead: quality %d scores VMAF %.1f (p5 %.1f), %.0f%% of source video", j.ID,
		res.Quality, res.VMAF.Mean, res.VMAF.P5, res.Ratio*100)
	note(fmt.Sprintf("quality measured ahead: q%d", res.Quality))
	e.broadcastQueue()
}

// markAheadFailed stops the worker retrying a job every tick; the job's
// own run still searches (and reports) normally.
func (e *Engine) markAheadFailed(id int64) {
	e.tuneMu.Lock()
	e.aheadFailed[id] = true
	e.tuneMu.Unlock()
}
