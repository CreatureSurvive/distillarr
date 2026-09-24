// Package api exposes the REST surface and serves the embedded UI.
package api

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"mediatrans/internal/config"
	"mediatrans/internal/encode"
	"mediatrans/internal/hwprobe"
	"mediatrans/internal/jobs"
	"mediatrans/internal/media"
	"mediatrans/internal/preview"
	"mediatrans/internal/recs"
	"mediatrans/internal/scan"
	"mediatrans/internal/still"
	"mediatrans/internal/store"
)

// Server wires every subsystem into HTTP routes.
type Server struct {
	st    *store.Store
	cfg   *config.Manager
	scan  *scan.Scanner
	eng   *jobs.Engine
	prev  *preview.Manager
	still *still.Manager
	hub   *Hub
	ui    fs.FS // embedded frontend (web/dist)

	// scanWasRunning is scan.Stats.Running as of the last Progress call,
	// used to fire the post-scan arr sync on the true->false transition
	// only. Stats.Running is also false during every single-file
	// ProbeSingle reprobe (after each finished job) — without edge
	// detection here, that fires a full arr sync on every job completion
	// instead of once per actual library scan.
	scanWasRunning atomic.Bool

	// diskPressure is the last state DiskPressureLoop computed:
	// true once any library's filesystem is at or below
	// Config.DiskPressurePct. Read by autopilotBacklog (budget
	// multiplier, quick-fix-first ordering) and the intake promoter
	// (job priority), and reported by GET /api/v1/system.
	diskPressure atomic.Bool
}

func NewServer(st *store.Store, cfg *config.Manager, sc *scan.Scanner,
	eng *jobs.Engine, pv *preview.Manager, stl *still.Manager, ui fs.FS) *Server {
	s := &Server{st: st, cfg: cfg, scan: sc, eng: eng, prev: pv, still: stl, hub: NewHub(), ui: ui}
	sc.Progress = func(st scan.Stats) {
		s.hub.Broadcast("scan", st)
		// A full scan pass just finished (true->false edge only — see
		// scanWasRunning's doc comment): files it found or updated need
		// an owning instance worked out.
		wasRunning := s.scanWasRunning.Swap(st.Running)
		if wasRunning && !st.Running && len(s.cfg.Get().ArrInstances) > 0 {
			s.TriggerArrSync()
		}
	}
	eng.Notify = func(event string, payload any) { s.hub.Broadcast(event, payload) }
	return s
}

