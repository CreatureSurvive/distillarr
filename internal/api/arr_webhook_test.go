// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// Synthetic payloads shaped after the Sonarr wiki's documented examples
// and the ArrAPI client, not captured from a live instance.
const sonarrDownloadFixture = `{
	"eventType": "Download",
	"series": {"id": 1, "title": "Test Show"},
	"episodes": [{"id": 10, "episodeNumber": 1, "seasonNumber": 1}],
	"episodeFile": {"id": 500, "relativePath": "Season 01/Test Show - S01E01.mkv", "path": "/data/tvshows/Test Show/Season 01/Test Show - S01E01.mkv"},
	"isUpgrade": true,
	"downloadClient": "qBittorrent",
	"downloadId": "abc123"
}`

const radarrDownloadFixture = `{
	"eventType": "Download",
	"movie": {"id": 2, "title": "Test Movie"},
	"movieFile": {"id": 600, "relativePath": "Test Movie.mkv", "path": "/data/movies/Test Movie.mkv"},
	"isUpgrade": false,
	"downloadClient": "qBittorrent",
	"downloadId": "def456"
}`

const sonarrRenameFixture = `{
	"eventType": "Rename",
	"series": {"id": 1, "title": "Test Show"},
	"renamedEpisodeFiles": [
		{"relativePath": "Season 01/new.mkv", "path": "/data/tvshows/Test Show/Season 01/new.mkv",
		 "previousRelativePath": "Season 01/old.mkv", "previousPath": "/data/tvshows/Test Show/Season 01/old.mkv"}
	]
}`

const radarrRenameFixture = `{
	"eventType": "Rename",
	"movie": {"id": 2, "title": "Test Movie"},
	"movieFile": {"path": "/data/movies/new.mkv", "previousPath": "/data/movies/old.mkv"}
}`

const sonarrDeleteFixture = `{
	"eventType": "EpisodeFileDelete",
	"series": {"id": 1, "title": "Test Show"},
	"episodeFile": {"id": 500, "relativePath": "Season 01/gone.mkv", "path": "/data/tvshows/Test Show/Season 01/gone.mkv"},
	"deleteReason": "manualOverride"
}`

const testEventFixture = `{"eventType": "Test"}`

func webhookInstance(t *testing.T, s *Server, kind string) config.ArrInstance {
	t.Helper()
	inst := config.ArrInstance{ID: "inst", Name: "Test", Kind: kind, PathMap: "/data=/local", WebhookToken: "s3cr3t"}
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{inst}
	}); err != nil {
		t.Fatal(err)
	}
	return *s.arrInstanceByID("inst")
}

func webhookReq(body string, user, pass string) *http.Request {
	req := httptest.NewRequest("POST", "/api/v1/hooks/arr/inst", strings.NewReader(body))
	if pass != "" || user != "" {
		req.SetBasicAuth(user, pass)
	}
	return req
}

func TestArrWebhookAuthFailure(t *testing.T) {
	s := newTestServer(t)
	webhookInstance(t, s, "sonarr")

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, webhookReq(testEventFixture, "x", "wrong-token"))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}

	rec2 := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec2, webhookReq(testEventFixture, "", ""))
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("no-auth status = %d, want 401", rec2.Code)
	}
}

