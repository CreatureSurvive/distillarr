package api

import (
	"net/http"

	"mediatrans/internal/recs"
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
