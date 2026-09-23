package api

import (
	"strconv"
	"testing"
	"time"

	"mediatrans/internal/autopilot"
	"mediatrans/internal/config"
	"mediatrans/internal/encode"
	"mediatrans/internal/recs"
	"mediatrans/internal/store"
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
