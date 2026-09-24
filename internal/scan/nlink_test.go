// SPDX-License-Identifier: GPL-3.0-or-later

package scan

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// walkLibrary must pick up a hardlink-count change on a file that is
// otherwise unchanged (same size, same mtime), without re-probing it,
// since nlink can change without either of those changing — the exact
// scenario a torrent client seeding a completed download creates.
func TestWalkLibraryUpdatesNlinkOnUnchangedFile(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "movies")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(root, "Movie (2020).mp4")
	if err := os.WriteFile(video, []byte("fake video bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(video)
	if err != nil {
		t.Fatal(err)
	}

	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Seed a DB row matching the file exactly (as if a previous scan
	// already probed it), with nlink=1.
	f := &store.File{Path: video, Library: "movies", Title: "Movie",
		Size: fi.Size(), MtimeNS: fi.ModTime().UnixNano(), Nlink: 1}
	if err := st.UpsertFile(f, nil); err != nil {
		t.Fatal(err)
	}

	cfg := config.NewManager(st)
	sc := New(st, cfg)
	lib := config.Library{Name: "movies", Path: root}

	seen := map[string]bool{}
	var probeMu sync.Mutex
	var toProbe []string

	// First pass: nothing changed, so it must not be queued for re-probe,
	// and nlink must stay 1.
	if err := sc.walkLibrary(lib, seen, &toProbe, &probeMu); err != nil {
		t.Fatal(err)
	}
	if len(toProbe) != 0 {
		t.Fatalf("unchanged file should not be queued for re-probe, got %v", toProbe)
	}
	got, err := st.GetFile(f.ID)
	if err != nil || got == nil {
		t.Fatalf("GetFile: %v", err)
	}
	if got.Nlink != 1 {
		t.Fatalf("nlink = %d, want 1 before linking", got.Nlink)
	}

	// Add a hardlink (simulating a torrent client still seeding the file)
	// without touching size or mtime.
	link := filepath.Join(dir, "seeding-copy.mp4")
	if err := os.Link(video, link); err != nil {
		t.Skipf("hardlinks not supported on this filesystem: %v", err)
	}

	seen = map[string]bool{}
	toProbe = nil
	if err := sc.walkLibrary(lib, seen, &toProbe, &probeMu); err != nil {
		t.Fatal(err)
	}
	if len(toProbe) != 0 {
		t.Fatalf("a linked-but-otherwise-unchanged file should still not be re-probed, got %v", toProbe)
	}
	got, err = st.GetFile(f.ID)
	if err != nil || got == nil {
		t.Fatalf("GetFile: %v", err)
	}
	if got.Nlink != 2 {
		t.Fatalf("nlink = %d, want 2 after hardlinking", got.Nlink)
	}

	// Removing the extra link must bring it back down.
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	seen = map[string]bool{}
	toProbe = nil
	if err := sc.walkLibrary(lib, seen, &toProbe, &probeMu); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetFile(f.ID)
	if err != nil || got == nil {
		t.Fatalf("GetFile: %v", err)
	}
	if got.Nlink != 1 {
		t.Fatalf("nlink = %d, want back to 1 after removing the link", got.Nlink)
	}
}
