package replace

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSameSource(t *testing.T) {
	base := &SrcStat{Size: 100, MtimeSec: 1000, MtimeNsec: 500, Ino: 42}
	same := &SrcStat{Size: 100, MtimeSec: 1000, MtimeNsec: 500, Ino: 42}
	if !SameSource(base, same) {
		t.Error("identical stats should compare equal")
	}

	diffSize := &SrcStat{Size: 200, MtimeSec: 1000, MtimeNsec: 500, Ino: 42}
	if SameSource(base, diffSize) {
		t.Error("a size change must be detected")
	}

	diffMtimeSec := &SrcStat{Size: 100, MtimeSec: 1001, MtimeNsec: 500, Ino: 42}
	if SameSource(base, diffMtimeSec) {
		t.Error("an mtime-seconds change must be detected")
	}

	diffMtimeNsec := &SrcStat{Size: 100, MtimeSec: 1000, MtimeNsec: 501, Ino: 42}
	if SameSource(base, diffMtimeNsec) {
		t.Error("a sub-second mtime change must be detected")
	}

	diffIno := &SrcStat{Size: 100, MtimeSec: 1000, MtimeNsec: 500, Ino: 99}
	if SameSource(base, diffIno) {
		t.Error("an inode change (replaced via rename) must be detected")
	}

	// atime is deliberately excluded: reading the file (ffprobe, a preview)
	// must not look like the source changed.
	diffAtimeOnly := &SrcStat{Size: 100, MtimeSec: 1000, MtimeNsec: 500, Ino: 42, AtimeSec: 99999}
	if !SameSource(base, diffAtimeOnly) {
		t.Error("an atime-only change must not count as the source changing")
	}

	if SameSource(nil, same) || SameSource(base, nil) || SameSource(nil, nil) {
		t.Error("a nil snapshot must never compare equal")
	}
}

// Reproduces the real scenario: something else (Sonarr/Radarr, a manual
// overwrite) replaces the file between the pre-encode snapshot and the
// post-encode check.
func TestSameSourceDetectsRealOverwrite(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(p, []byte("original content"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := Snapshot(p)
	if err != nil {
		t.Fatal(err)
	}

	// Force a distinct mtime: some filesystems have coarse mtime
	// resolution, and a same-second overwrite must still be caught by size.
	time.Sleep(10 * time.Millisecond)
	if err := os.WriteFile(p, []byte("upgraded release, much longer content"), 0o644); err != nil {
		t.Fatal(err)
	}

	after, err := Snapshot(p)
	if err != nil {
		t.Fatal(err)
	}
	if SameSource(before, after) {
		t.Error("an overwritten file must not be reported as unchanged")
	}
}

func TestSameSourceMissingFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "movie.mkv")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := Snapshot(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if _, err := Snapshot(p); err == nil {
		t.Error("Snapshot of a removed file should error")
	}
	_ = before
}
