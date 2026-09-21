// Package jobs runs the transcode queue: dispatcher, workers, hw
// semaphores, progress, decode-fallback retry, verification and
// in-place replacement.
package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mediatrans/internal/config"
	"mediatrans/internal/encode"
	"mediatrans/internal/hwprobe"
	"mediatrans/internal/media"
	"mediatrans/internal/recs"
	"mediatrans/internal/replace"
	"mediatrans/internal/scan"
	"mediatrans/internal/store"
	"mediatrans/internal/tune"
)

// Event names broadcast over SSE.
const (
	EvJob      = "job"
	EvProgress = "progress"
	EvQueue    = "queue"
	EvHW       = "hardware"
)

// Engine owns the queue runtime.
type Engine struct {
	st   *store.Store
	cfg  *config.Manager
	scan *scan.Scanner

	// Notify broadcasts an SSE event; set by main wiring (may be nil).
	Notify func(event string, payload any)

	// OnReplaced runs after a successful in-place replace (Jellyfin
	// refresh + DateCreated patch); set by main wiring.
	OnReplaced func(path string, oldStat *replace.SrcStat)

	windowOpen atomic.Bool
	active     atomic.Int32
	workerCap  atomic.Int32

	cancels sync.Map // job id → context.CancelFunc

	semMu sync.Mutex
	sems  map[string]chan struct{}

	hwMu     sync.Mutex
	hwReport *hwprobe.Report
	hwFails  map[string][]time.Time

	ms measurer

	stopCh chan struct{}
	wg     sync.WaitGroup
	kick   chan struct{}
}

// semCap is the per-backend concurrent-encode cap.
var semCap = map[string]int{"qsv": 2, "vaapi": 2, "nvenc": 2, "sw": 8}

func New(st *store.Store, cfg *config.Manager, sc *scan.Scanner) *Engine {
	e := &Engine{
		st: st, cfg: cfg, scan: sc,
		sems: map[string]chan struct{}{},
		hwFails: map[string][]time.Time{},
		stopCh: make(chan struct{}),
		kick:   make(chan struct{}, 1),
	}
	e.workerCap.Store(int32(cfg.Get().Workers))
	if rep, err := hwprobe.Load(st); err == nil && rep != nil {
		e.hwMu.Lock()
		e.hwReport = rep
		e.hwMu.Unlock()
	}
	return e
}

// Start launches scheduler + dispatcher and runs boot recovery.
func (e *Engine) Start() {
	e.recoverAtBoot()
	go e.schedulerLoop()
	go e.dispatcherLoop()
	e.wg.Add(1)
	go e.housekeepingLoop()
	e.wg.Add(1)
	go e.measureLoop()
	// Probe hardware in the background on first boot.
	if e.Report() == nil {
		go func() {
			if _, err := e.Reprobe(); err != nil {
				log.Printf("jobs: hw probe: %v", err)
			}
		}()
	}
}

// Stop drains workers.
func (e *Engine) Stop() {
	close(e.stopCh)
	e.wg.Wait()
}

// Kick nudges the dispatcher (new job / run-now / config change).
func (e *Engine) Kick() {
	select {
	case e.kick <- struct{}{}:
	default:
	}
}

// Report returns the current hardware report (may be nil).
func (e *Engine) Report() *hwprobe.Report {
	e.hwMu.Lock()
	defer e.hwMu.Unlock()
	return e.hwReport
}

// Reprobe re-runs hardware detection and notifies.
func (e *Engine) Reprobe() (*hwprobe.Report, error) {
	rep, err := hwprobe.Run(e.st)
	if err != nil {
		return nil, err
	}
	e.hwMu.Lock()
	e.hwReport = rep
	e.hwMu.Unlock()
	e.notify(EvHW, rep)
	return rep, nil
}

func (e *Engine) notify(event string, payload any) {
	if e.Notify != nil {
		e.Notify(event, payload)
	}
}

func (e *Engine) sem(key string) chan struct{} {
	e.semMu.Lock()
	defer e.semMu.Unlock()
	if c, ok := e.sems[key]; ok {
		return c
	}
	n := semCap[key]
	if n == 0 {
		n = 4
	}
	c := make(chan struct{}, n)
	e.sems[key] = c
	return c
}

