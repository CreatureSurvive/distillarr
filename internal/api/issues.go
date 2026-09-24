package api

import (
	"fmt"
	"net/http"

	"github.com/CreatureSurvive/distillarr/internal/issues"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

type issueSummary struct {
	issues.Type
	Movies   int   `json:"movies"`
	Episodes int   `json:"episodes"`
	Bytes    int64 `json:"bytes"`
}

// GET /api/v1/issues — every issue type with library-wide counts.
func (s *Server) issueSummary(w http.ResponseWriter, r *http.Request) {
	var keys []string
	for _, t := range issues.Types {
		if t.Scope == "file" {
			keys = append(keys, t.Key)
		}
	}
	counts, err := s.st.IssueCounts(keys)
	if err != nil {
		fail(w, 500, err)
		return
	}
	mixed, err := s.st.MixedSeasons("")
	if err != nil {
		fail(w, 500, err)
		return
	}
	out := []issueSummary{}
	for _, t := range issues.Types {
		is := issueSummary{Type: t}
		if c := counts[t.Key]; c != nil {
			is.Movies, is.Episodes, is.Bytes = c.Movies, c.Episodes, c.Bytes
		}
		if t.Key == "mixed_season" {
			is.Episodes = len(mixed) // seasons, not files
			for _, m := range mixed {
				is.Bytes += m.Size
			}
		}
		out = append(out, is)
	}
	writeJSON(w, http.StatusOK, map[string]any{"types": out})
}

// GET /api/v1/issues/mixed?show= — seasons with mixed formats.
func (s *Server) mixedSeasons(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.MixedSeasons(r.URL.Query().Get("show"))
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"seasons": list})
}

// GET /api/v1/libraries/composition?library= — filter facets.
func (s *Server) composition(w http.ResponseWriter, r *http.Request) {
	c, err := s.st.LibraryComposition(r.URL.Query().Get("library"))
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// quickJob queues a remux that fixes every quick issue on f.
func (s *Server) quickJob(f *store.File, runNow bool) (*store.Job, error) {
	if !issues.HasQuick(issues.Decode(f.Issues)) {
		return nil, fmt.Errorf("nothing a quick fix can solve")
	}
	cfg := s.cfg.Get()
	st := issues.QuickFix(f, cfg)
	applyLangPrune(f, cfg, &st)
	applySidecar(f, cfg, &st)
	applyImageSubs(f, cfg, &st)
	// The caller (fixFile, fixIssue) already checked HasQueuedForFile and
	// the hardlinked confirmation before reaching here.
	if f.Nlink > 1 {
		st.ConfirmedHardlinked = true
	}
	return s.enqueue(f, st, runNow, "issue-fix", "Quick fix")
}

// POST /api/v1/files/{id}/fix {run_now} — quick fix one file.
func (s *Server) fixFile(w http.ResponseWriter, r *http.Request) {
	f, err := s.st.GetFile(pathID(r))
	if err != nil || f == nil {
		fail(w, 404, fmt.Errorf("file not found"))
		return
	}
	if ok, _ := s.st.HasQueuedForFile(f.Path); ok {
		fail(w, http.StatusConflict, fmt.Errorf("already in the queue"))
		return
	}
	var req struct {
		RunNow            bool `json:"run_now"`
		ConfirmHardlinked bool `json:"confirm_hardlinked,omitempty"`
	}
	_ = readJSON(r, &req)
	if f.Nlink > 1 && !req.ConfirmHardlinked {
		writeHardlinkedConflict(w, []*store.File{f})
		return
	}
	j, err := s.quickJob(f, req.RunNow)
	if err != nil {
		fail(w, 400, err)
		return
	}
	s.eng.Kick()
	writeJSON(w, http.StatusOK, j)
}

// POST /api/v1/issues/{key}/fix {library, show, run_now} — queue a fix
// for every file with the issue: a remux for quick issues, the
// recommended encode for the rest.
func (s *Server) fixIssue(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	t, ok := issues.ByKey(key)
	if !ok || t.Scope != "file" || t.Fix == issues.Info {
		fail(w, 400, fmt.Errorf("issue %q has no bulk fix", key))
		return
	}
	var req struct {
		Library           string `json:"library"`
		Show              string `json:"show"`
		RunNow            bool   `json:"run_now"`
		ConfirmHardlinked bool   `json:"confirm_hardlinked,omitempty"`
		SkipHardlinked    bool   `json:"skip_hardlinked,omitempty"`
	}
	_ = readJSON(r, &req)
	files, _, err := s.st.ListFiles(store.FileFilter{Issue: key, Library: req.Library, Show: req.Show, Season: -1}, "size", 0, 2000)
	if err != nil {
		fail(w, 500, err)
		return
	}
	if !req.ConfirmHardlinked && !req.SkipHardlinked {
		var linked []*store.File
		for _, f := range files {
			if ok, _ := s.st.HasQueuedForFile(f.Path); !ok && f.Nlink > 1 {
				linked = append(linked, f)
			}
		}
		if len(linked) > 0 {
			writeHardlinkedConflict(w, linked)
			return
		}
	}
	queued, skipped := 0, 0
	for _, f := range files {
		if ok, _ := s.st.HasQueuedForFile(f.Path); ok {
			skipped++
			continue
		}
		if req.SkipHardlinked && f.Nlink > 1 {
			skipped++
			continue
		}
		var err error
		if t.Fix == issues.Quick {
			_, err = s.quickJob(f, req.RunNow)
		} else {
			st, _ := s.resolve(f, nil)
			if key == "interlaced" {
				st.Deinterlace = "on"
			}
			if f.Nlink > 1 {
				st.ConfirmedHardlinked = true
			}
			_, err = s.enqueue(f, st, req.RunNow, "issue-fix", "Issue: "+key)
		}
		if err != nil {
			skipped++
			continue
		}
		queued++
	}
	s.eng.Kick()
	writeJSON(w, http.StatusOK, map[string]any{"queued": queued, "skipped": skipped})
}

