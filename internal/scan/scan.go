package scan

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"mediatrans/internal/config"
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

	mu      sync.Mutex
	running bool
	stats   Stats

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
	defer s.report(func(st *Stats) { st.Running = false; st.EndedAt = time.Now(); st.Phase = "done" })

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

	// existing size+mtime for change detection
	type finfo struct{ size int64; mtime int64 }
	existing := map[string]finfo{}
	rows, err := s.st.ListFileStat(lib.Name)
	if err != nil {
		return err
	}
	for _, r := range rows {
		existing[r.Path] = finfo{r.Size, r.MtimeNS}
	}

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
					return nil
				}
			}
			probeMu.Lock()
			*toProbe = append(*toProbe, path)
			probeMu.Unlock()
		}
		return nil
	})

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
func buildFile(lib, path string, p *media.Probe) *store.File {
	pr := Parse(lib, path)
	v := p.Video()
	fi, _ := os.Stat(path)
	f := &store.File{
		Path: path, Library: lib,
		Title: pr.Title, Year: pr.Year, Season: pr.Season, Episode: pr.Episode,
		EpTitle: pr.EpTitle, QualityTag: pr.QualityTag,
		Container: strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), "."),
	}
	if fi != nil {
		f.Size = fi.Size()
		f.MtimeNS = fi.ModTime().UnixNano()
	}
	f.Duration = p.DurationSec()
	f.TotalBitrate = p.TotalBitrate()
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

// ProbeSingle re-probes one path now (used after a successful
// transcode replaces the file).
func (s *Scanner) ProbeSingle(path string) error {
	cfg := s.cfg.Get()
	s.probeOne(path, cfg)
	return nil
}
