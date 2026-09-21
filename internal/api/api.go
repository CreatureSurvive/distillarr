// Package api exposes the REST surface and serves the embedded UI.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mediatrans/internal/config"
	"mediatrans/internal/encode"
	"mediatrans/internal/jobs"
	"mediatrans/internal/jellyfin"
	"mediatrans/internal/media"
	"mediatrans/internal/preview"
	"mediatrans/internal/recs"
	"mediatrans/internal/scan"
	"mediatrans/internal/store"
)

// Server wires every subsystem into HTTP routes.
type Server struct {
	st    *store.Store
	cfg   *config.Manager
	scan  *scan.Scanner
	eng   *jobs.Engine
	prev  *preview.Manager
	hub   *Hub
	ui    fs.FS // embedded frontend (web/dist)
}

func NewServer(st *store.Store, cfg *config.Manager, sc *scan.Scanner,
	eng *jobs.Engine, pv *preview.Manager, ui fs.FS) *Server {
	s := &Server{st: st, cfg: cfg, scan: sc, eng: eng, prev: pv, hub: NewHub(), ui: ui}
	sc.Progress = func(st scan.Stats) { s.hub.Broadcast("scan", st) }
	eng.Notify = func(event string, payload any) { s.hub.Broadcast(event, payload) }
	return s
}

// Hub exposes the SSE hub.
func (s *Server) Hub() *Hub { return s.hub }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/events", s.hub.ServeHTTP)

	mux.HandleFunc("GET /api/v1/config", s.getConfig)
	mux.HandleFunc("PUT /api/v1/config", s.putConfig)

	mux.HandleFunc("POST /api/v1/scan", s.startScan)
	mux.HandleFunc("GET /api/v1/scan", s.scanStats)

	mux.HandleFunc("GET /api/v1/hw", s.getHW)
	mux.HandleFunc("POST /api/v1/hw", s.reprobe)
	mux.HandleFunc("POST /api/v1/hw/{backend}/reset", s.resetHW)

	mux.HandleFunc("GET /api/v1/libraries", s.listFiles)
	mux.HandleFunc("GET /api/v1/libraries/stats", s.libraryStats)
	mux.HandleFunc("GET /api/v1/series", s.listSeries)
	mux.HandleFunc("GET /api/v1/series/rec", s.seriesRec)
	mux.HandleFunc("GET /api/v1/series/episodes", s.listEpisodes)
	mux.HandleFunc("POST /api/v1/series/queue", s.queueSeries)

	mux.HandleFunc("GET /api/v1/files/{id}", s.fileDetail)
	mux.HandleFunc("GET /api/v1/files/{id}/rec", s.fileRec)
	mux.HandleFunc("POST /api/v1/files/{id}/queue", s.queueFile)
	mux.HandleFunc("POST /api/v1/files/{id}/preview", s.createPreview)

	mux.HandleFunc("GET /api/v1/jobs", s.listJobs)
	mux.HandleFunc("GET /api/v1/jobs/{id}", s.getJob)
	mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", s.jobCancel)
	mux.HandleFunc("POST /api/v1/jobs/{id}/retry", s.jobRetry)
	mux.HandleFunc("POST /api/v1/jobs/{id}/run-now", s.jobRunNow)
	mux.HandleFunc("POST /api/v1/jobs/{id}/priority", s.jobPriority)

	mux.HandleFunc("GET /api/v1/queue/summary", s.queueSummary)
	mux.HandleFunc("POST /api/v1/queue/pause", s.queuePause)
	mux.HandleFunc("POST /api/v1/queue/resume", s.queueResume)

	mux.HandleFunc("GET /api/v1/jellyfin/status", s.jfStatus)
	mux.HandleFunc("POST /api/v1/jellyfin/sync", s.jfSync)
	mux.HandleFunc("GET /api/v1/poster/{itemid}", s.poster)
	mux.HandleFunc("GET /api/v1/probe", s.freshProbe)

	mux.HandleFunc("GET /api/v1/previews", s.listPreviews)
	mux.HandleFunc("GET /api/v1/previews/{id}", s.getPreview)
	mux.HandleFunc("GET /api/v1/previews/{id}/{clip}", s.previewClip)

	// Static UI.
	if s.ui != nil {
		mux.Handle("GET /", s.spaHandler())
	}
	return mux
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "time": time.Now().UTC()})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]any{"error": err.Error()})
}

func readJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	if dec.More() {
		return dec.Decode(v)
	}
	return nil
}

// ---- config ----

