package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"mediatrans/internal/config"
	"mediatrans/internal/encode"
	"mediatrans/internal/hwprobe"
	"mediatrans/internal/recs"
	"mediatrans/internal/store"
)

// recSummary is the slim per-row recommendation shown in lists.
type recSummary struct {
	Action  string  `json:"action"`
	Savings float64 `json:"savings_pct"`
	EstOut  int64   `json:"est_out_bytes"`
	Quality int     `json:"quality"`
	Codec   string  `json:"codec"`
	Reason  string  `json:"reason"`
}

type fileOut struct {
	*store.File
	Rec      *recSummary `json:"rec,omitempty"`
	Image    string      `json:"image,omitempty"`
	Backdrop string      `json:"backdrop,omitempty"`
	JFName   string      `json:"jf_name,omitempty"`
	Overview string      `json:"overview,omitempty"`
	Genres   string      `json:"genres,omitempty"`
	Queued   bool        `json:"queued,omitempty"`
}

func imgURL(itemID, kind string, w int) string {
	if itemID == "" {
		return ""
	}
	return fmt.Sprintf("/api/v1/image/%s?kind=%s&w=%d", itemID, kind, w)
}

func (s *Server) decorate(files []*store.File) []fileOut {
	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.Path
	}
	jf, _ := s.st.JellyfinMap(paths)
	out := make([]fileOut, 0, len(files))
	for _, f := range files {
		fo := fileOut{File: f}
		if f.RecJSON != "" {
			var r recs.Recommendation
			if json.Unmarshal([]byte(f.RecJSON), &r) == nil {
				fo.Rec = &recSummary{Action: r.Action, Savings: r.Savings, EstOut: r.EstOut,
					Quality: r.Settings.Quality, Codec: string(r.Settings.Codec), Reason: r.Reason}
			}
		}
		f.RecJSON = ""
		if row, ok := jf[f.Path]; ok {
			fo.JFName, fo.Overview, fo.Genres = row.Name, row.Overview, row.Genres
			if f.Library == "tvshows" {
				fo.Image = imgURL(row.ItemID, "Primary", 480) // episode still
			} else {
				fo.Image = imgURL(row.ItemID, "Primary", 360)
				fo.Backdrop = imgURL(row.ItemID, "Backdrop", 1280)
			}
		}
		fo.Queued, _ = s.st.HasQueuedForFile(f.Path)
		out = append(out, fo)
	}
	return out
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.FileFilter{
		Library: q.Get("library"),
		Title:   q.Get("title"),
		Codec:   q.Get("codec"),
		HDR:     q.Get("hdr"),
		Season:  -1,

		Container:  q.Get("container"),
		AudioCodec: q.Get("audio"),
		Issue:      q.Get("issue"),
		ResClass:   atoi(q.Get("res")),
		Show:       q.Get("show"),
	}
	f.Candidates = q.Get("candidates") == "1"
	f.MinHeight = atoi(q.Get("min_height"))
	f.MinSize, _ = strconv.ParseInt(q.Get("min_size"), 10, 64)
	files, total, err := s.st.ListFiles(f, q.Get("sort"), atoi(q.Get("offset")), atoi(q.Get("limit")))
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": s.decorate(files), "total": total})
}

func (s *Server) libraryStats(w http.ResponseWriter, r *http.Request) {
	st, err := s.st.LibraryStats(r.URL.Query().Get("library"))
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// ---- shows ----

type seriesOut struct {
	store.Series
	Image string `json:"image,omitempty"`
}

func (s *Server) listSeries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	list, err := s.st.ListSeries(q.Get("title"), q.Get("sort"), q.Get("candidates") == "1")
	if err != nil {
		fail(w, 500, err)
		return
	}
	out := make([]seriesOut, len(list))
	for i, se := range list {
		out[i] = seriesOut{Series: se, Image: imgURL(se.SeriesID, "Primary", 360)}
	}
	writeJSON(w, http.StatusOK, map[string]any{"series": out})
}

