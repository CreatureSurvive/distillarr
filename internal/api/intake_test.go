// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"strconv"
	"testing"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/autopilot"
	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/recs"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// newIntakeRow creates a real file + intake row in st (the FK on
// intake.file_id needs a real files row to exist), independent of
// whatever the test's fake getFile later returns for that id — the fake
// controls what promoteOne "sees" about the file; the real row only
// anchors the intake row itself.
func newIntakeRow(t *testing.T, st *store.Store, state store.IntakeState, notBefore time.Time) store.Intake {
	t.Helper()
	f := &store.File{Path: "/m/" + t.Name() + ".mkv", Library: "movies", Title: "T"}
	if err := st.UpsertFile(f, nil); err != nil {
		t.Fatal(err)
	}
	it, err := st.UpsertIntake(f.ID, "webhook", "test reason", "", notBefore)
	if err != nil {
		t.Fatal(err)
	}
	if state != store.IntakeWaiting {
		if err := st.SetIntakeState(it.ID, state, ""); err != nil {
			t.Fatal(err)
		}
		it, err = st.IntakeByID(it.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	return *it
}

func fakeFile(id int64, nlink int) *store.File {
	return &store.File{ID: id, Path: "/m/f.mkv", Library: "movies", Title: "T", Nlink: nlink}
}

func TestPromoteOneQueuesWhenClear(t *testing.T) {
	st := newTestServer(t).st
	row := newIntakeRow(t, st, store.IntakeWaiting, time.Now().Add(-time.Minute))

	var enqueuedOrigin, enqueuedReason string
	p := &intakePromoter{
		now:     time.Now,
		reprobe: func(string) error { return nil },
		getFile: func(id int64) (*store.File, error) { return fakeFile(id, 1), nil },
		resolve: func(f *store.File, override *encode.Settings) (encode.Settings, recs.Recommendation) {
			return encode.Settings{Codec: "hevc"}, recs.Recommendation{Action: "transcode"}
		},
		enqueue: func(f *store.File, st encode.Settings, origin, reason string) (*store.Job, error) {
			enqueuedOrigin, enqueuedReason = origin, reason
			return &store.Job{ID: 555}, nil
		},
	}
	p.promoteOne(st, row)

	got, err := st.IntakeByID(row.ID)
	if err != nil || got == nil {
		t.Fatalf("IntakeByID: %v", err)
	}
	if got.State != store.IntakeQueued || got.JobID != 555 {
		t.Fatalf("got %+v, want queued with job_id 555", got)
	}
	if enqueuedOrigin != "webhook" || enqueuedReason != "test reason" {
		t.Errorf("enqueue got origin=%q reason=%q, want the row's own", enqueuedOrigin, enqueuedReason)
	}
}

func TestPromoteOneHoldsHardlinked(t *testing.T) {
	st := newTestServer(t).st
	row := newIntakeRow(t, st, store.IntakeWaiting, time.Now().Add(-time.Minute))

	enqueueCalled := false
	p := &intakePromoter{
		now:     time.Now,
		reprobe: func(string) error { return nil },
		getFile: func(id int64) (*store.File, error) { return fakeFile(id, 3), nil }, // shares data with 2 other links
		resolve: func(f *store.File, override *encode.Settings) (encode.Settings, recs.Recommendation) {
			return encode.Settings{}, recs.Recommendation{Action: "transcode"}
		},
		enqueue: func(f *store.File, st encode.Settings, origin, reason string) (*store.Job, error) {
			enqueueCalled = true
			return &store.Job{ID: 1}, nil
		},
	}
	p.promoteOne(st, row)

	got, err := st.IntakeByID(row.ID)
	if err != nil || got == nil {
		t.Fatalf("IntakeByID: %v", err)
	}
	if got.State != store.IntakeNeedsConfirmation || got.HoldReason != "hardlinked" {
		t.Fatalf("got %+v, want needs_confirmation/hardlinked", got)
	}
	if enqueueCalled {
		t.Error("a hardlinked file must never be enqueued without confirmation")
	}
}

// A needs_confirmation row whose hardlink resolved on its own (nlink
// back to 1) must be promoted the very next pass, same as a fresh
// waiting row.
func TestPromoteOneRecoversFromHardlinkOnRecheck(t *testing.T) {
	st := newTestServer(t).st
	row := newIntakeRow(t, st, store.IntakeNeedsConfirmation, time.Now().Add(time.Hour)) // not_before irrelevant here

	p := &intakePromoter{
		now:     time.Now,
		reprobe: func(string) error { return nil },
		getFile: func(id int64) (*store.File, error) { return fakeFile(id, 1), nil }, // link is gone now
		resolve: func(f *store.File, override *encode.Settings) (encode.Settings, recs.Recommendation) {
			return encode.Settings{}, recs.Recommendation{Action: "transcode"}
		},
		enqueue: func(f *store.File, st encode.Settings, origin, reason string) (*store.Job, error) {
			return &store.Job{ID: 7}, nil
		},
	}
	p.promoteOne(st, row)

	got, err := st.IntakeByID(row.ID)
	if err != nil || got == nil {
		t.Fatalf("IntakeByID: %v", err)
	}
	if got.State != store.IntakeQueued {
		t.Fatalf("state = %v, want queued once the hardlink resolved", got.State)
	}
}

func TestPromoteOneDismissesSkipRecommendation(t *testing.T) {
	st := newTestServer(t).st
	row := newIntakeRow(t, st, store.IntakeWaiting, time.Now().Add(-time.Minute))

	enqueueCalled := false
	p := &intakePromoter{
		now:     time.Now,
		reprobe: func(string) error { return nil },
		getFile: func(id int64) (*store.File, error) { return fakeFile(id, 1), nil },
		resolve: func(f *store.File, override *encode.Settings) (encode.Settings, recs.Recommendation) {
			return encode.Settings{}, recs.Recommendation{Action: "skip", Reason: "Tagged to skip in Radarr"}
		},
		enqueue: func(f *store.File, st encode.Settings, origin, reason string) (*store.Job, error) {
			enqueueCalled = true
			return &store.Job{ID: 1}, nil
		},
	}
	p.promoteOne(st, row)

	got, err := st.IntakeByID(row.ID)
	if err != nil || got == nil {
		t.Fatalf("IntakeByID: %v", err)
	}
	if got.State != store.IntakeDismissed || got.HoldReason != "Tagged to skip in Radarr" {
		t.Fatalf("got %+v, want dismissed with the recommendation's reason", got)
	}
	if enqueueCalled {
		t.Error("a skip recommendation must never be enqueued")
	}
}

// A transient reprobe failure (e.g. the file briefly unreadable mid-move)
// must leave the row alone for the next pass, not misclassify it.
func TestPromoteOneLeavesRowAloneOnReprobeError(t *testing.T) {
	st := newTestServer(t).st
	row := newIntakeRow(t, st, store.IntakeWaiting, time.Now().Add(-time.Minute))

	p := &intakePromoter{
		now:     time.Now,
		reprobe: func(string) error { return errFakeReprobe },
		getFile: func(id int64) (*store.File, error) { return fakeFile(id, 1), nil },
		resolve: func(f *store.File, override *encode.Settings) (encode.Settings, recs.Recommendation) {
			t.Fatal("resolve must not be called when the reprobe failed")
			return encode.Settings{}, recs.Recommendation{}
		},
		enqueue: func(f *store.File, st encode.Settings, origin, reason string) (*store.Job, error) {
			t.Fatal("enqueue must not be called when the reprobe failed")
			return nil, nil
		},
	}
	p.promoteOne(st, row)

	got, err := st.IntakeByID(row.ID)
	if err != nil || got == nil {
		t.Fatalf("IntakeByID: %v", err)
	}
	if got.State != store.IntakeWaiting {
		t.Fatalf("state = %v, want unchanged (waiting) after a reprobe error", got.State)
	}
}

func TestPromoteOneDismissesWhenFileGone(t *testing.T) {
	st := newTestServer(t).st
	row := newIntakeRow(t, st, store.IntakeWaiting, time.Now().Add(-time.Minute))

	p := &intakePromoter{
		now:     time.Now,
		reprobe: func(string) error { return nil },
		getFile: func(id int64) (*store.File, error) { return nil, nil },
		resolve: func(f *store.File, override *encode.Settings) (encode.Settings, recs.Recommendation) {
			t.Fatal("resolve must not be called for a missing file")
			return encode.Settings{}, recs.Recommendation{}
		},
	}
	p.promoteOne(st, row)

	got, err := st.IntakeByID(row.ID)
	if err != nil || got == nil {
		t.Fatalf("IntakeByID: %v", err)
	}
	if got.State != store.IntakeDismissed {
		t.Fatalf("state = %v, want dismissed", got.State)
	}
}

// runIntakePass must use the promoter's own clock, not wall time: a
// fake clock set far in the future makes an otherwise-not-yet-due
// waiting row due this pass.
func TestRunIntakePassUsesFakeClock(t *testing.T) {
	st := newTestServer(t).st
	row := newIntakeRow(t, st, store.IntakeWaiting, time.Now().Add(24*time.Hour)) // not due by real time

	queued := false
	p := &intakePromoter{
		now:     func() time.Time { return time.Now().Add(48 * time.Hour) }, // fake clock past not_before
		reprobe: func(string) error { return nil },
		getFile: func(id int64) (*store.File, error) { return fakeFile(id, 1), nil },
		resolve: func(f *store.File, override *encode.Settings) (encode.Settings, recs.Recommendation) {
			return encode.Settings{}, recs.Recommendation{Action: "transcode"}
		},
		enqueue: func(f *store.File, st encode.Settings, origin, reason string) (*store.Job, error) {
			queued = true
			return &store.Job{ID: 1}, nil
		},
	}
	p.runIntakePass(st)

	if !queued {
		t.Error("a row due only under the fake clock must still be processed this pass")
	}
	got, _ := st.IntakeByID(row.ID)
	if got == nil || got.State != store.IntakeQueued {
		t.Fatalf("got %+v, want queued", got)
	}
}

var errFakeReprobe = fakeErr("reprobe failed")

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

// ---- autopilot hook ----

func TestPromoteOneAutopilotIgnoreDismisses(t *testing.T) {
	st := newTestServer(t).st
	row := newIntakeRow(t, st, store.IntakeWaiting, time.Now().Add(-time.Minute))

	enqueueCalled := false
	p := &intakePromoter{
		now:     time.Now,
		reprobe: func(string) error { return nil },
		getFile: func(id int64) (*store.File, error) { return fakeFile(id, 1), nil },
		resolve: func(f *store.File, override *encode.Settings) (encode.Settings, recs.Recommendation) {
			return encode.Settings{}, recs.Recommendation{Action: "transcode"}
		},
		enqueue: func(f *store.File, st encode.Settings, origin, reason string) (*store.Job, error) {
			enqueueCalled = true
			return &store.Job{ID: 1}, nil
		},
		autopilot: func(f *store.File, rec recs.Recommendation, origin string) *autopilot.Decision {
			return &autopilot.Decision{Action: "ignore", Reason: "rule X says skip"}
		},
	}
	p.promoteOne(st, row)

	got, err := st.IntakeByID(row.ID)
	if err != nil || got == nil {
		t.Fatalf("IntakeByID: %v", err)
	}
	if got.State != store.IntakeDismissed || got.HoldReason != "rule X says skip" {
		t.Fatalf("got %+v, want dismissed with the autopilot reason", got)
	}
	if enqueueCalled {
		t.Error("an autopilot ignore decision must never enqueue")
	}
}

func TestPromoteOneAutopilotQueueOverrideAppliesCodecAndQuality(t *testing.T) {
	st := newTestServer(t).st
	row := newIntakeRow(t, st, store.IntakeWaiting, time.Now().Add(-time.Minute))

	var gotSettings encode.Settings
	p := &intakePromoter{
		now:     time.Now,
		reprobe: func(string) error { return nil },
		getFile: func(id int64) (*store.File, error) { return fakeFile(id, 1), nil },
		resolve: func(f *store.File, override *encode.Settings) (encode.Settings, recs.Recommendation) {
			return encode.Settings{Codec: "hevc", Quality: 50}, recs.Recommendation{Action: "transcode"}
		},
		enqueue: func(f *store.File, st encode.Settings, origin, reason string) (*store.Job, error) {
			gotSettings = st
			return &store.Job{ID: 9}, nil
		},
		autopilot: func(f *store.File, rec recs.Recommendation, origin string) *autopilot.Decision {
			return &autopilot.Decision{Action: "queue_override", Codec: "av1", Quality: 80, Reason: "rule Y forces AV1"}
		},
	}
	p.promoteOne(st, row)

	if gotSettings.Codec != "av1" || gotSettings.Quality != 80 {
		t.Fatalf("got settings %+v, want the override codec/quality applied", gotSettings)
	}
	got, err := st.IntakeByID(row.ID)
	if err != nil || got == nil {
		t.Fatalf("IntakeByID: %v", err)
	}
	if got.State != store.IntakeQueued {
		t.Fatalf("state = %v, want queued", got.State)
	}
}

func TestPromoteOneAutopilotQueueOverridePruneLanguages(t *testing.T) {
	st := newTestServer(t).st
	row := newIntakeRow(t, st, store.IntakeWaiting, time.Now().Add(-time.Minute))

	var gotSettings encode.Settings
	forcePruneCalled := false
	p := &intakePromoter{
		now:     time.Now,
		reprobe: func(string) error { return nil },
		getFile: func(id int64) (*store.File, error) { return fakeFile(id, 1), nil },
		resolve: func(f *store.File, override *encode.Settings) (encode.Settings, recs.Recommendation) {
			return encode.Settings{Codec: "hevc"}, recs.Recommendation{Action: "transcode"}
		},
		enqueue: func(f *store.File, st encode.Settings, origin, reason string) (*store.Job, error) {
			gotSettings = st
			return &store.Job{ID: 11}, nil
		},
		autopilot: func(f *store.File, rec recs.Recommendation, origin string) *autopilot.Decision {
			return &autopilot.Decision{Action: "queue_override", PruneLanguages: true, Reason: "rule W prunes languages"}
		},
		forcePrune: func(f *store.File, st *encode.Settings) {
			forcePruneCalled = true
			st.Audio = append(st.Audio, encode.AudioTrack{Index: 2, Action: "drop"})
		},
	}
	p.promoteOne(st, row)

	if !forcePruneCalled {
		t.Fatal("a queue_override decision with PruneLanguages must call forcePrune")
	}
	if len(gotSettings.Audio) != 1 || gotSettings.Audio[0].Index != 2 || gotSettings.Audio[0].Action != "drop" {
		t.Fatalf("got settings.Audio %+v, want the forcePrune drop entry", gotSettings.Audio)
	}
}

func TestPromoteOneAutopilotQuickFixUsesQuickFixSettings(t *testing.T) {
	st := newTestServer(t).st
	row := newIntakeRow(t, st, store.IntakeWaiting, time.Now().Add(-time.Minute))

	quickFixCalled := false
	var gotSettings encode.Settings
	p := &intakePromoter{
		now:     time.Now,
		reprobe: func(string) error { return nil },
		getFile: func(id int64) (*store.File, error) { return fakeFile(id, 1), nil },
		resolve: func(f *store.File, override *encode.Settings) (encode.Settings, recs.Recommendation) {
			return encode.Settings{Codec: "hevc"}, recs.Recommendation{Action: "transcode"}
		},
		enqueue: func(f *store.File, st encode.Settings, origin, reason string) (*store.Job, error) {
			gotSettings = st
			return &store.Job{ID: 3}, nil
		},
		autopilot: func(f *store.File, rec recs.Recommendation, origin string) *autopilot.Decision {
			return &autopilot.Decision{Action: "quick_fix", Reason: "rule Z quick-fixes"}
		},
		quickFix: func(f *store.File) encode.Settings {
			quickFixCalled = true
			return encode.Settings{VideoCopy: true}
		},
	}
	p.promoteOne(st, row)

	if !quickFixCalled {
		t.Fatal("a quick_fix decision must call the quickFix builder")
	}
	if !gotSettings.VideoCopy {
		t.Fatalf("got settings %+v, want VideoCopy from the quick-fix builder", gotSettings)
	}
}

// When autopilot is off (the function field returns nil, matching the
// production wiring's AutopilotEnabled=false case), behavior must be
// exactly the older recommendation-only path.
func TestPromoteOneAutopilotOffFallsBackToPlainRecommendation(t *testing.T) {
	st := newTestServer(t).st
	row := newIntakeRow(t, st, store.IntakeWaiting, time.Now().Add(-time.Minute))

	p := &intakePromoter{
		now:     time.Now,
		reprobe: func(string) error { return nil },
		getFile: func(id int64) (*store.File, error) { return fakeFile(id, 1), nil },
		resolve: func(f *store.File, override *encode.Settings) (encode.Settings, recs.Recommendation) {
			return encode.Settings{}, recs.Recommendation{Action: "skip", Reason: "not worth it"}
		},
		enqueue: func(f *store.File, st encode.Settings, origin, reason string) (*store.Job, error) {
			t.Fatal("must not enqueue: a skip recommendation with autopilot off")
			return nil, nil
		},
		autopilot: func(f *store.File, rec recs.Recommendation, origin string) *autopilot.Decision { return nil },
	}
	p.promoteOne(st, row)

	got, err := st.IntakeByID(row.ID)
	if err != nil || got == nil {
		t.Fatalf("IntakeByID: %v", err)
	}
	if got.State != store.IntakeDismissed || got.HoldReason != "not worth it" {
		t.Fatalf("got %+v, want the plain-recommendation dismiss path", got)
	}
}

// An autopilot-driven queue decision must relabel the job "autopilot"
// (not the intake row's own origin) and give it a priority above the
// manual 100000 default, so MoveJob's origin!='autopilot' exclusion and
// the value-per-GPU-second ordering both actually apply.
func TestPromoteOneAutopilotQueueSetsAutopilotOriginAndPriority(t *testing.T) {
	realStore := newTestServer(t).st
	row := newIntakeRow(t, realStore, store.IntakeWaiting, time.Now().Add(-time.Minute))

	var enqueuedOrigin string
	p := &intakePromoter{
		now:     time.Now,
		reprobe: func(string) error { return nil },
		getFile: func(id int64) (*store.File, error) { return fakeFile(id, 1), nil },
		resolve: func(f *store.File, override *encode.Settings) (encode.Settings, recs.Recommendation) {
			// fakeFile's Size is 0, so EstOut < f.Size never holds — this
			// exercises the "no real savings signal" branch of
			// autopilotPriority, still asserting it lands above the base.
			return encode.Settings{Codec: "hevc"}, recs.Recommendation{Action: "transcode", EstOut: 1 << 30}
		},
		enqueue: func(f *store.File, settings encode.Settings, origin, reason string) (*store.Job, error) {
			enqueuedOrigin = origin
			j := &store.Job{FileID: f.ID, SrcPath: f.Path, Priority: 100000, Backend: "sw", MaxAttempts: 1, Origin: origin, Reason: reason}
			return j, realStore.CreateJob(j)
		},
		autopilot: func(f *store.File, rec recs.Recommendation, origin string) *autopilot.Decision {
			return &autopilot.Decision{Action: "queue", Reason: "rule matched"}
		},
		estimateSeconds: func(f *store.File, settings encode.Settings) (float64, bool) { return 120, false },
	}
	p.promoteOne(realStore, row)

	if enqueuedOrigin != "autopilot" {
		t.Errorf("enqueue origin = %q, want autopilot", enqueuedOrigin)
	}
	got, err := realStore.IntakeByID(row.ID)
	if err != nil || got == nil || got.State != store.IntakeQueued {
		t.Fatalf("IntakeByID: %+v, %v", got, err)
	}
	j, err := realStore.GetJob(got.JobID)
	if err != nil || j == nil {
		t.Fatalf("GetJob: %v", err)
	}
	if j.Priority <= autopilotPriorityBase {
		t.Errorf("job priority = %d, want > %d (manual's default)", j.Priority, autopilotPriorityBase)
	}
}

func TestAutopilotPriority(t *testing.T) {
	cases := []struct {
		name       string
		savedBytes int64
		secs       float64
		want       int
	}{
		{"no savings", 0, 120, autopilotPriorityMax},
		{"no time", 1 << 30, 0, autopilotPriorityMax},
		{"negative savings", -100, 120, autopilotPriorityMax},
		{"clamped to base+1", 1 << 40, 1, autopilotPriorityBase + 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := autopilotPriority(c.savedBytes, c.secs, false, false); got != c.want {
				t.Errorf("autopilotPriority(%d, %v) = %d, want %d", c.savedBytes, c.secs, got, c.want)
			}
		})
	}

	// Higher value per GPU-second must sort first (a smaller priority
	// number, since ClaimNext orders ascending).
	better := autopilotPriority(10<<30, 60, false, false) // 10 GiB saved in a minute
	worse := autopilotPriority(1<<30, 3600, false, false) // 1 GiB saved over an hour
	if !(autopilotPriorityBase < better && better < worse && worse <= autopilotPriorityMax) {
		t.Errorf("want base < better(%d) < worse(%d) <= max, got out of order", better, worse)
	}
}

// TestAutopilotPriorityUnderPressureQuickFixFirst: under disk
// pressure, a quick fix must outrank a real re-encode even when the
// re-encode's value per GPU-second is far higher — freeing space now
// matters more than value per GPU-second while the disk is tight.
func TestAutopilotPriorityUnderPressureQuickFixFirst(t *testing.T) {
	quickFix := autopilotPriority(1<<20, 5, true, true)       // tiny savings, a few seconds
	bigReencode := autopilotPriority(50<<30, 60, false, true) // huge value per GPU-second
	if !(quickFix < bigReencode) {
		t.Errorf("under pressure, quick fix (%d) must outrank a big re-encode (%d)", quickFix, bigReencode)
	}
	if !(autopilotPriorityBase < quickFix && bigReencode <= autopilotPriorityMax) {
		t.Errorf("both must still stay within (base, max]: quickFix=%d bigReencode=%d", quickFix, bigReencode)
	}
	// Outside pressure, the same two jobs sort purely by value per
	// GPU-second, so the big re-encode now wins.
	quickFixNoPressure := autopilotPriority(1<<20, 5, true, false)
	bigReencodeNoPressure := autopilotPriority(50<<30, 60, false, false)
	if !(bigReencodeNoPressure < quickFixNoPressure) {
		t.Errorf("without pressure, value per GPU-second must decide: bigReencode=%d quickFix=%d", bigReencodeNoPressure, quickFixNoPressure)
	}
}

// GET /api/v1/files/{id} includes an autopilot preview when enabled.
func TestFileDetailIncludesAutopilotWhenEnabled(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{
		Path: "/m/a.mkv", Library: "movies", Title: "A", VideoCodec: "h264",
		Size: 4_000_000_000, Duration: 3600, Width: 1920, Height: 1080,
	})
	if err := s.cfg.Update(func(c *config.Config) { c.AutopilotEnabled = true }); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, "GET", "/api/v1/files/"+strconv.FormatInt(f.ID, 10), nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !contains(rec.Body.String(), `"autopilot"`) {
		t.Errorf("body = %s, want an autopilot field with autopilot_enabled=true", rec.Body.String())
	}
}

