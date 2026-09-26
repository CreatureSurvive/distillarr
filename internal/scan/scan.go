// SPDX-License-Identifier: GPL-3.0-or-later

package scan

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/issues"
	"github.com/CreatureSurvive/distillarr/internal/media"
	"github.com/CreatureSurvive/distillarr/internal/recs"
	"github.com/CreatureSurvive/distillarr/internal/store"
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
	type finfo struct{ size, mtime int64; nlink int; sidecars string }
	existing := map[string]finfo{}
	rows, err := s.st.ListFileStat(lib.Name)
	if err != nil {
		return err
	}
	for _, r := range rows {
		existing[r.Path] = finfo{r.Size, r.MtimeNS, r.Nlink, r.Sidecars}
	}
	// Subtitle files seen per directory, and the unchanged videos whose
	// stored sidecar list is re-derived from them after the walk.
	subsByDir := map[string][]string{}
	var unchanged []string

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
		if IsSubFile(name) {
			dir := filepath.Dir(path)
			subsByDir[dir] = append(subsByDir[dir], name)
			return nil
		}
		if IsVideoFile(name) {
			if fi, err := d.Info(); err == nil {
				mt := fi.ModTime().UnixNano()
				if ex, ok := existing[path]; ok && ex.size == fi.Size() && ex.mtime == mt {
					seen[path] = true
					unchanged = append(unchanged, path)
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
	sidecarUpdates := map[string]string{}
	for _, p := range unchanged {
		js, _ := json.Marshal(sidecarsFrom(p, subsByDir[filepath.Dir(p)]))
		if string(js) != existing[p].sidecars {
			sidecarUpdates[p] = string(js)
		}
	}
	if err := s.st.UpdateSidecars(sidecarUpdates); err != nil {
		return err
	}
	if len(sidecarUpdates) > 0 {
		s.RefreshRecsSoon() // missing_subs depends on sidecars
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
	forces, _ := s.st.ForcesTranscode(f.ID, issues.ForcesTranscodeLookbackDays)
	origLang, instanceName := "", ""
	if item, _ := s.st.ArrItemByFileID(f.ID); item != nil {
		origLang = item.OriginalLanguage
		instanceName = instanceNameByID(cfg, item.InstanceID)
	}
	f.Issues = issues.Encode(issues.Detect(f, cfg, rec.Action == "transcode", rec.Limited, rec.UpgradePending, forces, origLang, instanceName))

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

// instanceNameByID resolves an arr instance id to its configured display
// name ("" if removed/unknown) — the form issues.Detect/ExtraLanguages
// and EffectiveLangPolicy's scope key both expect, matching
// api.ArrPolicyFor's own instance lookup.
func instanceNameByID(cfg config.Config, id string) string {
	for _, x := range cfg.ArrInstances {
		if x.ID == id {
			return x.Name
		}
	}
	return ""
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
		a := a
		audio = append(audio, store.AudioStream{
			Index: a.Index, Codec: a.CodecName, Lang: a.Lang(), Title: a.Title(),
			Channels: a.Channels, BitRate: a.BitRateInt(),
			Default: a.Disposition["default"] == 1, Forced: a.Disposition["forced"] == 1,
			Commentary: a.IsCommentary(),
		})
	}
	f.Audio = audio
	subsList := p.Subtitles()
	subs := []store.SubStream{}
	for _, st := range subsList {
		st := st
		subs = append(subs, store.SubStream{
			Index: st.Index, Codec: st.CodecName, Lang: st.Lang(), Title: st.Title(),
			BitRate: st.BitRateInt(),
			Default: st.Disposition["default"] == 1, Forced: st.Disposition["forced"] == 1,
			Commentary: st.IsCommentary(), SDH: st.IsSDH(), IsText: st.IsTextSubtitle(),
		})
	}
	f.Subs = subs
	f.SubCount = len(subsList)
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
				Codec: st.CodecName, BitRate: st.BitRateInt(), Bytes: st.Bytes()})
		case "audio":
			out = append(out, store.Stream{Kind: "audio", StreamIndex: st.Index,
				Codec: st.CodecName, Lang: st.Lang(), Title: st.Title(),
				Channels: st.Channels, BitRate: st.BitRateInt(), Bytes: st.Bytes(),
				IsDefault: st.Disposition["default"] == 1, IsForced: st.Disposition["forced"] == 1})
		case "subtitle":
			out = append(out, store.Stream{Kind: "subtitle", StreamIndex: st.Index,
				Codec: st.CodecName, Lang: st.Lang(), Title: st.Title(),
				BitRate: st.BitRateInt(), Bytes: st.Bytes(),
				IsDefault: st.Disposition["default"] == 1, IsForced: st.Disposition["forced"] == 1,
				IsText: st.IsTextSubtitle()})
		}
	}
	return out
}

