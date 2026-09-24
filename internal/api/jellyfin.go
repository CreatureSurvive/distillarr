package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/jellyfin"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

var jfSyncing atomic.Bool

func (s *Server) jfClient() *jellyfin.Client {
	c := s.cfg.Get()
	if c.JellyfinURL == "" || c.JellyfinAPIKey == "" {
		return nil
	}
	return jellyfin.New(c.JellyfinURL, c.JellyfinAPIKey)
}

func (s *Server) jfStatus(w http.ResponseWriter, r *http.Request) {
	c := s.cfg.Get()
	out := map[string]any{
		"configured": c.JellyfinURL != "" && c.JellyfinAPIKey != "",
		"url":        c.JellyfinURL,
		"cached":     s.st.JellyfinCount(),
		"syncing":    jfSyncing.Load(),
	}
	if v, ok, _ := s.st.KVGet("jf_last_sync"); ok {
		out["last_sync"] = v
	}
	if cl := s.jfClient(); cl != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		defer cancel()
		if si, err := cl.Test(ctx); err == nil {
			out["connected"], out["server_name"], out["version"] = true, si.ServerName, si.Version
		} else {
			out["connected"], out["error"] = false, friendlyJFError(err)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func friendlyJFError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "401"):
		return "Jellyfin rejected the API key (401). Create one in Dashboard → API Keys."
	case strings.Contains(msg, "connection refused"):
		return "Nothing is listening at that URL. Check the host and port (Jellyfin uses 8096)."
	case strings.Contains(msg, "no such host"):
		return "That hostname doesn't resolve from inside the container."
	case strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, "Timeout"):
		return "Timed out reaching Jellyfin. Check the URL and that the container can reach it."
	}
	return msg
}

// jfTest checks a URL + key without saving them, and verifies that
// Jellyfin's library folders map onto paths this container can see.
func (s *Server) jfTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL     string `json:"url"`
		Key     string `json:"key"`
		PathMap string `json:"path_map"`
	}
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err)
		return
	}
	cfg := s.cfg.Get()
	if req.URL == "" {
		req.URL = cfg.JellyfinURL
	}
	if req.Key == "" {
		req.Key = cfg.JellyfinAPIKey
	}
	if req.PathMap != "" {
		cfg.JellyfinPathMap = req.PathMap
	}
	if req.URL == "" || req.Key == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Enter the server URL and an API key."})
		return
	}
	cl := jellyfin.New(req.URL, req.Key)
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	si, err := cl.Test(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": friendlyJFError(err)})
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
	if folders, err := cl.Libraries(ctx); err == nil {
		for _, f := range folders {
			if f.CollectionType == "boxsets" || f.CollectionType == "playlists" {
				continue // no media files of their own
			}
			for _, loc := range f.Locations {
				m := cfg.MapJellyfinPath(loc)
				_, statErr := os.Stat(m)
				libs = append(libs, libOut{Name: f.Name, Type: f.CollectionType, Location: loc,
					Mapped: m, Reachable: statErr == nil})
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "server_name": si.ServerName, "version": si.Version, "libraries": libs,
	})
}

func (s *Server) jfSync(w http.ResponseWriter, r *http.Request) {
	cl := s.jfClient()
	if cl == nil {
		fail(w, 400, fmt.Errorf("save a Jellyfin URL and API key first"))
		return
	}
	if !jfSyncing.CompareAndSwap(false, true) {
		writeJSON(w, http.StatusOK, map[string]any{"started": false, "syncing": true})
		return
	}
	go func() {
		defer jfSyncing.Store(false)
		n, err := s.SyncJellyfin(context.Background(), cl)
		if err != nil {
			log.Printf("jellyfin sync: %v", err)
			s.hub.Broadcast("jellyfin", map[string]any{"error": friendlyJFError(err)})
			return
		}
		s.hub.Broadcast("jellyfin", map[string]any{"done": true, "synced": n})
	}()
	writeJSON(w, http.StatusOK, map[string]any{"started": true})
}

// SyncJellyfin caches item ids/genres/overviews keyed by local path.
func (s *Server) SyncJellyfin(ctx context.Context, cl *jellyfin.Client) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	cfg := s.cfg.Get()
	seriesGenres := map[string]string{}
	n := 0
	err := cl.WalkSeries(ctx, func(items []jellyfin.Item) error {
		rows := make([]store.JellyfinRow, 0, len(items))
		for _, it := range items {
			g := strings.Join(it.Genres, ",")
			seriesGenres[it.ID] = g
			if it.Path == "" {
				continue
			}
			rows = append(rows, store.JellyfinRow{Path: cfg.MapJellyfinPath(it.Path), ItemID: it.ID,
				Name: it.Name, Overview: it.Overview, Genres: g, ItemType: "Series"})
		}
		n += len(rows)
		return s.st.UpsertJF(rows)
	})
	if err != nil {
		return n, err
	}
	err = cl.WalkItems(ctx, func(items []jellyfin.Item) error {
		rows := make([]store.JellyfinRow, 0, len(items))
		for _, it := range items {
			if it.Path == "" {
				continue
			}
			g := strings.Join(it.Genres, ",")
			if g == "" {
				g = seriesGenres[it.SeriesID]
			}
			rows = append(rows, store.JellyfinRow{
				Path: cfg.MapJellyfinPath(it.Path), ItemID: it.ID, SeriesID: it.SeriesID,
				SeasonID: it.SeasonID, Name: it.Name, ImageTag: it.ImageTags["Primary"],
				Overview: it.Overview, Genres: g, ItemType: it.Type,
			})
		}
		if err := s.st.UpsertJF(rows); err != nil {
			return err
		}
		n += len(rows)
		s.hub.Broadcast("jellyfin", map[string]any{"synced": n})
		return nil
	})
	if err != nil {
		return n, err
	}
	_ = s.st.KVSet("jf_last_sync", time.Now().UTC().Format(time.RFC3339))
	s.scan.RefreshRecsSoon() // genres feed the animation rule
	return n, nil
}

var safeID = regexp.MustCompile(`^[a-fA-F0-9-]{8,64}$`)

// image proxies Jellyfin artwork with an on-disk cache.
func (s *Server) image(w http.ResponseWriter, r *http.Request) {
	itemID := r.PathValue("itemid")
	kind := r.URL.Query().Get("kind")
	if kind != "Backdrop" && kind != "Thumb" {
		kind = "Primary"
	}
	width := max(80, min(1920, atoi(r.URL.Query().Get("w"))))
	if !safeID.MatchString(itemID) {
		http.NotFound(w, r)
		return
	}
	cacheDir := "/config/imgcache"
	cache := filepath.Join(cacheDir, fmt.Sprintf("%s-%s-%d", itemID, kind, width))
	if b, err := os.ReadFile(cache); err == nil {
		if len(b) == 0 { // negative cache: Jellyfin has no such image
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=604800")
		w.Header().Set("Content-Type", http.DetectContentType(b))
		w.Write(b)
		return
	}
	cl := s.jfClient()
	if cl == nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	b, ct, err := cl.FetchImage(ctx, itemID, kind, width)
	_ = os.MkdirAll(cacheDir, 0o755)
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			_ = os.WriteFile(cache, nil, 0o644)
		}
		http.NotFound(w, r)
		return
	}
	_ = os.WriteFile(cache, b, 0o644)
	w.Header().Set("Cache-Control", "public, max-age=604800")
	if ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Write(b)
}
