package api

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/arr"
	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/recs"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// setPenaltyReport stores a codec-penalty report for instance id, the
// same kv key syncOneArrInstance writes to after a real sync.
func setPenaltyReport(t *testing.T, s *Server, instanceID string, penalties []arr.Penalty) {
	t.Helper()
	b, err := json.Marshal(arr.PenaltyReport{Penalties: penalties})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.st.KVSet("arr_penalties_"+instanceID, string(b)); err != nil {
		t.Fatal(err)
	}
}

func onePenalty() []arr.Penalty {
	return []arr.Penalty{{Profile: "HD-1080p", CustomFormat: "H265", Score: -100, Matches: "release title", Terms: []string{"x265", "hevc"}}}
}

func TestCodecPenaltyBlockedWhenUnacknowledged(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A"})
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "radarr", Name: "Radarr", Kind: "radarr"}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f.ID, InstanceID: "radarr", Kind: "radarr", ItemID: 1}}); err != nil {
		t.Fatal(err)
	}
	setPenaltyReport(t, s, "radarr", onePenalty())

	blocked, name := s.codecPenaltyBlocked(f, encode.Settings{Codec: "hevc"})
	if !blocked || name != "Radarr" {
		t.Fatalf("blocked=%v name=%q, want blocked=true name=Radarr", blocked, name)
	}
}

func TestCodecPenaltyAllowedWhenAcknowledged(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A"})
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "radarr", Name: "Radarr", Kind: "radarr", PenaltyAck: true}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f.ID, InstanceID: "radarr", Kind: "radarr", ItemID: 1}}); err != nil {
		t.Fatal(err)
	}
	setPenaltyReport(t, s, "radarr", onePenalty())

	if blocked, _ := s.codecPenaltyBlocked(f, encode.Settings{Codec: "hevc"}); blocked {
		t.Error("an acknowledged penalty must not block")
	}
}

func TestCodecPenaltyAllowedWhenNoPenaltyFound(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A"})
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "radarr", Name: "Radarr", Kind: "radarr"}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f.ID, InstanceID: "radarr", Kind: "radarr", ItemID: 1}}); err != nil {
		t.Fatal(err)
	}
	// No penalty report stored at all — the instance was never found to
	// have one, so a stale penalty_ack=false default must not block.
	if blocked, _ := s.codecPenaltyBlocked(f, encode.Settings{Codec: "hevc"}); blocked {
		t.Error("an instance with no stored penalty report must never block (penalty_ack defaults false for everyone)")
	}
}

func TestCodecPenaltyAllowsRemux(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A"})
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "radarr", Name: "Radarr", Kind: "radarr"}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f.ID, InstanceID: "radarr", Kind: "radarr", ItemID: 1}}); err != nil {
		t.Fatal(err)
	}
	setPenaltyReport(t, s, "radarr", onePenalty())

	// A quick-fix/remux never changes the codec, so it can never feed
	// the exact re-download loop the gate exists to prevent.
	if blocked, _ := s.codecPenaltyBlocked(f, encode.Settings{Codec: "hevc", VideoCopy: true}); blocked {
		t.Error("a video-copy remux must never be blocked by the codec-penalty gate")
	}
}

func TestCodecPenaltyAllowsUnmanagedFile(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A"})
	// No arr_items row: nothing manages this file.
	if blocked, _ := s.codecPenaltyBlocked(f, encode.Settings{Codec: "hevc"}); blocked {
		t.Error("a file no arr instance manages must never be blocked")
	}
}

// End-to-end through the real promotion path: a blocked file goes to
// needs_confirmation with hold_reason codec_penalty instead of being
// enqueued, using the production intakePromoter (not a fake).
func TestPromoteOneRespectsCodecPenaltyGateEndToEnd(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{
		Path: "/m/a.mkv", Library: "movies", Title: "A", Nlink: 1, VideoCodec: "h264",
		Size: 4_000_000_000, Duration: 3600, Width: 1920, Height: 1080,
	})
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "radarr", Name: "Radarr", Kind: "radarr"}}
		c.DefaultCodec = "hevc"
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f.ID, InstanceID: "radarr", Kind: "radarr", ItemID: 1}}); err != nil {
		t.Fatal(err)
	}
	setPenaltyReport(t, s, "radarr", onePenalty())

	row, err := s.st.UpsertIntake(f.ID, "webhook", "test", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p := s.newIntakePromoter()
	// Fake only the reprobe/getFile pieces that need a real ffprobe-able
	// file; everything else (resolve, enqueue, the gate) is the real
	// production wiring.
	p.reprobe = func(string) error { return nil }
	p.getFile = s.st.GetFile
	p.promoteOne(s.st, *row)

	got, err := s.st.IntakeByID(row.ID)
	if err != nil || got == nil {
		t.Fatalf("IntakeByID: %v", err)
	}
	if got.State != store.IntakeNeedsConfirmation || got.HoldReason != "codec_penalty" {
		t.Fatalf("got %+v, want needs_confirmation/codec_penalty", got)
	}
}

