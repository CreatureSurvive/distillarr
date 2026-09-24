package api

import (
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

func TestArrPolicyForNoItem(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A"})
	if p := ArrPolicyFor(s.st, s.cfg)(f.ID); p != nil {
		t.Errorf("no arr_items row should give nil policy, got %+v", p)
	}
}

func TestArrPolicyForUpgradePending(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A"})
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "radarr", Name: "Radarr"}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{
		{FileID: f.ID, InstanceID: "radarr", Kind: "radarr", Monitored: true, CutoffNotMet: true},
	}); err != nil {
		t.Fatal(err)
	}

	p := ArrPolicyFor(s.st, s.cfg)(f.ID)
	if p == nil || !p.UpgradePending || p.Instance != "Radarr" {
		t.Fatalf("got %+v", p)
	}
}

func TestArrPolicyForUpgradePendingDisabledByToggle(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A"})
	off := false
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "radarr", Name: "Radarr", SkipUpgradePending: &off}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{
		{FileID: f.ID, InstanceID: "radarr", Kind: "radarr", Monitored: true, CutoffNotMet: true},
	}); err != nil {
		t.Fatal(err)
	}

	p := ArrPolicyFor(s.st, s.cfg)(f.ID)
	if p == nil || p.UpgradePending {
		t.Fatalf("toggle off should not set UpgradePending, got %+v", p)
	}
}

func TestArrPolicyForTags(t *testing.T) {
	s := newTestServer(t)
	skipF := mustUpsert(t, s.st, &store.File{Path: "/m/skip.mkv", Library: "movies", Title: "Skip"})
	av1F := mustUpsert(t, s.st, &store.File{Path: "/m/av1.mkv", Library: "movies", Title: "AV1"})
	plainF := mustUpsert(t, s.st, &store.File{Path: "/m/plain.mkv", Library: "movies", Title: "Plain"})

	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "radarr", Name: "Radarr"}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetArrTags("radarr", map[int64]string{1: "mt-skip", 2: "mt-av1", 3: "unrelated"}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{
		{FileID: skipF.ID, InstanceID: "radarr", Kind: "radarr", Tags: store.JoinTagIDs([]int64{1, 3})},
		{FileID: av1F.ID, InstanceID: "radarr", Kind: "radarr", Tags: store.JoinTagIDs([]int64{2})},
		{FileID: plainF.ID, InstanceID: "radarr", Kind: "radarr", Tags: store.JoinTagIDs([]int64{3})},
	}); err != nil {
		t.Fatal(err)
	}

	policy := ArrPolicyFor(s.st, s.cfg)
	if p := policy(skipF.ID); p == nil || !p.Skip {
		t.Errorf("skip tag: got %+v", p)
	}
	if p := policy(av1F.ID); p == nil || p.Codec != "av1" {
		t.Errorf("av1 tag: got %+v", p)
	}
	if p := policy(plainF.ID); p == nil || p.Skip || p.Codec != "" || p.RemuxOnly {
		t.Errorf("unrelated tag only: got %+v, want no opinion", p)
	}
}

func TestArrPolicyForTagsDisabledByToggle(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/skip.mkv", Library: "movies", Title: "Skip"})
	off := false
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "radarr", Name: "Radarr", TagPolicyEnabled: &off}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetArrTags("radarr", map[int64]string{1: "mt-skip"}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{
		{FileID: f.ID, InstanceID: "radarr", Kind: "radarr", Tags: store.JoinTagIDs([]int64{1})},
	}); err != nil {
		t.Fatal(err)
	}

	p := ArrPolicyFor(s.st, s.cfg)(f.ID)
	if p == nil || p.Skip {
		t.Fatalf("tag policy off should ignore the skip tag, got %+v", p)
	}
}

func TestArrPolicyForCustomTagNames(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A"})
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "radarr", Name: "Radarr", SkipTag: "never-touch"}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetArrTags("radarr", map[int64]string{1: "never-touch", 2: "mt-skip"}); err != nil {
		t.Fatal(err)
	}
	// Has the DEFAULT skip tag name ("mt-skip") but not the CONFIGURED
	// one — must not be treated as skip once a custom name is set.
	if err := s.st.UpsertArrItems([]store.ArrItem{
		{FileID: f.ID, InstanceID: "radarr", Kind: "radarr", Tags: store.JoinTagIDs([]int64{2})},
	}); err != nil {
		t.Fatal(err)
	}
	if p := ArrPolicyFor(s.st, s.cfg)(f.ID); p == nil || p.Skip {
		t.Fatalf("got %+v, want no skip (custom tag name didn't match)", p)
	}
}

func TestArrPolicyForRemovedInstanceGivesNil(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A"})
	// An arr_items row exists, but no matching instance in config
	// (removed since the last sync).
	if err := s.st.UpsertArrItems([]store.ArrItem{
		{FileID: f.ID, InstanceID: "gone", Kind: "radarr", Monitored: true, CutoffNotMet: true},
	}); err != nil {
		t.Fatal(err)
	}
	if p := ArrPolicyFor(s.st, s.cfg)(f.ID); p != nil {
		t.Errorf("got %+v, want nil (no config for the owning instance)", p)
	}
}