type configOut struct {
	config.Config
	JellyfinKeySet bool `json:"jellyfin_key_set"`
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.Get()
	keySet := c.JellyfinAPIKey != ""
	c.JellyfinAPIKey = ""
	writeJSON(w, http.StatusOK, configOut{Config: c, JellyfinKeySet: keySet})
}

func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	var in config.Config
	if err := readJSON(r, &in); err != nil {
		fail(w, 400, err)
		return
	}
	err := s.cfg.Update(func(cur *config.Config) {
		next := in
		// Never blank an existing key from an omitted field.
		if strings.TrimSpace(next.JellyfinAPIKey) == "" && cur.JellyfinAPIKey != "" {
			next.JellyfinAPIKey = cur.JellyfinAPIKey
		}
		if next.Workers == 0 {
			next.Workers = cur.Workers
		}
		*cur = next
	})
	if err != nil {
		fail(w, 500, err)
		return
	}
	s.eng.Kick()
	s.getConfig(w, r)
}

// ---- scan ----

func (s *Server) startScan(w http.ResponseWriter, r *http.Request) {
	if !s.scan.Start() {
		writeJSON(w, http.StatusConflict, map[string]any{"running": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"started": true})
}

func (s *Server) scanStats(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.scan.Stats())
}

// ---- hardware ----

