// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/langprune"
	"github.com/CreatureSurvive/distillarr/internal/recs"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// applyLangPrune merges language-pruning drop entries into st,
// for callers that build settings directly (issues.QuickFix) rather
// than through recs.Recommend, which already folds this in itself.
// instanceName is resolved the same way recs.Recommend does, via
// recs.ArrPolicy's Instance field.
func applyLangPrune(f *store.File, cfg config.Config, st *encode.Settings) {
	instanceName := ""
	if p := recs.ArrPolicy(f.ID); p != nil {
		instanceName = p.Instance
	}
	audio, subs := langprune.Drops(f, cfg, instanceName, recs.OriginalLanguage(f.ID))
	st.Audio = append(st.Audio, audio...)
	st.Subs = append(st.Subs, subs...)
}

// forceLangPrune is applyLangPrune's counterpart for an autopilot rule's
// explicit "apply language pruning" action (RuleAction.PruneLanguages):
// it drops tracks per the effective policy's keep lists even when the
// matching AudioMode/SubsMode isn't globally "apply".
func forceLangPrune(f *store.File, cfg config.Config, st *encode.Settings) {
	instanceName := ""
	if p := recs.ArrPolicy(f.ID); p != nil {
		instanceName = p.Instance
	}
	audio, subs := langprune.ForceDrops(f, cfg, instanceName, recs.OriginalLanguage(f.ID))
	st.Audio = append(st.Audio, audio...)
	st.Subs = append(st.Subs, subs...)
}