// AcquireSem / ReleaseSem let the preview subsystem share GPU slots.
func (e *Engine) AcquireSem(key string) func() {
	c := e.sem(key)
	c <- struct{}{}
	return func() { <-c }
}

// ---- scheduler / dispatcher ----

func (e *Engine) schedulerLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	cfgCh := e.cfg.Subscribe()
	for {
		e.windowOpen.Store(e.cfg.WindowOpen(time.Now()))
		cap := e.cfg.Get().Workers
		e.workerCap.Store(int32(cap))
		select {
		case <-ticker.C:
		case <-cfgCh:
		case <-e.stopCh:
			return
		}
	}
}

func (e *Engine) dispatcherLoop() {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		e.tryDispatch()
		select {
		case <-ticker.C:
		case <-e.kick:
		case <-e.stopCh:
			return
		}
	}
}

func (e *Engine) tryDispatch() {
	for {
		if int(e.active.Load()) >= int(e.workerCap.Load()) {
			return
		}
		j, err := e.st.ClaimNext(e.windowOpen.Load())
		if err != nil {
			log.Printf("jobs: claim: %v", err)
			return
		}
		if j == nil {
			return
		}
		e.active.Add(1)
		e.wg.Add(1)
		go func(j *store.Job) {
			defer e.wg.Done()
			defer e.active.Add(-1)
			e.runJob(j, false)
			e.broadcastQueue()
			e.Kick()
		}(j)
	}
}

// ---- job execution ----

