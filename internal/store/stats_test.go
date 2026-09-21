package store

import (
	"path/filepath"
	"testing"
)

// An upscale grows the file on purpose. Whatever its size, it must never count
// as space saved (or lost): "saved" is about re-encodes and remuxes.
func TestUpscaleJobsAreNotSavings(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	add := func(settings string, src, out int64) {
		j := &Job{SrcPath: "/m/x.mp4", Backend: "qsv", Codec: "hevc", SettingsJSON: settings, MaxAttempts: 1, SrcSize: src}
		if err := st.CreateJob(j); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishJob(j.ID, StatusDone, out, "", ""); err != nil {
			t.Fatal(err)
		}
	}
	add(`{"codec":"hevc","quality":60}`, 1000, 400)                                 // real saving: 600
	add(`{"codec":"hevc","upscale_to":1080,"upscale_preset":"film-lanczos"}`, 500, 2000) // grew by 1500
	add(`{"codec":"hevc","upscale_to":2160}`, 9000, 3000)                          // "shrank": still not a saving

	saved, n, err := st.RealizedSavings()
	if err != nil || saved != 600 || n != 1 {
		t.Errorf("realized savings must count only the re-encode: saved=%d jobs=%d err=%v", saved, n, err)
	}
	h, err := st.HistoryStats()
	if err != nil {
		t.Fatal(err)
	}
	if h.Totals.Saved != 600 || h.Totals.SrcBytes != 1000 || h.Totals.OutBytes != 400 {
		t.Errorf("history totals must exclude upscales: %+v", h.Totals)
	}
	if h.Totals.Done != 3 || h.Totals.Upscaled != 2 {
		t.Errorf("all finished jobs still count as done, upscales counted separately: %+v", h.Totals)
	}
	for _, g := range h.Kinds {
		if g.Saved < 0 {
			t.Errorf("no group may show negative savings: %+v", g)
		}
	}
	if len(h.Top) != 1 {
		t.Errorf("top savers must not list upscales: %+v", h.Top)
	}
}

func TestClaimNextHonoursBothWindows(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	add := func(settings string, runNow bool) *Job {
		j := &Job{SrcPath: "/m/x.mp4", Backend: "qsv", Codec: "hevc", SettingsJSON: settings, MaxAttempts: 3, RunNow: runNow, Priority: 100}
		if err := st.CreateJob(j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	neural := add(`{"codec":"hevc","upscale_to":1080,"upscale_tier":"neural"}`, false)
	shader := add(`{"codec":"hevc","upscale_to":1080,"upscale_tier":"shader"}`, false)
	plain := add(`{"codec":"hevc"}`, false)

	// Encode window open, neural window closed: never the neural job.
	for _, want := range []int64{shader.ID, plain.ID} {
		j, _ := st.ClaimNext(true, false)
		if j == nil || j.ID != want {
			t.Fatalf("want job %d, got %+v", want, j)
		}
	}
	if j, _ := st.ClaimNext(true, false); j != nil {
		t.Errorf("a neural job must wait for its own window, got %d", j.ID)
	}
	// Neural window open with the encode window closed: the neural job runs.
	if j, _ := st.ClaimNext(false, true); j == nil || j.ID != neural.ID {
		t.Errorf("the neural window should release the neural job, got %+v", j)
	}
	// Run-now bypasses both windows.
	nowNeural := add(`{"upscale_tier":"neural"}`, true)
	if j, _ := st.ClaimNext(false, false); j == nil || j.ID != nowNeural.ID {
		t.Errorf("run-now must bypass the windows, got %+v", j)
	}
}

// A neural job that pauses at the end of each overnight window and resumes in
// the next is not failing: pausing must not spend its retries.
func TestPauseJobDoesNotSpendAnAttempt(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	j := &Job{SrcPath: "/m/x.mp4", Backend: "qsv", Codec: "hevc", SettingsJSON: `{"upscale_tier":"neural"}`, MaxAttempts: 3}
	st.CreateJob(j)
	for night := 1; night <= 6; night++ { // twice the retry budget
		c, _ := st.ClaimNext(false, true)
		if c == nil {
			t.Fatalf("night %d: the paused job should be claimable again", night)
		}
		if c.Attempts != 1 {
			t.Fatalf("night %d: attempts = %d, want 1 (a pause is not an attempt)", night, c.Attempts)
		}
		if err := st.PauseJob(c.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.PauseJob(9999); err != nil {
		t.Errorf("pausing a missing job is harmless: %v", err)
	}
}
