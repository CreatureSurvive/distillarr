package jobs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/replace"
)

func TestTrashDirFor(t *testing.T) {
	f := filepath.Join(t.TempDir(), "mounts")
	_ = os.WriteFile(f, []byte("overlay / overlay rw 0 0\npool /media fuse.mergerfs rw 0 0\n"), 0o644)
	old := replace.MountsFile
	replace.MountsFile = f
	t.Cleanup(func() { replace.MountsFile = old })

	cfg := config.Config{Libraries: []config.Library{{Name: "movies", Path: "/media/movies"}, {Name: "tvshows", Path: "/data/tv"}}}
	cases := map[string]string{
		"/media/movies/A (2020)/a.mkv": "/media/.distillarr-trash", // mount root of the library's filesystem
		"/data/tv/Show/S01/e.mkv":       "/data/tv/.distillarr-trash", // not its own mount: the library folder
	}
	for p, want := range cases {
		if got := TrashDirFor(cfg, p); got != want {
			t.Errorf("%s: got %s want %s", p, got, want)
		}
	}
	cfg.TrashDir = "/srv/media/.mediatrans-trash"
	if got := TrashDirFor(cfg, "/media/movies/a.mkv"); got != cfg.TrashDir {
		t.Errorf("global override ignored: %s", got)
	}
	if got := trashRootOf("/media/.distillarr-trash/movies/A/a.mkv", ""); got != "/media/.distillarr-trash" {
		t.Errorf("trashRootOf %s", got)
	}
}
