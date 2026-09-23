package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mediatrans/internal/config"
	"mediatrans/internal/jobs"
	"mediatrans/internal/store"
)

// fastPolling overrides the poll timing for one test so it runs in
// milliseconds instead of minutes, and restores the real values after.
func fastPolling(t *testing.T, total time.Duration) {
	t.Helper()
	oldTotal, oldInitial, oldMax := arrPollTotal, arrPollInitial, arrPollMax
	arrPollTotal, arrPollInitial, arrPollMax = total, 5*time.Millisecond, 20*time.Millisecond
	t.Cleanup(func() { arrPollTotal, arrPollInitial, arrPollMax = oldTotal, oldInitial, oldMax })
}

// fastSeasonDebounce overrides the Sonarr per-series rescan debounce for
// one test so a batch settles in milliseconds instead of minutes.
func fastSeasonDebounce(t *testing.T, d time.Duration) {
	t.Helper()
	old := arrSeasonDebounce
	arrSeasonDebounce = d
	t.Cleanup(func() { arrSeasonDebounce = old })
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}

func TestArrRescanSubscriberSendsRescanSeries(t *testing.T) {
	fastSeasonDebounce(t, 20*time.Millisecond)
	var got struct{ path, body string }
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/command" {
			got.path = r.URL.Path
			buf := make([]byte, 200)
			n, _ := r.Body.Read(buf)
			got.body = string(buf[:n])
		}
	}))
	defer fake.Close()

	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/show/e1.mkv", Library: "tvshows", Title: "Show"})
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "sonarr", Name: "Sonarr", Kind: "sonarr", URL: fake.URL, APIKey: "k"}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f.ID, InstanceID: "sonarr", Kind: "sonarr", ItemID: 42}}); err != nil {
		t.Fatal(err)
	}

	sub := s.ArrRescanSubscriber()
	sub(jobs.ReplacedEvent{FileID: f.ID, Kind: "remux", OldPath: f.Path, NewPath: f.Path})

	waitFor(t, time.Second, func() bool { return got.path != "" })
	if got.path != "/api/v3/command" {
		t.Fatalf("path = %q", got.path)
	}
	if !contains(got.body, `"name":"RescanSeries"`) || !contains(got.body, `"seriesId":42`) {
		t.Errorf("body = %q", got.body)
	}
}

func TestArrRescanSubscriberSendsRescanMovie(t *testing.T) {
	var gotBody string
	var called int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/command" {
			atomic.AddInt32(&called, 1)
			buf := make([]byte, 200)
			n, _ := r.Body.Read(buf)
			gotBody = string(buf[:n])
		}
	}))
	defer fake.Close()

	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/movie.mkv", Library: "movies", Title: "Movie"})
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "radarr", Name: "Radarr", Kind: "radarr", URL: fake.URL, APIKey: "k"}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f.ID, InstanceID: "radarr", Kind: "radarr", ItemID: 7}}); err != nil {
		t.Fatal(err)
	}

	sub := s.ArrRescanSubscriber()
	sub(jobs.ReplacedEvent{FileID: f.ID, Kind: "encode", OldPath: f.Path, NewPath: f.Path})

	waitFor(t, time.Second, func() bool { return atomic.LoadInt32(&called) == 1 })
	if !contains(gotBody, `"name":"RescanMovie"`) || !contains(gotBody, `"movieId":7`) {
		t.Errorf("body = %q", gotBody)
	}
}

func TestArrRescanSubscriberIgnoresUnmanagedFile(t *testing.T) {
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
	// No arr_items row for f.

	s.ArrRescanSubscriber()(jobs.ReplacedEvent{FileID: f.ID, Kind: "encode", OldPath: f.Path, NewPath: f.Path})
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&called) != 0 {
		t.Error("a file with no arr_items row must never trigger a request")
	}
}

func TestArrRescanSubscriberRespectsToggleOff(t *testing.T) {
	var called int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&called, 1)
	}))
	defer fake.Close()

	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A"})
	off := false
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "radarr", Name: "Radarr", Kind: "radarr", URL: fake.URL, APIKey: "k", RescanAfterReplace: &off}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f.ID, InstanceID: "radarr", Kind: "radarr", ItemID: 1}}); err != nil {
		t.Fatal(err)
	}

	s.ArrRescanSubscriber()(jobs.ReplacedEvent{FileID: f.ID, Kind: "encode", OldPath: f.Path, NewPath: f.Path})
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&called) != 0 {
		t.Error("rescan_after_replace=false must suppress the rescan entirely")
	}
}

