package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"mediatrans/internal/config"
	"mediatrans/internal/jobs"
	"mediatrans/internal/store"
)

func trueP() *bool { v := true; return &v }

// TestArrWriteBackSubscriberCreatesMissingTagAndAppliesSeries covers the
// create-if-missing + apply path for Sonarr: no "distilled" tag exists
// yet, so the subscriber must create it, then PUT the series editor with
// that tag's id.
func TestArrWriteBackSubscriberCreatesMissingTagAndAppliesSeries(t *testing.T) {
	var (
		createCalled int32
		editorBody   string
	)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/tag" && r.Method == http.MethodGet:
			w.Write([]byte(`[]`)) // no tags yet
		case r.URL.Path == "/api/v3/tag" && r.Method == http.MethodPost:
			atomic.AddInt32(&createCalled, 1)
			w.Write([]byte(`{"id":9,"label":"distilled"}`))
		case r.URL.Path == "/api/v3/series/editor" && r.Method == http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			editorBody = string(b)
		}
	}))
	defer fake.Close()

	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/show/e1.mkv", Library: "tvshows", Title: "Show"})
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{
			ID: "sonarr", Name: "Sonarr", Kind: "sonarr", URL: fake.URL, APIKey: "k",
			TagAfterReencode: trueP(), ReencodeTag: "distilled",
		}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f.ID, InstanceID: "sonarr", Kind: "sonarr", ItemID: 42}}); err != nil {
		t.Fatal(err)
	}

	s.ArrWriteBackSubscriber()(jobs.ReplacedEvent{FileID: f.ID, Kind: "remux", OldPath: f.Path, NewPath: f.Path})

	waitFor(t, time.Second, func() bool { return editorBody != "" })
	if atomic.LoadInt32(&createCalled) != 1 {
		t.Error("a missing tag must be created exactly once")
	}
	var got map[string]any
	json.Unmarshal([]byte(editorBody), &got)
	if ids, _ := got["seriesIds"].([]any); len(ids) != 1 || ids[0].(float64) != 42 {
		t.Errorf("seriesIds = %v", got["seriesIds"])
	}
	if tags, _ := got["tags"].([]any); len(tags) != 1 || tags[0].(float64) != 9 {
		t.Errorf("tags = %v", got["tags"])
	}
	if got["applyTags"] != "add" {
		t.Errorf("applyTags = %v, want add", got["applyTags"])
	}
}

// An existing tag (matched case-insensitively) must be reused, not
// recreated.
func TestArrWriteBackSubscriberReusesExistingTag(t *testing.T) {
	var createCalled int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/tag" && r.Method == http.MethodGet:
			w.Write([]byte(`[{"id":5,"label":"Distilled"}]`))
		case r.URL.Path == "/api/v3/tag" && r.Method == http.MethodPost:
			atomic.AddInt32(&createCalled, 1)
			w.Write([]byte(`{"id":99,"label":"distilled"}`))
		case r.URL.Path == "/api/v3/movie/editor":
			w.WriteHeader(200)
		}
	}))
	defer fake.Close()

	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/movie.mkv", Library: "movies", Title: "Movie"})
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{
			ID: "radarr", Name: "Radarr", Kind: "radarr", URL: fake.URL, APIKey: "k",
			TagAfterReencode: trueP(), ReencodeTag: "distilled",
		}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f.ID, InstanceID: "radarr", Kind: "radarr", ItemID: 7}}); err != nil {
		t.Fatal(err)
	}

	s.ArrWriteBackSubscriber()(jobs.ReplacedEvent{FileID: f.ID, Kind: "encode", OldPath: f.Path, NewPath: f.Path})
	time.Sleep(100 * time.Millisecond)
	if atomic.LoadInt32(&createCalled) != 0 {
		t.Error("an existing tag (case-insensitive match) must not be recreated")
	}
}

// Radarr unmonitor: one PUT to the movie editor with monitored:false.
func TestArrWriteBackSubscriberUnmonitorsMovie(t *testing.T) {
	var body string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/movie/editor" {
			b, _ := io.ReadAll(r.Body)
			body = string(b)
		}
	}))
	defer fake.Close()

	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/movie.mkv", Library: "movies", Title: "Movie"})
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{
			ID: "radarr", Name: "Radarr", Kind: "radarr", URL: fake.URL, APIKey: "k",
			UnmonitorAfterReencode: trueP(),
		}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f.ID, InstanceID: "radarr", Kind: "radarr", ItemID: 7}}); err != nil {
		t.Fatal(err)
	}

	s.ArrWriteBackSubscriber()(jobs.ReplacedEvent{FileID: f.ID, Kind: "encode", OldPath: f.Path, NewPath: f.Path})
	waitFor(t, time.Second, func() bool { return body != "" })
	var got map[string]any
	json.Unmarshal([]byte(body), &got)
	if ids, _ := got["movieIds"].([]any); len(ids) != 1 || ids[0].(float64) != 7 {
		t.Errorf("movieIds = %v", got["movieIds"])
	}
	if got["monitored"] != false {
		t.Errorf("monitored = %v, want false", got["monitored"])
	}
}

