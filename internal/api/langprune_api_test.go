// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

func TestSetLangExempt(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A",
		VideoCodec: "hevc", Container: "mkv", Width: 1920, Height: 1080, Duration: 100})

	rec := doJSON(t, s, "POST", "/api/v1/files/"+itoa64(f.ID)+"/lang-exempt", map[string]any{"exempt": true})
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got, _ := s.st.GetFile(f.ID)
	if !got.LangPruneExempt {
		t.Error("expected LangPruneExempt = true")
	}

	rec = doJSON(t, s, "POST", "/api/v1/files/"+itoa64(f.ID)+"/lang-exempt", map[string]any{"exempt": false})
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got, _ = s.st.GetFile(f.ID)
	if got.LangPruneExempt {
		t.Error("expected LangPruneExempt = false")
	}
}

func TestLangpruneReport(t *testing.T) {
	s := newTestServer(t)
	must := func(cfg *config.Config) {
		if err := s.cfg.Update(func(c *config.Config) { *c = *cfg; c.AuthMode = config.AuthDisabled }); err != nil {
			t.Fatal(err)
		}
	}

	f1 := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A", Duration: 100,
		Audio: []store.AudioStream{{Index: 0, Lang: "eng"}, {Index: 1, Lang: "jpn", BitRate: 128_000}}})
	_ = mustUpsert(t, s.st, &store.File{Path: "/m/b.mkv", Library: "movies", Title: "B", Duration: 100,
		Audio: []store.AudioStream{{Index: 0, Lang: "eng"}}})
	exempt := mustUpsert(t, s.st, &store.File{Path: "/m/c.mkv", Library: "movies", Title: "C", Duration: 100,
		Audio: []store.AudioStream{{Index: 0, Lang: "eng"}, {Index: 1, Lang: "jpn", BitRate: 128_000}}})
	if err := s.st.SetLangPruneExempt(exempt.ID, true); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.LangPolicy = config.LangPolicy{AudioKeep: []string{"eng"}} // mode intentionally left "off": the report previews regardless
	must(&cfg)

	rec := doJSON(t, s, "GET", "/api/v1/langprune/report", nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out langpruneReport
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Files != 1 {
		t.Errorf("files = %d, want 1 (only a.mkv has a droppable track; b.mkv has nothing to drop, c.mkv is exempt)", out.Files)
	}
	if out.SavedBytes <= 0 {
		t.Errorf("saved_bytes = %d, want > 0", out.SavedBytes)
	}
	if len(out.ByLanguage) != 1 || out.ByLanguage[0].Language != "jpn" || out.ByLanguage[0].AudioTracks != 1 {
		t.Errorf("by_language = %+v, want one jpn row with 1 audio track", out.ByLanguage)
	}
	_ = f1
}

func TestLangpruneApplyQueuesQuickFixOnly(t *testing.T) {
	s := newTestServer(t)
	if err := s.cfg.Update(func(c *config.Config) {
		*c = config.Default()
		c.AuthMode = config.AuthDisabled
		c.LangPolicy = config.LangPolicy{AudioMode: "apply", AudioKeep: []string{"eng"}}
	}); err != nil {
		t.Fatal(err)
	}

	// Requires confirm=true.
	rec := doJSON(t, s, "POST", "/api/v1/langprune/apply", map[string]any{})
	if rec.Code != 400 {
		t.Fatalf("without confirm: status = %d, want 400", rec.Code)
	}

	onlyPrune := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A",
		VideoCodec: "hevc", Container: "mkv", Width: 1920, Height: 1080, Duration: 100,
		Audio: []store.AudioStream{{Index: 0, Codec: "aac", Lang: "eng"}, {Index: 1, Codec: "aac", Lang: "jpn"}}})
	nothingToDo := mustUpsert(t, s.st, &store.File{Path: "/m/b.mkv", Library: "movies", Title: "B",
		VideoCodec: "hevc", Container: "mkv", Width: 1920, Height: 1080, Duration: 100,
		Audio: []store.AudioStream{{Index: 0, Codec: "aac", Lang: "eng"}}})

	rec = doJSON(t, s, "POST", "/api/v1/langprune/apply", map[string]any{"confirm": true})
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Queued  int `json:"queued"`
		Skipped int `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Queued != 1 {
		t.Fatalf("queued = %d, want 1 (only a.mkv has a droppable track)", out.Queued)
	}
	if queued, _ := s.st.HasQueuedForFile(onlyPrune.Path); !queued {
		t.Error("a.mkv should have a queued job")
	}
	if queued, _ := s.st.HasQueuedForFile(nothingToDo.Path); queued {
		t.Error("b.mkv has nothing to prune and should not be queued")
	}
}

// Regression: the report handler re-derives "is this side active" from
// keep-list non-emptiness (so the preview reflects a mode that was
// never saved as "apply" — e.g. "report" globally but scoped custom).
// A library-off override must survive that re-derivation. Found live
// during live verification: with the global mode "apply" and a real
// keep list, an "off" override on the movies library was silently
// undone because it cleared the mode but (before the config.go fix)
// left the still-global keep list behind.
func TestLangpruneReportRespectsLibraryOffOverride(t *testing.T) {
	s := newTestServer(t)
	if err := s.cfg.Update(func(c *config.Config) {
		*c = config.Default()
		c.LangPolicy = config.LangPolicy{AudioMode: "apply", AudioKeep: []string{"eng"}}
		c.LangLibraryOverrides = map[string]config.LangOverride{"movies": {Mode: "off"}}
	}); err != nil {
		t.Fatal(err)
	}
	mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A", Duration: 100,
		Audio: []store.AudioStream{{Index: 0, Lang: "eng"}, {Index: 1, Lang: "jpn"}}})

	rec := doJSON(t, s, "GET", "/api/v1/langprune/report", nil)
	var out langpruneReport
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Files != 0 || len(out.ByLanguage) != 0 {
		t.Errorf("a library-off override must silence the report for that library: %+v", out)
	}
}

func TestLangpruneReportEmptyKeepListDropsNothing(t *testing.T) {
	s := newTestServer(t)
	mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A", Duration: 100,
		Audio: []store.AudioStream{{Index: 0, Lang: "eng"}, {Index: 1, Lang: "jpn"}}})

	rec := doJSON(t, s, "GET", "/api/v1/langprune/report", nil)
	var out langpruneReport
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Files != 0 || len(out.ByLanguage) != 0 {
		t.Errorf("an empty keep list must never preview as 'drop everything': %+v", out)
	}
}
