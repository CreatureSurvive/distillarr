// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/jobs"
	"github.com/CreatureSurvive/distillarr/internal/plex"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

var plexSyncing atomic.Bool

func (s *Server) plexClient() *plex.Client {
	c := s.cfg.Get()
	if c.PlexURL == "" || c.PlexToken == "" {
		return nil
	}
	return plex.New(c.PlexURL, c.PlexToken)
}

func (s *Server) plexStatus(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.Get()
	out := map[string]any{
		"configured": c.PlexURL != "" && c.PlexToken != "",
		"url":        c.PlexURL,
		"cached":     s.st.PlexCount(),
		"syncing":    plexSyncing.Load(),
	}
	if v, ok, _ := s.st.KVGet("plex_last_sync"); ok {
		out["last_sync"] = v
	}
	if cl := s.plexClient(); cl != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		defer cancel()
		if id, err := cl.Test(ctx); err == nil {
			out["connected"], out["server_name"], out["version"] = true, id.FriendlyName, id.Version
		} else {
			out["connected"], out["error"] = false, friendlyPlexError(err)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func friendlyPlexError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "401"), strings.Contains(msg, "Unauthorized"):
		return "Plex rejected the token (401). Find yours at https://support.plex.tv/articles/204059436."
	case strings.Contains(msg, "connection refused"):
		return "Nothing is listening at that URL. Check the host and port (Plex uses 32400)."
	case strings.Contains(msg, "no such host"):
		return "That hostname doesn't resolve from inside the container."
	case strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, "Timeout"):
		return "Timed out reaching Plex. Check the URL and that the container can reach it."
	}
	return msg
}

// plexTest checks a URL + token without saving them, and verifies that
// Plex's section locations map onto paths this container can see.
func (s *Server) plexTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL     string `json:"url"`
		Token   string `json:"token"`
		PathMap string `json:"path_map"`
	}
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err)
		return
	}
	cfg := s.cfg.Get()
	if req.URL == "" {
		req.URL = cfg.PlexURL
	}
	if req.Token == "" {
		req.Token = cfg.PlexToken
	}
	if req.PathMap != "" {
		cfg.PlexPathMap = req.PathMap
	}
	if req.URL == "" || req.Token == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Enter the server URL and a token."})
		return
	}
	cl := plex.New(req.URL, req.Token)
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	id, err := cl.Test(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": friendlyPlexError(err)})
		return
	}
	type libOut struct {
		Name      string `json:"name"`
		Type      string `json:"type"`
		Location  string `json:"location"`
		Mapped    string `json:"mapped"`
		Reachable bool   `json:"reachable"`
	}
	var libs []libOut
	if secs, err := cl.Sections(ctx); err == nil {
		for _, sec := range secs {
			if sec.Type != "movie" && sec.Type != "show" {
				continue // no media files this app tracks
			}
			for _, loc := range sec.Location {
				m := cfg.MapPlexPath(loc.Path)
				_, statErr := os.Stat(m)
				libs = append(libs, libOut{Name: sec.Title, Type: sec.Type, Location: loc.Path,
					Mapped: m, Reachable: statErr == nil})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "server_name": id.FriendlyName, "version": id.Version, "libraries": libs,
	})
}

func (s *Server) plexSync(w http.ResponseWriter, r *http.Request) {
	cl := s.plexClient()
	if cl == nil {
		fail(w, 400, fmt.Errorf("save a Plex URL and token first"))
		return
	}
	if !plexSyncing.CompareAndSwap(false, true) {
		writeJSON(w, http.StatusOK, map[string]any{"started": false, "syncing": true})
		return
	}
	go func() {
		defer plexSyncing.Store(false)
		n, err := s.SyncPlex(context.Background(), cl)
		if err != nil {
			log.Printf("plex sync: %v", err)
			s.hub.Broadcast("plex", map[string]any{"error": friendlyPlexError(err)})
			return
		}
		s.hub.Broadcast("plex", map[string]any{"done": true, "synced": n})
	}()
	writeJSON(w, http.StatusOK, map[string]any{"started": true})
}