func (e *Engine) runJob(j *store.Job, resume bool) {
	ctx, cancel := context.WithCancel(context.Background())
	e.cancels.Store(j.ID, cancel)
	defer func() {
		cancel()
		e.cancels.Delete(j.ID)
	}()

	e.notify(EvJob, map[string]any{"id": j.ID, "status": store.StatusRunning})

	var settings encode.Settings
	if err := json.Unmarshal([]byte(j.SettingsJSON), &settings); err != nil {
		e.fail(j, fmt.Sprintf("bad settings json: %v", err), "")
		return
	}

	// Fresh probe of the source.
	src, err := media.ProbeFile(ctx, j.SrcPath)
	if err != nil {
		e.fail(j, fmt.Sprintf("probe source: %v", err), "")
		return
	}

	// Pre-encode stat snapshot (timestamps + identity).
	st, err := replace.Snapshot(j.SrcPath)
	if err != nil {
		e.fail(j, err.Error(), "")
		return
	}
	stJSON, _ := json.Marshal(st)

	// Original timestamps: prefer the snapshot taken when the job first
	// started (a resumed job must not re-read a half-touched source).
	if j.SrcStatJSON != "" {
		var saved replace.SrcStat
		if json.Unmarshal([]byte(j.SrcStatJSON), &saved) == nil && saved.MtimeSec > 0 {
			st = &saved
			stJSON = []byte(j.SrcStatJSON)
		}
	}

	// Resolve backend + render node against live hardware, then build
	// the exact plan (also used to verify stream counts on resume).
	settings.Normalize()
	rep := e.Report()
	if !settings.VideoCopy {
		settings.Backend = e.resolveBackend(rep, settings, j.Backend)
	}
	if rep != nil && settings.Backend != encode.SW && !settings.VideoCopy {
		settings.RenderNode = hwprobe.NodeFor(rep, settings.Backend, settings.Codec)
	}
	if !resume {
		settings = e.tuneQuality(ctx, j, settings, src)
		if ctx.Err() != nil {
			e.st.FinishJob(j.ID, store.StatusCanceled, 0, "canceled", "")
			e.notify(EvJob, map[string]any{"id": j.ID, "status": store.StatusCanceled})
			return
		}
	}
	tempPath := j.TempPath
	if !resume || tempPath == "" {
		c, err := encode.ChooseContainer(settings, src)
		if err != nil {
			e.fail(j, err.Error(), "")
			return
		}
		tempPath = filepath.Join(filepath.Dir(j.SrcPath), fmt.Sprintf(".mediatrans-%d.%s.tmp", j.ID, c))
	}
	primary, fallback, err := encode.Build(settings, src, tempPath, nil)
	if err != nil {
		e.fail(j, err.Error(), "")
		return
	}
	container := primary.Container
	if !resume {
		if err := e.st.SetJobTemp(j.ID, tempPath, string(stJSON)); err != nil {
			log.Printf("jobs: set temp: %v", err)
		}
		_ = e.st.SetJobCmd(j.ID, encode.CommandString(primary.Args))
		j.TempPath = tempPath
		if serr := e.encode(ctx, j, primary, fallback, src); serr != nil {
			if ctx.Err() != nil {
				e.st.FinishJob(j.ID, store.StatusCanceled, 0, "canceled", "")
				removeTemp(tempPath)
				e.notify(EvJob, map[string]any{"id": j.ID, "status": store.StatusCanceled})
				return
			}
			removeTemp(tempPath)
			e.handleEncodeFailure(j, settings, serr)
			return
		}
	}

	// Verify.
	e.st.UpdateJobStatus(j.ID, store.StatusVerifying, "")
	e.notify(EvJob, map[string]any{"id": j.ID, "status": store.StatusVerifying})
	v := src.Video()
	vspec := replace.VerifySpec{
		WantVideoCodec: string(settings.Codec),
		Want10Bit:      settings.BitDepth != 8 && !settings.VideoCopy,
		SrcDuration:    src.DurationSec(),
		WantAudioCount: primary.ExpectAudio,
		WantSubCount:   primary.ExpectSubs,
		SrcSize:        st.Size,
		Container:      container,
	}
	if !settings.TonemapHDR && (v.HDRType() == "hdr10" || v.HDRType() == "hlg") {
		vspec.WantTransfer = v.ColorTransfer
	}
	if _, verr := replace.Verify(ctx, tempPath, vspec); verr != nil {
		e.fail(j, fmt.Sprintf("verification failed: %v", verr), "")
		removeTemp(tempPath)
		return
	}

	// Replace.
	e.st.UpdateJobStatus(j.ID, store.StatusReplacing, "")
	e.notify(EvJob, map[string]any{"id": j.ID, "status": store.StatusReplacing})
	cfg := e.cfg.Get()
	trashDir := ""
	if cfg.TrashEnabled {
		trashDir = cfg.TrashDir
	}
	before, _ := e.st.GetFileByPath(j.SrcPath)

	// Destination: the original path unless the container changed.
	destPath := j.SrcPath
	srcExt := strings.TrimPrefix(strings.ToLower(filepath.Ext(j.SrcPath)), ".")
	if container != srcExt && !(container == "mp4" && srcExt == "m4v") {
		destPath = strings.TrimSuffix(j.SrcPath, filepath.Ext(j.SrcPath)) + "." + container
	}
	trashPath, err := replace.Replace(tempPath, j.SrcPath, destPath, st, trashDir)
	if err != nil {
		e.fail(j, fmt.Sprintf("replace: %v", err), "")
		return
	}
	if trashPath != "" {
		_ = e.st.AddTrash(store.TrashItem{OrigPath: j.SrcPath, TrashPath: trashPath,
			CurrentPath: destPath, Size: st.Size, JobID: j.ID})
	}
	// External subtitles pair by filename stem, which never changes (only
	// the extension may), so sidecars keep working untouched.
	_ = e.st.SetJobDest(j.ID, destPath)
	newSize, _ := statSize(destPath)

	// Feed the size model with what really happened.
	if !settings.VideoCopy && before != nil && before.VideoBitrate > 0 && before.Duration > 0 && noAudioChanges(settings, before) {
		srcVideo := float64(before.VideoBitrate) * before.Duration / 8
		outVideo := float64(newSize) - (float64(st.Size) - srcVideo)
		if outVideo > 0 {
			recs.RecordObservation(before, settings, outVideo/srcVideo)
		}
	}

	e.st.FinishJob(j.ID, store.StatusDone, newSize, "", "")
	e.notify(EvJob, map[string]any{"id": j.ID, "status": store.StatusDone, "output_size": newSize})

	if e.scan != nil {
		_ = e.scan.ProbeSingle(destPath)
		if destPath != j.SrcPath {
			_ = e.st.ClearOldPath(j.SrcPath)
		}
		e.scan.RefreshRecsSoon()
	}
	if e.OnReplaced != nil {
		go e.OnReplaced(destPath, st)
	}
}

