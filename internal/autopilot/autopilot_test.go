// SPDX-License-Identifier: GPL-3.0-or-later

package autopilot

import (
	"testing"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/recs"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

func baseFile() *store.File {
	return &store.File{
		Library: "movies", VideoCodec: "h264", Width: 1920, Height: 1080,
		Issues: ",no_hvc1,", MtimeNS: time.Now().Add(-10 * 24 * time.Hour).UnixNano(),
	}
}

func TestEvaluateDefaultQueuesWhenRecommendedAndWorthIt(t *testing.T) {
	f := baseFile()
	rec := recs.Recommendation{Action: "transcode", Savings: 40}
	d := Evaluate(f, rec, nil, Context{}, nil, 20)
	if d.Action != "queue" || d.RuleID != "" {
		t.Fatalf("got %+v, want the built-in default to queue", d)
	}
}

func TestEvaluateDefaultIgnoresBelowSavingsFloor(t *testing.T) {
	f := baseFile()
	rec := recs.Recommendation{Action: "transcode", Savings: 10}
	d := Evaluate(f, rec, nil, Context{}, nil, 20)
	if d.Action != "ignore" {
		t.Fatalf("got %+v, want ignore (10%% < 20%% floor)", d)
	}
}

func TestEvaluateDefaultIgnoresNonTranscodeRecommendation(t *testing.T) {
	f := baseFile()
	rec := recs.Recommendation{Action: "skip", Savings: 90}
	d := Evaluate(f, rec, nil, Context{}, nil, 20)
	if d.Action != "ignore" {
		t.Fatalf("got %+v, want ignore (recommendation is skip, savings are irrelevant)", d)
	}
}

func TestEvaluateFirstMatchWins(t *testing.T) {
	f := baseFile()
	rec := recs.Recommendation{Action: "transcode", Savings: 50}
	rules := []config.AutoRule{
		{ID: "a", Name: "A", Enabled: true, When: config.RuleMatch{Libraries: []string{"movies"}}, Then: config.RuleAction{Kind: "quick_fix"}},
		{ID: "b", Name: "B", Enabled: true, When: config.RuleMatch{Libraries: []string{"movies"}}, Then: config.RuleAction{Kind: "ignore"}},
	}
	d := Evaluate(f, rec, nil, Context{}, rules, 20)
	if d.RuleID != "a" || d.Action != "quick_fix" {
		t.Fatalf("got %+v, want rule a (first match) to win over rule b", d)
	}
}

func TestEvaluateSkipsDisabledRules(t *testing.T) {
	f := baseFile()
	rec := recs.Recommendation{Action: "transcode", Savings: 50}
	rules := []config.AutoRule{
		{ID: "a", Name: "A", Enabled: false, When: config.RuleMatch{Libraries: []string{"movies"}}, Then: config.RuleAction{Kind: "quick_fix"}},
	}
	d := Evaluate(f, rec, nil, Context{}, rules, 20)
	if d.RuleID != "" || d.Action != "queue" {
		t.Fatalf("got %+v, want the disabled rule skipped and the default applied", d)
	}
}

func TestEvaluateFallsThroughToDefaultWhenNoRuleMatches(t *testing.T) {
	f := baseFile() // library "movies"
	rec := recs.Recommendation{Action: "transcode", Savings: 50}
	rules := []config.AutoRule{
		{ID: "a", Name: "A", Enabled: true, When: config.RuleMatch{Libraries: []string{"tvshows"}}, Then: config.RuleAction{Kind: "ignore"}},
	}
	d := Evaluate(f, rec, nil, Context{}, rules, 20)
	if d.RuleID != "" || d.Action != "queue" {
		t.Fatalf("got %+v, want the non-matching rule skipped and the default applied", d)
	}
}

func TestEvaluateQueueOverrideCarriesCodecAndQuality(t *testing.T) {
	f := baseFile()
	rec := recs.Recommendation{Action: "transcode", Savings: 50}
	rules := []config.AutoRule{
		{ID: "a", Name: "Force AV1", Enabled: true, When: config.RuleMatch{}, Then: config.RuleAction{Kind: "queue_override", Codec: "av1", Quality: 70}},
	}
	d := Evaluate(f, rec, nil, Context{}, rules, 20)
	if d.Action != "queue_override" || d.Codec != "av1" || d.Quality != 70 {
		t.Fatalf("got %+v", d)
	}
}

// Table test covering every RuleMatch condition in isolation.
func TestMatchesEachCondition(t *testing.T) {
	f := &store.File{
		Library: "tvshows", VideoCodec: "hevc", Width: 3840, Height: 2160,
		Issues: ",pcm_audio,no_hvc1,", MtimeNS: time.Now().Add(-40 * 24 * time.Hour).UnixNano(),
	}
	rec := recs.Recommendation{Savings: 35}
	policy := &recs.Policy{Instance: "Sonarr 4K"}
	ctx := Context{Origin: "webhook", TagNames: []string{"distilled", "anime"}}

	cases := []struct {
		name string
		m    config.RuleMatch
		want bool
	}{
		{"library match", config.RuleMatch{Libraries: []string{"tvshows"}}, true},
		{"library mismatch", config.RuleMatch{Libraries: []string{"movies"}}, false},
		{"instance match", config.RuleMatch{Instances: []string{"Sonarr 4K"}}, true},
		{"instance mismatch", config.RuleMatch{Instances: []string{"Radarr"}}, false},
		{"tag overlap", config.RuleMatch{Tags: []string{"anime", "nope"}}, true},
		{"tag no overlap", config.RuleMatch{Tags: []string{"nope"}}, false},
		{"origin match", config.RuleMatch{Origins: []string{"webhook"}}, true},
		{"origin mismatch", config.RuleMatch{Origins: []string{"manual"}}, false},
		{"src codec match", config.RuleMatch{SrcCodecs: []string{"hevc"}}, true},
		{"src codec mismatch", config.RuleMatch{SrcCodecs: []string{"h264"}}, false},
		{"res class match", config.RuleMatch{ResClasses: []int{2160}}, true},
		{"res class mismatch", config.RuleMatch{ResClasses: []int{1080}}, false},
		{"min savings met", config.RuleMatch{MinSavingsPct: 30}, true},
		{"min savings not met", config.RuleMatch{MinSavingsPct: 40}, false},
		{"min age met", config.RuleMatch{MinAgeDays: 30}, true},
		{"min age not met", config.RuleMatch{MinAgeDays: 60}, false},
		{"issue key overlap", config.RuleMatch{IssueKeys: []string{"pcm_audio"}}, true},
		{"issue key no overlap", config.RuleMatch{IssueKeys: []string{"black_bars"}}, false},
		{"animation=false matches a non-animation file", config.RuleMatch{Animation: boolPtr(false)}, true},
		{"animation=true rejects a non-animation file", config.RuleMatch{Animation: boolPtr(true)}, false},
		{"animation nil is don't-care", config.RuleMatch{}, true},
		{"combined AND, all pass", config.RuleMatch{Libraries: []string{"tvshows"}, SrcCodecs: []string{"hevc"}, MinSavingsPct: 30}, true},
		{"combined AND, one fails", config.RuleMatch{Libraries: []string{"tvshows"}, SrcCodecs: []string{"h264"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := matches(f, rec, policy, ctx, c.m); got != c.want {
				t.Errorf("matches() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestMatchesInstanceWithNilPolicy(t *testing.T) {
	f := baseFile()
	rec := recs.Recommendation{}
	// A rule that requires a specific instance must never match an
	// unmanaged file (nil policy) as if "" were a real instance name.
	if matches(f, rec, nil, Context{}, config.RuleMatch{Instances: []string{"Sonarr"}}) {
		t.Error("an instance-scoped rule must not match a file with no policy")
	}
}

func boolPtr(b bool) *bool { return &b }
