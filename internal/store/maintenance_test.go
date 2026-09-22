package store

import (
	"path/filepath"
	"testing"
)

func TestClearMeasurements(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	f := &File{Path: "/m/a.mp4", Library: "movies", Title: "a", Size: 1000, Container: "mp4", VideoCodec: "hevc"}
	if err := st.UpsertFile(f, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetTune(f.ID, `{"quality":60}`); err != nil {
		t.Fatal(err)
	}

	n, err := st.ClearMeasurements()
	if err != nil || n != 1 {
		t.Fatalf("cleared=%d err=%v, want 1", n, err)
	}
	got, _ := st.GetFile(f.ID)
	if got.TuneJSON != "" {
		t.Errorf("tune_json should be empty, got %q", got.TuneJSON)
	}
	// A second run finds nothing left to clear.
	if n, err := st.ClearMeasurements(); err != nil || n != 0 {
		t.Errorf("second run: cleared=%d err=%v, want 0", n, err)
	}
}

func TestClearCrop(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	f := &File{Path: "/m/a.mp4", Library: "movies", Title: "a", Size: 1000, Container: "mp4", VideoCodec: "hevc"}
	if err := st.UpsertFile(f, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCrop(f.ID, 1920, 800, 0, 140); err != nil {
		t.Fatal(err)
	}

	n, err := st.ClearCrop()
	if err != nil || n != 1 {
		t.Fatalf("cleared=%d err=%v, want 1", n, err)
	}
	got, _ := st.GetFile(f.ID)
	if got.CropChecked || got.CropW != 0 || got.CropH != 0 {
		t.Errorf("crop fields should be reset: %+v", got)
	}
}

func TestClearIssueTags(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	f := &File{Path: "/m/a.mp4", Library: "movies", Title: "a", Size: 1000, Container: "mp4", VideoCodec: "hevc"}
	if err := st.UpsertFile(f, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMeta(f.ID, "hvc1", 1); err != nil {
		t.Fatal(err)
	}

	n, err := st.ClearIssueTags()
	if err != nil || n != 1 {
		t.Fatalf("cleared=%d err=%v, want 1", n, err)
	}
	got, _ := st.GetFile(f.ID)
	if got.VideoTag != "" || got.Faststart != -1 || got.Issues != "" {
		t.Errorf("issue-detection fields should be reset: %+v", got)
	}
}

// A restored job's savings, an ordinary encode's savings, and job
// history clearing all interact through the same table: clearing
// history removes them all, but only for finished jobs.
func TestClearJobHistory(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	done := &Job{SrcPath: "/m/a.mp4", Backend: "qsv", Codec: "hevc", SettingsJSON: "{}", MaxAttempts: 1, SrcSize: 1000}
	st.CreateJob(done)
	st.FinishJob(done.ID, StatusDone, 400, "", "")

	failed := &Job{SrcPath: "/m/b.mp4", Backend: "qsv", Codec: "hevc", SettingsJSON: "{}", MaxAttempts: 1, SrcSize: 1000}
	st.CreateJob(failed)
	st.FinishJob(failed.ID, StatusFailed, 0, "boom", "")

	queued := &Job{SrcPath: "/m/c.mp4", Backend: "qsv", Codec: "hevc", SettingsJSON: "{}", MaxAttempts: 1, SrcSize: 1000}
	st.CreateJob(queued)

	n, err := st.ClearJobHistory()
	if err != nil || n != 2 {
		t.Fatalf("cleared=%d err=%v, want 2 (done+failed, not the queued one)", n, err)
	}
	if j, _ := st.GetJob(done.ID); j != nil {
		t.Error("done job should be gone")
	}
	if j, _ := st.GetJob(failed.ID); j != nil {
		t.Error("failed job should be gone")
	}
	if j, _ := st.GetJob(queued.ID); j == nil {
		t.Error("queued (active) job must survive")
	}
	if saved, jobs, _ := st.RealizedSavings(); saved != 0 || jobs != 0 {
		t.Errorf("stats should reflect the cleared history: saved=%d jobs=%d", saved, jobs)
	}
}
