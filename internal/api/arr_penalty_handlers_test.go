// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/config"
)

func TestArrGetReturnsStoredPenaltiesAndAck(t *testing.T) {
	s := newTestServer(t)
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "sonarr", Name: "Sonarr", Kind: "sonarr"}}
	}); err != nil {
		t.Fatal(err)
	}
	report := `{"penalties":[{"profile":"WEB-1080p","custom_format":"HEVC","score":-10000,"matches":"release title","terms":["h265"]}]}`
	if err := s.st.KVSet("arr_penalties_sonarr", report); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, "GET", "/api/v1/arr/sonarr", nil)
	var out struct {
		PenaltyAck bool `json:"penalty_ack"`
		Penalties  struct {
			Penalties []struct {
				Profile string `json:"profile"`
				Score   int    `json:"score"`
			} `json:"penalties"`
		} `json:"penalties"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.PenaltyAck {
		t.Error("penalty_ack should default false")
	}
	if len(out.Penalties.Penalties) != 1 || out.Penalties.Penalties[0].Score != -10000 {
		t.Errorf("got %+v", out.Penalties)
	}
}

func TestArrGetUnknownInstance404(t *testing.T) {
	s := newTestServer(t)
	rec := doJSON(t, s, "GET", "/api/v1/arr/nope", nil)
	if rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestArrAckSetsFlagAndSurvivesPlainSave(t *testing.T) {
	s := newTestServer(t)
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "radarr", Name: "Radarr", Kind: "radarr", APIKey: "k"}}
	}); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, "POST", "/api/v1/arr/radarr/ack", nil)
	if rec.Code != 200 {
		t.Fatalf("ack status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !s.cfg.Get().ArrInstances[0].PenaltyAck {
		t.Fatal("PenaltyAck should now be true")
	}

	// A plain settings save (the Settings UI's normal path) must not
	// reset it back to false, even though the save payload never
	// mentions penalty_ack at all.
	rec = doJSON(t, s, "PUT", "/api/v1/config", map[string]any{
		"arr_instances": []map[string]any{
			{"id": "radarr", "name": "Radarr (renamed)", "kind": "radarr", "api_key": ""},
		},
	})
	if rec.Code != 200 {
		t.Fatalf("PUT status = %d, body=%s", rec.Code, rec.Body.String())
	}
	if !s.cfg.Get().ArrInstances[0].PenaltyAck {
		t.Error("a plain settings save must not clear penalty_ack")
	}
	if s.cfg.Get().ArrInstances[0].Name != "Radarr (renamed)" {
		t.Error("other fields must still save normally")
	}
}

func TestArrAckUnknownInstance404(t *testing.T) {
	s := newTestServer(t)
	rec := doJSON(t, s, "POST", "/api/v1/arr/nope/ack", nil)
	if rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}
