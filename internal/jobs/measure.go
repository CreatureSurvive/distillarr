package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mediatrans/internal/hwprobe"
	"mediatrans/internal/media"
	"mediatrans/internal/recs"
	"mediatrans/internal/store"
	"mediatrans/internal/tune"
)

// EvMeasure is the SSE event for overnight measuring.
const EvMeasure = "measure"

// MeasureStatus is what the UI shows about overnight measuring.
type MeasureStatus struct {
	Enabled   bool   `json:"enabled"`
	WindowOpen bool  `json:"window_open"`
	Running   bool   `json:"running"`
	Current   string `json:"current,omitempty"`
	Note      string `json:"note,omitempty"`
	Measured  int    `json:"measured"`
	Remaining int    `json:"remaining"`
	Tonight   int    `json:"done_this_window"`
	LastError string `json:"last_error,omitempty"`
}

type measurer struct {
	mu     sync.Mutex
	status MeasureStatus
	cancel context.CancelFunc
}

// measureSlack widens the candidate set below the savings threshold:
// estimates there can flip to "worth it" once measured.
const measureSlack = 15

func (e *Engine) measureMin() float64 {
	return float64(e.cfg.Get().MinSavingsPct - measureSlack)
}

// MeasureStatus returns a snapshot for the API.
func (e *Engine) MeasureStatus() MeasureStatus {
	e.ms.mu.Lock()
	st := e.ms.status
	e.ms.mu.Unlock()
	cfg := e.cfg.Get()
	st.Enabled = cfg.Measure()
	st.WindowOpen = e.cfg.MeasureOpen(time.Now())
	st.Measured, st.Remaining = e.st.MeasureProgress(e.measureMin())
	return st
}

func (e *Engine) setMeasure(fn func(*MeasureStatus)) {
	e.ms.mu.Lock()
	fn(&e.ms.status)
	st := e.ms.status
	e.ms.mu.Unlock()
	e.notify(EvMeasure, st)
}

// StopMeasuring aborts the current measurement (encode work arrived,
// window closed, or the user turned it off).
func (e *Engine) stopMeasuring() {
	e.ms.mu.Lock()
	if e.ms.cancel != nil {
		e.ms.cancel()
	}
	e.ms.mu.Unlock()
}

// measureIdle: measuring only runs when nothing else wants the GPU.
func (e *Engine) measureIdle() bool {
	if e.active.Load() > 0 || (e.scan != nil && e.scan.Running()) {
		return false
	}
	if e.windowOpen.Load() {
		if c, _ := e.st.CountJobsByStatus(); c[store.StatusQueued] > 0 {
			return false
		}
	}
	return true
}

// measureLoop runs VMAF quality searches on candidates inside the
// measuring windows, one file at a time.
func (e *Engine) measureLoop() {
	defer e.wg.Done()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	wasOpen := false
	for {
		select {
		case <-e.stopCh:
			return
		case <-t.C:
		}
		open := e.cfg.MeasureOpen(time.Now()) && media.VMAFAvailable()
		if open && !wasOpen {
			e.setMeasure(func(s *MeasureStatus) { s.Tonight = 0 })
		}
		wasOpen = open
		if !open || !e.measureIdle() {
			continue
		}
		files, err := e.st.MeasureCandidates(e.measureMin(), 1)
		if err != nil || len(files) == 0 {
			continue
		}
		e.measureOne(files[0])
	}
}

func (e *Engine) measureOne(f *store.File) {
	cfg := e.cfg.Get()
	rec := recs.Recommend(f, cfg)
	s := rec.Settings
	if s.VMAFTarget <= 0 {
		return
	}
	s.Backend = e.ResolveFor(string(s.Backend), s.Codec)
	if rep := e.Report(); rep != nil {
		s.RenderNode = hwprobe.NodeFor(rep, s.Backend, s.Codec)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.ms.mu.Lock()
	e.ms.cancel = cancel
	e.ms.mu.Unlock()
	defer func() {
		cancel()
		e.ms.mu.Lock()
		e.ms.cancel = nil
		e.ms.mu.Unlock()
	}()
	// Watchdog: yield the GPU as soon as real work shows up.
	go func() {
		tk := time.NewTicker(5 * time.Second)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				if !e.cfg.MeasureOpen(time.Now()) || !e.measureIdle() {
					cancel()
					return
				}
			}
		}
	}()

	name := f.Title
	if f.Library == "tvshows" {
		name = fmt.Sprintf("%s S%02dE%02d", f.Title, f.Season, f.Episode)
	}
	e.setMeasure(func(m *MeasureStatus) { m.Running, m.Current, m.Note, m.LastError = true, name, "probing", "" })
	defer e.setMeasure(func(m *MeasureStatus) { m.Running, m.Current, m.Note = false, "", "" })

	src, err := media.ProbeFile(ctx, f.Path)
	if err != nil {
		e.measureFailed(f, name, err)
		return
	}
	dir := filepath.Join("/config/tune", fmt.Sprintf("measure-%d", f.ID))
	defer os.RemoveAll(dir)
	res, _, err := tune.Search(ctx, tune.Options{
		Settings: s, Probe: src, Target: s.VMAFTarget, WorkDir: dir,
		Acquire:  e.AcquireSem,
		Progress: func(msg string) { e.setMeasure(func(m *MeasureStatus) { m.Note = msg }) },
	})
	if err != nil {
		if ctx.Err() != nil {
			log.Printf("measure: %s: interrupted (queue busy or window closed)", name)
			return // not a failure — picked up again next time
		}
		e.measureFailed(f, name, err)
		return
	}
	b, _ := json.Marshal(recs.TuneRecord{Target: s.VMAFTarget, Codec: string(s.Codec), Backend: string(s.Backend),
		Crop: s.Crop, MaxHeight: s.MaxHeight, Quality: res.Quality, Ratio: res.Ratio, VMAF: res.VMAF,
		Met: res.Met, At: time.Now().UTC().Format(time.RFC3339)})
	_ = e.st.SetTune(f.ID, string(b))
	if res.Ratio > 0 {
		recs.RecordObservation(f, s, res.Ratio)
	}
	log.Printf("measure: %s: q%d VMAF %.1f, %.0f%% of source video", name, res.Quality, res.VMAF.Mean, res.Ratio*100)
	e.setMeasure(func(m *MeasureStatus) { m.Tonight++ })
	if e.scan != nil {
		e.scan.RefreshRecsSoon()
	}
}

// measureFailed marks the file so it isn't retried every minute.
func (e *Engine) measureFailed(f *store.File, name string, err error) {
	log.Printf("measure: %s: %v", name, err)
	b, _ := json.Marshal(map[string]any{"failed": err.Error(), "at": time.Now().UTC().Format(time.RFC3339)})
	_ = e.st.SetTune(f.ID, string(b))
	e.setMeasure(func(m *MeasureStatus) { m.LastError = name + ": " + err.Error() })
}