// Hub exposes the SSE hub.
func (s *Server) Hub() *Hub { return s.hub }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /api/v1/health", s.health)
	mux.HandleFunc("GET /api/v1/events", s.hub.ServeHTTP)
	mux.HandleFunc("GET /api/v1/system", s.system)

	mux.HandleFunc("GET /api/v1/config", s.getConfig)
	mux.HandleFunc("PUT /api/v1/config", s.putConfig)

	mux.HandleFunc("POST /api/v1/scan", s.startScan)
	mux.HandleFunc("GET /api/v1/scan", s.scanStats)

	mux.HandleFunc("GET /api/v1/hw", s.getHW)
	mux.HandleFunc("POST /api/v1/hw", s.reprobe)
	mux.HandleFunc("POST /api/v1/hw/{backend}/reset", s.resetHW)

	// library
	mux.HandleFunc("GET /api/v1/libraries", s.listFiles)
	mux.HandleFunc("GET /api/v1/libraries/stats", s.libraryStats)
	mux.HandleFunc("GET /api/v1/series", s.listSeries)
	mux.HandleFunc("GET /api/v1/show", s.showDetail)
	mux.HandleFunc("GET /api/v1/show/episodes", s.showEpisodes)
	mux.HandleFunc("POST /api/v1/show/plan", s.showPlan)
	mux.HandleFunc("POST /api/v1/show/queue", s.queueShow)

	mux.HandleFunc("GET /api/v1/files/{id}", s.fileDetail)
	mux.HandleFunc("POST /api/v1/files/{id}/plan", s.filePlan)
	mux.HandleFunc("POST /api/v1/files/{id}/queue", s.queueFile)
	mux.HandleFunc("POST /api/v1/files/{id}/fix", s.fixFile)
	mux.HandleFunc("POST /api/v1/files/{id}/lang-exempt", s.setLangExempt)
	mux.HandleFunc("POST /api/v1/files/{id}/sidecar-mode", s.setSidecarMode)
	mux.HandleFunc("GET /api/v1/langprune/report", s.langpruneReportHandler)
	mux.HandleFunc("POST /api/v1/langprune/apply", s.langpruneApplyHandler)
	mux.HandleFunc("GET /api/v1/issues", s.issueSummary)
	mux.HandleFunc("GET /api/v1/stats", func(w http.ResponseWriter, r *http.Request) {
		h, err := s.st.HistoryStats()
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, http.StatusOK, h)
	})
	mux.HandleFunc("GET /api/v1/measure", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, s.eng.MeasureStatus())
	})
	mux.HandleFunc("GET /api/v1/issues/mixed", s.mixedSeasons)
	mux.HandleFunc("POST /api/v1/issues/{key}/fix", s.fixIssue)
	mux.HandleFunc("GET /api/v1/libraries/composition", s.composition)
	mux.HandleFunc("POST /api/v1/files/{id}/preview", s.createPreview)
	mux.HandleFunc("GET /api/v1/files/{id}/upscale", s.upscaleInfo)
	mux.HandleFunc("POST /api/v1/files/{id}/still", s.makeStill)
	mux.HandleFunc("GET /api/v1/stills/{key}/{name}", s.stillFile)

	// jobs + queue
	mux.HandleFunc("GET /api/v1/jobs", s.listJobs)
	mux.HandleFunc("GET /api/v1/jobs/{id}", s.getJob)
	mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", s.jobCancel)
	mux.HandleFunc("POST /api/v1/jobs/{id}/retry", s.jobRetry)
	mux.HandleFunc("POST /api/v1/jobs/{id}/run-now", s.jobRunNow)
	mux.HandleFunc("POST /api/v1/jobs/{id}/move", s.jobMove)
	mux.HandleFunc("GET /api/v1/queue/summary", s.queueSummary)
	mux.HandleFunc("POST /api/v1/queue/pause", s.queuePause)
	mux.HandleFunc("POST /api/v1/queue/resume", s.queueResume)
	mux.HandleFunc("POST /api/v1/queue/clear", s.queueClear)

	// intake: settle delay + needs-confirmation holding area
	mux.HandleFunc("GET /api/v1/intake", s.intakeList)
	mux.HandleFunc("POST /api/v1/intake/{id}/approve", s.intakeApprove)
	mux.HandleFunc("POST /api/v1/intake/{id}/dismiss", s.intakeDismiss)
	mux.HandleFunc("POST /api/v1/intake/approve", s.intakeApproveBulk)
	mux.HandleFunc("POST /api/v1/intake/dismiss", s.intakeDismissBulk)

	// autopilot
	mux.HandleFunc("GET /api/v1/autopilot/preview", s.autopilotPreview)
	mux.HandleFunc("POST /api/v1/autopilot/backlog", s.autopilotBacklog)

	// trash
	mux.HandleFunc("GET /api/v1/trash", s.listTrash)
	mux.HandleFunc("POST /api/v1/trash/{id}/restore", s.restoreTrash)
	mux.HandleFunc("DELETE /api/v1/trash/{id}", s.deleteTrash)
	mux.HandleFunc("POST /api/v1/trash/purge", s.purgeTrash)

	// jellyfin
	mux.HandleFunc("GET /api/v1/jellyfin/status", s.jfStatus)
	mux.HandleFunc("POST /api/v1/jellyfin/test", s.jfTest)
	mux.HandleFunc("POST /api/v1/jellyfin/sync", s.jfSync)
	mux.HandleFunc("GET /api/v1/image/{itemid}", s.image)

	// plex
	mux.HandleFunc("GET /api/v1/plex/status", s.plexStatus)
	mux.HandleFunc("POST /api/v1/plex/test", s.plexTest)
	mux.HandleFunc("POST /api/v1/plex/sync", s.plexSync)

	// sonarr / radarr
	mux.HandleFunc("GET /api/v1/arr/{id}", s.arrGet)
	mux.HandleFunc("POST /api/v1/arr/{id}/test", s.arrTest)
	mux.HandleFunc("POST /api/v1/arr/{id}/ack", s.arrAck)
	mux.HandleFunc("POST /api/v1/arr/{id}/rename-tag", s.arrRenameTag)
	mux.HandleFunc("POST /api/v1/arr/{id}/regenerate-webhook-token", s.arrRegenerateWebhookToken)
	mux.HandleFunc("POST /api/v1/arr/sync", s.arrSyncNow)
	// authenticated by its own per-instance token (see arrWebhook),
	// so app-wide auth must leave it reachable.
	mux.HandleFunc("POST /api/v1/hooks/arr/{instanceID}", s.arrWebhook)

	// previews
	mux.HandleFunc("GET /api/v1/previews", s.listPreviews)
	mux.HandleFunc("GET /api/v1/previews/{id}", s.getPreview)
	mux.HandleFunc("GET /api/v1/previews/{id}/{clip}", s.previewClip)

	mux.HandleFunc("GET /api/v1/calibration", s.calibration)

	// maintenance: reset caches/derived state that a pipeline change can
	// make stale, without touching the media files themselves.
	mux.HandleFunc("POST /api/v1/maintenance/measurements/clear", s.clearMeasurements)
	mux.HandleFunc("POST /api/v1/maintenance/history/clear", s.clearHistory)
	mux.HandleFunc("POST /api/v1/maintenance/calibration/clear", s.clearCalibration)
	mux.HandleFunc("POST /api/v1/maintenance/crop/clear", s.clearCrop)
	mux.HandleFunc("POST /api/v1/maintenance/issues/clear", s.clearIssueTags)

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
	if err := dec.Decode(v); err != nil && err.Error() != "EOF" {
		return err
	}
	return nil
}

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

