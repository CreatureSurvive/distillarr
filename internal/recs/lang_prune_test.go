// SPDX-License-Identifier: GPL-3.0-or-later

package recs

import (
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// withOriginalLanguage overrides the OriginalLanguage hook for one test.
func withOriginalLanguage(t *testing.T, name string) {
	t.Helper()
	OriginalLanguage = func(int64) string { return name }
	t.Cleanup(func() { OriginalLanguage = func(int64) string { return "" } })
}

func TestRecommendFoldsInLanguagePruning(t *testing.T) {
	withOriginalLanguage(t, "English")
	f := file(1080, 8_000_000, "h264", 2015)
	f.Audio = []store.AudioStream{{Index: 0, Codec: "aac", Lang: "eng"}, {Index: 1, Codec: "aac", Lang: "jpn"}}
	cfg := config.Default()
	cfg.LangPolicy = config.LangPolicy{AudioMode: "apply", AudioKeep: []string{"eng"}}

	r := Recommend(f, cfg)
	if r.Action != "transcode" {
		t.Fatalf("action = %q, want transcode", r.Action)
	}
	found := false
	for _, tr := range r.Settings.Audio {
		if tr.Index == 1 && tr.Action == "drop" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected the jpn track (index 1) dropped in Settings.Audio: %+v", r.Settings.Audio)
	}
	// r.Audio (the display plan, and what fillEstimate sizes from) must
	// agree, since it's derived from the same Settings.Audio.
	dropped := false
	for _, a := range r.Audio {
		if a.Index == 1 && a.Action == "drop" {
			dropped = true
		}
	}
	if !dropped {
		t.Errorf("expected r.Audio to show the jpn track dropped: %+v", r.Audio)
	}
}

func TestRecommendReportModeDropsNothing(t *testing.T) {
	f := file(1080, 8_000_000, "h264", 2015)
	f.Audio = []store.AudioStream{{Index: 0, Codec: "aac", Lang: "eng"}, {Index: 1, Codec: "aac", Lang: "jpn"}}
	cfg := config.Default()
	cfg.LangPolicy = config.LangPolicy{AudioMode: "report", AudioKeep: []string{"eng"}}

	r := Recommend(f, cfg)
	for _, tr := range r.Settings.Audio {
		if tr.Action == "drop" {
			t.Errorf("report mode should never drop tracks in Settings.Audio: %+v", r.Settings.Audio)
		}
	}
}
