// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/hwprobe"
	"github.com/CreatureSurvive/distillarr/internal/imagesubs"
	"github.com/CreatureSurvive/distillarr/internal/issues"
	"github.com/CreatureSurvive/distillarr/internal/recs"
	"github.com/CreatureSurvive/distillarr/internal/store"
	"github.com/CreatureSurvive/distillarr/internal/upscale"
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
	// Upscaled is set when this file is the output of a finished upscale job.
	Upscaled *store.UpscaleRecord `json:"upscaled,omitempty"`
	// Arr is set when a connected Sonarr/Radarr instance manages this file.
	Arr *arrItemOut `json:"arr,omitempty"`
	// OCR is the last OCR attempt on this file's image subtitle track
	// parsed from the internal-only OCRJSON cache.
	OCR *imagesubs.OCRResult `json:"ocr,omitempty"`
	// UpgradeLoopAt is when this file was put on the upgrade-loop skip
	// list; "" when it isn't.
	UpgradeLoopAt string `json:"upgrade_loop_at,omitempty"`
}

// arrItemOut is a store.ArrItem shaped for display: the instance name and
// tag labels resolved instead of bare ids.
type arrItemOut struct {
	InstanceID       string   `json:"instance_id"`
	InstanceName     string   `json:"instance_name"`
	Kind             string   `json:"kind"`
	Monitored        bool     `json:"monitored"`
	CutoffNotMet     bool     `json:"cutoff_not_met"`
	CFScore          int      `json:"cf_score"`
	TagNames         []string `json:"tag_names,omitempty"`
	OriginalLanguage string   `json:"original_language,omitempty"`
	SeriesStatus     string   `json:"series_status,omitempty"`
}

// arrItemOuts resolves a batch of arr_items rows against the current
// instance list and each instance's cached tag names, for one API
// response — so instance/tag lookups happen once per response, not once
// per file.
func (s *Server) arrItemOuts(items map[int64]store.ArrItem) map[int64]arrItemOut {
	if len(items) == 0 {
		return nil
	}
	names := map[string]string{}
	for _, inst := range s.cfg.Get().ArrInstances {
		names[inst.ID] = inst.Name
	}
	tagCache := map[string]map[int64]string{}
	out := make(map[int64]arrItemOut, len(items))
	for fid, a := range items {
		tags, ok := tagCache[a.InstanceID]
		if !ok {
			tags = s.st.ArrTags(a.InstanceID)
			tagCache[a.InstanceID] = tags
		}
		var tagNames []string
		for _, id := range a.TagIDs() {
			if n, ok := tags[id]; ok {
				tagNames = append(tagNames, n)
			}
		}
		out[fid] = arrItemOut{
			InstanceID: a.InstanceID, InstanceName: names[a.InstanceID], Kind: a.Kind,
			Monitored: a.Monitored, CutoffNotMet: a.CutoffNotMet, CFScore: a.CFScore,
			TagNames: tagNames, OriginalLanguage: a.OriginalLanguage, SeriesStatus: a.SeriesStatus,
		}
	}
	return out
}

func imgURL(itemID, kind string, w int) string {
	if itemID == "" {
		return ""
	}
	return fmt.Sprintf("/api/v1/image/%s?kind=%s&w=%d", itemID, kind, w)
}

