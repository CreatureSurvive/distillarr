// SPDX-License-Identifier: GPL-3.0-or-later

package jobs

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/replace"
	"github.com/CreatureSurvive/distillarr/internal/scan"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.NewManager(st)
	sc := scan.New(st, cfg)
	return New(st, cfg, sc)
}

func waitFor(t *testing.T, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

func TestPollSessionsMapsPathsAndCountsTranscoding(t *testing.T) {
	jf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"NowPlayingItem":{"Path":"/jf/movies/A.mkv"},"TranscodingInfo":{"IsVideoDirect":false}},
			{"NowPlayingItem":{"Path":"/jf/movies/B.mkv"}}
		]`))
	}))
	defer jf.Close()
	px := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"MediaContainer":{"Metadata":[
			{"ratingKey":"1","Media":[{"Part":[{"file":"/plex/movies/C.mkv"}]}],"TranscodeSession":{"videoDecision":"transcode"}}
		]}}`))
	}))
	defer px.Close()

	e := newTestEngine(t)
	if err := e.cfg.Update(func(c *config.Config) {
		c.JellyfinURL, c.JellyfinAPIKey, c.JellyfinPathMap = jf.URL, "key", "/jf=/local"
		c.PlexURL, c.PlexToken, c.PlexPathMap = px.URL, "tok", "/plex=/local"
	}); err != nil {
		t.Fatal(err)
	}

	e.pollSessions()

	playing, transcoding := e.sessionsSnapshot()
	if transcoding != 2 { // one video transcode from each server
		t.Errorf("transcoding = %d, want 2", transcoding)
	}
	for _, want := range []string{"/local/movies/A.mkv", "/local/movies/B.mkv", "/local/movies/C.mkv"} {
		if !playing[want] {
			t.Errorf("expected %s to be marked playing; got %v", want, playing)
		}
	}
	if !e.IsPlaying("/local/movies/B.mkv") {
		t.Error("IsPlaying must reflect a direct-play session too, not just transcoding ones")
	}
	if e.Transcoding() != 2 {
		t.Errorf("Transcoding() = %d, want 2", e.Transcoding())
	}
}

func TestPollSessionsRecordsPlaybackEvents(t *testing.T) {
	jf := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"NowPlayingItem":{"Path":"/jf/movies/A.mkv"},
			 "TranscodingInfo":{"IsVideoDirect":false,"TranscodeReasons":["VideoCodecNotSupported","AudioCodecNotSupported"]}}
		]`))
	}))
	defer jf.Close()

	e := newTestEngine(t)
	if err := e.cfg.Update(func(c *config.Config) {
		c.JellyfinURL, c.JellyfinAPIKey, c.JellyfinPathMap = jf.URL, "key", "/jf=/local"
	}); err != nil {
		t.Fatal(err)
	}
	f := &store.File{Path: "/local/movies/A.mkv", Library: "movies"}
	if err := e.st.UpsertFile(f, nil); err != nil {
		t.Fatal(err)
	}

	e.pollSessions()

	forces, err := e.st.ForcesTranscode(f.ID, 30)
	if err != nil {
		t.Fatal(err)
	}
	if !forces {
		t.Error("a non-direct session must record a playback event the forces_transcode issue can see")
	}
	detail, err := e.st.ForcesTranscodeDetail(f.ID, 30)
	if err != nil {
		t.Fatal(err)
	}
	if detail == nil || detail.Reasons["video_codec"] != 1 || detail.Reasons["audio_codec"] != 1 {
		t.Errorf("wrong reason detail: %+v", detail)
	}
}

func TestReasonCategories(t *testing.T) {
	got := reasonCategories([]string{"VideoCodecNotSupported", "VideoProfileNotSupported", "VideoLevelNotSupported"})
	if len(got) != 3 {
		t.Errorf("distinct raw reasons must map to distinct categories: %v", got)
	}
	got = reasonCategories([]string{"AudioCodecNotSupported", "AudioBitrateNotSupported"})
	if len(got) != 1 || got[0] != "audio_codec" {
		t.Errorf("raw reasons mapping to the same category must dedup: %v", got)
	}
}

func TestDispatcherGateWhileTranscoding(t *testing.T) {
	e := newTestEngine(t)
	e.workerCap.Store(1)
	e.windowOpen.Store(true)

	j := &store.Job{SrcPath: "/m/x.mkv", Backend: "sw", Codec: "hevc", MaxAttempts: 3}
	if err := e.st.CreateJob(j); err != nil {
		t.Fatal(err)
	}

	e.sess.mu.Lock()
	e.sess.transcoding = 1
	e.sess.mu.Unlock()

	e.tryDispatch()

	got, err := e.st.GetJob(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != store.StatusQueued || got.Attempts != 0 {
		t.Fatalf("job should not have been claimed while a viewer is transcoding: %+v", got)
	}

	// Turning the setting off must let it dispatch even while transcoding.
	if err := e.cfg.Update(func(c *config.Config) { f := false; c.DeferWhileTranscoding = &f }); err != nil {
		t.Fatal(err)
	}
	e.tryDispatch()
	if !waitFor(t, func() bool {
		got, _ := e.st.GetJob(j.ID)
		return got.Status != store.StatusQueued
	}) {
		t.Fatal("job should have been claimed once the setting was turned off")
	}
}

func TestCanResume(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mkv")
	if err := os.WriteFile(src, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := replace.Snapshot(src)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(snap)
	j := &store.Job{SrcPath: src, SrcStatJSON: string(b)}

	if canResume(j) {
		t.Error("no temp path yet: must not be resumable")
	}

	temp := filepath.Join(dir, "job1.mp4.tmp")
	j.TempPath = temp
	if canResume(j) {
		t.Error("temp path set but no file on disk: must not be resumable")
	}

	if err := os.WriteFile(temp, []byte("finished encode"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !canResume(j) {
		t.Error("temp file present and source unchanged: must be resumable")
	}

	if err := os.WriteFile(src, []byte("hello, but the source changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if canResume(j) {
		t.Error("source changed since the snapshot: must not be resumable")
	}
}

func TestWaitForPlaybackDoneNotPlaying(t *testing.T) {
	e := newTestEngine(t)
	j := &store.Job{SrcPath: "/m/not-playing.mkv"}
	ok, capped := e.waitForPlaybackDone(context.Background(), j)
	if !ok || capped {
		t.Errorf("ok=%v capped=%v, want ok=true capped=false when nobody is playing the file", ok, capped)
	}
}

func TestWaitForPlaybackDoneCanceled(t *testing.T) {
	e := newTestEngine(t)
	e.sess.mu.Lock()
	e.sess.playing = map[string]bool{"/m/playing.mkv": true}
	e.sess.mu.Unlock()

	j := &store.Job{SrcPath: "/m/playing.mkv"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ok, capped := e.waitForPlaybackDone(ctx, j)
	if ok || capped {
		t.Errorf("ok=%v capped=%v, want both false on cancel", ok, capped)
	}
}