// SyncPlex walks every movie/show section and caches each item's Plex
// identity against the local file(s) its Parts map onto.
func (s *Server) SyncPlex(ctx context.Context, cl *plex.Client) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	cfg := s.cfg.Get()
	secs, err := cl.Sections(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, sec := range secs {
		var typ, itemType int
		switch sec.Type {
		case "movie":
			typ, itemType = 0, 1 // typ 0 omits the filter (Plex's own default); items are still metadata type 1
		case "show":
			typ, itemType = 4, 4 // flattens every episode out of the section in one walk
		default:
			continue
		}
		err := cl.WalkSection(ctx, sec.Key, typ, func(items []plex.Item) error {
			var rows []store.PlexRow
			for _, it := range items {
				for _, media := range it.Media {
					for _, part := range media.Part {
						if part.File == "" {
							continue
						}
						local := cfg.MapPlexPath(part.File)
						f, err := s.st.GetFileByPath(local)
						if err != nil || f == nil {
							continue
						}
						rows = append(rows, store.PlexRow{FileID: f.ID, RatingKey: it.RatingKey,
							SectionID: sec.Key, AddedAt: it.AddedAt, ItemType: itemType})
					}
				}
			}
			if err := s.st.UpsertPlex(rows); err != nil {
				return err
			}
			n += len(rows)
			s.hub.Broadcast("plex", map[string]any{"synced": n})
			return nil
		})
		if err != nil {
			return n, err
		}
	}
	_ = s.st.KVSet("plex_last_sync", time.Now().UTC().Format(time.RFC3339))
	return n, nil
}

// PlexRefreshSubscriber returns an OnFinished subscriber that asks Plex
// to rescan the affected directory after a replace or upscale-copy, so
// it picks up the changed/new file without waiting for its own scan
// interval. plex_items is keyed by file_id, which survives the replace,
// so no new item id needs to be looked up. When PlexKeepAddedAtOn,
// it also restores the item's "date added" if the refresh changed it —
// best-effort throughout: any Metadata/SetAddedAt failure just logs, the
// same as a Refresh failure does.
func (s *Server) PlexRefreshSubscriber() func(jobs.ReplacedEvent) {
	return func(ev jobs.ReplacedEvent) {
		c := s.cfg.Get()
		if c.PlexURL == "" || c.PlexToken == "" {
			return
		}
		row, err := s.st.PlexByFileID(ev.FileID)
		if err != nil || row == nil {
			return
		}
		cl := plex.New(c.PlexURL, c.PlexToken)
		dir := c.ToPlexPath(filepath.Dir(ev.NewPath))
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		var before int64
		keepAddedAt := c.PlexKeepAddedAtOn()
		if keepAddedAt {
			if it, err := cl.Metadata(ctx, row.RatingKey); err == nil {
				before = it.AddedAt
			} else {
				log.Printf("plex addedAt before-read %s: %v", ev.NewPath, err)
				keepAddedAt = false
			}
		}

		if err := cl.Refresh(ctx, row.SectionID, dir); err != nil {
			log.Printf("plex refresh %s: %v", ev.NewPath, err)
			return
		}

		if !keepAddedAt || before == 0 {
			return
		}
		it, err := cl.Metadata(ctx, row.RatingKey)
		if err != nil {
			log.Printf("plex addedAt after-read %s: %v", ev.NewPath, err)
			return
		}
		if it.AddedAt == before {
			return
		}
		if err := cl.SetAddedAt(ctx, row.SectionID, row.RatingKey, row.ItemType, before, true); err != nil {
			log.Printf("plex addedAt restore %s: %v", ev.NewPath, err)
			return
		}
		if err := s.st.SetPlexAddedAt(ev.FileID, before); err != nil {
			log.Printf("plex addedAt cache update %s: %v", ev.NewPath, err)
		}
	}
}
