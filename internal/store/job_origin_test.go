// SPDX-License-Identifier: GPL-3.0-or-later

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

// MoveJob must never fold an autopilot-origin job's priority into the
// 10/20/30… scheme it renumbers manual jobs into — otherwise a
// person reordering their own queue would silently destroy autopilot's
// value-per-GPU-second ordering.
func TestMoveJobExcludesAutopilotOriginJobs(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	auto := &Job{SrcPath: "/m/auto.mp4", Backend: "qsv", Codec: "hevc", MaxAttempts: 1, Origin: "autopilot", Priority: 150000}
	if err := st.CreateJob(auto); err != nil {
		t.Fatal(err)
	}
	m1 := &Job{SrcPath: "/m/m1.mp4", Backend: "qsv", Codec: "hevc", MaxAttempts: 1, Priority: 100000}
	if err := st.CreateJob(m1); err != nil {
		t.Fatal(err)
	}
	m2 := &Job{SrcPath: "/m/m2.mp4", Backend: "qsv", Codec: "hevc", MaxAttempts: 1, Priority: 100000}
	if err := st.CreateJob(m2); err != nil {
		t.Fatal(err)
	}

	// Move m2 to the front of the manual set.
	if err := st.MoveJob(m2.ID, m1.ID); err != nil {
		t.Fatal(err)
	}

	gotAuto, err := st.GetJob(auto.ID)
	if err != nil || gotAuto == nil {
		t.Fatalf("GetJob(auto): %v", err)
	}
	if gotAuto.Priority != 150000 {
		t.Errorf("autopilot job priority = %d, want unchanged at 150000", gotAuto.Priority)
	}

	gotM1, _ := st.GetJob(m1.ID)
	gotM2, _ := st.GetJob(m2.ID)
	if gotM2.Priority >= gotM1.Priority {
		t.Errorf("m2 priority %d must now sort before m1 priority %d", gotM2.Priority, gotM1.Priority)
	}
	if gotM1.Priority >= 100000 || gotM2.Priority >= 100000 {
		t.Errorf("renumbered manual jobs must land well below the 100000 default, got m1=%d m2=%d", gotM1.Priority, gotM2.Priority)
	}
}
