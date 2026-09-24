package api

import (
	"mediatrans/internal/config"
	"mediatrans/internal/encode"
	"mediatrans/internal/sidecar"
	"mediatrans/internal/store"
)

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
