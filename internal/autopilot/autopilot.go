// SPDX-License-Identifier: GPL-3.0-or-later

// Package autopilot decides, from a file's recommendation and an
// ordered list of user rules, whether an unattended candidate should be
// queued, queued with an override, quick-fixed, or left alone. Evaluate
// is pure — no I/O, no DB — so every input it needs (the file, its
// recommendation, its arr policy, and the two bits of context no other
// type carries: intake origin and resolved arr tag names) is passed in
// already resolved by the caller.
//
// The rule types (config.AutoRule / RuleMatch / RuleAction) live in
// internal/config, not here: this package imports internal/recs, which
// imports internal/config, so the types can't live on this side of that
// edge without an import cycle.
package autopilot

import (
	"fmt"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/issues"
	"github.com/CreatureSurvive/distillarr/internal/recs"
	"github.com/CreatureSurvive/distillarr/internal/res"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// Context carries the two pieces of per-evaluation information that
// don't live on *store.File, recs.Recommendation or *recs.Policy: where
// this candidate came from, and (for a managed file) its owning
// instance's resolved tag names.
type Context struct {
	Origin   string
	TagNames []string
	// Plays and LastPlayed (zero = never) feed the popularity
	// conditions.
	Plays      int
	LastPlayed time.Time
}

// Decision is Evaluate's result: which rule fired (empty for the
// built-in default), what to do, and a human reason for the file page.
type Decision struct {
	RuleID  string `json:"rule_id,omitempty"`
	Rule    string `json:"rule,omitempty"`
	Action  string `json:"action"` // queue | queue_override | quick_fix | ignore
	Codec   string `json:"codec,omitempty"`
	Quality int    `json:"quality,omitempty"`
	// AudioRules is the rule's audio-policy override (per-rule
	// override, RuleAction.AudioRules); nil means "use the global policy".
	AudioRules map[string]encode.AudioRule `json:"audio_rules,omitempty"`
	// PruneLanguages is the rule's language-pruning override (// RuleAction.PruneLanguages); false means "use the global/scoped
	// policy as-is".
	PruneLanguages bool `json:"prune_languages,omitempty"`
	// SidecarMode is the rule's subtitle-sidecar override (// RuleAction.SidecarMode); "" means "use the file/global resolution
	// as-is".
	SidecarMode string `json:"sidecar_mode,omitempty"`
	// ImageSubsMode is the rule's image-subtitle override (// RuleAction.ImageSubsMode); "" means "use the file/global resolution
	// as-is".
	ImageSubsMode string `json:"image_subs_mode,omitempty"`
	Reason        string `json:"reason"`
}

// Evaluate walks rules in order and returns the first enabled match.
// When none matches (including when rules is empty), the built-in
// default applies: queue when the recommendation is "transcode" and its
// savings meet minSavingsPct — the same bar a manual queue would apply.
func Evaluate(f *store.File, rec recs.Recommendation, policy *recs.Policy, ctx Context, rules []config.AutoRule, minSavingsPct int) Decision {
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if matches(f, rec, policy, ctx, r.When) {
			return Decision{
				RuleID: r.ID, Rule: r.Name, Action: r.Then.Kind,
				Codec: r.Then.Codec, Quality: r.Then.Quality, AudioRules: r.Then.AudioRules,
				PruneLanguages: r.Then.PruneLanguages,
				SidecarMode:    r.Then.SidecarMode,
				ImageSubsMode:  r.Then.ImageSubsMode,
				Reason:         fmt.Sprintf("rule %q matched", r.Name),
			}
		}
	}
	if rec.Action == "transcode" && rec.Savings >= float64(minSavingsPct) {
		return Decision{Action: "queue", Reason: "default: recommended and worth it"}
	}
	return Decision{Action: "ignore", Reason: "default: not recommended, or below the savings floor"}
}

func matches(f *store.File, rec recs.Recommendation, policy *recs.Policy, ctx Context, m config.RuleMatch) bool {
	if len(m.Libraries) > 0 && !contains(m.Libraries, f.Library) {
		return false
	}
	if len(m.Instances) > 0 {
		inst := ""
		if policy != nil {
			inst = policy.Instance
		}
		if !contains(m.Instances, inst) {
			return false
		}
	}
	if len(m.Tags) > 0 && !overlaps(ctx.TagNames, m.Tags) {
		return false
	}
	if len(m.Origins) > 0 && !contains(m.Origins, ctx.Origin) {
		return false
	}
	if len(m.SrcCodecs) > 0 && !contains(m.SrcCodecs, f.VideoCodec) {
		return false
	}
	if len(m.ResClasses) > 0 && !containsInt(m.ResClasses, res.Class(f.Width, f.Height)) {
		return false
	}
	if m.MinSavingsPct > 0 && rec.Savings < float64(m.MinSavingsPct) {
		return false
	}
	if m.MinAgeDays > 0 && ageDays(f) < float64(m.MinAgeDays) {
		return false
	}
	if len(m.IssueKeys) > 0 && !overlaps(issueKeys(f), m.IssueKeys) {
		return false
	}
	if m.Animation != nil && *m.Animation != recs.IsAnimation(f) {
		return false
	}
	if m.MinPlays > 0 && ctx.Plays < m.MinPlays {
		return false
	}
	if m.MaxPlays != nil && ctx.Plays > *m.MaxPlays {
		return false
	}
	if m.NotPlayedDays > 0 && !ctx.LastPlayed.IsZero() && time.Since(ctx.LastPlayed) < time.Duration(m.NotPlayedDays)*24*time.Hour {
		return false
	}
	return true
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func containsInt(list []int, v int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// overlaps reports whether any of have appears in want.
func overlaps(have, want []string) bool {
	for _, w := range want {
		if contains(have, w) {
			return true
		}
	}
	return false
}

// ageDays uses the file's own mtime — Sonarr/Radarr's import sets it, so
// it's the closest proxy to "how long has this been sitting here"
// available without a dedicated first-seen column.
func ageDays(f *store.File) float64 {
	if f.MtimeNS <= 0 {
		return 0
	}
	return time.Since(time.Unix(0, f.MtimeNS)).Hours() / 24
}

func issueKeys(f *store.File) []string {
	return issues.Decode(f.Issues)
}
