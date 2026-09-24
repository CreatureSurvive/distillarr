package recs

import (
	"testing"

	"mediatrans/internal/config"
	"mediatrans/internal/store"
)

func TestRecommendFoldsInSidecarMode(t *testing.T) {
	f := file(1080, 8_000_000, "h264", 2015)
	f.Subs = []store.SubStream{{Index: 4, Codec: "subrip", Lang: "eng", IsText: true}}
	cfg := config.Default()
	cfg.SubsSidecarMode = "extract_remove"

	r := Recommend(f, cfg)
	if r.Settings.SidecarMode != "extract_remove" {
		t.Errorf("Settings.SidecarMode = %q, want extract_remove", r.Settings.SidecarMode)
	}
	dropped := false
	for _, tr := range r.Settings.Subs {
		if tr.Index == 4 && tr.Action == "drop" {
			dropped = true
		}
	}
	if !dropped {
		t.Errorf("extract_remove should drop the text sub from Settings.Subs: %+v", r.Settings.Subs)
	}
}

func TestRecommendExtractKeepDropsNoSubs(t *testing.T) {
	f := file(1080, 8_000_000, "h264", 2015)
	f.Subs = []store.SubStream{{Index: 4, Codec: "subrip", Lang: "eng", IsText: true}}
	cfg := config.Default()
	cfg.SubsSidecarMode = "extract_keep"

	r := Recommend(f, cfg)
	if r.Settings.SidecarMode != "extract_keep" {
		t.Errorf("Settings.SidecarMode = %q, want extract_keep", r.Settings.SidecarMode)
	}
	for _, tr := range r.Settings.Subs {
		if tr.Action == "drop" {
			t.Errorf("extract_keep must not drop embedded tracks: %+v", r.Settings.Subs)
		}
	}
}

func TestRecommendPerFileSidecarOverrideWinsOverGlobal(t *testing.T) {
	f := file(1080, 8_000_000, "h264", 2015)
	f.SidecarMode = "off"
	cfg := config.Default()
	cfg.SubsSidecarMode = "extract_remove"

	r := Recommend(f, cfg)
	if r.Settings.SidecarMode != "off" {
		t.Errorf("Settings.SidecarMode = %q, want off (per-file override)", r.Settings.SidecarMode)
	}
}
