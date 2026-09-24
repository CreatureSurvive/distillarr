package store

import (
	"path/filepath"
	"testing"
)

func TestImageSubsModeAndOCRJSONSurviveReprobe(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	f := &File{Path: "/m/a.mkv", Library: "movies", Title: "A"}
	if err := st.UpsertFile(f, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetImageSubsMode(f.ID, "ocr"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOCRResult(f.ID, `{"track_index":8,"confidence":90}`); err != nil {
		t.Fatal(err)
	}

	// A reprobe (UpsertFile on the same path again) must not clear either
	// — both are user/job-set, not probe-derived.
	if err := st.UpsertFile(&File{Path: "/m/a.mkv", Library: "movies", Title: "A", VideoCodec: "hevc"}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetFile(f.ID)
	if err != nil || got == nil {
		t.Fatalf("GetFile: %v", err)
	}
	if got.ImageSubsMode != "ocr" {
		t.Errorf("ImageSubsMode should survive a reprobe, got %q", got.ImageSubsMode)
	}
	if got.OCRJSON != `{"track_index":8,"confidence":90}` {
		t.Errorf("OCRJSON should survive a reprobe, got %q", got.OCRJSON)
	}

	if err := st.SetImageSubsMode(f.ID, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOCRResult(f.ID, ""); err != nil {
		t.Fatal(err)
	}
	got2, _ := st.GetFile(f.ID)
	if got2.ImageSubsMode != "" || got2.OCRJSON != "" {
		t.Errorf("expected both cleared, got mode=%q ocr_json=%q", got2.ImageSubsMode, got2.OCRJSON)
	}
}