func (s *Server) showDetail(w http.ResponseWriter, r *http.Request) {
	show := r.URL.Query().Get("title")
	seasons, err := s.st.ListSeasons(show)
	if err != nil || len(seasons) == 0 {
		fail(w, 404, fmt.Errorf("show %q not found", show))
		return
	}
	out := map[string]any{"title": show, "seasons": seasons}
	if p := s.st.FirstPathForShow(show); p != "" {
		if row := s.st.SeriesFor(p); row != nil {
			out["overview"] = row.Overview
			out["genres"] = row.Genres
			out["image"] = imgURL(row.ItemID, "Primary", 480)
			out["backdrop"] = imgURL(row.ItemID, "Backdrop", 1600)
		}
	}
	type seasonOut struct {
		store.SeasonStat
		Image string `json:"image,omitempty"`
	}
	so := make([]seasonOut, len(seasons))
	for i, se := range seasons {
		so[i] = seasonOut{SeasonStat: se, Image: imgURL(se.SeasonID, "Primary", 300)}
	}
	out["seasons"] = so
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) showEpisodes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	season := -1
	if v := q.Get("season"); v != "" {
		season = atoi(v)
	}
	files, _, err := s.st.ListFiles(store.FileFilter{Show: q.Get("title"), Season: season}, "episode", 0, 2000)
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"episodes": s.decorate(files)})
}

// ShowOverrides adjust every episode's own recommendation.
type ShowOverrides struct {
	Codec        string `json:"codec,omitempty"`
	Backend      string `json:"backend,omitempty"`
	Speed        string `json:"speed,omitempty"`
	BitDepth     int    `json:"bit_depth,omitempty"`
	MaxHeight    *int   `json:"max_height,omitempty"`
	QualityDelta int    `json:"quality_delta,omitempty"`
	Quality      int    `json:"quality,omitempty"` // fixed quality for every episode
	Container    string `json:"container,omitempty"`
	AudioPCM     string `json:"audio_pcm_target,omitempty"`
	Deinterlace  string `json:"deinterlace,omitempty"`
	TonemapHDR   *bool  `json:"tonemap_hdr,omitempty"`
	FilmGrain    *int   `json:"film_grain,omitempty"`
}

type showReq struct {
	Title     string        `json:"title"`
	Season    *int          `json:"season,omitempty"` // nil = whole show
	FileIDs   []int64       `json:"file_ids,omitempty"`
	Overrides ShowOverrides `json:"overrides"`
	RunNow    bool          `json:"run_now,omitempty"`
	OnlyWorth bool          `json:"only_worth,omitempty"`
}

func (o ShowOverrides) apply(st encode.Settings) encode.Settings {
	if o.Codec != "" {
		st.Codec = encode.Codec(o.Codec)
	}
	if o.Backend != "" {
		st.Backend = encode.Backend(o.Backend)
	}
	if o.Speed != "" {
		st.Speed = o.Speed
	}
	if o.BitDepth != 0 {
		st.BitDepth = o.BitDepth
	}
	if o.MaxHeight != nil {
		st.MaxHeight = *o.MaxHeight
	}
	if o.Quality > 0 {
		st.Quality = o.Quality
	}
	st.Quality = max(20, min(95, st.Quality+o.QualityDelta))
	if o.Container != "" {
		st.Container = o.Container
	}
	if o.AudioPCM != "" {
		st.AudioPCMTarget = o.AudioPCM
	}
	if o.Deinterlace != "" {
		st.Deinterlace = o.Deinterlace
	}
	if o.TonemapHDR != nil {
		st.TonemapHDR = *o.TonemapHDR
	}
	if o.FilmGrain != nil {
		st.FilmGrain = *o.FilmGrain
	}
	return st
}

type episodePlan struct {
	FileID  int64           `json:"file_id"`
	Action  string          `json:"action"`
	Worth   bool            `json:"worth"`
	Size    int64           `json:"size"`
	EstOut  int64           `json:"est_out_bytes"`
	Savings float64         `json:"savings_pct"`
	Setting encode.Settings `json:"settings"`
}

