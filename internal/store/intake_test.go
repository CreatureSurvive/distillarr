// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func testFile(t *testing.T, st *Store, path string) *File {
	t.Helper()
	f := &File{Path: path, Library: "movies", Title: "T"}
	if err := st.UpsertFile(f, nil); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestUpsertIntakeCreatesWaitingRow(t *testing.T) {
	st := testStore(t)
	f := testFile(t, st, "/m/a.mkv")
	notBefore := time.Now().Add(30 * time.Minute)

	it, err := st.UpsertIntake(f.ID, "webhook", "Imported by Sonarr", "", notBefore)
	if err != nil {
		t.Fatal(err)
	}
	if it.State != IntakeWaiting || it.Origin != "webhook" || it.Reason != "Imported by Sonarr" {
		t.Fatalf("got %+v", it)
	}
	if it.CreatedAt == "" || it.UpdatedAt == "" {
		t.Error("created_at/updated_at must be set")
	}
}

// A second UpsertIntake for the same file while the first row is still
// open must update that row in place, not create a second one — "at
// most one open row per file, the latest wins".
func TestUpsertIntakeReplacesOpenRow(t *testing.T) {
	st := testStore(t)
	f := testFile(t, st, "/m/a.mkv")

	first, err := st.UpsertIntake(f.ID, "webhook", "first", "", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.UpsertIntake(f.ID, "webhook", "second", "", time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID {
		t.Fatalf("expected the same row to be reused, got id %d then %d", first.ID, second.ID)
	}
	if second.Reason != "second" {
		t.Errorf("reason = %q, want the latest value", second.Reason)
	}

	all, err := st.ListIntake("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("len(all) = %d, want 1 (no duplicate row)", len(all))
	}
}

// Once a row is queued (closed), a later UpsertIntake for the same file
// must open a fresh row rather than reopening the closed one.
func TestUpsertIntakeAfterQueuedOpensNewRow(t *testing.T) {
	st := testStore(t)
	f := testFile(t, st, "/m/a.mkv")

	first, err := st.UpsertIntake(f.ID, "webhook", "first", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetIntakeQueued(first.ID, 99); err != nil {
		t.Fatal(err)
	}

	second, err := st.UpsertIntake(f.ID, "webhook", "second", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if second.ID == first.ID {
		t.Error("a closed row must not be reopened by a later UpsertIntake")
	}

	all, err := st.ListIntake("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("len(all) = %d, want 2 (the queued row plus the new waiting one)", len(all))
	}
}

func TestDueIntakeRespectsNotBefore(t *testing.T) {
	st := testStore(t)
	fPast := testFile(t, st, "/m/past.mkv")
	fFuture := testFile(t, st, "/m/future.mkv")

	past, err := st.UpsertIntake(fPast.ID, "webhook", "", "", time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertIntake(fFuture.ID, "webhook", "", "", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	due, err := st.DueIntake(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].ID != past.ID {
		t.Fatalf("due = %+v, want only the past-due row", due)
	}
}

// needs_confirmation rows are due every pass regardless of not_before,
// so a hardlink hold that resolved on its own gets picked up again.
func TestDueIntakeAlwaysIncludesNeedsConfirmation(t *testing.T) {
	st := testStore(t)
	f := testFile(t, st, "/m/a.mkv")
	it, err := st.UpsertIntake(f.ID, "webhook", "", "", time.Now().Add(time.Hour)) // not due by time
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetIntakeState(it.ID, IntakeNeedsConfirmation, "hardlinked"); err != nil {
		t.Fatal(err)
	}

	due, err := st.DueIntake(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 || due[0].ID != it.ID {
		t.Fatalf("due = %+v, want the needs_confirmation row despite its future not_before", due)
	}
}

func TestSetIntakeQueuedAndCount(t *testing.T) {
	st := testStore(t)
	f := testFile(t, st, "/m/a.mkv")
	it, err := st.UpsertIntake(f.ID, "webhook", "", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetIntakeState(it.ID, IntakeNeedsConfirmation, "hardlinked"); err != nil {
		t.Fatal(err)
	}
	if n, err := st.CountIntakeNeedsConfirmation(); err != nil || n != 1 {
		t.Fatalf("count = %d, err = %v, want 1", n, err)
	}

	if err := st.SetIntakeQueued(it.ID, 42); err != nil {
		t.Fatal(err)
	}
	got, err := st.IntakeByID(it.ID)
	if err != nil || got == nil {
		t.Fatalf("IntakeByID: %v", err)
	}
	if got.State != IntakeQueued || got.JobID != 42 {
		t.Fatalf("got %+v, want queued with job_id 42", got)
	}
	if got.HoldReason != "" {
		t.Errorf("hold_reason = %q, want cleared once queued", got.HoldReason)
	}
	if n, err := st.CountIntakeNeedsConfirmation(); err != nil || n != 0 {
		t.Fatalf("count = %d, err = %v, want 0 after queueing", n, err)
	}
}

func TestListIntakeFiltersByState(t *testing.T) {
	st := testStore(t)
	f1 := testFile(t, st, "/m/a.mkv")
	f2 := testFile(t, st, "/m/b.mkv")
	it1, err := st.UpsertIntake(f1.ID, "webhook", "", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertIntake(f2.ID, "webhook", "", "", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := st.SetIntakeState(it1.ID, IntakeDismissed, "skip"); err != nil {
		t.Fatal(err)
	}

	waiting, err := st.ListIntake(string(IntakeWaiting))
	if err != nil {
		t.Fatal(err)
	}
	if len(waiting) != 1 {
		t.Fatalf("waiting = %+v, want 1", waiting)
	}
	dismissed, err := st.ListIntake(string(IntakeDismissed))
	if err != nil {
		t.Fatal(err)
	}
	if len(dismissed) != 1 || dismissed[0].ID != it1.ID {
		t.Fatalf("dismissed = %+v", dismissed)
	}
}

// The intake row must go away when its file does (ON DELETE CASCADE).
func TestIntakeCascadesOnFileDelete(t *testing.T) {
	st := testStore(t)
	f := testFile(t, st, "/m/a.mkv")
	it, err := st.UpsertIntake(f.ID, "webhook", "", "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.dbW.Exec(`DELETE FROM files WHERE id=?`, f.ID); err != nil {
		t.Fatal(err)
	}
	got, err := st.IntakeByID(it.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Errorf("intake row survived its file's deletion: %+v", got)
	}
}
