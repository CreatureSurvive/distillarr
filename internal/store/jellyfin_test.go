// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"path/filepath"
	"testing"
)

// A container change (e.g. .avi -> .mp4) moves the file to a new path
// that Jellyfin won't report until its own next scan. RenamePath keeps
// the cached row usable in the meantime, instead of it going stale
// until a full sync runs.
func TestRenamePath(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	old := "/tv/Show/Season 1/e01.avi"
	if err := st.UpsertJF([]JellyfinRow{{Path: old, ItemID: "abc", SeasonID: "s1", Genres: "Comedy"}}); err != nil {
		t.Fatal(err)
	}

	next := "/tv/Show/Season 1/e01.mp4"
	if err := st.RenamePath(old, next); err != nil {
		t.Fatal(err)
	}
	if row, _ := st.JellyfinByPath(old); row != nil {
		t.Errorf("old path should no longer resolve: %+v", row)
	}
	row, _ := st.JellyfinByPath(next)
	if row == nil || row.ItemID != "abc" || row.SeasonID != "s1" {
		t.Fatalf("new path should carry the same cached item: %+v", row)
	}
	if got := st.GenresFor(next); len(got) != 1 || got[0] != "Comedy" {
		t.Errorf("genre cache should invalidate and pick up the new path: %v", got)
	}

	// A row already sitting at the destination (Jellyfin resynced first)
	// must not make the rename fail with a PRIMARY KEY collision.
	if err := st.UpsertJF([]JellyfinRow{{Path: old, ItemID: "def"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.RenamePath(old, next); err != nil {
		t.Fatalf("rename onto an existing path must not error: %v", err)
	}
	row, _ = st.JellyfinByPath(next)
	if row == nil || row.ItemID != "def" {
		t.Fatalf("the renamed row should win at the destination: %+v", row)
	}
}