// tuneQuality runs the per-file VMAF search when the settings ask for a
// quality target (or reuses a matching stored measurement), and returns
// the settings with Quality set to what the search found. On any failure
// it keeps the given quality: tuning never blocks an encode.
func (e *Engine) tuneQuality(ctx context.Context, j *store.Job, s encode.Settings, src *media.Probe) encode.Settings {
	if s.VMAFTarget <= 0 || s.VideoCopy || !media.VMAFAvailable() {
		return s
	}
	f, _ := e.st.GetFileByPath(j.SrcPath)
	if t := recs.TunedFor(f, s); t != nil {
		s.Quality = t.Quality
		e.saveJobSettings(j, s)
		return s
	}
	e.notify(EvJob, map[string]any{"id": j.ID, "status": "running",
		"note": fmt.Sprintf("measuring quality (target VMAF %.0f)", s.VMAFTarget)})
	res, _, err := tune.Search(ctx, tune.Options{
		Settings: s, Probe: src, Target: s.VMAFTarget,
		WorkDir: filepath.Join("/config/tune", fmt.Sprintf("job-%d", j.ID)),
		Acquire: e.AcquireSem,
		Progress: func(msg string) {
			e.notify(EvJob, map[string]any{"id": j.ID, "status": "running", "note": msg})
		},
	})
	_ = os.RemoveAll(filepath.Join("/config/tune", fmt.Sprintf("job-%d", j.ID)))
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("jobs: %d: quality search skipped: %v", j.ID, err)
		}
		return s
	}
	s.Quality = res.Quality
	e.saveJobSettings(j, s)
	if f != nil {
		rec := recs.TuneRecord{Target: s.VMAFTarget, Codec: string(s.Codec), Backend: string(s.Backend),
			Crop: s.Crop, MaxHeight: s.MaxHeight, Quality: res.Quality, Ratio: res.Ratio, VMAF: res.VMAF,
			Met: res.Met, At: time.Now().UTC().Format(time.RFC3339)}
		b, _ := json.Marshal(rec)
		_ = e.st.SetTune(f.ID, string(b))
		if res.Ratio > 0 {
			recs.RecordObservation(f, s, res.Ratio)
		}
	}
	log.Printf("jobs: %d: quality %d scores VMAF %.1f (p5 %.1f), %.0f%% of source video", j.ID,
		res.Quality, res.VMAF.Mean, res.VMAF.P5, res.Ratio*100)
	return s
}

func (e *Engine) saveJobSettings(j *store.Job, s encode.Settings) {
	b, _ := json.Marshal(s)
	j.SettingsJSON, j.Quality = string(b), s.Quality
	_ = e.st.UpdateJobSettings(j.ID, s.Quality, string(b))
}

// noAudioChanges reports whether audio was passed through untouched (so
// the size delta is purely video and safe to learn from).
func noAudioChanges(s encode.Settings, f *store.File) bool {
	for _, t := range s.Audio {
		if t.Action != "" && t.Action != "copy" {
			return false
		}
	}
	for _, a := range f.Audio {
		if strings.HasPrefix(a.Codec, "pcm_") && s.AudioPCMTarget != "copy" {
			return false
		}
	}
	return true
}

