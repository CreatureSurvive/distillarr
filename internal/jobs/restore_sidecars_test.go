// SPDX-License-Identifier: GPL-3.0-or-later

package jobs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/store"
)

// TestRestoreTrashDeletesJobSidecars is restore test: reverting a
// job must delete the sidecar files it created, and only those (never
// touching the restored media file itself).
func TestRestoreTrashDeletesJobSidecars(t *testing.T) {
	e := newTestEngine(t)
	dir := t.TempDir()
	orig := filepath.Join(dir, "movie.mkv")
	trash := filepath.Join(dir, "movie.trash.mkv")
	keep := filepath.Join(dir, "movie.keep.txt") // must survive: not on the job's sidecar list
	sc := filepath.Join(dir, "movie.eng.srt")

	for path, content := range map[string]string{
		trash: "original bytes",
		orig:  "encoded bytes",
		keep:  "unrelated file",
		sc:    "1\n00:00:00,000 --> 00:00:01,000\nhi\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	j := &store.Job{SrcPath: orig, Status: store.StatusDone}
	if err := e.st.CreateJob(j); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetJobSidecars(j.ID, []string{sc}); err != nil {
		t.Fatal(err)
	}
	if err := e.st.AddTrash(store.TrashItem{OrigPath: orig, TrashPath: trash, CurrentPath: orig, Size: 5, JobID: j.ID}); err != nil {
		t.Fatal(err)
	}
	items, err := e.st.ListTrash()
	if err != nil || len(items) != 1 {
		t.Fatalf("ListTrash: %v, %+v", err, items)
	}

	if err := e.RestoreTrash(items[0].ID); err != nil {
		t.Fatalf("RestoreTrash: %v", err)
	}

	if _, err := os.Stat(sc); !os.IsNotExist(err) {
		t.Errorf("sidecar %s should be deleted, stat err = %v", sc, err)
	}
	if _, err := os.Stat(orig); err != nil {
		t.Errorf("restored original should exist at %s: %v", orig, err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("unrelated file should survive: %v", err)
	}
}
