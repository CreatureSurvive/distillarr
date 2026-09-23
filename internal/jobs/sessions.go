package jobs

import (
	"context"
	"log"
	"sync"
	"time"

	"mediatrans/internal/jellyfin"
	"mediatrans/internal/plex"
	"mediatrans/internal/store"
)

// sessionsState is the latest snapshot from the sessions poller:
// which local paths are currently playing (across both servers), and how
// many sessions are transcoding video — the dispatcher's gate.
type sessionsState struct {
	mu          sync.Mutex
	playing     map[string]bool
	transcoding int
}

func (e *Engine) sessionsSnapshot() (playing map[string]bool, transcoding int) {
	e.sess.mu.Lock()
	defer e.sess.mu.Unlock()
	return e.sess.playing, e.sess.transcoding
}

// Transcoding reports how many playback sessions are currently
// transcoding video: the dispatcher's gate and the queue header.
func (e *Engine) Transcoding() int {
	_, n := e.sessionsSnapshot()
	return n
}

// IsPlaying reports whether path is currently being played (either
// server) — the replace-hold check.
func (e *Engine) IsPlaying(path string) bool {
	playing, _ := e.sessionsSnapshot()
	return playing[path]
}

// needSessions: only poll while there's queue work the gate or hold could
// affect (matches measureIdle's reasoning in reverse — sessions polling
// only matters when jobs exist).
func (e *Engine) needSessions() bool {
	if e.active.Load() > 0 {
		return true
	}
	c, _ := e.st.CountJobsByStatus()
	return c[store.StatusQueued] > 0
}

func (e *Engine) sessionsLoop() {
	defer e.wg.Done()
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		if e.needSessions() {
			e.pollSessions()
		}
		select {
		case <-t.C:
		case <-e.stopCh:
			return
		}
	}
}

// pollSessions fetches both servers' current sessions and replaces the
// snapshot. Best-effort: a fetch error just logs and leaves that server's
// contribution empty for this round, same as every other integration in
// this app.
func (e *Engine) pollSessions() {
	cfg := e.cfg.Get()
	playing := map[string]bool{}
	transcoding := 0

	if cfg.JellyfinURL != "" && cfg.JellyfinAPIKey != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		sessions, err := jellyfin.New(cfg.JellyfinURL, cfg.JellyfinAPIKey).Sessions(ctx)
		cancel()
		if err != nil {
			log.Printf("jobs: jellyfin sessions: %v", err)
		}
		for _, sess := range sessions {
			if sess.NowPlayingItem == nil || sess.NowPlayingItem.Path == "" {
				continue
			}
			playing[cfg.MapJellyfinPath(sess.NowPlayingItem.Path)] = true
			if sess.TranscodingInfo != nil && !sess.TranscodingInfo.IsVideoDirect {
				transcoding++
			}
		}
	}

	if cfg.PlexURL != "" && cfg.PlexToken != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		sessions, err := plex.New(cfg.PlexURL, cfg.PlexToken).Sessions(ctx)
		cancel()
		if err != nil {
			log.Printf("jobs: plex sessions: %v", err)
		}
		for _, it := range sessions {
			for _, m := range it.Media {
				for _, part := range m.Part {
					if part.File != "" {
						playing[cfg.MapPlexPath(part.File)] = true
					}
				}
			}
			if it.TranscodeSession != nil && it.TranscodeSession.VideoDecision == "transcode" {
				transcoding++
			}
		}
	}

	e.sess.mu.Lock()
	e.sess.playing, e.sess.transcoding = playing, transcoding
	e.sess.mu.Unlock()
}
