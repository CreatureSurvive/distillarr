package api

import (
	"encoding/json"
	"testing"

	"mediatrans/internal/store"
)

func TestSetSidecarMode(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A",
		VideoCodec: "hevc", Container: "mkv", Width: 1920, Height: 1080, Duration: 100})

	rec := doJSON(t, s, "POST", "/api/v1/files/"+itoa64(f.ID)+"/sidecar-mode", map[string]any{"mode": "extract_remove"})
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got, _ := s.st.GetFile(f.ID)
	if got.SidecarMode != "extract_remove" {
		t.Errorf("SidecarMode = %q, want extract_remove", got.SidecarMode)
	}

	// Clearing back to "" (inherit) works.
	rec = doJSON(t, s, "POST", "/api/v1/files/"+itoa64(f.ID)+"/sidecar-mode", map[string]any{"mode": ""})
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got, _ = s.st.GetFile(f.ID)
	if got.SidecarMode != "" {
		t.Errorf("SidecarMode = %q, want cleared", got.SidecarMode)
	}

	// An unrecognized mode is rejected.
	rec = doJSON(t, s, "POST", "/api/v1/files/"+itoa64(f.ID)+"/sidecar-mode", map[string]any{"mode": "bogus"})
	if rec.Code != 400 {
		t.Fatalf("bogus mode: status = %d, want 400", rec.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
}