func (s *Server) getHW(w http.ResponseWriter, r *http.Request) {
	rep := s.eng.Report()
	health := map[string]bool{}
	for _, b := range encode.AllBackends {
		health[string(b)] = s.eng.BackendDegraded(string(b))
	}
	if rep == nil {
		writeJSON(w, http.StatusOK, map[string]any{"report": nil, "health": health})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": rep, "health": health})
}

func (s *Server) reprobe(w http.ResponseWriter, r *http.Request) {
	go func() {
		if _, err := s.eng.Reprobe(); err != nil {
			fmt.Println("reprobe:", err)
		}
	}()
	writeJSON(w, http.StatusOK, map[string]any{"started": true})
}

func (s *Server) resetHW(w http.ResponseWriter, r *http.Request) {
	s.eng.ResetHealth(r.PathValue("backend"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- library browsing ----

type fileOut struct {
	*store.File
	Poster   string `json:"poster,omitempty"` // /api/v1/poster/{itemId}
	JFName   string `json:"jf_name,omitempty"`
	Overview string `json:"overview,omitempty"`
	Queued   bool   `json:"queued,omitempty"`
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.FileFilter{
		Library: q.Get("library"),
		Title:   q.Get("title"),
		Codec:   q.Get("codec"),
		HDR:     q.Get("hdr"),
	}
	if q.Get("candidates") == "1" {
		f.Candidates = true
	}
	if v := q.Get("min_height"); v != "" {
		f.MinHeight, _ = strconv.Atoi(v)
	}
	if v := q.Get("min_size"); v != "" {
		n, _ := strconv.ParseInt(v, 10, 64)
		f.MinSize = n
	}
	cursorID64, _ := strconv.ParseInt(q.Get("cursor"), 10, 64)
	cursorID := int(cursorID64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	files, more, err := s.st.ListFiles(f, q.Get("cursor_title"), cursorID, limit)
	if err != nil {
		fail(w, 500, err)
		return
	}
	out := make([]fileOut, 0, len(files))
	paths := make([]string, 0, len(files))
	for _, fl := range files {
		paths = append(paths, fl.Path)
	}
	jf, _ := s.st.JellyfinMap(paths)
	for _, fl := range files {
		fo := fileOut{File: fl}
		if row, ok := jf[fl.Path]; ok {
			fo.Poster = "/api/v1/poster/" + row.ItemID
			fo.JFName = row.Name
			fo.Overview = row.Overview
		}
		fo.Queued, _ = s.st.HasQueuedForFile(fl.Path)
		out = append(out, fo)
	}
	var next string
	if more && len(out) > 0 {
		last := out[len(out)-1]
		next = fmt.Sprintf("%d:%s", last.ID, last.Title)
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": out, "next_cursor": next})
}

func (s *Server) libraryStats(w http.ResponseWriter, r *http.Request) {
	lib := r.URL.Query().Get("library")
	st, err := s.st.LibraryStats(lib)
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) listSeries(w http.ResponseWriter, r *http.Request) {
	series, err := s.st.ListSeries(r.URL.Query().Get("title"), 0)
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"series": series})
}

func (s *Server) listEpisodes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	show := q.Get("show")
	season, _ := strconv.Atoi(q.Get("season"))
	if show == "" {
		fail(w, 400, fmt.Errorf("show required"))
		return
	}
	files, _, err := s.st.ListFiles(store.FileFilter{Library: "tvshows", Show: show, Season: season}, "", 0, 500)
	if err != nil {
		fail(w, 500, err)
		return
	}
	out := make([]fileOut, 0, len(files))
	for _, fl := range files {
		fo := fileOut{File: fl}
		fo.Queued, _ = s.st.HasQueuedForFile(fl.Path)
		out = append(out, fo)
	}
	seasons, _ := s.st.ListSeasons(show)
	writeJSON(w, http.StatusOK, map[string]any{"episodes": out, "seasons": seasons})
}

// seriesRec returns the aggregate recommendation for a show or one
// season (?season=-1 for the whole show).
func (s *Server) seriesRec(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	show := q.Get("show")
	season, _ := strconv.Atoi(q.Get("season")) // -1 or 0 means whole show
	if show == "" {
		fail(w, 400, fmt.Errorf("show required"))
		return
	}
	filter := store.FileFilter{Library: "tvshows", Show: show}
	if season > 0 {
		filter.Season = season
	}
	files, _, err := s.st.ListFiles(filter, "", 0, 1000)
	if err != nil {
		fail(w, 500, err)
		return
	}
	cfg := s.cfg.Get()
	agg := recs.Aggregate(files, cfg)
	writeJSON(w, http.StatusOK, agg)
}

func (s *Server) fileDetail(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	f, err := s.st.GetFile(id)
	if err != nil || f == nil {
		fail(w, 404, fmt.Errorf("file not found"))
		return
	}
	streams, _ := s.st.Streams(f.ID)
	jf, _ := s.st.JellyfinMap([]string{f.Path})
	fo := fileOut{File: f}
	if row, ok := jf[f.Path]; ok {
		fo.Poster = "/api/v1/poster/" + row.ItemID
		fo.JFName = row.Name
		fo.Overview = row.Overview
	}
	fo.Queued, _ = s.st.HasQueuedForFile(f.Path)
	writeJSON(w, http.StatusOK, map[string]any{"file": fo, "streams": streams})
}

func (s *Server) fileRec(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	f, err := s.st.GetFile(id)
	if err != nil || f == nil {
		fail(w, 404, fmt.Errorf("file not found"))
		return
	}
	rec := recs.Recommend(f, s.cfg.Get())
	writeJSON(w, http.StatusOK, rec)
}

// ---- queueing ----

type queueReq struct {
	Settings *encode.Settings `json:"settings,omitempty"`
	RunNow   bool             `json:"run_now,omitempty"`
	Priority int              `json:"priority,omitempty"`
}

func (s *Server) resolveSettings(f *store.File, in *encode.Settings) encode.Settings {
	cfg := s.cfg.Get()
	if in == nil {
		rec := recs.Recommend(f, cfg)
		return rec.Settings
	}
	st := *in
	if st.Quality == 0 {
		st.Quality = cfg.DefaultQuality
	}
	if st.Codec == "" {
		st.Codec = encode.Codec(cfg.DefaultCodec)
	}
	if st.AudioPCMTarget == "" {
		st.AudioPCMTarget = cfg.AudioPCMTarget
	}
	if st.Container == "" {
		st.Container = "auto"
	}
	return st
}

func (s *Server) queueFile(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	f, err := s.st.GetFile(id)
	if err != nil || f == nil {
		fail(w, 404, fmt.Errorf("file not found"))
		return
	}
	if ok, _ := s.st.HasQueuedForFile(f.Path); ok {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "already queued"})
		return
	}
	var req queueReq
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err)
		return
	}
	cfg := s.cfg.Get()
	settings := s.resolveSettings(f, req.Settings)
	priority := req.Priority
	if req.RunNow {
		priority = 0
	}
	if priority == 0 && !req.RunNow {
		priority = 100000
	}
	maxAtt := cfg.MaxAttempts
	if maxAtt <= 0 {
		maxAtt = 3
	}
	sj, _ := json.Marshal(settings)
	j := &store.Job{
		FileID: f.ID, SrcPath: f.Path, Priority: priority, RunNow: req.RunNow,
		Backend: string(settings.Backend), Codec: string(settings.Codec),
		Quality: settings.Quality, SettingsJSON: string(sj),
		MaxAttempts: maxAtt, SrcSize: f.Size,
	}
	if err := s.st.CreateJob(j); err != nil {
		fail(w, 500, err)
		return
	}
	s.hub.Broadcast("job", map[string]any{"id": j.ID, "status": store.StatusQueued})
	s.eng.Kick()
	writeJSON(w, http.StatusOK, j)
}

