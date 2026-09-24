package langprune

import (
	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// Drops resolves f's effective LangPolicy (global policy, library/
// instance scope overrides, per-file exemption — see
// config.Config.EffectiveLangPolicy) and, for whichever of audio/subs is
// in "apply" mode, returns the explicit drop entries
// encode.Settings.Audio/Subs need to prune those tracks. A side left in
// "report" (or "off") mode contributes no drops here — ExtraLanguages
// already reports it without touching encode settings. instanceName
// is the owning arr instance's display name ("" for an unmanaged file);
// origLangName is arr's original_language name ("" if unknown).
func Drops(f *store.File, cfg config.Config, instanceName, origLangName string) ([]encode.AudioTrack, []encode.SubTrack) {
	policy := cfg.EffectiveLangPolicy(f.Library, instanceName, f.LangPruneExempt)
	return dropsFor(f, policy, origLangName)
}

// ForceDrops applies language pruning to f regardless of the effective
// policy's AudioMode/SubsMode (autopilot rule action, "apply
// language pruning") — used for a rule that wants pruning on for just
// its own matched files, independent of the global/scoped apply toggle.
// A side whose effective keep list is empty is skipped, the same safety
// validateLangPolicy enforces for the global policy: an accidentally
// empty list must never read as "drop everything in this kind." A
// per-file exemption still wins (EffectiveLangPolicy forces both modes
// off for an exempt file, and this never overrides that).
func ForceDrops(f *store.File, cfg config.Config, instanceName, origLangName string) ([]encode.AudioTrack, []encode.SubTrack) {
	policy := cfg.EffectiveLangPolicy(f.Library, instanceName, f.LangPruneExempt)
	if f.LangPruneExempt {
		return nil, nil
	}
	policy.AudioMode = "off"
	policy.SubsMode = "off"
	if len(policy.AudioKeep) > 0 {
		policy.AudioMode = "apply"
	}
	if len(policy.SubsKeep) > 0 {
		policy.SubsMode = "apply"
	}
	return dropsFor(f, policy, origLangName)
}

func dropsFor(f *store.File, policy config.LangPolicy, origLangName string) ([]encode.AudioTrack, []encode.SubTrack) {
	if policy.AudioMode != "apply" && policy.SubsMode != "apply" {
		return nil, nil
	}
	res := Select(f.Audio, f.Subs, NameToCode(origLangName), f.Duration, policy)
	var audio []encode.AudioTrack
	if policy.AudioMode == "apply" {
		for _, i := range res.DropAudio {
			audio = append(audio, encode.AudioTrack{Index: i, Action: "drop"})
		}
	}
	var subs []encode.SubTrack
	if policy.SubsMode == "apply" {
		for _, i := range res.DropSubs {
			subs = append(subs, encode.SubTrack{Index: i, Action: "drop"})
		}
	}
	return audio, subs
}
