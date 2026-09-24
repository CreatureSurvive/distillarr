// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"testing"
)

// End-to-end through the real HTTP handlers: keys stay masked on read,
// a blank key on write keeps the stored one, and a fresh instance gets
// an id back.
func TestArrInstancesConfigRoundTrip(t *testing.T) {
	s := newTestServer(t)

	rec := doJSON(t, s, "PUT", "/api/v1/config", map[string]any{
		"arr_instances": []map[string]any{
			{"name": "Radarr", "kind": "radarr", "url": "http://radarr:7878", "api_key": "secret-key"},
		},
	})
	if rec.Code != 200 {
		t.Fatalf("PUT status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		ArrInstances []struct {
			ID        string `json:"id"`
			APIKey    string `json:"api_key"`
			APIKeySet bool   `json:"api_key_set"`
		} `json:"arr_instances"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.ArrInstances) != 1 {
		t.Fatalf("got %d instances, want 1", len(out.ArrInstances))
	}
	inst := out.ArrInstances[0]
	if inst.ID == "" {
		t.Error("new instance should have gotten an id")
	}
	if inst.APIKey != "" {
		t.Errorf("PUT response must not echo the key, got %q", inst.APIKey)
	}
	if !inst.APIKeySet {
		t.Error("api_key_set should be true")
	}

	// GET must mask it the same way.
	rec = doJSON(t, s, "GET", "/api/v1/config", nil)
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.ArrInstances[0].APIKey != "" || !out.ArrInstances[0].APIKeySet {
		t.Errorf("GET masking: got %+v", out.ArrInstances[0])
	}

	// Saving again with a blank key (as the UI would, editing only the
	// name) must not erase the stored key.
	rec = doJSON(t, s, "PUT", "/api/v1/config", map[string]any{
		"arr_instances": []map[string]any{
			{"id": inst.ID, "name": "Radarr (main)", "kind": "radarr", "url": "http://radarr:7878", "api_key": ""},
		},
	})
	if rec.Code != 200 {
		t.Fatalf("second PUT status = %d, body=%s", rec.Code, rec.Body.String())
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.ArrInstances[0].APIKeySet {
		t.Error("a blank key on save must not erase the stored key")
	}

	// Saving an empty list removes the instance.
	rec = doJSON(t, s, "PUT", "/api/v1/config", map[string]any{"arr_instances": []map[string]any{}})
	json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.ArrInstances) != 0 {
		t.Errorf("saving an empty list should remove instances, got %+v", out.ArrInstances)
	}
}