// planShow computes per-episode settings (own recommendation +
// overrides) and totals.
func (s *Server) planShow(req showReq) ([]*store.File, []episodePlan, recs.SeasonRec, error) {
	season := -1
	if req.Season != nil {
		season = *req.Season
	}
	files, _, err := s.st.ListFiles(store.FileFilter{Show: req.Title, Season: season}, "episode", 0, 2000)
	if err != nil {
		return nil, nil, recs.SeasonRec{}, err
	}
	if len(req.FileIDs) > 0 {
		want := map[int64]bool{}
		for _, id := range req.FileIDs {
			want[id] = true
		}
		kept := files[:0]
		for _, f := range files {
			if want[f.ID] {
				kept = append(kept, f)
			}
		}
		files = kept
	}
	cfg := s.cfg.Get()
	agg := recs.Aggregate(files, cfg)
	plans := make([]episodePlan, 0, len(files))
	var worthSize, estTotal int64
	agg.WorthCount, agg.QualityMin, agg.QualityMax = 0, 100, 0
	for _, f := range files {
		base := recs.Recommend(f, cfg)
		st := req.Overrides.apply(base.Settings)
		est := recs.Estimate(f, st, cfg)
		worth := base.Action == "transcode" && est.Worth
		plans = append(plans, episodePlan{FileID: f.ID, Action: base.Action, Worth: worth, Size: f.Size,
			EstOut: est.EstOut, Savings: est.Savings, Setting: est.Settings})
		if worth {
			agg.WorthCount++
			worthSize += f.Size
			estTotal += est.EstOut
			agg.QualityMin = min(agg.QualityMin, est.Settings.Quality)
			agg.QualityMax = max(agg.QualityMax, est.Settings.Quality)
		}
	}
	if agg.QualityMin == 100 {
		agg.QualityMin = 0
	}
	agg.WorthSize, agg.EstOutTotal = worthSize, estTotal
	agg.SavingsPct = 0
	if worthSize > 0 {
		agg.SavingsPct = (1 - float64(estTotal)/float64(worthSize)) * 100
	}
	return files, plans, agg, nil
}

func (s *Server) showPlan(w http.ResponseWriter, r *http.Request) {
	var req showReq
	if err := readJSON(r, &req); err != nil || req.Title == "" {
		fail(w, 400, fmt.Errorf("title required"))
		return
	}
	_, plans, agg, err := s.planShow(req)
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"summary": agg, "episodes": plans})
}

func (s *Server) queueShow(w http.ResponseWriter, r *http.Request) {
	var req showReq
	if err := readJSON(r, &req); err != nil || req.Title == "" {
		fail(w, 400, fmt.Errorf("title required"))
		return
	}
	files, plans, _, err := s.planShow(req)
	if err != nil {
		fail(w, 500, err)
		return
	}
	created, skipped := 0, 0
	for i, f := range files {
		p := plans[i]
		if (req.OnlyWorth && !p.Worth) || p.Action == "caution" && len(req.FileIDs) == 0 {
			skipped++
			continue
		}
		if ok, _ := s.st.HasQueuedForFile(f.Path); ok {
			skipped++
			continue
		}
		if _, err := s.enqueue(f, p.Setting, req.RunNow); err == nil {
			created++
		}
	}
	s.eng.Kick()
	writeJSON(w, http.StatusOK, map[string]any{"created": created, "skipped": skipped})
}

// ---- single file ----

