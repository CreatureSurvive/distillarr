package store

import (
	"path/filepath"
	"testing"
)

// FailJobKeepAttempt must mark the job failed but give back the attempt
// that ClaimNext spent claiming it, so a user-triggered retry isn't
// penalized for a run that never had a real chance (source changed
// under us).
func TestFailJobKeepAttemptDoesNotSpendAnAttempt(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	j := &Job{SrcPath: "/m/x.mp4", Backend: "qsv", Codec: "hevc", MaxAttempts: 3}
	if err := st.CreateJob(j); err != nil {
		t.Fatal(err)
	}
	claimed, err := st.ClaimNext(true, true)
	if err != nil || claimed == nil || claimed.ID != j.ID {
		t.Fatalf("ClaimNext: %v, %+v", err, claimed)
	}
	if claimed.Attempts != 1 {
		t.Fatalf("attempts after claim = %d, want 1", claimed.Attempts)
	}

	if err := st.FailJobKeepAttempt(j.ID, "source changed during encode", ""); err != nil {
		t.Fatal(err)
	}

	got, err := st.GetJob(j.ID)
	if err != nil || got == nil {
		t.Fatalf("GetJob: %v", err)
	}
	if got.Status != StatusFailed {
		t.Errorf("status = %q, want failed", got.Status)
	}
	if got.Attempts != 0 {
		t.Errorf("attempts = %d, want 0 (given back)", got.Attempts)
	}
	if got.Error != "source changed during encode" {
		t.Errorf("error = %q", got.Error)
	}
	if got.OutputSize != 0 {
		t.Errorf("output_size = %d, want 0", got.OutputSize)
	}
}