func TestArrRescanSubscriberIgnoresOtherEventKinds(t *testing.T) {
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

	s.ArrRescanSubscriber()(jobs.ReplacedEvent{FileID: f.ID, Kind: "something-else", OldPath: f.Path, NewPath: f.Path})
	time.Sleep(50 * time.Millisecond)
	if atomic.LoadInt32(&called) != 0 {
		t.Error("an unrecognized event kind must not trigger a rescan")
	}
}

// When the container extension changed, the subscriber also polls for
// confirmation. This fake reports the OLD path for the first couple of
// polls, then the new one — the poll must find it and never record a
// warning.
func TestArrRescanSubscriberPollFindsNewPath(t *testing.T) {
	fastPolling(t, 2*time.Second)
	fastSeasonDebounce(t, 20*time.Millisecond)
	var polls int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/command":
			w.WriteHeader(200)
		case "/api/v3/episodefile":
			n := atomic.AddInt32(&polls, 1)
			path := "/data/show/e1.mkv"
			if n >= 3 {
				path = "/data/show/e1.mp4"
			}
			w.Write([]byte(`[{"id":1,"seriesId":42,"path":"` + path + `"}]`))
		}
	}))
	defer fake.Close()

	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/srv/media/show/e1.mkv", Library: "tvshows", Title: "Show"})
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "sonarr", Name: "Sonarr", Kind: "sonarr", URL: fake.URL, APIKey: "k", PathMap: "/data=/srv/media"}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f.ID, InstanceID: "sonarr", Kind: "sonarr", ItemID: 42}}); err != nil {
		t.Fatal(err)
	}

	s.ArrRescanSubscriber()(jobs.ReplacedEvent{
		FileID: f.ID, Kind: "remux",
		OldPath: "/srv/media/show/e1.mkv", NewPath: "/srv/media/show/e1.mp4",
	})

	waitFor(t, 3*time.Second, func() bool { return atomic.LoadInt32(&polls) >= 3 })
	time.Sleep(50 * time.Millisecond) // let the poll goroutine observe success and return
	if _, ok, _ := s.st.KVGet("arr_warning_sonarr"); ok {
		t.Error("a poll that eventually finds the new path must not record a warning")
	}
}

// The fake never reports the new path: the poll must time out and
// record a warning (kv + would-be SSE broadcast).
func TestArrRescanSubscriberPollTimeoutRecordsWarning(t *testing.T) {
	fastPolling(t, 30*time.Millisecond)
	fastSeasonDebounce(t, 5*time.Millisecond)
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/command":
			w.WriteHeader(200)
		case "/api/v3/episodefile":
			w.Write([]byte(`[{"id":1,"seriesId":42,"path":"/data/show/e1.mkv"}]`)) // always the OLD path
		}
	}))
	defer fake.Close()

	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/srv/media/show/e1.mkv", Library: "tvshows", Title: "Show"})
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "sonarr", Name: "Sonarr", Kind: "sonarr", URL: fake.URL, APIKey: "k", PathMap: "/data=/srv/media"}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f.ID, InstanceID: "sonarr", Kind: "sonarr", ItemID: 42}}); err != nil {
		t.Fatal(err)
	}

	s.ArrRescanSubscriber()(jobs.ReplacedEvent{
		FileID: f.ID, Kind: "remux",
		OldPath: "/srv/media/show/e1.mkv", NewPath: "/srv/media/show/e1.mp4",
	})

	waitFor(t, time.Second, func() bool {
		_, ok, _ := s.st.KVGet("arr_warning_sonarr")
		return ok
	})
}