// queueSeries enqueues every candidate episode of a show (or season).
type queueSeriesReq struct {
	Show     string           `json:"show"`
	Season   int              `json:"season"` // -1/0 = whole show
	Settings *encode.Settings `json:"settings,omitempty"`
	RunNow   bool             `json:"run_now,omitempty"`
	OnlyWorth bool            `json:"only_worth,omitempty"` // skip non-candidates
}

func (s *Server) queueSeries(w http.ResponseWriter, r *http.Request) {
	var req queueSeriesReq
	if err := readJSON(r, &req); err != nil || req.Show == "" {
		fail(w, 400, fmt.Errorf("show required"))
		return
	}
	filter := store.FileFilter{Library: "tvshows", Show: req.Show}
	if req.Season > 0 {
		filter.Season = req.Season
	}
	files, _, err := s.st.ListFiles(filter, "", 0, 1000)
	if err != nil {
		fail(w, 500, err)
		return
	}
	cfg := s.cfg.Get()
	maxAtt := cfg.MaxAttempts
	if maxAtt <= 0 {
		maxAtt = 3
	}
	created := 0
	for _, f := range files {
		if ok, _ := s.st.HasQueuedForFile(f.Path); ok {
			continue
		}
		settings := s.resolveSettings(f, req.Settings)
		if req.OnlyWorth {
			rec := recs.Recommend(f, cfg)
			if !rec.Worth {
				continue
			}
		}
		sj, _ := json.Marshal(settings)
		priority := 100000
		if req.RunNow {
			priority = 0
		}
		j := &store.Job{
			FileID: f.ID, SrcPath: f.Path, Priority: priority, RunNow: req.RunNow,
			Backend: string(settings.Backend), Codec: string(settings.Codec),
			Quality: settings.Quality, SettingsJSON: string(sj),
			MaxAttempts: maxAtt, SrcSize: f.Size,
		}
		if err := s.st.CreateJob(j); err != nil {
			continue
		}
		created++
	}
	s.eng.Kick()
	writeJSON(w, http.StatusOK, map[string]any{"created": created})
}

// ---- jobs ----

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var statuses []string
	if v := q.Get("status"); v != "" {
		statuses = strings.Split(v, ",")
	}
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	jobs, err := s.st.ListJobs(statuses, before, limit)
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": jobs})
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	j, err := s.st.GetJob(id)
	if err != nil || j == nil {
		fail(w, 404, fmt.Errorf("job not found"))
		return
	}
	var progress any
	if j.ProgressJSON != "" {
		var p encode.Progress
		if json.Unmarshal([]byte(j.ProgressJSON), &p) == nil {
			progress = p
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"job": j, "progress": progress})
}

func (s *Server) jobCancel(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.eng.Cancel(id); err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) jobRetry(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.eng.Retry(id); err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) jobRunNow(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err := s.st.SetRunNow(id); err != nil {
		fail(w, 500, err)
		return
	}
	s.eng.Kick()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type priorityReq struct {
	Priority int `json:"priority"`
}

func (s *Server) jobPriority(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	var req priorityReq
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.st.Reprioritize(id, req.Priority); err != nil {
		fail(w, 500, err)
		return
	}
	s.eng.Kick()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- queue state ----

func (s *Server) queueSummary(w http.ResponseWriter, r *http.Request) {
	counts, _ := s.st.CountJobsByStatus()
	realized, jobsDone, _ := s.st.RealizedSavings()
	writeJSON(w, http.StatusOK, map[string]any{
		"counts": counts,
		"realized_saved": realized,
		"jobs_done": jobsDone,
		"window_open": s.cfg.WindowOpen(time.Now()),
		"paused": s.cfg.Get().Paused,
	})
}

func (s *Server) queuePause(w http.ResponseWriter, r *http.Request) {
	_ = s.cfg.Update(func(c *config.Config) { c.Paused = true })
	s.eng.Kick()
	writeJSON(w, http.StatusOK, map[string]any{"paused": true})
}

func (s *Server) queueResume(w http.ResponseWriter, r *http.Request) {
	_ = s.cfg.Update(func(c *config.Config) { c.Paused = false })
	s.eng.Kick()
	writeJSON(w, http.StatusOK, map[string]any{"paused": false})
}

// ---- preview ----

type previewReq struct {
	Settings *encode.Settings `json:"settings,omitempty"`
	Segments int              `json:"segments,omitempty"`
	Starts   []float64        `json:"starts,omitempty"`
}

func (s *Server) createPreview(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	f, err := s.st.GetFile(id)
	if err != nil || f == nil {
		fail(w, 404, fmt.Errorf("file not found"))
		return
	}
	var req previewReq
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err)
		return
	}
	settings := s.resolveSettings(f, req.Settings)
	// Resolve a real backend so previews exercise the same hw path.
	if settings.Backend == "" || settings.Backend == "auto" {
		settings.Backend = "sw"
		if rep := s.eng.Report(); rep != nil {
			if bs := rep.BackendsFor(settings.Codec); len(bs) > 0 {
				settings.Backend = bs[0]
			}
		}
	}
	manual := req.Starts
	if manual == nil && req.Segments > 0 && req.Segments <= 5 {
		manual = autoStartsFor(f.Duration, req.Segments)
	}
	p, err := s.prev.Create(f.ID, f.Path, f.Duration, settings, manual)
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func autoStartsFor(dur float64, n int) []float64 {
	var out []float64
	for i := 1; i <= n; i++ {
		out = append(out, dur*float64(i)/float64(n+1)-10)
	}
	return out
}

func (s *Server) listPreviews(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"previews": s.prev.List()})
}

