package scan

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"mediatrans/internal/config"
	"mediatrans/internal/issues"
	"mediatrans/internal/media"
	"mediatrans/internal/recs"
	"mediatrans/internal/store"
)

// Stats is scan progress, broadcast over SSE while running.
type Stats struct {
	Running   bool      `json:"running"`
	Phase     string    `json:"phase"`
	Library   string    `json:"library,omitempty"`
	Seen      int64     `json:"seen"`
	Probed    int64     `json:"probed"`
	Updated   int64     `json:"updated"`
	Errors    int64     `json:"errors"`
	StartedAt time.Time `json:"started_at,omitempty"`
	EndedAt   time.Time `json:"ended_at,omitempty"`
	LastErr   string    `json:"last_err,omitempty"`
}

// Scanner walks libraries incrementally and upserts probe results.
type Scanner struct {
	st  *store.Store
	cfg *config.Manager

	mu           sync.Mutex
	running      bool
	stats        Stats
	refreshTimer *time.Timer

	// Progress is called (from the scan goroutine) on updates.
	Progress func(Stats)
}

func New(st *store.Store, cfg *config.Manager) *Scanner {
	return &Scanner{st: st, cfg: cfg}
}

// Stats returns the current/latest stats snapshot.
func (s *Scanner) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

// Running reports whether a scan pass is active.
func (s *Scanner) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Start launches a scan pass in the background if none is running.
// Returns false if a scan is already active.
func (s *Scanner) Start() bool {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return false
	}
	s.running = true
	s.stats = Stats{Running: true, StartedAt: time.Now(), Phase: "starting"}
	s.mu.Unlock()
	go s.run()
	return true
}

func (s *Scanner) report(mut func(*Stats)) {
	s.mu.Lock()
	mut(&s.stats)
	st := s.stats
	s.mu.Unlock()
	if s.Progress != nil {
		s.Progress(st)
	}
}

func (s *Scanner) run() {
	defer func() {
		// Reset BOTH the mutex-guarded gate and the reported stats —
		// a scan pass must always be restartable afterwards.
		s.mu.Lock()
		s.running = false
		s.stats.Running = false
		s.stats.EndedAt = time.Now()
		s.stats.Phase = "done"
		st := s.stats
		s.mu.Unlock()
		if s.Progress != nil {
			s.Progress(st)
		}
	}()

	seen := map[string]bool{}          // every existing media path this pass
	var probeMu sync.Mutex
	var toProbe []string

	for _, lib := range s.cfg.Get().Libraries {
		if err := s.walkLibrary(lib, seen, &toProbe, &probeMu); err != nil {
			log.Printf("scan: %s: %v", lib.Name, err)
		}
	}

	// Fast path: unchanged files just clear the missing flag.
	s.report(func(st *Stats) { st.Phase = "indexing unchanged" })
	for p := range seen {
		_ = s.st.ClearMissing(p)
	}

	// Probe pool.
	s.report(func(st *Stats) { st.Phase = "probing" })
	cfg := s.cfg.Get()
	var wg sync.WaitGroup
	sem := make(chan struct{}, 4)
	for _, p := range toProbe {
		wg.Add(1)
		go func(p string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s.probeOne(p, cfg)
		}(p)
	}
	wg.Wait()

	// Anything not seen this pass is missing on disk.
	s.report(func(st *Stats) { st.Phase = "marking missing" })
	s.markMissing(seen)
}

