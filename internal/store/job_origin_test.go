package store

import (
	"path/filepath"
	"testing"
)

func TestJobOriginRoundTrip(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Origin defaults to "manual" when left unset, matching old callers
	// that never set it.
	j1 := &Job{SrcPath: "/m/a.mp4", Backend: "qsv", Codec: "hevc", MaxAttempts: 1}
	if err := st.CreateJob(j1); err != nil {
		t.Fatal(err)
	}
	if j1.Origin != "manual" {
		t.Errorf("default origin = %q, want manual", j1.Origin)
	}

	j2 := &Job{SrcPath: "/m/b.mp4", Backend: "qsv", Codec: "hevc", MaxAttempts: 1,
		Origin: "issue-fix", Reason: "Issue: legacy_codec"}
	if err := st.CreateJob(j2); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetJob(j2.ID)
	if err != nil || got == nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Origin != "issue-fix" || got.Reason != "Issue: legacy_codec" {
		t.Errorf("got origin=%q reason=%q, want issue-fix / Issue: legacy_codec", got.Origin, got.Reason)
	}

	got1, err := st.GetJob(j1.ID)
	if err != nil || got1 == nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got1.Origin != "manual" || got1.Reason != "" {
		t.Errorf("got origin=%q reason=%q, want manual / \"\"", got1.Origin, got1.Reason)
	}

	list, err := st.ListJobs(nil, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("ListJobs returned %d, want 2", len(list))
	}
}