// A copy-mode upscale sends the rescan but never polls, even when the
// paths differ (which they always do for a copy) — nothing was replaced,
// so there's no "new path" to confirm.
func TestArrRescanSubscriberUpscaleCopyNeverPolls(t *testing.T) {
	fastPolling(t, 30*time.Millisecond)
	fastSeasonDebounce(t, 5*time.Millisecond)
	var episodeFileCalls int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/command":
			w.WriteHeader(200)
		case "/api/v3/episodefile":
			atomic.AddInt32(&episodeFileCalls, 1)
			w.Write([]byte(`[]`))
		}
	}))
	defer fake.Close()

	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/srv/media/show/e1.mkv", Library: "tvshows", Title: "Show"})
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "sonarr", Name: "Sonarr", Kind: "sonarr", URL: fake.URL, APIKey: "k"}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f.ID, InstanceID: "sonarr", Kind: "sonarr", ItemID: 42}}); err != nil {
		t.Fatal(err)
	}

	s.ArrRescanSubscriber()(jobs.ReplacedEvent{
		FileID: f.ID, Kind: "upscale-copy",
		OldPath: "/srv/media/show/e1.mkv", NewPath: "/srv/media/show/e1 - 1080p upscale.mkv",
	})
	time.Sleep(100 * time.Millisecond) // longer than fastPolling's total: if it were polling, it'd have finished by now
	if atomic.LoadInt32(&episodeFileCalls) != 0 {
		t.Error("upscale-copy must never poll for a new path")
	}
	if _, ok, _ := s.st.KVGet("arr_warning_sonarr"); ok {
		t.Error("upscale-copy must never record a poll-timeout warning")
	}
}

// Three episodes finishing in a burst for the same series must produce
// exactly one RescanSeries call, not three — the whole point of
// batching a season re-encode.
func TestArrRescanSubscriberBatchesSeasonIntoOneCall(t *testing.T) {
	fastSeasonDebounce(t, 30*time.Millisecond)
	var calls int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/command" {
			atomic.AddInt32(&calls, 1)
		}
	}))
	defer fake.Close()

	s := newTestServer(t)
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "sonarr", Name: "Sonarr", Kind: "sonarr", URL: fake.URL, APIKey: "k"}}
	}); err != nil {
		t.Fatal(err)
	}
	sub := s.ArrRescanSubscriber()
	for i := 0; i < 3; i++ {
		f := mustUpsert(t, s.st, &store.File{Path: fmt.Sprintf("/m/show/e%d.mkv", i), Library: "tvshows", Title: "Show"})
		if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f.ID, InstanceID: "sonarr", Kind: "sonarr", ItemID: 42}}); err != nil {
			t.Fatal(err)
		}
		sub(jobs.ReplacedEvent{FileID: f.ID, Kind: "encode", OldPath: f.Path, NewPath: f.Path})
		time.Sleep(5 * time.Millisecond) // well inside the debounce window
	}

	time.Sleep(80 * time.Millisecond) // past the debounce window from the last event
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("RescanSeries calls = %d, want exactly 1 for a 3-episode burst", got)
	}
}

// A second finish arriving before the debounce elapses must push the
// timer out again, not let it fire on the first event's original
// schedule — otherwise a slow burst could still split into two calls.
func TestArrRescanSubscriberDebounceResetsOnEachFinish(t *testing.T) {
	fastSeasonDebounce(t, 60*time.Millisecond)
	var calls int32
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/command" {
			atomic.AddInt32(&calls, 1)
		}
	}))
	defer fake.Close()

	s := newTestServer(t)
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "sonarr", Name: "Sonarr", Kind: "sonarr", URL: fake.URL, APIKey: "k"}}
	}); err != nil {
		t.Fatal(err)
	}
	sub := s.ArrRescanSubscriber()
	f1 := mustUpsert(t, s.st, &store.File{Path: "/m/show/e1.mkv", Library: "tvshows", Title: "Show"})
	if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f1.ID, InstanceID: "sonarr", Kind: "sonarr", ItemID: 42}}); err != nil {
		t.Fatal(err)
	}
	sub(jobs.ReplacedEvent{FileID: f1.ID, Kind: "encode", OldPath: f1.Path, NewPath: f1.Path})

	time.Sleep(40 * time.Millisecond) // less than the 60ms debounce
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatal("must not have fired yet")
	}

	f2 := mustUpsert(t, s.st, &store.File{Path: "/m/show/e2.mkv", Library: "tvshows", Title: "Show"})
	if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f2.ID, InstanceID: "sonarr", Kind: "sonarr", ItemID: 42}}); err != nil {
		t.Fatal(err)
	}
	sub(jobs.ReplacedEvent{FileID: f2.ID, Kind: "encode", OldPath: f2.Path, NewPath: f2.Path})

	time.Sleep(40 * time.Millisecond) // 80ms since f1, but only 40ms since f2: still not due
	if atomic.LoadInt32(&calls) != 0 {
		t.Fatal("the second finish must have pushed the timer out, not fired on the first one's schedule")
	}
	waitFor(t, time.Second, func() bool { return atomic.LoadInt32(&calls) == 1 })
}

