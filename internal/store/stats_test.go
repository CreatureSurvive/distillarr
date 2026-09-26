// SPDX-License-Identifier: GPL-3.0-or-later

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

// A restored job's savings were never actually kept: it must vanish
// from RealizedSavings and the history totals, while its status stays
// 'done' (the encode itself did complete).
func TestMarkJobRevertedExcludesFromStats(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	add := func(src, out int64) *Job {
		j := &Job{SrcPath: "/m/x.mp4", Backend: "qsv", Codec: "hevc", SettingsJSON: `{"codec":"hevc"}`, MaxAttempts: 1, SrcSize: src}
		if err := st.CreateJob(j); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishJob(j.ID, StatusDone, out, "", ""); err != nil {
			t.Fatal(err)
		}
		return j
	}
	kept := add(1000, 400)   // real, kept saving: 600
	reverted := add(2000, 800) // would-be saving of 1200, but gets restored below

	if saved, n, _ := st.RealizedSavings(); saved != 1800 || n != 2 {
		t.Fatalf("before revert: saved=%d jobs=%d, want 1800/2", saved, n)
	}
	if err := st.MarkJobReverted(reverted.ID); err != nil {
		t.Fatal(err)
	}
	saved, n, err := st.RealizedSavings()
	if err != nil || saved != 600 || n != 1 {
		t.Errorf("reverted job must drop out: saved=%d jobs=%d err=%v", saved, n, err)
	}
	h, err := st.HistoryStats()
	if err != nil {
		t.Fatal(err)
	}
	if h.Totals.Saved != 600 || h.Totals.Done != 1 {
		t.Errorf("history totals must exclude the reverted job: %+v", h.Totals)
	}
	j, err := st.GetJob(reverted.ID)
	if err != nil || j == nil || j.Status != StatusDone {
		t.Errorf("the job's own status stays done: %+v", j)
	}

	// Marking an already-reverted or nonexistent job is harmless.
	if err := st.MarkJobReverted(reverted.ID); err != nil {
		t.Errorf("re-marking must not error: %v", err)
	}
	if err := st.MarkJobReverted(0); err != nil {
		t.Errorf("a zero job id (no trash.job_id) must not error: %v", err)
	}
	_ = kept
}