// walkLibrary walks one library root, classifying videos and sidecars.
func (s *Scanner) walkLibrary(lib config.Library, seen map[string]bool,
	toProbe *[]string, probeMu *sync.Mutex) error {
	s.report(func(st *Stats) { st.Phase = "walking"; st.Library = lib.Name })

	// existing size+mtime+nlink for change detection
	type finfo struct{ size, mtime int64; nlink int }
	existing := map[string]finfo{}
	rows, err := s.st.ListFileStat(lib.Name)
	if err != nil {
		return err
	}
	for _, r := range rows {
		existing[r.Path] = finfo{r.Size, r.MtimeNS, r.Nlink}
	}

	// nlink can change (a torrent client adding or removing a hardlink)
	// without size or mtime changing, so it's checked here even for
	// files the walk otherwise treats as unchanged, and applied in one
	// batched update per library.
	nlinkUpdates := map[string]int{}

	root := lib.Path
	filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable entries
		}
		if d.IsDir() {
			if path == root || !SkipDir(d.Name()) {
				return nil
			}
			return filepath.SkipDir
		}
		name := d.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if SkipFile(name, ext) {
			return nil
		}
		if IsVideoFile(name) {
			if fi, err := d.Info(); err == nil {
				mt := fi.ModTime().UnixNano()
				if ex, ok := existing[path]; ok && ex.size == fi.Size() && ex.mtime == mt {
					seen[path] = true
					s.report(func(st *Stats) { st.Seen++ })
					if nlink := nlinkOf(fi); nlink != ex.nlink {
						nlinkUpdates[path] = nlink
					}
					return nil
				}
			}
			probeMu.Lock()
			*toProbe = append(*toProbe, path)
			probeMu.Unlock()
		}
		return nil
	})

	if err := s.st.UpdateNlinks(nlinkUpdates); err != nil {
		return err
	}

	// Probed files are also "seen".
	for _, p := range *toProbe {
		if strings.HasPrefix(p, root) {
			seen[p] = true
		}
	}
	return nil
}

// probeOne ffprobes one file and upserts it.
func (s *Scanner) probeOne(path string, cfg config.Config) {
	lib := s.libraryFor(path)
	if lib == "" {
		s.report(func(st *Stats) { st.Probed++; st.Errors++ })
		return
	}
	p, err := media.ProbeFile(context.Background(), path)
	if err != nil {
		log.Printf("scan: probe %s: %v", path, err)
		s.report(func(st *Stats) { st.Probed++; st.Errors++; st.LastErr = err.Error() })
		return
	}
	f := buildFile(lib, path, p)
	f.Sidecars = findSidecars(path)
	rec := recs.Recommend(f, cfg)
	f.TranscodeScore = rec.Score
	f.RecJSON = rec.JSON()
	f.Issues = issues.Encode(issues.Detect(f, rec.Action == "transcode", rec.Limited))

	if err := s.st.UpsertFile(f, buildStreams(f.ID, p)); err != nil {
		log.Printf("scan: upsert %s: %v", path, err)
		s.report(func(st *Stats) { st.Probed++; st.Errors++; st.LastErr = err.Error() })
		return
	}
	s.report(func(st *Stats) {
		st.Probed++
		st.Updated++
		if st.Probed%25 == 0 {
			st.Phase = "probing"
		}
	})
}

func (s *Scanner) libraryFor(path string) string {
	for _, lib := range s.cfg.Get().Libraries {
		if strings.HasPrefix(path, lib.Path+"/") || path == lib.Path {
			return lib.Name
		}
	}
	return ""
}

// buildFile converts a probe result into a store.File.
// nlinkOf returns a file's hardlink count (1 for a file with no other
// links). A count above 1 usually means a torrent client is still
// seeding the same file, so replacing it wouldn't reclaim any space.
func nlinkOf(fi os.FileInfo) int {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return int(st.Nlink)
	}
	return 1
}