// ---- system ----

func diskUsage(path string) map[string]int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return nil
	}
	return map[string]int64{
		"total": int64(st.Blocks) * st.Bsize,
		"free":  int64(st.Bavail) * st.Bsize,
	}
}

func (s *Server) system(w http.ResponseWriter, r *http.Request) {
	items, _ := s.st.ListTrash()
	var trashBytes int64
	for _, t := range items {
		trashBytes += t.Size
	}
	checked, total, bars := s.st.CropProgress()
	writeJSON(w, http.StatusOK, map[string]any{
		"crop":          map[string]int{"checked": checked, "total": total, "with_bars": bars},
		"media":         diskUsage("/srv/media"),
		"config":        diskUsage("/config"),
		"trash_bytes":   trashBytes,
		"trash_count":   len(items),
		"disk_pressure": s.diskPressure.Load(),
	})
}

// ---- config ----

type configOut struct {
	config.Config
	JellyfinKeySet bool             `json:"jellyfin_key_set"`
	PlexTokenSet   bool             `json:"plex_token_set"`
	ArrInstances   []arrInstanceOut `json:"arr_instances"`
}

func (s *Server) getConfig(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.Get()
	keySet := c.JellyfinAPIKey != ""
	c.JellyfinAPIKey = ""
	plexTokSet := c.PlexToken != ""
	c.PlexToken = ""
	arrOuts := make([]arrInstanceOut, len(c.ArrInstances))
	for i, inst := range c.ArrInstances {
		arrOuts[i] = arrOut(inst)
	}
	writeJSON(w, http.StatusOK, configOut{Config: c, JellyfinKeySet: keySet, PlexTokenSet: plexTokSet, ArrInstances: arrOuts})
}

// putConfig merges a partial JSON object into the config: only the
// fields present in the body change.
func (s *Server) putConfig(w http.ResponseWriter, r *http.Request) {
	var patch map[string]json.RawMessage
	if err := readJSON(r, &patch); err != nil {
		fail(w, 400, err)
		return
	}
	if k, ok := patch["jellyfin_api_key"]; ok {
		var key string
		_ = json.Unmarshal(k, &key)
		if strings.TrimSpace(key) == "" {
			delete(patch, "jellyfin_api_key") // blank never erases a saved key
		}
	}
	delete(patch, "jellyfin_key_set")
	if k, ok := patch["plex_token"]; ok {
		var tok string
		_ = json.Unmarshal(k, &tok)
		if strings.TrimSpace(tok) == "" {
			delete(patch, "plex_token") // blank never erases a saved token
		}
	}
	delete(patch, "plex_token_set")
	var perr error
	err := s.cfg.Update(func(cur *config.Config) {
		if raw, ok := patch["arr_instances"]; ok {
			merged, err := mergeArrInstances(cur.ArrInstances, raw)
			if err != nil {
				perr = err
				return
			}
			b, _ := json.Marshal(merged)
			patch["arr_instances"] = b
		}
		b, _ := json.Marshal(cur)
		var merged map[string]json.RawMessage
		_ = json.Unmarshal(b, &merged)
		for k, v := range patch {
			merged[k] = v
		}
		b, _ = json.Marshal(merged)
		next := *cur
		if perr = json.Unmarshal(b, &next); perr != nil {
			return
		}
		if perr = validateLangPolicy(next.LangPolicy); perr != nil {
			return
		}
		*cur = next
	})
	if err == nil {
		err = perr
	}
	if err != nil {
		fail(w, 400, err)
		return
	}
	s.eng.Kick()
	for _, k := range []string{"default_codec", "default_quality", "preferred_backend", "min_savings_pct",
		"audio_pcm_target", "recompress_hevc", "max_height", "tonemap_hdr", "default_speed", "prefer_mp4", "crop_bars",
		"lang_policy", "lang_library_overrides", "lang_instance_overrides", "subs_sidecar_mode"} {
		if _, ok := patch[k]; ok {
			s.scan.RefreshRecsSoon()
			break
		}
	}
	s.getConfig(w, r)
}

