package api

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/store"
)

func mustUpsert(t *testing.T, st *store.Store, f *store.File) *store.File {
	t.Helper()
	if err := st.UpsertFile(f, nil); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetFile(f.ID)
	if err != nil || got == nil {
		t.Fatalf("GetFile: %v", err)
	}
	return got
}

func doJSON(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// A hardlinked file (nlink>1) must be refused with 409 "hardlinked" and
// its file listed, and nothing queued, until confirmed.
func TestQueueFileRefusesHardlinkedUntilConfirmed(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A",
		VideoCodec: "h264", Container: "mkv", Width: 1920, Height: 1080, Duration: 100, Nlink: 3})

	rec := doJSON(t, s, "POST", "/api/v1/files/"+itoa64(f.ID)+"/queue", map[string]any{"run_now": false})
	if rec.Code != 409 {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Error string `json:"error"`
		Files []struct {
			ID    int64  `json:"id"`
			Path  string `json:"path"`
			Nlink int    `json:"nlink"`
		} `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Error != "hardlinked" || len(out.Files) != 1 || out.Files[0].ID != f.ID || out.Files[0].Nlink != 3 {
		t.Fatalf("unexpected body: %+v", out)
	}
	jobs, err := s.st.ListJobs(nil, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 0 {
		t.Fatalf("a refused queue must create no job, got %d", len(jobs))
	}

	// Confirmed: it must go through.
	rec = doJSON(t, s, "POST", "/api/v1/files/"+itoa64(f.ID)+"/queue", map[string]any{"run_now": false, "confirm_hardlinked": true})
	if rec.Code != 200 {
		t.Fatalf("confirmed queue: status = %d, body=%s", rec.Code, rec.Body.String())
	}
	jobs, err = s.st.ListJobs(nil, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("confirmed queue should create exactly one job, got %d", len(jobs))
	}
}

// A non-hardlinked file must queue exactly as before, with no confirmation.
func TestQueueFileUnaffectedWhenNotHardlinked(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/b.mkv", Library: "movies", Title: "B",
		VideoCodec: "h264", Container: "mkv", Width: 1920, Height: 1080, Duration: 100, Nlink: 1})

	rec := doJSON(t, s, "POST", "/api/v1/files/"+itoa64(f.ID)+"/queue", map[string]any{"run_now": false})
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}

// Bulk fixIssue: refusing must queue nothing at all, even the
// non-hardlinked candidates in the same batch; skip_hardlinked must queue
// everything else and only skip the hardlinked ones.
func TestFixIssueHardlinkedAllOrNothingThenSkip(t *testing.T) {
	s := newTestServer(t)
	linked := mustUpsert(t, s.st, &store.File{Path: "/m/linked.mkv", Library: "movies", Title: "Linked",
		VideoCodec: "mpeg2video", Container: "avi", Width: 1920, Height: 1080, Duration: 100, Nlink: 2,
		Issues: ",legacy_container,"})
	plain := mustUpsert(t, s.st, &store.File{Path: "/m/plain.avi", Library: "movies", Title: "Plain",
		VideoCodec: "mpeg2video", Container: "avi", Width: 1920, Height: 1080, Duration: 100, Nlink: 1,
		Issues: ",legacy_container,"})
	_ = linked
	_ = plain

	// Unconfirmed: refused, nothing queued at all (not even the plain file).
	rec := doJSON(t, s, "POST", "/api/v1/issues/legacy_container/fix", map[string]any{})
	if rec.Code != 409 {
		t.Fatalf("status = %d, want 409; body=%s", rec.Code, rec.Body.String())
	}
	jobs, _ := s.st.ListJobs(nil, 0, 10)
	if len(jobs) != 0 {
		t.Fatalf("refused bulk fix must queue nothing, got %d jobs", len(jobs))
	}

	// skip_hardlinked: the plain file queues, the linked one is skipped.
	rec = doJSON(t, s, "POST", "/api/v1/issues/legacy_container/fix", map[string]any{"skip_hardlinked": true})
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		Queued  int `json:"queued"`
		Skipped int `json:"skipped"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Queued != 1 || out.Skipped != 1 {
		t.Fatalf("got queued=%d skipped=%d, want 1/1", out.Queued, out.Skipped)
	}
	jobs, _ = s.st.ListJobs(nil, 0, 10)
	if len(jobs) != 1 || jobs[0].SrcPath != plain.Path {
		t.Fatalf("expected exactly the plain file queued, got %+v", jobs)
	}
}

func itoa64(id int64) string { return strconv.FormatInt(id, 10) }
