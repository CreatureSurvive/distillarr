package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mediatrans/internal/store"
)

func TestMetrics(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A"})
	if err := s.st.CreateJob(&store.Job{FileID: f.ID, SrcPath: f.Path, Priority: 100000, Backend: "sw", Codec: "hevc", MaxAttempts: 1}); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.metrics(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := w.Body.String()
	for _, want := range []string{
		"# TYPE distillarr_jobs gauge",
		`distillarr_jobs{status="queued"} 1`,
		"distillarr_queue_depth 1",
		"distillarr_saved_bytes_total 0",
		"distillarr_window_open ",
		"distillarr_scan_running 0",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain; version=0.0.4") {
		t.Errorf("content type %q", ct)
	}
}