// Two different series must each get their own call, independently
// timed — one series' burst must not delay or merge with another's.
func TestArrRescanSubscriberDifferentSeriesGetSeparateCalls(t *testing.T) {
	fastSeasonDebounce(t, 20*time.Millisecond)
	var bodies []string
	var mu sync.Mutex
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/command" {
			buf := make([]byte, 200)
			n, _ := r.Body.Read(buf)
			mu.Lock()
			bodies = append(bodies, string(buf[:n]))
			mu.Unlock()
		}
	}))
	defer fake.Close()

	s := newTestServer(t)
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "sonarr", Name: "Sonarr", Kind: "sonarr", URL: fake.URL, APIKey: "k"}}
	}); err != nil {
		t.Fatal(err)
	}
	sub := s.ArrRescanSubscriber()
	for _, seriesID := range []int64{42, 99} {
		f := mustUpsert(t, s.st, &store.File{Path: fmt.Sprintf("/m/show/%d.mkv", seriesID), Library: "tvshows", Title: "Show"})
		if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f.ID, InstanceID: "sonarr", Kind: "sonarr", ItemID: seriesID}}); err != nil {
			t.Fatal(err)
		}
		sub(jobs.ReplacedEvent{FileID: f.ID, Kind: "encode", OldPath: f.Path, NewPath: f.Path})
	}

	waitFor(t, time.Second, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(bodies) == 2
	})
	mu.Lock()
	defer mu.Unlock()
	if !(contains(bodies[0], `"seriesId":42`) || contains(bodies[1], `"seriesId":42`)) ||
		!(contains(bodies[0], `"seriesId":99`) || contains(bodies[1], `"seriesId":99`)) {
		t.Fatalf("bodies = %v, want one call per series", bodies)
	}
}

// Every episode in a batch that changed its container extension must
// get its own path-confirmation poll once the batch's single rescan
// succeeds — not just the last one queued.
func TestArrRescanSubscriberBatchFlushesAllPendingPolls(t *testing.T) {
	fastSeasonDebounce(t, 20*time.Millisecond)
	fastPolling(t, time.Second)
	var polled sync.Map // path -> true
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v3/command":
			w.WriteHeader(200)
		case "/api/v3/episodefile":
			polled.Store("hit", true)
			w.Write([]byte(`[{"id":1,"seriesId":42,"path":"/data/show/e1.mp4"},{"id":2,"seriesId":42,"path":"/data/show/e2.mp4"}]`))
		}
	}))
	defer fake.Close()

	s := newTestServer(t)
	if err := s.cfg.Update(func(c *config.Config) {
		c.ArrInstances = []config.ArrInstance{{ID: "sonarr", Name: "Sonarr", Kind: "sonarr", URL: fake.URL, APIKey: "k", PathMap: "/data=/srv/media"}}
	}); err != nil {
		t.Fatal(err)
	}
	sub := s.ArrRescanSubscriber()
	for i := 1; i <= 2; i++ {
		f := mustUpsert(t, s.st, &store.File{Path: fmt.Sprintf("/srv/media/show/e%d.mp4", i), Library: "tvshows", Title: "Show"})
		if err := s.st.UpsertArrItems([]store.ArrItem{{FileID: f.ID, InstanceID: "sonarr", Kind: "sonarr", ItemID: 42}}); err != nil {
			t.Fatal(err)
		}
		sub(jobs.ReplacedEvent{
			FileID: f.ID, Kind: "remux",
			OldPath: fmt.Sprintf("/srv/media/show/e%d.mkv", i), NewPath: fmt.Sprintf("/srv/media/show/e%d.mp4", i),
		})
	}

	waitFor(t, time.Second, func() bool { _, ok := polled.Load("hit"); return ok })
	// Both episodes' warnings must stay unset: the batch's one rescan
	// must have satisfied both queued polls, not just one.
	time.Sleep(100 * time.Millisecond)
	if _, ok, _ := s.st.KVGet("arr_warning_sonarr"); ok {
		t.Error("both episodes' new paths were reported: neither poll should time out")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || (len(substr) > 0 && indexOf(s, substr) >= 0))
}

func indexOf(s, substr string) int {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}
