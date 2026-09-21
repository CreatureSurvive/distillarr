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
