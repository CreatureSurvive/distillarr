package api

import (
	"encoding/json"
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/recs"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// backlogResponse mirrors autopilotBacklog's JSON shape for assertions.
type backlogResponse struct {
	DryRun           bool                   `json:"dry_run"`
	TotalCandidates  int                    `json:"total_candidates"`
	SelectedCount    int                    `json:"selected_count"`
	QueuedIntake     int                    `json:"queued_intake"`
	DiskPressure     bool                   `json:"disk_pressure"`
	BudgetMultiplier float64                `json:"budget_multiplier"`
	Selected         []autopilotBacklogFile `json:"selected"`
}

func TestAutopilotBacklogOrdersByValueAndRespectsBudget(t *testing.T) {
	s := newTestServer(t)
	a := mustUpsert(t, s.st, withRec(&store.File{
		Path: "/m/a.mkv", Library: "movies", Title: "A", Size: 10 << 30, Duration: 3600, Width: 1920, Height: 1080,
	}, recs.Recommendation{Action: "transcode", Savings: 70, EstOut: 7 << 30})) // saves 3 GB
	b := mustUpsert(t, s.st, withRec(&store.File{
		Path: "/m/b.mkv", Library: "movies", Title: "B", Size: 10 << 30, Duration: 3600, Width: 1920, Height: 1080,
	}, recs.Recommendation{Action: "transcode", Savings: 90, EstOut: 9 << 30})) // saves 1 GB

	if err := s.cfg.Update(func(c *config.Config) {
		c.AutopilotBudgetGB = 3.5 // a (3 GB) fits; a+b (4 GB) does not
	}); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, "POST", "/api/v1/autopilot/backlog?dry_run=true", nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out backlogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.DryRun {
		t.Error("dry_run=true must report dry_run:true")
	}
	if out.TotalCandidates != 2 {
		t.Errorf("total_candidates = %d, want 2", out.TotalCandidates)
	}
	if out.SelectedCount != 1 || len(out.Selected) != 1 {
		t.Fatalf("selected = %+v, want exactly the higher-value file", out.Selected)
	}
	if out.Selected[0].FileID != a.ID {
		t.Errorf("selected[0].file_id = %d, want %d (a saves more per estimated second)", out.Selected[0].FileID, a.ID)
	}
	if out.QueuedIntake != 0 {
		t.Error("a dry run must never create intake rows")
	}
	if list, err := s.st.ListIntake(string(store.IntakeWaiting)); err != nil || len(list) != 0 {
		t.Fatalf("ListIntake(waiting) = %+v, %v, want none after a dry run", list, err)
	}
	_ = b
}

func TestAutopilotBacklogRealRunOpensIntakeRows(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, withRec(&store.File{
		Path: "/m/c.mkv", Library: "movies", Title: "C", Size: 10 << 30, Duration: 3600, Width: 1920, Height: 1080,
	}, recs.Recommendation{Action: "transcode", Savings: 70, EstOut: 3 << 30}))

	rec := doJSON(t, s, "POST", "/api/v1/autopilot/backlog?dry_run=false", nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out backlogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.QueuedIntake != 1 {
		t.Fatalf("queued_intake = %d, want 1", out.QueuedIntake)
	}
	list, err := s.st.ListIntake(string(store.IntakeWaiting))
	if err != nil || len(list) != 1 || list[0].FileID != f.ID {
		t.Fatalf("ListIntake(waiting) = %+v, %v, want one row for %d", list, err, f.ID)
	}
	if list[0].Origin != "autopilot" {
		t.Errorf("intake row origin = %q, want autopilot", list[0].Origin)
	}
}

func TestAutopilotBacklogSkipsAlreadyQueuedFiles(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, withRec(&store.File{
		Path: "/m/d.mkv", Library: "movies", Title: "D", Size: 10 << 30, Duration: 3600, Width: 1920, Height: 1080,
	}, recs.Recommendation{Action: "transcode", Savings: 70, EstOut: 3 << 30}))
	if err := s.st.CreateJob(&store.Job{FileID: f.ID, SrcPath: f.Path, Priority: 100000, Backend: "sw", MaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, "POST", "/api/v1/autopilot/backlog?dry_run=true", nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var out backlogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.TotalCandidates != 0 {
		t.Errorf("total_candidates = %d, want 0 (already queued file must be excluded)", out.TotalCandidates)
	}
}

// TestAutopilotBacklogPressureMultipliesBudget: the same
// candidate is excluded by a tight budget normally, but included once
// disk-pressure mode multiplies it (default 2x).
func TestAutopilotBacklogPressureMultipliesBudget(t *testing.T) {
	s := newTestServer(t)
	mustUpsert(t, s.st, withRec(&store.File{
		Path: "/m/e.mkv", Library: "movies", Title: "E", Size: 10 << 30, Duration: 3600, Width: 1920, Height: 1080,
	}, recs.Recommendation{Action: "transcode", Savings: 60, EstOut: 4 << 30})) // saves 6 GB
	if err := s.cfg.Update(func(c *config.Config) {
		c.AutopilotBudgetGB = 5 // 6 GB saved doesn't fit alone
	}); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, "POST", "/api/v1/autopilot/backlog?dry_run=true", nil)
	var out backlogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.DiskPressure || out.BudgetMultiplier != 1 || out.SelectedCount != 0 {
		t.Fatalf("without pressure: %+v, want disk_pressure=false budget_multiplier=1 selected_count=0", out)
	}

	s.diskPressure.Store(true)
	rec = doJSON(t, s, "POST", "/api/v1/autopilot/backlog?dry_run=true", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.DiskPressure || out.BudgetMultiplier != 2 || out.SelectedCount != 1 {
		t.Fatalf("under pressure: %+v, want disk_pressure=true budget_multiplier=2 selected_count=1", out)
	}
}

// TestAutopilotBacklogPressureSortsQuickFixFirst: under
// pressure a quick fix outranks a much higher-value re-encode; without
// pressure, value per GPU-second decides as before.
func TestAutopilotBacklogPressureSortsQuickFixFirst(t *testing.T) {
	s := newTestServer(t)
	quick := mustUpsert(t, s.st, withRec(&store.File{
		Path: "/m/f.mkv", Library: "movies", Title: "Quick", Size: 10 << 30, Duration: 3600, Width: 1920, Height: 1080,
	}, recs.Recommendation{Action: "transcode", Savings: 5, EstOut: 9<<30 + 900<<20})) // saves ~0.1 GB
	big := mustUpsert(t, s.st, withRec(&store.File{
		Path: "/tv/g.mkv", Library: "tvshows", Title: "Big", Size: 10 << 30, Duration: 3600, Width: 1920, Height: 1080,
	}, recs.Recommendation{Action: "transcode", Savings: 80, EstOut: 2 << 30})) // saves 8 GB
	if err := s.cfg.Update(func(c *config.Config) {
		c.AutoRules = []config.AutoRule{
			{ID: "movies-quickfix", Name: "Movies quick fix", Enabled: true,
				When: config.RuleMatch{Libraries: []string{"movies"}}, Then: config.RuleAction{Kind: "quick_fix"}},
		}
	}); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, "POST", "/api/v1/autopilot/backlog?dry_run=true", nil)
	var out backlogResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Selected) != 2 || out.Selected[0].FileID != big.ID {
		t.Fatalf("without pressure, value per GPU-second must decide: %+v, want %d first", out.Selected, big.ID)
	}

	s.diskPressure.Store(true)
	rec = doJSON(t, s, "POST", "/api/v1/autopilot/backlog?dry_run=true", nil)
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Selected) != 2 || out.Selected[0].FileID != quick.ID || out.Selected[0].Action != "quick_fix" {
		t.Fatalf("under pressure, the quick fix must sort first: %+v, want %d first", out.Selected, quick.ID)
	}
}
