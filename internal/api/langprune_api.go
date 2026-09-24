package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"mediatrans/internal/issues"
	"mediatrans/internal/langprune"
	"mediatrans/internal/recs"
	"mediatrans/internal/store"
)

// POST /api/v1/files/{id}/lang-exempt {exempt} — opt one file in/out of
// language pruning, independent of the global/scoped policy.
func (s *Server) setLangExempt(w http.ResponseWriter, r *http.Request) {
	f, err := s.st.GetFile(pathID(r))
	if err != nil || f == nil {
		fail(w, 404, fmt.Errorf("file not found"))
		return
	}
	var req struct {
		Exempt bool `json:"exempt"`
	}
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err)
		return
	}
	if err := s.st.SetLangPruneExempt(f.ID, req.Exempt); err != nil {
		fail(w, 500, err)
		return
	}
	s.scan.RefreshRecsSoon()
	writeJSON(w, http.StatusOK, map[string]any{"id": f.ID, "exempt": req.Exempt})
}

// langpruneReportRow is one language's drop counts in the dry-run report.
type langpruneReportRow struct {
	Language    string `json:"language"` // as stored on the track (ISO-ish code, or "" for undetermined)
	AudioTracks int    `json:"audio_tracks"`
	SubTracks   int    `json:"sub_tracks"`
}

// langpruneReport is GET /api/v1/langprune/report's response: what
// applying the current keep lists library-wide would do right now,
// regardless of whether a mode is currently "report" or "apply" — the
// UI shows this before the first switch to "apply".
type langpruneReport struct {
	Files      int                  `json:"files"`
	SavedBytes int64                `json:"saved_bytes"`
	ByLanguage []langpruneReportRow `json:"by_language"`
}

// GET /api/v1/langprune/report — dry-run preview, library-wide.
func (s *Server) langpruneReportHandler(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	origLangs, err := s.st.ArrOriginalLanguages()
	if err != nil {
		fail(w, 500, err)
		return
	}
	instanceIDs, err := s.st.ArrInstanceIDs()
	if err != nil {
		fail(w, 500, err)
		return
	}
	instanceNames := map[int64]string{}
	for fileID, id := range instanceIDs {
		for _, x := range cfg.ArrInstances {
			if x.ID == id {
				instanceNames[fileID] = x.Name
				break
			}
		}
	}

	var files int
	var savedBytes int64
	byLang := map[string]*langpruneReportRow{}
	row := func(lang string) *langpruneReportRow {
		if r, ok := byLang[lang]; ok {
			return r
		}
		r := &langpruneReportRow{Language: lang}
		byLang[lang] = r
		return r
	}

	_ = s.st.EachFile(func(f *store.File) error {
		if f.Missing || f.LangPruneExempt {
			return nil
		}
		p := cfg.EffectiveLangPolicy(f.Library, instanceNames[f.ID], false)
		// Preview what applying would do, whatever mode is currently
		// saved: a side counts only when it has a keep list to apply —
		// an empty list is never treated as "drop everything" here
		// either (the same safety validateLangPolicy enforces on save).
		p.AudioMode, p.SubsMode = "off", "off"
		if len(p.AudioKeep) > 0 {
			p.AudioMode = "apply"
		}
		if len(p.SubsKeep) > 0 {
			p.SubsMode = "apply"
		}
		if p.AudioMode != "apply" && p.SubsMode != "apply" {
			return nil
		}
		res := langprune.Select(f.Audio, f.Subs, langprune.NameToCode(origLangs[f.ID]), f.Duration, p)
		if len(res.DropAudio) == 0 && len(res.DropSubs) == 0 {
			return nil
		}
		files++
		savedBytes += res.SavedBytes
		for _, idx := range res.DropAudio {
			for _, a := range f.Audio {
				if a.Index == idx {
					row(a.Lang).AudioTracks++
					break
				}
			}
		}
		for _, idx := range res.DropSubs {
			for _, sub := range f.Subs {
				if sub.Index == idx {
					row(sub.Lang).SubTracks++
					break
				}
			}
		}
		return nil
	})

	rows := make([]langpruneReportRow, 0, len(byLang))
	for _, r := range byLang {
		rows = append(rows, *r)
	}
	writeJSON(w, http.StatusOK, langpruneReport{Files: files, SavedBytes: savedBytes, ByLanguage: rows})
}

// POST /api/v1/langprune/apply {confirm} — queue video-copy quick-fix
// jobs for every file whose only outstanding change right now is
// language pruning under the effective (apply-mode) policy. A
// file already queued, already recommended for a real re-encode, or
// carrying another quick-fixable issue is left alone: that job (queued
// manually, by autopilot, or by its own issue's fix) already folds
// pruning in via recs.Recommend/applyLangPrune — this only picks up
// files nothing else would ever touch. Requires confirm=true, matching
// the plan's "applying needs explicit confirmation" — dropped tracks
// only survive in the trash copy until the retention period ends.
func (s *Server) langpruneApplyHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Confirm bool `json:"confirm"`
	}
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err)
		return
	}
	if !req.Confirm {
		fail(w, 400, fmt.Errorf("confirm is required"))
		return
	}
	cfg := s.cfg.Get()
	origLangs, err := s.st.ArrOriginalLanguages()
	if err != nil {
		fail(w, 500, err)
		return
	}
	instanceIDs, err := s.st.ArrInstanceIDs()
	if err != nil {
		fail(w, 500, err)
		return
	}
	instanceNames := map[int64]string{}
	for fileID, id := range instanceIDs {
		for _, x := range cfg.ArrInstances {
			if x.ID == id {
				instanceNames[fileID] = x.Name
				break
			}
		}
	}

	queued, skipped := 0, 0
	_ = s.st.EachFile(func(f *store.File) error {
		if f.Missing {
			return nil
		}
		audio, subs := langprune.Drops(f, cfg, instanceNames[f.ID], origLangs[f.ID])
		if len(audio) == 0 && len(subs) == 0 {
			return nil // nothing to prune, or report/off mode, or exempt
		}
		if issues.HasQuick(issues.Decode(f.Issues)) {
			return nil // its own quick fix already folds pruning in
		}
		var rec recs.Recommendation
		if f.RecJSON != "" {
			_ = json.Unmarshal([]byte(f.RecJSON), &rec)
		}
		if rec.Action == "transcode" {
			return nil // its re-encode already folds pruning in
		}
		if ok, _ := s.st.HasQueuedForFile(f.Path); ok {
			skipped++
			return nil
		}
		if f.Nlink > 1 {
			skipped++ // needs explicit hardlink confirmation; a bulk apply skips rather than assumes
			return nil
		}
		st := issues.QuickFix(f, cfg)
		st.Audio = append(st.Audio, audio...)
		st.Subs = append(st.Subs, subs...)
		if _, err := s.enqueue(f, st, false, "issue-fix", "Language pruning"); err != nil {
			skipped++
			return nil
		}
		queued++
		return nil
	})
	s.eng.Kick()
	writeJSON(w, http.StatusOK, map[string]any{"queued": queued, "skipped": skipped})
}