// encode runs ffmpeg, retrying once with software decode on a
// hardware-decode startup failure.
func (e *Engine) encode(ctx context.Context, j *store.Job, primary, fallback *encode.CmdSpec, src *media.Probe) error {
	release := e.AcquireSem(primary.SemKey)
	defer release()

	sawFrames := false
	var lastPct float64
	lastPersist := time.Now().Add(-time.Hour)
	onProgress := func(p encode.Progress) {
		if p.Frame > 0 {
			sawFrames = true
		}
		if p.Pct() >= lastPct+0.002 || p.Pct() == 1 {
			lastPct = p.Pct()
			e.notify(EvProgress, map[string]any{
				"job_id": j.ID, "pct": p.Pct(), "fps": p.FPS,
				"speed": p.Speed, "eta_sec": p.ETA(), "size": p.TotalSize,
			})
		}
		if time.Since(lastPersist) > 5*time.Second {
			lastPersist = time.Now()
			pj, _ := json.Marshal(p)
			_ = e.st.SetJobProgress(j.ID, string(pj))
		}
	}

	tail, runErr := encode.Runner(ctx, *primary, src.DurationSec(), onProgress)
	if runErr != nil && ctx.Err() == nil && fallback != nil && encode.LooksLikeHWFailure(tail.String(), sawFrames) {
		log.Printf("jobs: %d: hw decode failed (%v); retrying with software decode", j.ID, runErr)
		e.notify(EvJob, map[string]any{"id": j.ID, "status": "running", "note": "hardware decode failed, using software decode"})
		sawFrames, lastPct = false, 0
		_ = e.st.SetJobCmd(j.ID, encode.CommandString(fallback.Args))
		tail, runErr = encode.Runner(ctx, *fallback, src.DurationSec(), onProgress)
	}
	if runErr != nil {
		if ctx.Err() == nil {
			e.noteHWFailure(primary.SemKey)
		}
		return fmt.Errorf("ffmpeg: %v: %s", runErr, lastLines(tail.String(), 4))
	}
	return nil
}

// handleEncodeFailure decides retry vs terminal failure.
func (e *Engine) handleEncodeFailure(j *store.Job, s encode.Settings, err error) {
	if j.Attempts < j.MaxAttempts {
		_ = e.st.Requeue(j.ID)
		e.notify(EvJob, map[string]any{"id": j.ID, "status": store.StatusQueued, "note": err.Error()})
		return
	}
	e.fail(j, err.Error(), "")
}

func (e *Engine) fail(j *store.Job, msg, tail string) {
	_ = e.st.FinishJob(j.ID, store.StatusFailed, 0, msg, tail)
	log.Printf("jobs: %d failed: %s", j.ID, msg)
	e.notify(EvJob, map[string]any{"id": j.ID, "status": store.StatusFailed, "error": msg})
}

// Cancel stops a running/pending job.
func (e *Engine) Cancel(jobID int64) error {
	j, err := e.st.GetJob(jobID)
	if err != nil || j == nil {
		return fmt.Errorf("job %d not found", jobID)
	}
	switch j.Status {
	case store.StatusQueued:
		_ = e.st.FinishJob(jobID, store.StatusCanceled, 0, "canceled before start", "")
		e.notify(EvJob, map[string]any{"id": jobID, "status": store.StatusCanceled})
		e.broadcastQueue()
		return nil
	case store.StatusRunning, store.StatusVerifying, store.StatusReplacing:
		if c, ok := e.cancels.Load(jobID); ok {
			c.(context.CancelFunc)()
		}
		return nil
	}
	return fmt.Errorf("job %d is %s", jobID, j.Status)
}

// Retry requeues a canceled/failed job with fresh attempts.
func (e *Engine) Retry(jobID int64) error {
	if err := e.st.RetryJob(jobID); err != nil {
		return err
	}
	e.notify(EvJob, map[string]any{"id": jobID, "status": store.StatusQueued})
	e.Kick()
	return nil
}

func (e *Engine) broadcastQueue() {
	counts, _ := e.st.CountJobsByStatus()
	e.notify(EvQueue, counts)
}

// ---- hw health ----

func (e *Engine) noteHWFailure(backend string) {
	e.hwMu.Lock()
	defer e.hwMu.Unlock()
	now := time.Now()
	fails := e.hwFails[backend][:0]
	for _, t := range e.hwFails[backend] {
		if now.Sub(t) < 30*time.Minute {
			fails = append(fails, t)
		}
	}
	fails = append(fails, now)
	e.hwFails[backend] = fails
	if len(fails) >= 2 {
		_ = e.st.KVSet("hw_health."+backend, "degraded")
		e.notify(EvHW, map[string]any{"backend": backend, "degraded": true})
	}
}

