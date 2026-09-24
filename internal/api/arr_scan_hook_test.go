// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/scan"
)

// Regression test for a real bug: the post-scan arr-sync hook fired on
// every scan.Progress call where Running was false, which happens not
// only when a full library scan finishes but on every single-file
// reprobe after a job completes (scan.ProbeSingle never touches
// Running). On a host with an active queue that meant a full arr sync
// launched roughly once per finished job — hammering Sonarr/Radarr with
// a complete resync every 15-20 seconds instead of once per real scan.
// The fix requires an actual true->false transition.
func TestPostScanArrSyncOnlyOnFullScanTransition(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]any{})
	}))
	defer fake.Close()

	s := newTestServer(t)
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "radarr", Name: "Radarr", Kind: "radarr", URL: fake.URL, APIKey: "k"}}
	}); err != nil {
		t.Fatal(err)
	}

	// A lone Running=false report — what a single-file reprobe
	// (scan.ProbeSingle, called after every finished job) sends — must
	// not trigger a sync.
	s.scan.Progress(scan.Stats{Running: false})
	time.Sleep(100 * time.Millisecond)
	if arrSyncing.Load() {
		t.Fatal("a lone Running=false report (a single-file reprobe) must not trigger a sync")
	}
	if _, ok, _ := s.st.KVGet("arr_last_sync"); ok {
		t.Fatal("no sync should have run yet")
	}

	// Repeating that report must not trigger one either — this is the
	// exact steady-state a busy queue produces continuously.
	for i := 0; i < 5; i++ {
		s.scan.Progress(scan.Stats{Running: false})
	}
	time.Sleep(100 * time.Millisecond)
	if arrSyncing.Load() {
		t.Fatal("repeated Running=false reports must not trigger a sync")
	}

	// A real scan pass — Running goes true, then false — must trigger
	// exactly one, on the transition.
	s.scan.Progress(scan.Stats{Running: true})
	s.scan.Progress(scan.Stats{Running: false})

	deadline := time.Now().Add(3 * time.Second)
	for arrSyncing.Load() {
		if time.Now().After(deadline) {
			t.Fatal("sync did not finish in time")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, ok, _ := s.st.KVGet("arr_last_sync"); !ok {
		t.Error("a real scan completing should have triggered a sync")
	}
}
