// SPDX-License-Identifier: GPL-3.0-or-later

package jobs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// A job that starts while the sweep walks the library must keep its temp
// file, and only old unclaimed temps are removed.
func TestSweepStaleTempsKeepsLiveEncodes(t *testing.T) {
	e := newTestEngine(t)
	lib := t.TempDir()
	if err := e.cfg.Update(func(c *config.Config) { c.Libraries = []config.Library{{Name: "tv", Path: lib}} }); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-2 * staleTempAge)
	write := func(name string, mtime time.Time) string {
		p := filepath.Join(lib, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mtime, mtime); err != nil {
			t.Fatal(err)
		}
		return p
	}
	orphan := write(".distillarr-1.mp4.tmp", old)
	fresh := write(".distillarr-2.mp4.tmp", time.Now())
	claimed := write(".distillarr-3.mp4.tmp", old)

	j := &store.Job{SrcPath: filepath.Join(lib, "x.mp4"), Backend: "qsv", Codec: "hevc", SettingsJSON: `{}`, MaxAttempts: 3}
	e.st.CreateJob(j)
	c, _ := e.st.ClaimNext(true, false)
	if err := e.st.SetJobTemp(c.ID, claimed, ""); err != nil {
		t.Fatal(err)
	}

	e.sweepStaleTemps()
	for p, want := range map[string]bool{orphan: false, fresh: true, claimed: true} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s exists=%v, want %v", filepath.Base(p), err == nil, want)
		}
	}
}
