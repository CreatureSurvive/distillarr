// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/issues"
	"github.com/CreatureSurvive/distillarr/internal/jellystat"
)

var jellystatImporting atomic.Bool

func (s *Server) jellystatTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
		Key string `json:"key"`
	}
	_ = readJSON(r, &req)
	cfg := s.cfg.Get()
	if req.URL == "" {
		req.URL = cfg.JellystatURL
	}
	if req.Key == "" {
		req.Key = cfg.JellystatKey
	}
	if req.URL == "" || req.Key == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Enter the Jellystat URL and an API key (Jellystat: Settings > API Keys)."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	pages, err := jellystat.New(req.URL, req.Key).Test(ctx)
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "403") || strings.Contains(msg, "401") {
			msg = "Jellystat rejected the API key."
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": msg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "history_groups": pages})
}

func (s *Server) jellystatStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"importing": jellystatImporting.Load()}
	if v, ok, _ := s.st.KVGet("jellystat_last_import"); ok {
		out["last_import"] = v
	}
	if v, ok, _ := s.st.KVGet("jellystat_last_count"); ok {
		out["last_count"] = v
	}
	writeJSON(w, http.StatusOK, out)
}

// jellystatImport starts a full (not incremental) history import.
func (s *Server) jellystatImport(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	if cfg.JellystatURL == "" || cfg.JellystatKey == "" {
		fail(w, 400, fmt.Errorf("save a Jellystat URL and API key first"))
		return
	}
	if !jellystatImporting.CompareAndSwap(false, true) {
		writeJSON(w, http.StatusOK, map[string]any{"started": false, "importing": true})
		return
	}
	go func() {
		defer jellystatImporting.Store(false)
		if _, err := s.ImportJellystat(context.Background(), time.Time{}); err != nil {
			log.Printf("jellystat import: %v", err)
		}
	}()
	writeJSON(w, http.StatusOK, map[string]any{"started": true})
}

// ImportJellystat copies Jellystat's sessions into playback_events
// (idempotent: the same session-hour upserts the same row), mapping
// Jellyfin item ids to files through the Jellyfin cache. Returns how many
// sessions matched a tracked file.
func (s *Server) ImportJellystat(ctx context.Context, since time.Time) (int, error) {
	cfg := s.cfg.Get()
	cl := jellystat.New(cfg.JellystatURL, cfg.JellystatKey)
	paths, err := s.st.JellyfinPathsByItem()
	if err != nil {
		return 0, err
	}
	fileIDs := map[string]int64{}
	started := time.Now()
	n := 0
	err = cl.History(ctx, since, func(acts []jellystat.Activity) error {
		for _, a := range acts {
			p, ok := paths[strings.ToLower(strings.ReplaceAll(a.ItemID(), "-", ""))]
			if !ok || a.At.IsZero() {
				continue
			}
			id, ok := fileIDs[p]
			if !ok {
				if f, _ := s.st.GetFileByPath(p); f != nil {
					id = f.ID
				}
				fileIDs[p] = id
			}
			if id == 0 {
				continue
			}
			var reasons []string
			if !a.Direct() {
				reasons = reasonCats(a.Reasons())
			}
			if err := s.st.RecordPlaybackEvent(id, "jellyfin", a.At, a.Direct(), reasons); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	if err != nil {
		return n, err
	}
	_ = s.st.KVSet("jellystat_last_import", started.UTC().Format(time.RFC3339))
	_ = s.st.KVSet("jellystat_last_count", fmt.Sprint(n))
	s.scan.RefreshRecsSoon() // forces_transcode may have changed
	return n, nil
}

func reasonCats(raw []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range raw {
		if c := issues.ReasonCategory(r); !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	return out
}

// JellystatLoop imports new history every 6 hours while Jellystat is
// configured (a day of overlap, since Jellystat filters by each item
// group's latest session).
func (s *Server) JellystatLoop(stop <-chan struct{}) {
	t := time.NewTicker(6 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			cfg := s.cfg.Get()
			if cfg.JellystatURL == "" || cfg.JellystatKey == "" || !jellystatImporting.CompareAndSwap(false, true) {
				continue
			}
			since := time.Time{}
			if v, ok, _ := s.st.KVGet("jellystat_last_import"); ok {
				if t, err := time.Parse(time.RFC3339, v); err == nil {
					since = t.Add(-24 * time.Hour)
				}
			}
			if _, err := s.ImportJellystat(context.Background(), since); err != nil {
				log.Printf("jellystat import: %v", err)
			}
			jellystatImporting.Store(false)
		}
	}
}