func (s *Server) fileDetail(w http.ResponseWriter, r *http.Request) {
	f, err := s.st.GetFile(pathID(r))
	if err != nil || f == nil {
		fail(w, 404, fmt.Errorf("file not found"))
		return
	}
	streams, _ := s.st.Streams(f.ID)
	fo := s.decorate([]*store.File{f})[0]
	if f.Library == "tvshows" {
		if row := s.st.SeriesFor(f.Path); row != nil {
			fo.Backdrop = imgURL(row.ItemID, "Backdrop", 1600)
			if fo.Genres == "" {
				fo.Genres = row.Genres
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"file": fo, "streams": streams})
}

// planReq is used by plan/queue/preview: nil settings = recommendation.
type planReq struct {
	Settings *encode.Settings `json:"settings,omitempty"`
	RunNow   bool             `json:"run_now,omitempty"`
	Segments int              `json:"segments,omitempty"`
	Starts   []float64        `json:"starts,omitempty"`
}

// resolve returns the settings to use for f and the matching estimate.
func (s *Server) resolve(f *store.File, in *encode.Settings) (encode.Settings, recs.Recommendation) {
	cfg := s.cfg.Get()
	auto := recs.Recommend(f, cfg)
	if in == nil {
		return auto.Settings, auto
	}
	st := *in
	st.Normalize()
	est := recs.Estimate(f, st, cfg)
	est.Why = auto.Why
	est.Reason = auto.Reason
	return est.Settings, est
}

// concrete fills the backend and render node the engine would use.
func (s *Server) concrete(st encode.Settings) encode.Settings {
	if st.Backend == "" || st.Backend == "auto" {
		st.Backend = s.eng.ResolveFor("auto", st.Codec)
	} else {
		st.Backend = s.eng.ResolveFor(string(st.Backend), st.Codec)
	}
	if rep := s.eng.Report(); rep != nil && st.Backend != encode.SW {
		st.RenderNode = hwprobe.NodeFor(rep, st.Backend, st.Codec)
	}
	return st
}

// filePlan returns the recommendation (or a custom estimate) plus the
// exact ffmpeg command it would run.
func (s *Server) filePlan(w http.ResponseWriter, r *http.Request) {
	f, err := s.st.GetFile(pathID(r))
	if err != nil || f == nil {
		fail(w, 404, fmt.Errorf("file not found"))
		return
	}
	var req planReq
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err)
		return
	}
	st, rec := s.resolve(f, req.Settings)
	auto := recs.Recommend(f, s.cfg.Get())
	out := map[string]any{"rec": rec, "auto": auto}
	cst := s.concrete(st)
	if src, err := probeOf(r, f.Path); err == nil {
		prim, fb, err := encode.Build(cst, src, "/path/to/output", nil)
		if err != nil {
			out["command_error"] = err.Error()
		} else {
			out["command"] = encode.CommandString(prim.Args)
			out["container"] = prim.Container
			if fb != nil {
				out["fallback_command"] = encode.CommandString(fb.Args)
			}
		}
	} else {
		out["command_error"] = err.Error()
	}
	out["backend"] = cst.Backend
	out["render_node"] = cst.RenderNode
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) enqueue(f *store.File, st encode.Settings, runNow bool) (*store.Job, error) {
	cfg := s.cfg.Get()
	sj, _ := json.Marshal(st)
	priority := 100000
	if runNow {
		priority = 0
	}
	j := &store.Job{
		FileID: f.ID, SrcPath: f.Path, Priority: priority, RunNow: runNow,
		Backend: string(st.Backend), Codec: string(st.Codec), Quality: st.Quality,
		SettingsJSON: string(sj), MaxAttempts: max(1, cfg.MaxAttempts), SrcSize: f.Size,
	}
	if st.VideoCopy {
		j.Backend, j.Codec, j.Quality = "remux", f.VideoCodec, 0
	}
	if err := s.st.CreateJob(j); err != nil {
		return nil, err
	}
	s.hub.Broadcast("job", map[string]any{"id": j.ID, "status": store.StatusQueued})
	return j, nil
}

func (s *Server) queueFile(w http.ResponseWriter, r *http.Request) {
	f, err := s.st.GetFile(pathID(r))
	if err != nil || f == nil {
		fail(w, 404, fmt.Errorf("file not found"))
		return
	}
	if ok, _ := s.st.HasQueuedForFile(f.Path); ok {
		fail(w, http.StatusConflict, fmt.Errorf("already in the queue"))
		return
	}
	var req planReq
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err)
		return
	}
	st, _ := s.resolve(f, req.Settings)
	j, err := s.enqueue(f, st, req.RunNow)
	if err != nil {
		fail(w, 500, err)
		return
	}
	s.eng.Kick()
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) createPreview(w http.ResponseWriter, r *http.Request) {
	f, err := s.st.GetFile(pathID(r))
	if err != nil || f == nil {
		fail(w, 404, fmt.Errorf("file not found"))
		return
	}
	var req planReq
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err)
		return
	}
	st, _ := s.resolve(f, req.Settings)
	st = s.concrete(st)
	manual := req.Starts
	if manual == nil && req.Segments > 1 && req.Segments <= 5 {
		for i := 1; i <= req.Segments; i++ {
			manual = append(manual, f.Duration*float64(i)/float64(req.Segments+1)-10)
		}
	}
	p, err := s.prev.Create(f.ID, f.Path, f.Duration, st, manual)
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

var _ = config.Default
