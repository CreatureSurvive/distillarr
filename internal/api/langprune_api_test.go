package api

import (
	"encoding/json"
	"testing"

	"mediatrans/internal/config"
	"mediatrans/internal/store"
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
		if err := s.cfg.Update(func(c *config.Config) { *c = *cfg }); err != nil {
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
