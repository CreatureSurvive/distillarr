// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/config"
)

func withFakeFreePct(t *testing.T, pcts map[string]float64) {
	t.Helper()
	orig := statFreePct
	statFreePct = func(path string) float64 {
		if pct, ok := pcts[path]; ok {
			return pct
		}
		return -1
	}
	t.Cleanup(func() { statFreePct = orig })
}

func TestDiskPressureForOffByDefault(t *testing.T) {
	withFakeFreePct(t, map[string]float64{"/srv/media/movies": 1}) // deep in the red
	cfg := config.Config{Libraries: []config.Library{{Name: "movies", Path: "/srv/media/movies"}}}
	if diskPressureFor(cfg) {
		t.Error("DiskPressurePct=0 (the default) must never trigger pressure")
	}
}

func TestDiskPressureForTriggersAtOrBelowThreshold(t *testing.T) {
	withFakeFreePct(t, map[string]float64{"/srv/media/movies": 10})
	cfg := config.Config{
		DiskPressurePct: 10,
		Libraries:       []config.Library{{Name: "movies", Path: "/srv/media/movies"}},
	}
	if !diskPressureFor(cfg) {
		t.Error("free% == threshold must trigger pressure")
	}
}

func TestDiskPressureForClearAboveThreshold(t *testing.T) {
	withFakeFreePct(t, map[string]float64{
		"/srv/media/movies":  50,
		"/srv/media/tvshows": 40,
	})
	cfg := config.Config{
		DiskPressurePct: 10,
		Libraries: []config.Library{
			{Name: "movies", Path: "/srv/media/movies"},
			{Name: "tvshows", Path: "/srv/media/tvshows"},
		},
	}
	if diskPressureFor(cfg) {
		t.Error("every library well above threshold must not trigger pressure")
	}
}

func TestDiskPressureForAnyLibraryTriggers(t *testing.T) {
	withFakeFreePct(t, map[string]float64{
		"/srv/media/movies":  50, // fine
		"/srv/media/tvshows": 3,  // tight
	})
	cfg := config.Config{
		DiskPressurePct: 10,
		Libraries: []config.Library{
			{Name: "movies", Path: "/srv/media/movies"},
			{Name: "tvshows", Path: "/srv/media/tvshows"},
		},
	}
	if !diskPressureFor(cfg) {
		t.Error("one library below threshold must trigger pressure even if others are fine")
	}
}

func TestDiskPressureForStatfsFailureIgnored(t *testing.T) {
	withFakeFreePct(t, map[string]float64{}) // every path "fails" -> -1
	cfg := config.Config{
		DiskPressurePct: 10,
		Libraries:       []config.Library{{Name: "movies", Path: "/does/not/exist"}},
	}
	if diskPressureFor(cfg) {
		t.Error("a statfs failure (-1) must never be read as pressure")
	}
}