func (s *Server) decorate(files []*store.File) []fileOut {
	paths := make([]string, len(files))
	ids := make([]int64, len(files))
	for i, f := range files {
		paths[i] = f.Path
		ids[i] = f.ID
	}
	jf, _ := s.st.JellyfinMap(paths)
	ups, _ := s.st.UpscaledFiles(paths)
	arrItems, _ := s.st.ArrItemsMap(ids)
	arrOuts := s.arrItemOuts(arrItems)
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
		if u, ok := ups[f.Path]; ok {
			fo.Upscaled = &u
		}
		if a, ok := arrOuts[f.ID]; ok {
			fo.Arr = &a
			fo.UpgradeLoopAt, _, _ = s.st.KVGet(upgradeLoopKey(f.ID))
		}
		if r, ok := imagesubs.UnmarshalResult(f.OCRJSON); ok {
			fo.OCR = &r
		}
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
		Upscale:    q.Get("upscale"),
		Hardlinked: q.Get("hardlinked"),
		Managed:    q.Get("managed"),
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
	Title             string        `json:"title"`
	Season            *int          `json:"season,omitempty"` // nil = whole show
	FileIDs           []int64       `json:"file_ids,omitempty"`
	Overrides         ShowOverrides `json:"overrides"`
	RunNow            bool          `json:"run_now,omitempty"`
	OnlyWorth         bool          `json:"only_worth,omitempty"`
	ConfirmHardlinked bool          `json:"confirm_hardlinked,omitempty"`
	SkipHardlinked    bool          `json:"skip_hardlinked,omitempty"`
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
	// Which of these would actually be queued (same conditions as the
	// second pass below), for the hardlinked pre-check: refusing must
	// queue nothing, so it has to happen before any job is created.
	wouldQueue := func(i int, f *store.File) bool {
		p := plans[i]
		if (req.OnlyWorth && !p.Worth) || p.Action == "caution" && len(req.FileIDs) == 0 {
			return false
		}
		if ok, _ := s.st.HasQueuedForFile(f.Path); ok {
			return false
		}
		return true
	}
	if !req.ConfirmHardlinked && !req.SkipHardlinked {
		var linked []*store.File
		for i, f := range files {
			if wouldQueue(i, f) && f.Nlink > 1 {
				linked = append(linked, f)
			}
		}
		if len(linked) > 0 {
			writeHardlinkedConflict(w, linked)
			return
		}
	}
	created, skipped := 0, 0
	for i, f := range files {
		if !wouldQueue(i, f) {
			skipped++
			continue
		}
		if req.SkipHardlinked && f.Nlink > 1 {
			skipped++
			continue
		}
		st := plans[i].Setting
		if f.Nlink > 1 {
			st.ConfirmedHardlinked = true
		}
		if _, err := s.enqueue(f, st, req.RunNow, "manual", "Queued from the show page"); err == nil {
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
	resp := map[string]any{"file": fo, "streams": streams}
	settings, rec := s.resolve(f, nil)
	if s.cfg.Get().AutopilotEnabled {
		// "manual" here just means "not evaluated as part of an intake
		// row" — this is a file-page preview of what autopilot would
		// decide, not a real promotion.
		if d := s.autopilotDecision(f, rec, "manual"); d != nil {
			resp["autopilot"] = d
		}
	}
	// Informational only: manual queueing is never blocked by the
	// codec-penalty gate, but the file page still warns before the user
	// clicks Queue — the gate itself only holds autopilot/webhook-intake.
	if blocked, instName := s.codecPenaltyBlocked(f, settings); blocked {
		resp["codec_penalty_warning"] = instName
	}
	// forces_transcode detail: only fetched for the one file this
	// page is showing, not the bulk list — reasons/counts a client
	// couldn't play this file directly for, in the lookback window.
	for _, k := range issues.Decode(f.Issues) {
		if k == "forces_transcode" {
			if d, _ := s.st.ForcesTranscodeDetail(f.ID, issues.ForcesTranscodeLookbackDays); d != nil {
				resp["forces_transcode"] = d
			}
			break
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// planReq is used by plan/queue/preview: nil settings = recommendation.
type planReq struct {
	Settings          *encode.Settings `json:"settings,omitempty"`
	RunNow            bool             `json:"run_now,omitempty"`
	Segments          int              `json:"segments,omitempty"`
	Starts            []float64        `json:"starts,omitempty"`
	ConfirmHardlinked bool             `json:"confirm_hardlinked,omitempty"`
}

// hardlinkedOut is one file reported in a "hardlinked" 409 response.
type hardlinkedOut struct {
	ID    int64  `json:"id"`
	Path  string `json:"path"`
	Nlink int    `json:"nlink"`
}

// writeHardlinkedConflict refuses to queue files that share their data
// with another link (usually a seeding torrent): replacing them frees no
// space until the other link is gone, so queueing needs an explicit
// confirmation. Nothing is queued when this is written.
func writeHardlinkedConflict(w http.ResponseWriter, files []*store.File) {
	out := make([]hardlinkedOut, len(files))
	for i, f := range files {
		out[i] = hardlinkedOut{ID: f.ID, Path: f.Path, Nlink: f.Nlink}
	}
	writeJSON(w, http.StatusConflict, map[string]any{"error": "hardlinked", "files": out})
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
	if st.UpscaleTo > 0 {
		// Vulkan picks by index, not render node: prefer the encode node's own
		// GPU so decode, upscale and encode stay on one device.
		if d := hwprobe.BestVulkan(s.eng.Report(), st.RenderNode); d != nil {
			st.VulkanDevice = d.Index
		}
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
			out["warnings"] = encode.AudioWarnings(cst.AudioRules, src.Audios(), prim.Container)
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

// enqueue creates a job. origin records why it exists ("manual",
// "issue-fix", "upscale", and later "webhook" / "autopilot" / "playback");
// reason is a short free-text note shown in the UI and may be "".
// ErrAlreadyReencoded refuses a second re-encode of the same file.
var ErrAlreadyReencoded = errors.New("already re-encoded by Distillarr; restore the original from the trash first to encode it again")

func (s *Server) enqueue(f *store.File, st encode.Settings, runNow bool, origin, reason string) (*store.Job, error) {
	// Re-encoding Distillarr's own output only loses quality. Remuxes,
	// OCR and upscales don't touch the encoded video that way.
	if !st.VideoCopy && st.UpscaleTo <= 0 && st.Backend != "ocr" && s.st.HasReencode(f.ID) {
		return nil, ErrAlreadyReencoded
	}
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
		Origin: origin, Reason: reason,
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
	if f.Nlink > 1 && !req.ConfirmHardlinked {
		writeHardlinkedConflict(w, []*store.File{f})
		return
	}
	st, _ := s.resolve(f, req.Settings)
	if f.Nlink > 1 {
		st.ConfirmedHardlinked = true
	}
	if err := s.checkUpscale(r, f, &st); err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	origin, reason := "manual", ""
	if st.UpscaleTo > 0 {
		origin = "upscale"
	}
	j, err := s.enqueue(f, st, req.RunNow, origin, reason)
	if err != nil {
		enqueueFail(w, err)
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
	if err := s.checkUpscale(r, f, &st); err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	if p, ok := upscale.Get(st.UpscalePreset); ok && st.UpscaleTo > 0 && p.Neural() {
		// A clip would need the whole chunked pipeline; the single-frame still
		// shows the same detail in seconds.
		fail(w, http.StatusConflict, fmt.Errorf("clip previews aren't available for neural presets: use the frame preview to judge the detail"))
		return
	}
	st = s.concrete(st)
	// req.Segments picks a manual, evenly-spread sample count - only send
	// it when the caller actually wants fixed positions. It bypasses
	// Manager.Create's smart sampling entirely (packet-size scan for a
	// VMAF-target search), which found this the hard way: FileDetail's
	// "Measure & compare" button used to send segments:3 unconditionally,
	// silently defeating every sample-placement improvement for its whole
	// lifetime. Leave req.Starts/req.Segments both unset to get the smart
	// default (3 samples).
	manual := req.Starts
	if manual == nil && req.Segments > 1 && req.Segments <= 5 {
		for i := 1; i <= req.Segments; i++ {
			manual = append(manual, f.Duration*float64(i)/float64(req.Segments+1)-10)
		}
	}
	cambi := recs.IsAnimation(f) || st.TonemapHDR
	p, err := s.prev.Create(f.ID, f.Path, f.Duration, st, manual, cambi)
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

var _ = config.Default

// enqueueFail answers a refused enqueue: 409 for a file that's already
// queued or already re-encoded, 500 otherwise.
func enqueueFail(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrActiveJob) || errors.Is(err, ErrAlreadyReencoded) {
		fail(w, http.StatusConflict, err)
		return
	}
	fail(w, 500, err)
}
