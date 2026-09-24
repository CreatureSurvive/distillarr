package jobs

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/issues"
	"github.com/CreatureSurvive/distillarr/internal/jellyfin"
	"github.com/CreatureSurvive/distillarr/internal/plex"
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

// sessionsLoop polls unconditionally every 15s, not just while jobs are
// queued/running: dispatch gate and replace hold only matter
// during queue activity, but playback_events history (what a
// file has forced clients to transcode, over time) needs to see
// ordinary playback too, which happens most often when nothing's
// queued.
func (e *Engine) sessionsLoop() {
	defer e.wg.Done()
	t := time.NewTicker(15 * time.Second)
	defer t.Stop()
	for {
		e.pollSessions()
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
	now := time.Now()

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
			path := cfg.MapJellyfinPath(sess.NowPlayingItem.Path)
			playing[path] = true
			direct := sess.TranscodingInfo == nil
			var reasons []string
			if sess.TranscodingInfo != nil {
				if !sess.TranscodingInfo.IsVideoDirect {
					transcoding++
				}
				reasons = reasonCategories(sess.TranscodingInfo.TranscodeReasons)
			}
			e.recordPlaybackEvent(path, "jellyfin", now, direct, reasons)
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
			direct := it.TranscodeSession == nil
			var raw, reasons []string
			if ts := it.TranscodeSession; ts != nil {
				if ts.VideoDecision == "transcode" {
					transcoding++
					raw = append(raw, "VideoCodecNotSupported")
				}
				if ts.AudioDecision == "transcode" {
					raw = append(raw, "AudioCodecNotSupported")
				}
				if ts.SubtitleDecision == "transcode" || ts.SubtitleDecision == "burn" {
					raw = append(raw, "SubtitleCodecNotSupported")
				}
				reasons = reasonCategories(raw)
			}
			for _, m := range it.Media {
				for _, part := range m.Part {
					if part.File == "" {
						continue
					}
					path := cfg.MapPlexPath(part.File)
					playing[path] = true
					e.recordPlaybackEvent(path, "plex", now, direct, reasons)
				}
			}
		}
	}

	e.sess.mu.Lock()
	e.sess.playing, e.sess.transcoding = playing, transcoding
	e.sess.mu.Unlock()
}

// reasonCategories maps raw server-specific reason strings onto
// issues.ReasonCategory's small canonical vocabulary, deduplicated —
// what playback_events.reasons and the forces_transcode detail store
// and count.
func reasonCategories(raw []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range raw {
		c := issues.ReasonCategory(r)
		if !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// recordPlaybackEvent looks up path's tracked file and records the
// observation. Best-effort and silent when the path isn't a
// tracked file (e.g. outside every library, or a mid-rename gap) — same
// as every other best-effort integration write in this poller.
func (e *Engine) recordPlaybackEvent(path, server string, at time.Time, direct bool, reasons []string) {
	f, err := e.st.GetFileByPath(path)
	if err != nil || f == nil {
		return
	}
	if err := e.st.RecordPlaybackEvent(f.ID, server, at, direct, reasons); err != nil {
		log.Printf("jobs: record playback event: %v", err)
	}
}