func buildFile(lib, path string, p *media.Probe) *store.File {
	pr := Parse(lib, path)
	v := p.Video()
	fi, _ := os.Stat(path)
	f := &store.File{
		Path: path, Library: lib,
		Title: pr.Title, Year: pr.Year, Season: pr.Season, Episode: pr.Episode,
		EpTitle: pr.EpTitle, QualityTag: pr.QualityTag,
		Container: strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."),
		Nlink:     1,
	}
	if fi != nil {
		f.Size = fi.Size()
		f.MtimeNS = fi.ModTime().UnixNano()
		f.Nlink = nlinkOf(fi)
	}
	f.Duration = p.DurationSec()
	f.TotalBitrate = p.TotalBitrate()
	f.Faststart, f.MetaChecked = faststartOf(path, f.Container), true
	audio := []store.AudioStream{}
	for _, a := range p.Audios() {
		audio = append(audio, store.AudioStream{
			Index: a.Index, Codec: a.CodecName, Lang: a.Lang(), Title: a.Title(),
			Channels: a.Channels, BitRate: a.BitRateInt(),
			Default: a.Disposition["default"] == 1, Forced: a.Disposition["forced"] == 1,
		})
	}
	f.Audio = audio
	f.SubCount = len(p.Subtitles())
	if v != nil {
		f.VideoCodec = v.CodecName
		f.Width, f.Height = v.Width, v.Height
		f.BitDepth = v.BitDepth()
		f.FPS = v.FPS()
		f.HDR = v.HDRType()
		f.Interlaced = v.Interlaced()
		f.VideoTag = v.CodecTagString
		f.VideoBitrate = p.VideoBitrate()
	}
	return f
}

func buildStreams(fileID int64, p *media.Probe) []store.Stream {
	out := []store.Stream{}
	for _, st := range p.Streams {
		switch st.CodecType {
		case "video":
			if st.IsAttachedPic() {
				continue
			}
			out = append(out, store.Stream{Kind: "video", StreamIndex: st.Index,
				Codec: st.CodecName, BitRate: st.BitRateInt()})
		case "audio":
			out = append(out, store.Stream{Kind: "audio", StreamIndex: st.Index,
				Codec: st.CodecName, Lang: st.Lang(), Title: st.Title(),
				Channels: st.Channels, BitRate: st.BitRateInt(),
				IsDefault: st.Disposition["default"] == 1, IsForced: st.Disposition["forced"] == 1})
		case "subtitle":
			out = append(out, store.Stream{Kind: "subtitle", StreamIndex: st.Index,
				Codec: st.CodecName, Lang: st.Lang(), Title: st.Title(),
				IsDefault: st.Disposition["default"] == 1, IsForced: st.Disposition["forced"] == 1,
				IsText: st.IsTextSubtitle()})
		}
	}
	return out
}

// findSidecars lists external subtitle files matching the media stem.
func findSidecars(path string) []store.Sidecar {
	dir := filepath.Dir(path)
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := []store.Sidecar{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !IsSubFile(name) {
			continue
		}
		s := strings.TrimSuffix(name, filepath.Ext(name))
		if s != stem && !strings.HasPrefix(s, stem+".") {
			continue
		}
		lang := ""
		if rest := strings.TrimPrefix(s, stem+"."); rest != s {
			lang = strings.SplitN(rest, ".", 2)[0]
		}
		out = append(out, store.Sidecar{Name: name, Lang: lang,
			Kind: strings.TrimPrefix(strings.ToLower(filepath.Ext(name)), ".")})
	}
	return out
}

// markMissing flags rows not visited this pass (grace: rows are only
// deleted by SweepMissing after 7 days missing).
func (s *Scanner) markMissing(seen map[string]bool) {
	// Build a temp table of seen paths, then flag the rest.
	if err := s.st.ExecDdl(`CREATE TEMP TABLE IF NOT EXISTS seen_paths(path TEXT PRIMARY KEY)`); err != nil {
		log.Printf("scan: seen table: %v", err)
		return
	}
	_ = s.st.ExecDdl(`DELETE FROM seen_paths`)
	const chunk = 400
	paths := make([]string, 0, chunk)
	for p := range seen {
		paths = append(paths, p)
		if len(paths) == chunk {
			_ = s.st.InsertSeen(paths)
			paths = paths[:0]
		}
	}
	if len(paths) > 0 {
		_ = s.st.InsertSeen(paths)
	}
	if err := s.st.MarkNotSeen(); err != nil {
		log.Printf("scan: mark missing: %v", err)
	}
}

