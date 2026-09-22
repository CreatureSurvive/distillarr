package jellyfin

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMediaUpdated(t *testing.T) {
	var gotPath, gotMethod, gotAuth string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod, gotAuth = r.URL.Path, r.Method, r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New(srv.URL, "secret-key")
	if err := c.MediaUpdated(context.Background(), "/data/movies/A/A - 1080p upscale.mp4", "Created"); err != nil {
		t.Fatal(err)
	}
	if gotMethod != http.MethodPost || gotPath != "/Library/Media/Updated" {
		t.Errorf("wrong call: %s %s", gotMethod, gotPath)
	}
	ups, _ := body["Updates"].([]any)
	if len(ups) != 1 {
		t.Fatalf("one update expected: %v", body)
	}
	u := ups[0].(map[string]any)
	if u["Path"] != "/data/movies/A/A - 1080p upscale.mp4" || u["UpdateType"] != "Created" {
		t.Errorf("update payload: %v", u)
	}
	// The key travels in the Authorization header (a query api_key is rejected).
	if !strings.Contains(gotAuth, "secret-key") {
		t.Errorf("api key must be sent in the Authorization header, got %q", gotAuth)
	}

	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "nope", http.StatusForbidden) })
	if err := c.MediaUpdated(context.Background(), "/data/x.mp4", "Created"); err == nil {
		t.Error("a non-2xx answer must be an error")
	}
	if err := (*Client)(nil).MediaUpdated(context.Background(), "/x", "Created"); err == nil {
		t.Error("an unconfigured client must fail, not panic")
	}
}
