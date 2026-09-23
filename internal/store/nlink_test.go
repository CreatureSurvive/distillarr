package store

import (
	"path/filepath"
	"testing"
)

func TestUpsertFileDefaultsNlinkToOne(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// A caller that never sets Nlink (the zero value) must not record
	// "shares data with zero other links".
	f := &File{Path: "/m/a.mkv", Library: "movies", Title: "A"}
	if err := st.UpsertFile(f, nil); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetFile(f.ID)
	if err != nil || got == nil {
		t.Fatalf("GetFile: %v", err)
	}
	if got.Nlink != 1 {
		t.Errorf("nlink = %d, want 1 (default)", got.Nlink)
	}

	f2 := &File{Path: "/m/b.mkv", Library: "movies", Title: "B", Nlink: 3}
	if err := st.UpsertFile(f2, nil); err != nil {
		t.Fatal(err)
	}
	got2, err := st.GetFile(f2.ID)
	if err != nil || got2 == nil {
		t.Fatalf("GetFile: %v", err)
	}
	if got2.Nlink != 3 {
		t.Errorf("nlink = %d, want 3", got2.Nlink)
	}
}

func TestListFileStatIncludesNlink(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	f := &File{Path: "/m/a.mkv", Library: "movies", Title: "A", Size: 100, MtimeNS: 5, Nlink: 2}
	if err := st.UpsertFile(f, nil); err != nil {
		t.Fatal(err)
	}
	stats, err := st.ListFileStat("movies")
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].Nlink != 2 {
		t.Fatalf("got %+v, want one row with Nlink=2", stats)
	}
}

func TestUpdateNlinksBatches(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	a := &File{Path: "/m/a.mkv", Library: "movies", Title: "A", Nlink: 1}
	b := &File{Path: "/m/b.mkv", Library: "movies", Title: "B", Nlink: 1}
	if err := st.UpsertFile(a, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertFile(b, nil); err != nil {
		t.Fatal(err)
	}

	if err := st.UpdateNlinks(map[string]int{"/m/a.mkv": 2}); err != nil {
		t.Fatal(err)
	}
	gotA, _ := st.GetFile(a.ID)
	gotB, _ := st.GetFile(b.ID)
	if gotA.Nlink != 2 {
		t.Errorf("a.nlink = %d, want 2", gotA.Nlink)
	}
	if gotB.Nlink != 1 {
		t.Errorf("b.nlink = %d, want unchanged 1", gotB.Nlink)
	}

	// A nil/empty map must be a safe no-op, not an error.
	if err := st.UpdateNlinks(nil); err != nil {
		t.Errorf("UpdateNlinks(nil): %v", err)
	}
	if err := st.UpdateNlinks(map[string]int{}); err != nil {
		t.Errorf("UpdateNlinks({}): %v", err)
	}
}

func TestHardlinkedFilter(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	linked := &File{Path: "/m/linked.mkv", Library: "movies", Title: "Linked", Nlink: 2}
	plain := &File{Path: "/m/plain.mkv", Library: "movies", Title: "Plain", Nlink: 1}
	if err := st.UpsertFile(linked, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertFile(plain, nil); err != nil {
		t.Fatal(err)
	}

	files, total, err := st.ListFiles(FileFilter{Hardlinked: "yes", Season: -1}, "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(files) != 1 || files[0].Path != linked.Path {
		t.Fatalf("got %d files (total %d), want exactly the hardlinked one", len(files), total)
	}

	all, total, err := st.ListFiles(FileFilter{Season: -1}, "", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if total != 2 || len(all) != 2 {
		t.Fatalf("unfiltered: got %d (total %d), want both files", len(all), total)
	}
}
