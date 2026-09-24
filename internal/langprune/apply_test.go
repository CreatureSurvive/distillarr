package langprune

import (
	"testing"

	"mediatrans/internal/config"
	"mediatrans/internal/store"
)

func testFile() *store.File {
	return &store.File{Library: "movies", Duration: 100,
		Audio: []store.AudioStream{{Index: 0, Lang: "eng"}, {Index: 1, Lang: "jpn", BitRate: 128_000}},
		Subs:  []store.SubStream{{Index: 0, Lang: "eng"}, {Index: 1, Lang: "jpn"}},
	}
}

func TestDropsModeGating(t *testing.T) {
	f := testFile()
	cfg := config.Config{}
	if a, s := Drops(f, cfg, "", ""); a != nil || s != nil {
		t.Errorf("policy off should drop nothing: %v %v", a, s)
	}

	cfg.LangPolicy = config.LangPolicy{AudioMode: "report", AudioKeep: []string{"eng"}}
	if a, s := Drops(f, cfg, "", ""); a != nil || s != nil {
		t.Errorf("report mode should drop nothing (that's ExtraLanguages' job): %v %v", a, s)
	}

	cfg.LangPolicy = config.LangPolicy{AudioMode: "apply", AudioKeep: []string{"eng"}}
	audio, subs := Drops(f, cfg, "", "")
	if len(audio) != 1 || audio[0].Index != 1 || audio[0].Action != "drop" {
		t.Errorf("audio apply should drop the jpn track: %v", audio)
	}
	if subs != nil {
		t.Errorf("subs_mode off should drop no subs: %v", subs)
	}
}

func TestDropsPerFileExempt(t *testing.T) {
	f := testFile()
	f.LangPruneExempt = true
	cfg := config.Config{LangPolicy: config.LangPolicy{AudioMode: "apply", AudioKeep: []string{"eng"},
		SubsMode: "apply", SubsKeep: []string{"eng"}}}
	if a, s := Drops(f, cfg, "", ""); a != nil || s != nil {
		t.Errorf("exempt file should drop nothing: %v %v", a, s)
	}
}

func TestDropsLibraryOverride(t *testing.T) {
	f := testFile()
	cfg := config.Config{
		LangPolicy:           config.LangPolicy{AudioMode: "apply", AudioKeep: []string{"eng"}},
		LangLibraryOverrides: map[string]config.LangOverride{"movies": {Mode: "off"}},
	}
	if a, s := Drops(f, cfg, "", ""); a != nil || s != nil {
		t.Errorf("library override off should drop nothing: %v %v", a, s)
	}
}

func TestDropsInstanceOverride(t *testing.T) {
	f := testFile()
	cfg := config.Config{
		LangPolicy:            config.LangPolicy{AudioMode: "apply", AudioKeep: []string{"eng"}},
		LangInstanceOverrides: map[string]config.LangOverride{"Radarr 4K": {Mode: "custom", AudioKeep: []string{"eng", "jpn"}}},
	}
	if a, _ := Drops(f, cfg, "Radarr 4K", ""); a != nil {
		t.Errorf("instance override keeping both languages should drop nothing: %v", a)
	}
	if a, _ := Drops(f, cfg, "Some Other Instance", ""); len(a) != 1 {
		t.Errorf("an unscoped instance should fall back to the global policy: %v", a)
	}
}

func TestForceDropsIgnoresMode(t *testing.T) {
	f := testFile()
	cfg := config.Config{LangPolicy: config.LangPolicy{AudioMode: "off", AudioKeep: []string{"eng"}}}
	audio, _ := ForceDrops(f, cfg, "", "")
	if len(audio) != 1 || audio[0].Index != 1 {
		t.Errorf("a force-drop rule should prune even with the global mode off: %v", audio)
	}

	// An empty keep list must never read as "drop everything".
	cfg.LangPolicy = config.LangPolicy{AudioMode: "off"}
	if audio, _ := ForceDrops(f, cfg, "", ""); audio != nil {
		t.Errorf("an empty keep list should never force-drop every track: %v", audio)
	}

	f.LangPruneExempt = true
	cfg.LangPolicy = config.LangPolicy{AudioMode: "off", AudioKeep: []string{"eng"}}
	if audio, _ := ForceDrops(f, cfg, "", ""); audio != nil {
		t.Errorf("exemption should still win over a force-drop rule: %v", audio)
	}
}

// Regression: ForceDrops re-derives "is this side active" from whether
// the effective keep list is non-empty (so it can force pruning on even
// with the global mode "report"/"off"). A library-off override must
// still win — found live during live verification, where a scoped "off"
// override was silently undone because it cleared the mode but left the
// (still globally non-empty) keep list behind.
func TestForceDropsRespectsLibraryOffOverride(t *testing.T) {
	f := testFile()
	cfg := config.Config{
		LangPolicy:           config.LangPolicy{AudioMode: "off", AudioKeep: []string{"eng"}},
		LangLibraryOverrides: map[string]config.LangOverride{"movies": {Mode: "off"}},
	}
	if audio, subs := ForceDrops(f, cfg, "", ""); audio != nil || subs != nil {
		t.Errorf("a library-off override must survive ForceDrops' mode re-derivation: %v %v", audio, subs)
	}
}