// The one-time backfill catches jobs restored before reverted_at
// existed: a 'done' job whose source path now holds a file the exact
// size of what the job started from is back to its pre-job state.
func TestBackfillRevertedJobs(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	add := func(path string, src, out int64) *Job {
		j := &Job{SrcPath: path, Backend: "qsv", Codec: "hevc", SettingsJSON: `{"codec":"hevc"}`, MaxAttempts: 1, SrcSize: src}
		if err := st.CreateJob(j); err != nil {
			t.Fatal(err)
		}
		if err := st.FinishJob(j.ID, StatusDone, out, "", ""); err != nil {
			t.Fatal(err)
		}
		return j
	}
	reverted := add("/m/back.mp4", 1000, 400) // current file is back to 1000 bytes
	kept := add("/m/kept.mp4", 1000, 400)     // current file matches the job's own output

	upsert := func(path string, size int64) {
		if err := st.UpsertFile(&File{Path: path, Library: "movies", Title: path, Size: size, Container: "mp4", VideoCodec: "hevc"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	upsert("/m/back.mp4", 1000) // reverted: matches src_size, not output_size
	upsert("/m/kept.mp4", 400)  // kept: matches output_size, not src_size

	n, err := st.BackfillRevertedJobs()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected exactly 1 backfilled job, got %d", n)
	}
	if j, _ := st.GetJob(reverted.ID); j.Status != StatusDone {
		t.Fatal("status must stay done")
	}
	if saved, jobs, _ := st.RealizedSavings(); saved != 600 || jobs != 1 {
		t.Errorf("only the kept job should count: saved=%d jobs=%d", saved, jobs)
	}

	// The kv guard makes a second run a no-op, even against fresh matches.
	upsert("/m/kept.mp4", 1000) // now the kept job's path also matches src_size
	n2, err := st.BackfillRevertedJobs()
	if err != nil || n2 != 0 {
		t.Errorf("second run must be a no-op: n=%d err=%v", n2, err)
	}
	if saved, _, _ := st.RealizedSavings(); saved != 600 {
		t.Errorf("the kv guard must have prevented a second pass: saved=%d", saved)
	}
	_ = kept
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

func TestUpscaledFilesAndFilter(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	file := func(path string, w, h int) {
		f := &File{Path: path, Library: "movies", Title: path, Size: 1000, MtimeNS: 1, Container: "mp4", Width: w, Height: h, VideoCodec: "hevc"}
		if err := st.UpsertFile(f, nil); err != nil {
			t.Fatal(err)
		}
	}
	done := func(src, dest, settings string) {
		j := &Job{SrcPath: src, Backend: "qsv", Codec: "hevc", SettingsJSON: settings, MaxAttempts: 1, SrcSize: 1}
		st.CreateJob(j)
		st.SetJobDest(j.ID, dest)
		st.FinishJob(j.ID, StatusDone, 2000, "", "")
	}
	file("/m/copy.mp4", 854, 480)               // the original a copy-mode upscale left behind
	file("/m/copy - 1080p upscale.mp4", 1920, 1080) // ...and the copy it made
	file("/m/replaced.mp4", 1920, 1080)         // replace mode: same path, now upscaled
	file("/m/plain.mp4", 1280, 720)             // never upscaled, below 4K
	file("/m/uhd.mp4", 3840, 2160)              // already 4K: nothing to upscale to
	done("/m/copy.mp4", "/m/copy - 1080p upscale.mp4", `{"upscale_to":1080,"upscale_preset":"film-lanczos","upscale_tier":"shader"}`)
	done("/m/replaced.mp4", "/m/replaced.mp4", `{"upscale_to":720,"upscale_preset":"fsr","upscale_tier":"shader"}`)
	done("/m/replaced.mp4", "/m/replaced.mp4", `{"upscale_to":1080,"upscale_preset":"neural-anime","upscale_tier":"neural"}`) // a later one
	done("/m/plain.mp4", "/m/plain.mp4", `{"codec":"hevc","quality":60}`)                                                   // an ordinary re-encode

	recs, err := st.UpscaledFiles([]string{"/m/copy.mp4", "/m/copy - 1080p upscale.mp4", "/m/replaced.mp4", "/m/plain.mp4", "/m/uhd.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("only the two upscale outputs are flagged, got %v", recs)
	}
	if r := recs["/m/copy - 1080p upscale.mp4"]; r.To != 1080 || r.Preset != "film-lanczos" || r.Tier != "shader" {
		t.Errorf("copy-mode record: %+v", r)
	}
	if r := recs["/m/replaced.mp4"]; r.To != 1080 || r.Tier != "neural" {
		t.Errorf("the latest upscale of a path wins: %+v", r)
	}
	if _, ok := recs["/m/copy.mp4"]; ok {
		t.Error("the original a copy was made from is not itself an upscale")
	}
	if got, _ := st.UpscaledFiles(nil); len(got) != 0 {
		t.Error("no paths, no records")
	}

	names := func(up string) []string {
		fs, _, err := st.ListFiles(FileFilter{Library: "movies", Upscale: up}, "title", 0, 50)
		if err != nil {
			t.Fatalf("filter %q: %v", up, err)
		}
		var out []string
		for _, f := range fs {
			out = append(out, f.Path)
		}
		return out
	}
	if got := names("upscaled"); len(got) != 2 || got[0] != "/m/copy - 1080p upscale.mp4" || got[1] != "/m/replaced.mp4" {
		t.Errorf("upscaled: %v", got)
	}
	// Upscalable: below 4K and not already an upscale's output. The 4K file has
	// nothing to go to, and the outputs are already done.
	if got := names("upscalable"); len(got) != 2 || got[0] != "/m/copy.mp4" || got[1] != "/m/plain.mp4" {
		t.Errorf("upscalable: %v", got)
	}
	if got := names(""); len(got) != 5 {
		t.Errorf("no filter lists everything: %v", got)
	}
}

// Restarting the app mid-encode is not the job failing: a graceful shutdown
// requeues running jobs with their attempt refunded, however many restarts.
func TestRequeueRunningDoesNotSpendAnAttempt(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	j := &Job{SrcPath: "/m/x.mp4", Backend: "qsv", Codec: "hevc", SettingsJSON: `{}`, MaxAttempts: 3}
	st.CreateJob(j)
	for restart := 1; restart <= 6; restart++ {
		c, _ := st.ClaimNext(true, false)
		if c == nil {
			t.Fatalf("restart %d: the requeued job should be claimable again", restart)
		}
		if c.Attempts != 1 {
			t.Fatalf("restart %d: attempts = %d, want 1", restart, c.Attempts)
		}
		if err := st.SetJobTemp(c.ID, "/m/.distillarr-1.mp4.tmp", ""); err != nil {
			t.Fatal(err)
		}
		if n, err := st.RequeueRunning(); err != nil || n != 1 {
			t.Fatalf("restart %d: RequeueRunning = %d, %v", restart, n, err)
		}
		if g, _ := st.GetJob(c.ID); g.TempPath != "" {
			t.Fatalf("restart %d: temp path kept (%q): the next run would verify a half-written file", restart, g.TempPath)
		}
	}
}

// Tune-ahead measures jobs in the order the dispatcher will claim them,
// never upscales, and only what may run now (window closed: run-now only).
func TestQueuedForTuneFollowsClaimOrder(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	add := func(path string, prio int, settings string, runNow bool) *Job {
		j := &Job{SrcPath: path, Backend: "qsv", Codec: "hevc", SettingsJSON: settings, MaxAttempts: 3, Priority: prio, RunNow: runNow}
		if err := st.CreateJob(j); err != nil {
			t.Fatal(err)
		}
		return j
	}
	a := add("/m/a", 200, `{}`, false)
	b := add("/m/b", 100, `{}`, false)
	add("/m/up", 50, `{"upscale_to":2160}`, false)
	c := add("/m/c", 300, `{}`, true)
	got, err := st.QueuedForTune(true, 10)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, j := range got {
		ids = append(ids, j.ID)
	}
	if len(ids) != 3 || ids[0] != b.ID || ids[1] != a.ID || ids[2] != c.ID {
		t.Errorf("window open: got %v, want [%d %d %d]", ids, b.ID, a.ID, c.ID)
	}
	if got, _ := st.QueuedForTune(false, 10); len(got) != 1 || got[0].ID != c.ID {
		t.Errorf("window closed: only the run-now job, got %d jobs", len(got))
	}
}
