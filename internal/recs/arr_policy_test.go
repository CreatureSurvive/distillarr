package recs

import (
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/encode"
)

// withPolicy overrides the ArrPolicy hook for one test and restores the
// no-op default afterward, so it can't leak into other tests in this
// package (they run sequentially, sharing this package-level var).
func withPolicy(t *testing.T, p *Policy) {
	t.Helper()
	ArrPolicy = func(int64) *Policy { return p }
	t.Cleanup(func() { ArrPolicy = func(int64) *Policy { return nil } })
}

func TestRecommendArrPolicySkip(t *testing.T) {
	withPolicy(t, &Policy{Instance: "Radarr 4K", Skip: true})
	cfg := config.Default()
	r := Recommend(file(1080, 8_000_000, "h264", 2015), cfg)
	if r.Action != "skip" {
		t.Fatalf("action = %q, want skip", r.Action)
	}
	if r.Reason == "" {
		t.Error("expected a reason naming the instance")
	}
}

func TestRecommendArrPolicyUpgradePending(t *testing.T) {
	withPolicy(t, &Policy{Instance: "Sonarr", UpgradePending: true})
	cfg := config.Default()
	r := Recommend(file(1080, 8_000_000, "h264", 2015), cfg)
	if r.Action != "skip" || !r.UpgradePending {
		t.Fatalf("got action=%q upgradePending=%v, want skip/true", r.Action, r.UpgradePending)
	}
}

func TestRecommendArrPolicyRemuxOnly(t *testing.T) {
	withPolicy(t, &Policy{Instance: "Radarr", RemuxOnly: true})
	cfg := config.Default()
	r := Recommend(file(1080, 8_000_000, "h264", 2015), cfg)
	if r.Action != "skip" {
		t.Fatalf("action = %q, want skip (remux-only)", r.Action)
	}
}

func TestRecommendArrPolicyCodecOverride(t *testing.T) {
	cfg := config.Default() // default codec is hevc
	withPolicy(t, &Policy{Instance: "Radarr", Codec: "av1"})
	r := Recommend(file(1080, 8_000_000, "h264", 2015), cfg)
	if r.Action != "transcode" {
		t.Fatalf("action = %q, want transcode", r.Action)
	}
	if r.Settings.Codec != encode.AV1 {
		t.Errorf("codec = %q, want av1 (policy override)", r.Settings.Codec)
	}
}

func TestRecommendNoPolicyUnaffected(t *testing.T) {
	// The default hook (no override) must behave exactly as without arr policies.
	cfg := config.Default()
	r := Recommend(file(1080, 8_000_000, "h264", 2015), cfg)
	if r.Action != "transcode" {
		t.Fatalf("action = %q, want transcode", r.Action)
	}
	if r.UpgradePending {
		t.Error("UpgradePending should be false with no policy")
	}
}