func TestFileDetailOmitsAutopilotWhenDisabled(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A"})

	rec := doJSON(t, s, "GET", "/api/v1/files/"+strconv.FormatInt(f.ID, 10), nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if contains(rec.Body.String(), `"autopilot"`) {
		t.Error("autopilot_enabled defaults off: the field must be absent, not just empty")
	}
}

// ---- HTTP handlers ----

func TestIntakeApproveHandlerQueuesDespiteHardlink(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A", Nlink: 1, VideoCodec: "h264", Size: 1_000_000_000, Duration: 3600, Width: 1920, Height: 1080})
	it, err := s.st.UpsertIntake(f.ID, "webhook", "settled", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.st.SetIntakeState(it.ID, store.IntakeNeedsConfirmation, "hardlinked"); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, "POST", "/api/v1/intake/"+strconv.FormatInt(it.ID, 10)+"/approve", nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got, err := s.st.IntakeByID(it.ID)
	if err != nil || got == nil {
		t.Fatalf("IntakeByID: %v", err)
	}
	if got.State != store.IntakeQueued || got.JobID == 0 {
		t.Fatalf("got %+v, want queued with a job id", got)
	}
}

func TestIntakeDismissHandler(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A"})
	it, err := s.st.UpsertIntake(f.ID, "webhook", "", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, "POST", "/api/v1/intake/"+strconv.FormatInt(it.ID, 10)+"/dismiss", nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	got, err := s.st.IntakeByID(it.ID)
	if err != nil || got == nil {
		t.Fatalf("IntakeByID: %v", err)
	}
	if got.State != store.IntakeDismissed {
		t.Fatalf("state = %v, want dismissed", got.State)
	}
}

func TestIntakeListHandlerFiltersAndIncludesFileInfo(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A Movie"})
	if _, err := s.st.UpsertIntake(f.ID, "webhook", "", "", time.Now()); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, "GET", "/api/v1/intake?state=waiting", nil)
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if !contains(rec.Body.String(), `"file_title":"A Movie"`) {
		t.Errorf("body = %s, want file_title included", rec.Body.String())
	}
}

func TestPopularPriority(t *testing.T) {
	a := autopilotPriority(10<<30, 3600, false, false) // good value
	b := autopilotPriority(1<<30, 3600, false, false)  // poorer value
	if !(a < b) {
		t.Fatalf("value order broken: %d %d", a, b)
	}
	// Watched 5 times beats better value never watched.
	if !(popularPriority(b, 5) < popularPriority(a, 0)) {
		t.Errorf("popular: %d vs %d", popularPriority(b, 5), popularPriority(a, 0))
	}
	// Same count: value still decides.
	if !(popularPriority(a, 2) < popularPriority(b, 2)) {
		t.Error("ties must keep value order")
	}
	if popularPriority(0, 3) != 0 {
		t.Error("no-override priority must stay 0")
	}
	if p := popularPriority(a, 1000000); p <= autopilotPriorityBase {
		t.Errorf("must stay after manual jobs: %d", p)
	}
}