// BackendDegraded reports hw health state.
func (e *Engine) BackendDegraded(backend string) bool {
	v, ok, _ := e.st.KVGet("hw_health." + backend)
	return ok && v == "degraded"
}

// ResetHealth clears degraded state for a backend.
func (e *Engine) ResetHealth(backend string) {
	e.hwMu.Lock()
	delete(e.hwFails, backend)
	e.hwMu.Unlock()
	_ = e.st.KVSet("hw_health."+backend, "ok")
}

func (e *Engine) resolveBackend(rep *hwprobe.Report, s encode.Settings, jobBackend string) encode.Backend {
	b := resolveBackend(rep, s, jobBackend)
	if b != encode.SW && e.BackendDegraded(string(b)) {
		if rep != nil {
			for _, alt := range rep.BackendsFor(s.Codec) {
				if alt != b && !e.BackendDegraded(string(alt)) {
					return alt
				}
			}
		}
		return encode.SW
	}
	return b
}

// ResolveFor is the non-job resolver used by recommendations + previews.
func (e *Engine) ResolveFor(pref string, c encode.Codec) encode.Backend {
	return e.resolveBackend(e.Report(), encode.Settings{Backend: encode.Backend(pref), Codec: c}, pref)
}

func resolveBackend(rep *hwprobe.Report, s encode.Settings, jobBackend string) encode.Backend {
	b := s.Backend
	if b == "" || b == "auto" {
		if jobBackend != "" && jobBackend != "auto" {
			b = encode.Backend(jobBackend)
		}
	}
	if b != "" && b != "auto" && rep != nil && rep.Available(b, s.Codec) != nil {
		return b
	}
	if rep == nil {
		return encode.SW
	}
	if bs := rep.BackendsFor(s.Codec); len(bs) > 0 {
		return bs[0]
	}
	return encode.SW
}

// ---- boot recovery & housekeeping ----

func (e *Engine) recoverAtBoot() {
	active, err := e.st.ActiveJobs()
	if err != nil {
		log.Printf("jobs: recovery: %v", err)
		return
	}
	for _, j := range active {
		switch j.Status {
		case store.StatusRunning:
			removeTemp(j.TempPath)
			if j.Attempts < j.MaxAttempts {
				_ = e.st.Requeue(j.ID)
				log.Printf("jobs: recovery: requeued %d", j.ID)
			} else {
				_ = e.st.FinishJob(j.ID, store.StatusFailed, 0, "interrupted by restart", "")
			}
		case store.StatusVerifying, store.StatusReplacing:
			if srcUnchanged(j) {
				log.Printf("jobs: recovery: resuming %d at %s", j.ID, j.Status)
				e.wg.Add(1)
				go func(j *store.Job) {
					defer e.wg.Done()
					e.runJob(j, true)
				}(j)
			} else {
				_ = e.st.FinishJob(j.ID, store.StatusFailed, 0, "source changed during encode", "")
				moveToManual(j.TempPath)
			}
		}
	}
}

func srcUnchanged(j *store.Job) bool {
	if j.SrcStatJSON == "" {
		return false
	}
	var st replace.SrcStat
	if err := json.Unmarshal([]byte(j.SrcStatJSON), &st); err != nil {
		return false
	}
	cur, err := replace.Snapshot(j.SrcPath)
	if err != nil {
		return false
	}
	return cur.Size == st.Size && cur.MtimeSec == st.MtimeSec && cur.MtimeNsec == st.MtimeNsec
}

func (e *Engine) housekeepingLoop() {
	defer e.wg.Done()
	e.sweepStaleTemps()
	e.PurgeTrash(false)
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for n := 1; ; n++ {
		select {
		case <-ticker.C:
			e.PurgeTrash(false)
			if n%24 == 0 {
				e.sweepStaleTemps()
				if n, err := e.st.SweepMissing(); err == nil && n > 0 {
					log.Printf("jobs: swept %d missing files", n)
				}
			}
		case <-e.stopCh:
			return
		}
	}
}

