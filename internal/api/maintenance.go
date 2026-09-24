// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/recs"
)

// clearMeasurements resets every file's VMAF/CAMBI quality search
// result, so the overnight loop and future encodes re-measure with the
// current sampling method.
func (s *Server) clearMeasurements(w http.ResponseWriter, r *http.Request) {
	n, err := s.st.ClearMeasurements()
	if err != nil {
		fail(w, 500, err)
		return
	}
	s.scan.RefreshRecsSoon()
	writeJSON(w, http.StatusOK, map[string]any{"cleared": n})
}

// clearHistory permanently deletes finished job records - this is the
// data behind the savings/history dashboard and the "upscaled" badge.
func (s *Server) clearHistory(w http.ResponseWriter, r *http.Request) {
	n, err := s.st.ClearJobHistory()
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleared": n})
}

// clearCalibration discards every learned size-model correction.
func (s *Server) clearCalibration(w http.ResponseWriter, r *http.Request) {
	recs.ResetCalibration()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// clearCrop re-queues every checked file for black-bar detection.
func (s *Server) clearCrop(w http.ResponseWriter, r *http.Request) {
	n, err := s.st.ClearCrop()
	if err != nil {
		fail(w, 500, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleared": n})
}

// clearIssueTags re-queues every checked file for container-tag
// re-probing (hvc1/faststart) and the issues list that depends on it.
func (s *Server) clearIssueTags(w http.ResponseWriter, r *http.Request) {
	n, err := s.st.ClearIssueTags()
	if err != nil {
		fail(w, 500, err)
		return
	}
	s.scan.RefreshRecsSoon()
	writeJSON(w, http.StatusOK, map[string]any{"cleared": n})
}

// removeHistoryUnder deletes finished job records for one folder (e.g.
// a scratch test library), then rebuilds the trend history from what's
// left. dry_run (the default) only counts.
func (s *Server) removeHistoryUnder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Path   string `json:"path"`
		DryRun *bool  `json:"dry_run"`
	}
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err)
		return
	}
	p := strings.TrimSpace(req.Path)
	if !strings.HasPrefix(p, "/") || strings.Trim(p, "/") == "" {
		fail(w, 400, fmt.Errorf("enter an absolute folder path, not /"))
		return
	}
	if req.DryRun == nil || *req.DryRun {
		n, err := s.st.CountFinishedJobsUnder(p)
		if err != nil {
			fail(w, 500, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"matches": n})
		return
	}
	n, err := s.st.RemoveFinishedJobsUnder(p)
	if err != nil {
		fail(w, 500, err)
		return
	}
	if n > 0 {
		_ = s.st.ClearTrends()
		s.snapshotTrend(time.Now())
	}
	writeJSON(w, http.StatusOK, map[string]any{"cleared": n})
}
