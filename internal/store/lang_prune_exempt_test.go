package store

import (
	"path/filepath"
	"testing"
)

func TestSetLangPruneExemptSurvivesReprobe(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	f := &File{Path: "/m/a.mkv", Library: "movies", Title: "A"}
	if err := st.UpsertFile(f, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetLangPruneExempt(f.ID, true); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetFile(f.ID)
	if err != nil || got == nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !got.LangPruneExempt {
		t.Fatal("expected LangPruneExempt = true")
	}

	// A reprobe (UpsertFile on the same path again) must not clear it —
	// it's user-set, not probe-derived.
	if err := st.UpsertFile(&File{Path: "/m/a.mkv", Library: "movies", Title: "A", VideoCodec: "hevc"}, nil); err != nil {
		t.Fatal(err)
	}
	got2, err := st.GetFile(f.ID)
	if err != nil || got2 == nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !got2.LangPruneExempt {
		t.Error("LangPruneExempt should survive a reprobe")
	}

	if err := st.SetLangPruneExempt(f.ID, false); err != nil {
		t.Fatal(err)
	}
	got3, _ := st.GetFile(f.ID)
	if got3.LangPruneExempt {
		t.Error("expected LangPruneExempt = false after clearing")
	}
}