// RefreshRecs recomputes every cached recommendation (after settings,
// hardware or size-model calibration change). Cheap: no probing.
func (s *Scanner) RefreshRecs() {
	cfg := s.cfg.Get()
	batch := map[int64]store.RecUpdate{}
	flush := func() {
		if len(batch) > 0 {
			if err := s.st.UpdateRecs(batch); err != nil {
				log.Printf("scan: refresh recs: %v", err)
			}
			batch = map[int64]store.RecUpdate{}
		}
	}
	_ = s.st.EachFile(func(f *store.File) error {
		r := recs.Recommend(f, cfg)
		batch[f.ID] = store.RecUpdate{Score: r.Score, Rec: r.JSON(),
			Issues: issues.Encode(issues.Detect(f, r.Action == "transcode", r.Limited))}
		if len(batch) >= 500 {
			flush()
		}
		return nil
	})
	flush()
	if s.Progress != nil {
		st := s.Stats()
		st.Phase = "recommendations refreshed"
		s.Progress(st)
	}
}

// RefreshRecsSoon debounces RefreshRecs (many triggers → one pass).
func (s *Scanner) RefreshRecsSoon() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.refreshTimer != nil {
		s.refreshTimer.Stop()
	}
	s.refreshTimer = time.AfterFunc(3*time.Second, s.RefreshRecs)
}

// ProbeSingle re-probes one path now (used after a successful
// transcode replaces the file).
func (s *Scanner) ProbeSingle(path string) error {
	cfg := s.cfg.Get()
	s.probeOne(path, cfg)
	return nil
}

// faststartOf reports MP4 index placement: 1 first, 0 last, -1 not MP4
// or unreadable.
func faststartOf(path, container string) int {
	switch container {
	case "mp4", "m4v", "mov":
	default:
		return -1
	}
	ok, err := media.MoovFirst(path)
	if err != nil {
		return -1
	}
	if ok {
		return 1
	}
	return 0
}

// fillMeta reads video tag + faststart for files scanned before those
// facts were recorded. Cheap: one small ffprobe and a few box headers.
func (s *Scanner) fillMeta() int {
	todo, err := s.st.FilesNeedingMeta(200)
	if err != nil || len(todo) == 0 {
		return 0
	}
	for _, t := range todo {
		tag := ""
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if p, err := media.ProbeFile(ctx, t.Path); err == nil && p.Video() != nil {
			tag = p.Video().CodecTagString
		}
		cancel()
		_ = s.st.SetMeta(t.ID, tag, faststartOf(t.Path, t.Container))
	}
	return len(todo)
}

// CropLoop detects black bars in the background, re-encode candidates
// first, two files at a time. New or changed files are picked up after
// each scan pass; recommendations refresh as results land.
func (s *Scanner) CropLoop(stop <-chan struct{}) {
	for {
		if s.Running() {
			if sleepOr(stop, 30*time.Second) {
				return
			}
			continue
		}
		// Container facts first: fast, and they feed the issues list.
		if n := s.fillMeta(); n > 0 {
			s.RefreshRecsSoon() // debounced; issues fill in as batches land
			continue
		}
		todo, err := s.st.FilesNeedingCrop(40)
		if err != nil || len(todo) == 0 {
			if sleepOr(stop, 10*time.Minute) {
				return
			}
			continue
		}
		var wg sync.WaitGroup
		sem := make(chan struct{}, 2)
		for _, t := range todo {
			wg.Add(1)
			sem <- struct{}{}
			go func(t store.CropTodo) {
				defer wg.Done()
				defer func() { <-sem }()
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
				defer cancel()
				c, ok, err := media.DetectCrop(ctx, t.Path, t.Width, t.Height, t.Duration)
				if err != nil {
					log.Printf("crop: %s: %v", t.Path, err)
				}
				if !ok {
					c = media.Crop{}
				}
				if err := s.st.SetCrop(t.ID, c.W, c.H, c.X, c.Y); err != nil {
					log.Printf("crop: save %s: %v", t.Path, err)
				}
			}(t)
		}
		wg.Wait()
		s.RefreshRecsSoon()
		select {
		case <-stop:
			return
		default:
		}
	}
}

func sleepOr(stop <-chan struct{}, d time.Duration) bool {
	select {
	case <-stop:
		return true
	case <-time.After(d):
		return false
	}
}
