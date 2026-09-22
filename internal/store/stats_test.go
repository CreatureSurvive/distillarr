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