func (e *Engine) sweepStaleTemps() {
	keep := map[string]bool{}
	if active, err := e.st.ActiveJobs(); err == nil {
		for _, j := range active {
			keep[j.TempPath] = true
		}
	}
	for _, lib := range e.cfg.Get().Libraries {
		filepath.WalkDir(lib.Path, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() && d.Name() == ".mediatrans-manual" {
				return filepath.SkipDir
			}
			if !d.IsDir() && strings.HasPrefix(d.Name(), ".mediatrans-") && strings.HasSuffix(d.Name(), ".tmp") && !keep[path] {
				removeTemp(path)
			}
			return nil
		})
	}
}

// PurgeTrash deletes retained originals older than the retention
// window (all of them when all=true) and returns bytes freed.
func (e *Engine) PurgeTrash(all bool) (int64, int) {
	cfg := e.cfg.Get()
	days := cfg.TrashDays
	if all {
		days = -1
	}
	items, err := e.st.ExpiredTrash(days)
	if err != nil {
		log.Printf("trash: %v", err)
		return 0, 0
	}
	var freed int64
	for _, t := range items {
		if err := os.Remove(t.TrashPath); err != nil && !os.IsNotExist(err) {
			log.Printf("trash: remove %s: %v", t.TrashPath, err)
			continue
		}
		pruneEmptyDirs(filepath.Dir(t.TrashPath), cfg.TrashDir)
		_ = e.st.DeleteTrash(t.ID)
		freed += t.Size
	}
	if len(items) > 0 {
		log.Printf("trash: purged %d originals", len(items))
	}
	return freed, len(items)
}

// RestoreTrash puts an original back and removes the encoded file.
func (e *Engine) RestoreTrash(id int64) error {
	t, err := e.st.GetTrash(id)
	if err != nil || t == nil {
		return fmt.Errorf("trash item %d not found", id)
	}
	if err := replace.Restore(t.TrashPath, t.OrigPath, t.CurrentPath); err != nil {
		return err
	}
	pruneEmptyDirs(filepath.Dir(t.TrashPath), e.cfg.Get().TrashDir)
	_ = e.st.DeleteTrash(id)
	if e.scan != nil {
		_ = e.scan.ProbeSingle(t.OrigPath)
		if t.CurrentPath != t.OrigPath {
			_ = e.st.ClearOldPath(t.CurrentPath)
		}
	}
	return nil
}

// DeleteTrashItem permanently removes one retained original.
func (e *Engine) DeleteTrashItem(id int64) error {
	t, err := e.st.GetTrash(id)
	if err != nil || t == nil {
		return fmt.Errorf("trash item %d not found", id)
	}
	if err := os.Remove(t.TrashPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	pruneEmptyDirs(filepath.Dir(t.TrashPath), e.cfg.Get().TrashDir)
	return e.st.DeleteTrash(id)
}

func pruneEmptyDirs(dir, stop string) {
	stop = filepath.Clean(stop)
	for dir = filepath.Clean(dir); strings.HasPrefix(dir, stop+"/"); dir = filepath.Dir(dir) {
		if os.Remove(dir) != nil {
			return
		}
	}
}

func removeTemp(p string) {
	if p != "" {
		_ = os.Remove(p)
	}
}

func moveToManual(p string) {
	if p == "" {
		return
	}
	dst := filepath.Join(filepath.Dir(p), ".mediatrans-manual", filepath.Base(p))
	_ = os.MkdirAll(filepath.Dir(dst), 0o755)
	_ = os.Rename(p, dst)
}

// ---- helpers ----

func statSize(p string) (int64, error) {
	fi, err := os.Stat(p)
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}

// RecommendFor builds a recommendation-backed settings payload for a
// file (used by the queue API when no explicit settings are given).
func RecommendFor(f *store.File, cfg config.Config) recs.Recommendation {
	return recs.Recommend(f, cfg)
}