// findSidecars lists external subtitle files matching the media stem.
func findSidecars(path string) []store.Sidecar {
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && IsSubFile(e.Name()) {
			names = append(names, e.Name())
		}
	}
	return sidecarsFrom(path, names)
}

// sidecarsFrom picks the subtitle file names (from path's directory)
// that belong to path's stem.
func sidecarsFrom(path string, names []string) []store.Sidecar {
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	out := []store.Sidecar{}
	for _, name := range names {
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
	forces, err := s.st.ForcesTranscodeFileIDs(issues.ForcesTranscodeLookbackDays)
	if err != nil {
		log.Printf("scan: refresh recs: forces_transcode lookup: %v", err)
	}
	origLangs, err := s.st.ArrOriginalLanguages()
	if err != nil {
		log.Printf("scan: refresh recs: original_language lookup: %v", err)
	}
	instanceIDs, err := s.st.ArrInstanceIDs()
	if err != nil {
		log.Printf("scan: refresh recs: instance id lookup: %v", err)
	}
	instanceNames := map[int64]string{}
	for fileID, id := range instanceIDs {
		instanceNames[fileID] = instanceNameByID(cfg, id)
	}
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
			Issues: issues.Encode(issues.Detect(f, cfg, r.Action == "transcode", r.Limited, r.UpgradePending, forces[f.ID], origLangs[f.ID], instanceNames[f.ID]))}
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

// streamStatsCursorKey tracks the one-time pass that reads mkvmerge's
// per-track statistics for MKVs scanned before they were recorded; -1 = done.
const streamStatsCursorKey = "stream_stats_cursor"

func (s *Scanner) fillStreamStats() int {
	cur, _, _ := s.st.KVGet(streamStatsCursorKey)
	after, _ := strconv.ParseInt(cur, 10, 64)
	if after < 0 {
		return 0
	}
	todo, err := s.st.StreamStatsTodo(after, 100)
	if err != nil {
		return 0
	}
	if len(todo) == 0 {
		_ = s.st.KVSet(streamStatsCursorKey, "-1")
		return 0
	}
	for _, t := range todo {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		p, err := media.ProbeFile(ctx, t.Path)
		cancel()
		if err == nil {
			var stats []store.StreamStat
			for _, st := range p.Streams {
				stats = append(stats, store.StreamStat{Index: st.Index, BitRate: st.BitRateInt(), Bytes: st.Bytes()})
			}
			if err := s.st.SetStreamStats(t.ID, stats, p.VideoBitrate()); err != nil {
				log.Printf("scan: stream stats %s: %v", t.Path, err)
			}
		}
	}
	_ = s.st.KVSet(streamStatsCursorKey, strconv.FormatInt(todo[len(todo)-1].ID, 10))
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
		if n := s.fillStreamStats(); n > 0 {
			s.RefreshRecsSoon() // video bitrates feed recommendations
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

// ParserVersion bumps whenever Parse learns new filename forms; boot
// re-parses every stored name once per version (ReparseNames).
const ParserVersion = "2"

// ReparseNames re-applies Parse to every stored file when ParserVersion
// changed, updating only rows whose name-derived fields differ. Cheap:
// no probing, just the path strings.
func (s *Scanner) ReparseNames() {
	if v, ok, _ := s.st.KVGet("parser_version"); ok && v == ParserVersion {
		return
	}
	type upd struct {
		id int64
		p  Parsed
	}
	var ups []upd
	_ = s.st.EachFile(func(f *store.File) error {
		p := Parse(f.Library, f.Path)
		if p.Title != f.Title || p.Year != f.Year || p.Season != f.Season || p.Episode != f.Episode ||
			p.EpTitle != f.EpTitle || p.QualityTag != f.QualityTag {
			ups = append(ups, upd{f.ID, p})
		}
		return nil
	})
	for _, u := range ups {
		if err := s.st.UpdateParsed(u.id, u.p.Title, u.p.Year, u.p.Season, u.p.Episode, u.p.EpTitle, u.p.QualityTag); err != nil {
			log.Printf("scan: reparse: %v", err)
			return
		}
	}
	_ = s.st.KVSet("parser_version", ParserVersion)
	if len(ups) > 0 {
		log.Printf("scan: re-parsed %d file names (parser v%s)", len(ups), ParserVersion)
		s.RefreshRecsSoon()
	}
}