func (s *Server) getPreview(w http.ResponseWriter, r *http.Request) {
	p := s.prev.Get(r.PathValue("id"))
	if p == nil {
		fail(w, 404, fmt.Errorf("preview not found"))
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) previewClip(w http.ResponseWriter, r *http.Request) {
	path, ok := s.prev.ClipPath(r.PathValue("id"), r.PathValue("clip"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Accept-Ranges", "bytes")
	http.ServeFile(w, r, path) // handles Range + Content-Type
}

// ---- jellyfin ----

func (s *Server) jfClient() *jellyfin.Client {
	c := s.cfg.Get()
	if c.JellyfinURL == "" || c.JellyfinAPIKey == "" {
		return nil
	}
	return jellyfin.New(c.JellyfinURL, c.JellyfinAPIKey)
}

func (s *Server) jfStatus(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.Get()
	out := map[string]any{
		"configured": c.JellyfinURL != "" && c.JellyfinAPIKey != "",
	}
	if cl := s.jfClient(); cl != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		if si, err := cl.Test(ctx); err == nil {
			out["server_name"] = si.ServerName
			out["version"] = si.Version
			out["connected"] = true
		} else {
			out["connected"] = false
			out["error"] = err.Error()
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) jfSync(w http.ResponseWriter, r *http.Request) {
	cl := s.jfClient()
	if cl == nil {
		fail(w, 400, fmt.Errorf("jellyfin not configured"))
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		n := 0
		err := cl.WalkItems(ctx, func(items []jellyfin.Item) error {
			rows := make([]store.JellyfinRow, 0, len(items))
			for _, it := range items {
				if it.Path == "" {
					continue
				}
				rows = append(rows, store.JellyfinRow{
					Path: it.Path, ItemID: it.ID, SeriesID: it.SeriesID, SeasonID: it.SeasonID,
					Name: it.Name, ImageTag: it.ImageTags["Primary"], Overview: it.Overview,
					UpdatedAt: time.Now().UTC().Format(time.RFC3339),
				})
			}
			if err := s.st.UpsertJF(rows); err != nil {
				return err
			}
			n += len(rows)
			s.hub.Broadcast("jellyfin", map[string]any{"synced": n})
			return nil
		})
		if err != nil {
			s.hub.Broadcast("jellyfin", map[string]any{"error": err.Error()})
			return
		}
		s.hub.Broadcast("jellyfin", map[string]any{"done": true, "synced": n})
	}()
	writeJSON(w, http.StatusOK, map[string]any{"started": true})
}

func (s *Server) poster(w http.ResponseWriter, r *http.Request) {
	itemID := r.PathValue("itemid")
	cl := s.jfClient()
	if cl == nil || itemID == "" {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	b, ct, err := cl.FetchImage(ctx, itemID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "private, max-age=86400")
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Write(b)
}

// ---- static UI ----

func (s *Server) spaHandler() http.Handler {
	fileServer := http.FileServer(http.FS(s.ui))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(s.ui, p); err != nil {
			// SPA fallback
			r2 := new(http.Request)
			*r2 = *r
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

// mediaInfo is a small helper endpoint used by the detail view to show
// a fresh probe without a rescan.
func (s *Server) freshProbe(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	if p == "" {
		fail(w, 400, fmt.Errorf("path required"))
		return
	}
	pr, err := media.ProbeFile(r.Context(), p)
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, pr)
}