func TestFileDetailIncludesCodecPenaltyWarning(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{
		Path: "/m/a.mkv", Library: "movies", Title: "A", VideoCodec: "h264",
		Size: 4_000_000_000, Duration: 3600, Width: 1920, Height: 1080,
	})
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "radarr", Name: "Radarr", Kind: "radarr"}}
		c.DefaultCodec = "hevc"
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f.ID, InstanceID: "radarr", Kind: "radarr", ItemID: 1}}); err != nil {
		t.Fatal(err)
	}
	setPenaltyReport(t, s, "radarr", onePenalty())

	rec := doJSON(t, s, "GET", "/api/v1/files/"+strconv.FormatInt(f.ID, 10), nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !contains(rec.Body.String(), `"codec_penalty_warning":"Radarr"`) {
		t.Errorf("body = %s, want the codec_penalty_warning field naming the instance", rec.Body.String())
	}
}

func TestFileDetailOmitsCodecPenaltyWarningWhenClear(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A"})

	rec := doJSON(t, s, "GET", "/api/v1/files/"+strconv.FormatInt(f.ID, 10), nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if contains(rec.Body.String(), `codec_penalty_warning`) {
		t.Error("an unmanaged file must never carry a codec_penalty_warning")
	}
}

// ---- GET /api/v1/autopilot/preview ----

func withRec(f *store.File, rec recs.Recommendation) *store.File {
	f.RecJSON = rec.JSON()
	return f
}

func TestAutopilotPreviewGroupsByRuleAndAction(t *testing.T) {
	s := newTestServer(t)
	// Two files a rule matches (movies -> quick_fix), one the built-in
	// default queues, one the default ignores (below the savings floor),
	// and one non-candidate (Action=skip) that must be excluded entirely.
	mustUpsert(t, s.st, withRec(&store.File{Path: "/m/1.mkv", Library: "movies", Title: "M1", Size: 10 << 30},
		recs.Recommendation{Action: "transcode", Savings: 50, EstOut: 5 << 30}))
	mustUpsert(t, s.st, withRec(&store.File{Path: "/m/2.mkv", Library: "movies", Title: "M2", Size: 8 << 30},
		recs.Recommendation{Action: "transcode", Savings: 60, EstOut: 3 << 30}))
	mustUpsert(t, s.st, withRec(&store.File{Path: "/tv/3.mkv", Library: "tvshows", Title: "T1", Size: 4 << 30},
		recs.Recommendation{Action: "transcode", Savings: 40, EstOut: 2 << 30}))
	mustUpsert(t, s.st, withRec(&store.File{Path: "/tv/4.mkv", Library: "tvshows", Title: "T2", Size: 4 << 30},
		recs.Recommendation{Action: "transcode", Savings: 5, EstOut: 3800 << 20}))
	mustUpsert(t, s.st, withRec(&store.File{Path: "/tv/5.mkv", Library: "tvshows", Title: "T3 (skip)", Size: 4 << 30},
		recs.Recommendation{Action: "skip", Savings: 0}))

	if err := s.cfg.Update(func(c *config.Config) {
		c.MinSavingsPct = 20
		c.AutoRules = []config.AutoRule{
			{ID: "movies-quickfix", Name: "Movies quick fix", Enabled: true,
				When: config.RuleMatch{Libraries: []string{"movies"}}, Then: config.RuleAction{Kind: "quick_fix"}},
		}
	}); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, "GET", "/api/v1/autopilot/preview", nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Groups []struct {
			RuleID     string  `json:"rule_id"`
			Action     string  `json:"action"`
			Count      int     `json:"count"`
			EstSavedGB float64 `json:"est_saved_gb"`
			Sample     []struct {
				Title string `json:"title"`
			} `json:"sample"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	byKey := map[string]int{}
	for _, g := range out.Groups {
		byKey[g.RuleID+"|"+g.Action] = g.Count
	}
	if byKey["movies-quickfix|quick_fix"] != 2 {
		t.Errorf("movies rule group count = %d, want 2", byKey["movies-quickfix|quick_fix"])
	}
	if byKey["|queue"] != 1 {
		t.Errorf("default-queue group count = %d, want 1", byKey["|queue"])
	}
	if byKey["|ignore"] != 1 {
		t.Errorf("default-ignore group count = %d, want 1", byKey["|ignore"])
	}
	total := 0
	for _, g := range out.Groups {
		total += g.Count
	}
	if total != 4 {
		t.Errorf("total previewed files = %d, want 4 (the skip-action file must be excluded)", total)
	}
}

func TestAutopilotPreviewSkipsFilesWithoutCachedRecommendation(t *testing.T) {
	s := newTestServer(t)
	mustUpsert(t, s.st, &store.File{Path: "/m/unrec.mkv", Library: "movies", Title: "Unrecommended"}) // no RecJSON

	rec := doJSON(t, s, "GET", "/api/v1/autopilot/preview", nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if contains(rec.Body.String(), `"count"`) {
		t.Errorf("body = %s, want no groups for a file with no cached recommendation", rec.Body.String())
	}
}
