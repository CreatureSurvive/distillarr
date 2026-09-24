// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"path/filepath"
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/jobs"
	"github.com/CreatureSurvive/distillarr/internal/preview"
	"github.com/CreatureSurvive/distillarr/internal/scan"
	"github.com/CreatureSurvive/distillarr/internal/still"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// newTestServer builds a real Server against a temp SQLite store, with no
// UI and no background loops started — enough to exercise the HTTP
// handlers directly via Handler().ServeHTTP.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.NewManager(st)
	sc := scan.New(st, cfg)
	eng := jobs.New(st, cfg, sc)
	pv := preview.NewManager(t.TempDir(), eng.AcquireSem, func(string, any) {})
	stl := still.New(t.TempDir(), eng.AcquireSem)
	// Handler tests run without auth; auth has its own tests (auth_test.go).
	_ = cfg.Update(func(c *config.Config) { c.AuthMode = config.AuthDisabled })
	return NewServer(st, cfg, sc, eng, pv, stl, nil)
}