// Sonarr unmonitor must resolve the specific episode via its episode
// file id (arr_items only tracks the series id), then unmonitor only
// that episode.
func TestArrWriteBackSubscriberUnmonitorsSpecificEpisode(t *testing.T) {
	var monitorBody string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/episode":
			w.Write([]byte(`[
				{"id":100,"seriesId":42,"episodeFileId":500,"monitored":true},
				{"id":101,"seriesId":42,"episodeFileId":501,"monitored":true}
			]`))
		case "/api/v3/episode/monitor":
			b, _ := io.ReadAll(r.Body)
			monitorBody = string(b)
		}
	}))
	defer fake.Close()

	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/show/e2.mkv", Library: "tvshows", Title: "Show"})
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{
			ID: "sonarr", Name: "Sonarr", Kind: "sonarr", URL: fake.URL, APIKey: "k",
			UnmonitorAfterReencode: trueP(),
		}}
	}); err != nil {
		t.Fatal(err)
	}
	// FileRecID 501 -> episode 101, not 100.
	if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f.ID, InstanceID: "sonarr", Kind: "sonarr", ItemID: 42, FileRecID: 501}}); err != nil {
		t.Fatal(err)
	}

	s.ArrWriteBackSubscriber()(jobs.ReplacedEvent{FileID: f.ID, Kind: "remux", OldPath: f.Path, NewPath: f.Path})
	waitFor(t, time.Second, func() bool { return monitorBody != "" })
	var got map[string]any
	json.Unmarshal([]byte(monitorBody), &got)
	ids, _ := got["episodeIds"].([]any)
	if len(ids) != 1 || ids[0].(float64) != 101 {
		t.Errorf("episodeIds = %v, want [101]", got["episodeIds"])
	}
}

func TestArrWriteBackSubscriberNoopWhenBothTogglesOff(t *testing.T) {
	var called int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&called, 1)
	}))
	defer fake.Close()

	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A"})
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "radarr", Name: "Radarr", Kind: "radarr", URL: fake.URL, APIKey: "k"}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f.ID, InstanceID: "radarr", Kind: "radarr", ItemID: 1}}); err != nil {
		t.Fatal(err)
	}

	s.ArrWriteBackSubscriber()(jobs.ReplacedEvent{FileID: f.ID, Kind: "encode", OldPath: f.Path, NewPath: f.Path})
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&called) != 0 {
		t.Error("both toggles off must never call the instance")
	}
}

func TestArrWriteBackSubscriberIgnoresUpscaleCopy(t *testing.T) {
	var called int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&called, 1)
	}))
	defer fake.Close()

	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A"})
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{
			ID: "radarr", Name: "Radarr", Kind: "radarr", URL: fake.URL, APIKey: "k",
			TagAfterReencode: trueP(), ReencodeTag: "distilled", UnmonitorAfterReencode: trueP(),
		}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f.ID, InstanceID: "radarr", Kind: "radarr", ItemID: 1}}); err != nil {
		t.Fatal(err)
	}

	s.ArrWriteBackSubscriber()(jobs.ReplacedEvent{FileID: f.ID, Kind: "upscale-copy", OldPath: f.Path, NewPath: f.Path + ".copy"})
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&called) != 0 {
		t.Error("upscale-copy must never trigger write-back: nothing was replaced")
	}
}

// PUT /api/v1/config must reject enabling tag_after_reencode with a
// blank reencode_tag.
func TestPutConfigRejectsTagAfterReencodeWithoutTagName(t *testing.T) {
	s := newTestServer(t)
	body := map[string]any{
		"arr_instances": []map[string]any{
			{"name": "Radarr", "kind": "radarr", "url": "http://radarr:7878", "api_key": "k", "tag_after_reencode": true},
		},
	}
	rec := doJSON(t, s, "PUT", "/api/v1/config", body)
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
}

// POST /api/v1/arr/{id}/rename-tag renames a matching tag by label.
func TestArrRenameTag(t *testing.T) {
	var renameBody string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v3/tag" && r.Method == http.MethodGet:
			w.Write([]byte(`[{"id":3,"label":"old-name"}]`))
		case r.URL.Path == "/api/v3/tag/3" && r.Method == http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			renameBody = string(b)
		}
	}))
	defer fake.Close()

	s := newTestServer(t)
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "radarr", Name: "Radarr", Kind: "radarr", URL: fake.URL, APIKey: "k"}}
	}); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, "POST", "/api/v1/arr/radarr/rename-tag", map[string]string{"old_name": "old-name", "new_name": "new-name"})
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["renamed"] != true {
		t.Errorf("renamed = %v, want true; body = %s", resp["renamed"], rec.Body.String())
	}
	var got map[string]any
	json.Unmarshal([]byte(renameBody), &got)
	if got["label"] != "new-name" {
		t.Errorf("PUT body label = %v, want new-name", got["label"])
	}
}

// No matching tag: renamed:false, no error.
func TestArrRenameTagNoMatch(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/tag" {
			w.Write([]byte(`[]`))
		}
	}))
	defer fake.Close()

	s := newTestServer(t)
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "radarr", Name: "Radarr", Kind: "radarr", URL: fake.URL, APIKey: "k"}}
	}); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, s, "POST", "/api/v1/arr/radarr/rename-tag", map[string]string{"old_name": "nope", "new_name": "new-name"})
	if rec.Code != 200 {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["renamed"] != false {
		t.Errorf("renamed = %v, want false", resp["renamed"])
	}
}
