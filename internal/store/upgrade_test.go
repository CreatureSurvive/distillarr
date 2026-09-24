package store

import (
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestDBUpgrade opens every fixture DB kept from a tagged release
// (testdata/<version>.db), migrates a copy to the current schema and
// runs the basic queries the app makes at boot. Add a fixture per
// release with DISTILLARR_WRITE_FIXTURE=<version>.
func TestDBUpgrade(t *testing.T) {
	fixtures, _ := filepath.Glob("testdata/*.db")
	if len(fixtures) == 0 {
		t.Fatal("no fixture DBs in internal/store/testdata")
	}
	for _, fx := range fixtures {
		t.Run(filepath.Base(fx), func(t *testing.T) {
			dst := filepath.Join(t.TempDir(), "upgrade.db")
			copyFileT(t, fx, dst)
			st, err := Open(dst)
			if err != nil {
				t.Fatalf("open/migrate: %v", err)
			}
			defer st.Close()
			if _, err := st.CountJobsByStatus(); err != nil {
				t.Errorf("jobs: %v", err)
			}
			if _, err := st.ListTrash(); err != nil {
				t.Errorf("trash: %v", err)
			}
			if _, ok, err := st.KVGet("config"); err != nil || !ok {
				t.Errorf("config kv: ok=%v err=%v", ok, err)
			}
			n := 0
			if err := st.EachFile(func(*File) error { n++; return nil }); err != nil || n == 0 {
				t.Errorf("files: n=%d err=%v", n, err)
			}
			if _, err := st.CountUsers(); err != nil {
				t.Errorf("users: %v", err)
			}
			if _, err := st.ListTrends(); err != nil {
				t.Errorf("trends: %v", err)
			}
			if _, _, err := st.RealizedSavings(); err != nil {
				t.Errorf("savings: %v", err)
			}
		})
	}
}

// TestWriteFixture creates testdata/<version>.db from the current schema
// with a little data. Only runs when DISTILLARR_WRITE_FIXTURE is set.
func TestWriteFixture(t *testing.T) {
	v := os.Getenv("DISTILLARR_WRITE_FIXTURE")
	if v == "" {
		t.Skip("set DISTILLARR_WRITE_FIXTURE=<version> to write a fixture")
	}
	path := filepath.Join("testdata", v+".db")
	_ = os.Remove(path)
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	f := &File{Path: "/media/movies/Film (2020)/Film (2020).mkv", Library: "movies", Title: "Film", Year: 2020,
		Size: 4 << 30, Duration: 6000, VideoCodec: "h264", Width: 1920, Height: 1080, Container: "mkv"}
	if err := st.UpsertFile(f, nil); err != nil {
		t.Fatal(err)
	}
	j := &Job{FileID: f.ID, SrcPath: f.Path, Priority: 100000, Backend: "sw", Codec: "hevc", Quality: 60, MaxAttempts: 3}
	if err := st.CreateJob(j); err != nil {
		t.Fatal(err)
	}
	_ = st.FinishJob(j.ID, StatusDone, 1<<30, "", "")
	_ = st.KVSet("config", `{"libraries":[{"name":"movies","path":"/media/movies"}],"paused":true}`)
	_ = st.KVSet("fixture_written", time.Now().UTC().Format(time.RFC3339))
	st.Close()
	for _, ext := range []string{"-wal", "-shm"} {
		_ = os.Remove(path + ext)
	}
}

func copyFileT(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
}