// validateLangPolicy rejects turning on report/apply mode while its
// matching keep list is empty — Select would otherwise drop every track
// in that kind, which is never what silently saving an empty list means.
func validateLangPolicy(p config.LangPolicy) error {
	if p.AudioMode != "" && p.AudioMode != "off" && len(p.AudioKeep) == 0 {
		return fmt.Errorf("lang_policy: audio_mode needs at least one language in audio_keep")
	}
	if p.SubsMode != "" && p.SubsMode != "off" && len(p.SubsKeep) == 0 {
		return fmt.Errorf("lang_policy: subs_mode needs at least one language in subs_keep")
	}
	return nil
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
	health := map[string]bool{}
	for _, b := range encode.AllBackends {
		health[string(b)] = s.eng.BackendDegraded(string(b))
	}
	rep := s.eng.Report()
	pref := s.cfg.Get().PreferredBackend
	resolved := map[string]string{}
	active := map[string]hwprobe.Device{} // what an encode would run on now, by codec
	for _, c := range []encode.Codec{encode.HEVC, encode.AV1} {
		resolved[string(c)] = string(s.eng.ResolveFor("auto", c))
		active[string(c)] = rep.ActiveDevice(s.eng.ResolveFor(pref, c), c)
	}
	writeJSON(w, http.StatusOK, map[string]any{"report": rep, "health": health, "auto": resolved,
		"devices": rep.Devices(), "active": active, "upscale_device": rep.UpscaleDevice()})
}

func (s *Server) reprobe(w http.ResponseWriter, r *http.Request) {
	go func() {
		if _, err := s.eng.Reprobe(); err != nil {
			log.Printf("reprobe: %v", err)
		}
		s.scan.RefreshRecsSoon()
	}()
	writeJSON(w, http.StatusOK, map[string]any{"started": true})
}

func (s *Server) resetHW(w http.ResponseWriter, r *http.Request) {
	s.eng.ResetHealth(r.PathValue("backend"))
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) calibration(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, recs.CalibrationSummary())
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
	list, err := s.st.ListJobs(statuses, before, limit)
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": list})
}

func (s *Server) getJob(w http.ResponseWriter, r *http.Request) {
	j, err := s.st.GetJob(pathID(r))
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
	if err := s.eng.Cancel(pathID(r)); err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) jobRetry(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConfirmHardlinked bool `json:"confirm_hardlinked,omitempty"`
	}
	_ = readJSON(r, &req)
	var err error
	if req.ConfirmHardlinked {
		err = s.eng.RetryConfirmHardlinked(pathID(r))
	} else {
		err = s.eng.Retry(pathID(r))
	}
	if err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) jobRunNow(w http.ResponseWriter, r *http.Request) {
	if err := s.st.SetRunNow(pathID(r)); err != nil {
		fail(w, 500, err)
		return
	}
	s.eng.Kick()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) jobMove(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Before int64 `json:"before"` // 0 = move to end
	}
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.st.MoveJob(pathID(r), req.Before); err != nil {
		fail(w, 500, err)
		return
	}
	s.hub.Broadcast("queue", map[string]any{"moved": pathID(r)})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) queueSummary(w http.ResponseWriter, r *http.Request) {
	counts, _ := s.st.CountJobsByStatus()
	realized, jobsDone, _ := s.st.RealizedSavings()
	cfg := s.cfg.Get()
	writeJSON(w, http.StatusOK, map[string]any{
		"counts":              counts,
		"realized_saved":      realized,
		"jobs_done":           jobsDone,
		"window_open":         s.cfg.WindowOpen(time.Now()),
		"paused":              cfg.Paused,
		"viewers_transcoding": s.eng.Transcoding(),
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

// queueClear cancels every pending (not running) job.
func (s *Server) queueClear(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListJobs([]string{store.StatusQueued}, 0, 200)
	if err != nil {
		fail(w, 500, err)
		return
	}
	for _, j := range list {
		_ = s.eng.Cancel(j.ID)
	}
	writeJSON(w, http.StatusOK, map[string]any{"canceled": len(list)})
}

// ---- trash ----

func (s *Server) listTrash(w http.ResponseWriter, r *http.Request) {
	items, err := s.st.ListTrash()
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "retention_days": s.cfg.Get().TrashDays})
}

func (s *Server) restoreTrash(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.RestoreTrash(pathID(r)); err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) deleteTrash(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.DeleteTrashItem(pathID(r)); err != nil {
		fail(w, 400, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) purgeTrash(w http.ResponseWriter, r *http.Request) {
	freed, n := s.eng.PurgeTrash(r.URL.Query().Get("all") == "1")
	writeJSON(w, http.StatusOK, map[string]any{"freed": freed, "count": n})
}

// ---- previews ----

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
	http.ServeFile(w, r, path)
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
			r2 := new(http.Request)
			*r2 = *r
			r2.URL.Path = "/"
			fileServer.ServeHTTP(w, r2)
			return
		}
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// probeOf is a shared helper for handlers that need a fresh probe.
func probeOf(r *http.Request, path string) (*media.Probe, error) {
	return media.ProbeFile(r.Context(), path)
}
