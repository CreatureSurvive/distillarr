package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"mediatrans/internal/config"
)

func TestArrTestUnsavedInstance(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/system/status":
			json.NewEncoder(w).Encode(map[string]string{"version": "4.0.0", "appName": "Sonarr"})
		case "/api/v3/rootfolder":
			json.NewEncoder(w).Encode([]map[string]any{{"path": "/data/tvshows", "accessible": true}})
		}
	}))
	defer fake.Close()

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "tvshows"), 0o755); err != nil {
		t.Fatal(err)
	}

	s := newTestServer(t)
	rec := doJSON(t, s, "POST", "/api/v1/arr/new/test", map[string]any{
		"url": fake.URL, "api_key": "k", "kind": "sonarr",
		"path_map": "/data=" + dir,
	})
	var out struct {
		OK          bool            `json:"ok"`
		Version     string          `json:"version"`
		RootFolders []rootFolderOut `json:"root_folders"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.Version != "4.0.0" {
		t.Fatalf("got %+v, body=%s", out, rec.Body.String())
	}
	if len(out.RootFolders) != 1 || !out.RootFolders[0].Reachable || out.RootFolders[0].Mapped != filepath.Join(dir, "tvshows") {
		t.Errorf("root folders = %+v", out.RootFolders)
	}
}

func TestArrTestMissingCredentials(t *testing.T) {
	s := newTestServer(t)
	rec := doJSON(t, s, "POST", "/api/v1/arr/new/test", map[string]any{})
	var out struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	json.Unmarshal(rec.Body.Bytes(), &out)
	if out.OK || out.Error == "" {
		t.Errorf("expected a friendly error, got %+v", out)
	}
}

// A saved instance's key is not re-sent by Settings; the test must fall
// back to the stored one, matching the Jellyfin test's behavior.
func TestArrTestFallsBackToStoredKey(t *testing.T) {
	var gotKey string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		json.NewEncoder(w).Encode(map[string]string{"version": "1", "appName": "Radarr"})
	}))
	defer fake.Close()

	s := newTestServer(t)
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "radarr", Name: "Radarr", Kind: "radarr", URL: fake.URL, APIKey: "stored-key"}}
	}); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, "POST", "/api/v1/arr/radarr/test", map[string]any{})
	var out struct{ OK bool }
	json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.OK {
		t.Fatalf("body=%s", rec.Body.String())
	}
	if gotKey != "stored-key" {
		t.Errorf("X-Api-Key = %q, want the stored key", gotKey)
	}
}
