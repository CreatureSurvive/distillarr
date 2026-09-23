package api

import (
	"encoding/json"
	"testing"

	"mediatrans/internal/config"
)

func TestSlugify(t *testing.T) {
	for in, want := range map[string]string{
		"Radarr 4K":  "radarr-4k",
		"  Sonarr  ": "sonarr",
		"":           "instance",
		"###":        "instance",
	} {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMergeArrInstancesNewGetsID(t *testing.T) {
	patch, _ := json.Marshal([]config.ArrInstance{
		{Name: "Radarr", Kind: "radarr", URL: "http://radarr:7878", APIKey: "k1"},
	})
	out, err := mergeArrInstances(nil, patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].ID != "radarr" || out[0].APIKey != "k1" {
		t.Fatalf("got %+v", out)
	}
}

func TestMergeArrInstancesBlankKeyKeepsStored(t *testing.T) {
	stored := []config.ArrInstance{{ID: "radarr", Name: "Radarr", Kind: "radarr", URL: "http://radarr:7878", APIKey: "secret"}}
	patch, _ := json.Marshal([]config.ArrInstance{
		{ID: "radarr", Name: "Radarr (renamed)", Kind: "radarr", URL: "http://radarr:7878", APIKey: ""},
	})
	out, err := mergeArrInstances(stored, patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].APIKey != "secret" {
		t.Fatalf("blank key on save must keep the stored key, got %+v", out)
	}
	if out[0].Name != "Radarr (renamed)" {
		t.Errorf("other fields must still update, got name=%q", out[0].Name)
	}
}

func TestMergeArrInstancesExplicitKeyReplaces(t *testing.T) {
	stored := []config.ArrInstance{{ID: "radarr", Name: "Radarr", Kind: "radarr", APIKey: "old"}}
	patch, _ := json.Marshal([]config.ArrInstance{
		{ID: "radarr", Name: "Radarr", Kind: "radarr", APIKey: "new"},
	})
	out, _ := mergeArrInstances(stored, patch)
	if out[0].APIKey != "new" {
		t.Errorf("an explicit non-blank key must replace the stored one, got %q", out[0].APIKey)
	}
}

func TestMergeArrInstancesRemovesDroppedEntries(t *testing.T) {
	stored := []config.ArrInstance{
		{ID: "sonarr", Name: "Sonarr", Kind: "sonarr", APIKey: "a"},
		{ID: "radarr", Name: "Radarr", Kind: "radarr", APIKey: "b"},
	}
	// The patch is the full desired list: only sonarr survives.
	patch, _ := json.Marshal([]config.ArrInstance{
		{ID: "sonarr", Name: "Sonarr", Kind: "sonarr", APIKey: "a"},
	})
	out, _ := mergeArrInstances(stored, patch)
	if len(out) != 1 || out[0].ID != "sonarr" {
		t.Fatalf("got %+v, want only sonarr", out)
	}
}

func TestMergeArrInstancesDuplicateNamesGetDistinctIDs(t *testing.T) {
	patch, _ := json.Marshal([]config.ArrInstance{
		{Name: "Radarr", Kind: "radarr", APIKey: "a"},
		{Name: "Radarr", Kind: "radarr", APIKey: "b"},
	})
	out, err := mergeArrInstances(nil, patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].ID == out[1].ID {
		t.Fatalf("two same-named new instances must get distinct ids, got %+v", out)
	}
}

func TestMergeArrInstancesNewIDAvoidsExistingCollision(t *testing.T) {
	stored := []config.ArrInstance{{ID: "radarr", Name: "Radarr", Kind: "radarr", APIKey: "a"}}
	// A second, different instance that happens to slugify to the same id.
	patch, _ := json.Marshal([]config.ArrInstance{
		{ID: "radarr", Name: "Radarr", Kind: "radarr", APIKey: "a"},
		{Name: "Radarr", Kind: "radarr", APIKey: "c"},
	})
	out, err := mergeArrInstances(stored, patch)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 || out[0].ID == out[1].ID {
		t.Fatalf("new instance colliding with an existing id must get a different one, got %+v", out)
	}
}

func TestArrOutMasksKey(t *testing.T) {
	out := arrOut(config.ArrInstance{ID: "x", Name: "X", APIKey: "secret"})
	if out.APIKey != "" {
		t.Errorf("api key must never be echoed, got %q", out.APIKey)
	}
	if !out.APIKeySet {
		t.Error("api_key_set must be true when a key is stored")
	}
	b, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	json.Unmarshal(b, &raw)
	if raw["api_key"] != "" {
		t.Errorf("marshaled api_key = %v, want empty string", raw["api_key"])
	}
	if raw["api_key_set"] != true {
		t.Errorf("marshaled api_key_set = %v, want true", raw["api_key_set"])
	}
}
