// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"fmt"
	"net/http"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/imagesubs"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// POST /api/v1/files/{id}/image-subs-mode {mode} — per-file override of
// image_subs_mode. mode "" clears the override back to "inherit
// the rule/global resolution".
func (s *Server) setImageSubsMode(w http.ResponseWriter, r *http.Request) {
	f, err := s.st.GetFile(pathID(r))
	if err != nil || f == nil {
		fail(w, 404, fmt.Errorf("file not found"))
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err)
		return
	}
	switch req.Mode {
	case "", imagesubs.Sidecar, imagesubs.OCR:
	default:
		fail(w, 400, fmt.Errorf("mode must be \"\", %q, or %q", imagesubs.Sidecar, imagesubs.OCR))
		return
	}
	if err := s.st.SetImageSubsMode(f.ID, req.Mode); err != nil {
		fail(w, 500, err)
		return
	}
	s.scan.RefreshRecsSoon()
	writeJSON(w, http.StatusOK, map[string]any{"id": f.ID, "mode": req.Mode})
}

// imageSubsDrops resolves f's OCR-drop args from its cached OCR result,
// shared by applyImageSubs/forceImageSubsMode and recs.Recommend.
func imageSubsDrops(f *store.File, mode string, keepOriginal bool, existing []encode.SubTrack) []encode.SubTrack {
	var ocrTrackIndex int
	var ocrSucceeded bool
	if r, ok := imagesubs.UnmarshalResult(f.OCRJSON); ok && !r.Failed {
		ocrTrackIndex, ocrSucceeded = r.TrackIndex, true
	}
	return imagesubs.DropEntries(f.Subs, mode, keepOriginal, ocrTrackIndex, ocrSucceeded, existing)
}

// applyImageSubs merges image-subtitle mode and any drop entries
// into st, for callers that build settings directly (issues.QuickFix)
// rather than through recs.Recommend, which already folds this in
// itself.
func applyImageSubs(f *store.File, cfg config.Config, st *encode.Settings) {
	mode := cfg.EffectiveImageSubsMode(f.ImageSubsMode, "")
	st.ImageSubsMode = mode
	st.Subs = append(st.Subs, imageSubsDrops(f, mode, cfg.ImageSubsKeepOriginalOn(), st.Subs)...)
}

// forceImageSubsMode is applyImageSubs's counterpart for an autopilot
// rule's explicit image-subs override (RuleAction.ImageSubsMode): wins
// over the global default, but a per-file override still wins over it.
func forceImageSubsMode(f *store.File, cfg config.Config, ruleMode string, st *encode.Settings) {
	mode := cfg.EffectiveImageSubsMode(f.ImageSubsMode, ruleMode)
	st.ImageSubsMode = mode
	st.Subs = append(st.Subs, imageSubsDrops(f, mode, cfg.ImageSubsKeepOriginalOn(), st.Subs)...)
}

// POST /api/v1/files/{id}/ocr {run_now} — queue an OCR job for the
// file's primary image subtitle track (internal/imagesubs.PrimaryTrack).
// A dedicated endpoint, not routed through the generic per-issue "Fix"
// button (like language-pruning apply; sidecars have no such need):
// OCR is a distinct job kind, not something issues.QuickFix builds.
func (s *Server) ocrFile(w http.ResponseWriter, r *http.Request) {
	f, err := s.st.GetFile(pathID(r))
	if err != nil || f == nil {
		fail(w, 404, fmt.Errorf("file not found"))
		return
	}
	track, ok := imagesubs.PrimaryTrack(f.Subs, "")
	if !ok {
		fail(w, 400, fmt.Errorf("no image subtitle track on this file"))
		return
	}
	if track.Codec != imagesubs.CodecPGS {
		fail(w, 400, fmt.Errorf("OCR only supports PGS tracks (this file's primary image track is %s)", track.Codec))
		return
	}
	if ok, _ := s.st.HasQueuedForFile(f.Path); ok {
		fail(w, http.StatusConflict, fmt.Errorf("already in the queue"))
		return
	}
	var req struct {
		RunNow bool `json:"run_now"`
	}
	_ = readJSON(r, &req)
	j, err := s.enqueue(f, encode.Settings{Backend: "ocr"}, req.RunNow, "issue-fix", "OCR image subtitles")
	if err != nil {
		fail(w, 500, err)
		return
	}
	s.eng.Kick()
	writeJSON(w, http.StatusOK, j)
}
