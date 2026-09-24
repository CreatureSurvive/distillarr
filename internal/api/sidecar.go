// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"fmt"
	"net/http"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/sidecar"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// POST /api/v1/files/{id}/sidecar-mode {mode} — per-file override of
// subs_sidecar_mode. mode "" clears the override back to
// "inherit the rule/global resolution".
func (s *Server) setSidecarMode(w http.ResponseWriter, r *http.Request) {
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
	case "", "off", sidecar.ExtractKeep, sidecar.ExtractRemove:
	default:
		fail(w, 400, fmt.Errorf("mode must be \"\", \"off\", %q, or %q", sidecar.ExtractKeep, sidecar.ExtractRemove))
		return
	}
	if err := s.st.SetSidecarMode(f.ID, req.Mode); err != nil {
		fail(w, 500, err)
		return
	}
	s.scan.RefreshRecsSoon()
	writeJSON(w, http.StatusOK, map[string]any{"id": f.ID, "mode": req.Mode})
}

// applySidecar merges subtitle-sidecar mode and any
// extract_remove drop entries into st, for callers that build settings
// directly (issues.QuickFix) rather than through recs.Recommend, which
// already folds this in itself.
func applySidecar(f *store.File, cfg config.Config, st *encode.Settings) {
	mode := cfg.EffectiveSidecarMode(f.SidecarMode, "")
	st.SidecarMode = mode
	st.Subs = append(st.Subs, sidecar.DropEntries(f.Subs, mode, st.Subs)...)
}

// forceSidecarMode is applySidecar's counterpart for an autopilot rule's
// explicit sidecar-mode override (RuleAction.SidecarMode): it wins over
// the global default, but a per-file override still wins over it (see
// config.Config.EffectiveSidecarMode).
func forceSidecarMode(f *store.File, cfg config.Config, ruleMode string, st *encode.Settings) {
	mode := cfg.EffectiveSidecarMode(f.SidecarMode, ruleMode)
	st.SidecarMode = mode
	st.Subs = append(st.Subs, sidecar.DropEntries(f.Subs, mode, st.Subs)...)
}