func TestArrWebhookAuthFailureUnknownInstance(t *testing.T) {
	s := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/hooks/arr/nope", strings.NewReader(testEventFixture))
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestArrWebhookTestEventRecordsLastReceived(t *testing.T) {
	s := newTestServer(t)
	webhookInstance(t, s, "sonarr")

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, webhookReq(testEventFixture, "x", "s3cr3t"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	info := s.arrWebhookInfo("inst")
	if info.LastReceived == "" {
		t.Error("last_received must be set after a Test event")
	}
}

func TestArrWebhookDownloadIntakeOff(t *testing.T) {
	s := newTestServer(t)
	webhookInstance(t, s, "sonarr")

	var probed string
	arrWebhookReprobe = func(srv *Server, path string) error {
		probed = path
		mustUpsert(t, srv.st, &store.File{Path: path, Library: "tvshows", Title: "Test Show"})
		return nil
	}
	t.Cleanup(func() { arrWebhookReprobe = func(srv *Server, path string) error { return srv.scan.ProbeSingle(path) } })

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, webhookReq(sonarrDownloadFixture, "x", "s3cr3t"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if probed != "/local/tvshows/Test Show/Season 01/Test Show - S01E01.mkv" {
		t.Errorf("probed path = %q", probed)
	}
	rows, err := s.st.ListIntake("")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Errorf("webhook_intake is off by default: got %d intake row(s), want 0", len(rows))
	}
}

func TestArrWebhookDownloadIntakeOn(t *testing.T) {
	s := newTestServer(t)
	inst := webhookInstance(t, s, "sonarr")
	on := true
	if err := s.cfg.Update(func(c *config.Config) {
		for i := range c.ArrInstances {
			if c.ArrInstances[i].ID == inst.ID {
				c.ArrInstances[i].WebhookIntake = &on
			}
		}
	}); err != nil {
		t.Fatal(err)
	}

	arrWebhookReprobe = func(srv *Server, path string) error {
		mustUpsert(t, srv.st, &store.File{Path: path, Library: "tvshows", Title: "Test Show"})
		return nil
	}
	t.Cleanup(func() { arrWebhookReprobe = func(srv *Server, path string) error { return srv.scan.ProbeSingle(path) } })

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, webhookReq(sonarrDownloadFixture, "x", "s3cr3t"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rows, err := s.st.ListIntake("")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d intake rows, want 1", len(rows))
	}
	if rows[0].Origin != "webhook" || !strings.Contains(rows[0].Reason, "upgrade") {
		t.Errorf("got %+v, want origin=webhook and an upgrade-mentioning reason (isUpgrade=true)", rows[0])
	}
	wantNotBefore := time.Now().Add(30 * time.Minute)
	gotNotBefore, err := time.Parse(time.RFC3339, rows[0].NotBefore)
	if err != nil {
		t.Fatal(err)
	}
	if gotNotBefore.Before(wantNotBefore.Add(-time.Minute)) || gotNotBefore.After(wantNotBefore.Add(time.Minute)) {
		t.Errorf("not_before = %v, want ~30m from now (the default settle)", gotNotBefore)
	}
}

func TestArrWebhookDownloadRadarr(t *testing.T) {
	s := newTestServer(t)
	inst := webhookInstance(t, s, "radarr")
	on := true
	if err := s.cfg.Update(func(c *config.Config) {
		for i := range c.ArrInstances {
			if c.ArrInstances[i].ID == inst.ID {
				c.ArrInstances[i].WebhookIntake = &on
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	arrWebhookReprobe = func(srv *Server, path string) error {
		mustUpsert(t, srv.st, &store.File{Path: path, Library: "movies", Title: "Test Movie"})
		return nil
	}
	t.Cleanup(func() { arrWebhookReprobe = func(srv *Server, path string) error { return srv.scan.ProbeSingle(path) } })

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, webhookReq(radarrDownloadFixture, "x", "s3cr3t"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rows, err := s.st.ListIntake("")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || strings.Contains(rows[0].Reason, "upgrade") {
		t.Fatalf("got %+v, want 1 row with a non-upgrade reason (isUpgrade=false)", rows)
	}
}

func TestArrWebhookRenameSonarr(t *testing.T) {
	s := newTestServer(t)
	webhookInstance(t, s, "sonarr")
	oldLocal := "/local/tvshows/Test Show/Season 01/old.mkv"
	mustUpsert(t, s.st, &store.File{Path: oldLocal, Library: "tvshows", Title: "Test Show"})

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, webhookReq(sonarrRenameFixture, "x", "s3cr3t"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	newLocal := "/local/tvshows/Test Show/Season 01/new.mkv"
	if f, err := s.st.GetFileByPath(newLocal); err != nil || f == nil {
		t.Fatalf("GetFileByPath(new): %v, %v", f, err)
	}
	if f, err := s.st.GetFileByPath(oldLocal); err != nil || f != nil {
		t.Fatalf("old path should no longer resolve after rename, got %+v", f)
	}
}

func TestArrWebhookRenameRadarrSingleMovieFile(t *testing.T) {
	s := newTestServer(t)
	webhookInstance(t, s, "radarr")
	oldLocal := "/local/movies/old.mkv"
	mustUpsert(t, s.st, &store.File{Path: oldLocal, Library: "movies", Title: "Test Movie"})

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, webhookReq(radarrRenameFixture, "x", "s3cr3t"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	newLocal := "/local/movies/new.mkv"
	if f, err := s.st.GetFileByPath(newLocal); err != nil || f == nil {
		t.Fatalf("GetFileByPath(new): %v, %v", f, err)
	}
}

func TestArrWebhookDeleteCancelsJobsAndDismissesIntake(t *testing.T) {
	s := newTestServer(t)
	webhookInstance(t, s, "sonarr")
	local := "/local/tvshows/Test Show/Season 01/gone.mkv"
	f := mustUpsert(t, s.st, &store.File{Path: local, Library: "tvshows", Title: "Test Show"})

	if _, err := s.enqueue(f, encode.Settings{Codec: "hevc"}, false, "manual", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.UpsertIntake(f.ID, "webhook", "settling", "", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, webhookReq(sonarrDeleteFixture, "x", "s3cr3t"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	jobs, err := s.st.ListJobs(nil, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range jobs {
		if j.FileID == f.ID && j.Status == "queued" {
			t.Errorf("job %d for the deleted file is still queued", j.ID)
		}
	}
	rows, err := s.st.ListIntake("")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.FileID == f.ID && r.State != store.IntakeDismissed {
			t.Errorf("intake row %d for the deleted file is still %s", r.ID, r.State)
		}
	}
	got, err := s.st.GetFile(f.ID)
	if err != nil || got == nil {
		t.Fatalf("GetFile: %v", err)
	}
	if !got.Missing {
		t.Error("file must be marked missing after a delete webhook")
	}
}

