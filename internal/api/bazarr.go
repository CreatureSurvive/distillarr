package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/bazarr"
	"github.com/CreatureSurvive/distillarr/internal/jobs"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

func (s *Server) bazarrClient() *bazarr.Client {
	c := s.cfg.Get()
	if c.BazarrURL == "" || c.BazarrKey == "" {
		return nil
	}
	return bazarr.New(c.BazarrURL, c.BazarrKey)
}

func friendlyBazarrError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "401"), strings.Contains(msg, "403"):
		return "Bazarr rejected the API key. Find it in Bazarr under Settings > General > Security."
	case strings.Contains(msg, "connection refused"):
		return "Nothing is listening at that URL. Check the host and port (Bazarr uses 6767)."
	case strings.Contains(msg, "no such host"):
		return "That hostname doesn't resolve from inside the container."
	case strings.Contains(msg, "deadline exceeded"), strings.Contains(msg, "Timeout"):
		return "Timed out reaching Bazarr. Check the URL and that the container can reach it."
	}
	return msg
}

// bazarrTest checks a URL + key without saving them, shows Bazarr's
// version, and checks that a sample series/movie path Bazarr reports
// maps onto a path this container can see.
func (s *Server) bazarrTest(w http.ResponseWriter, r *http.Request) {
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
		req.URL = cfg.BazarrURL
	}
	if req.Key == "" {
		req.Key = cfg.BazarrKey
	}
	if req.PathMap != "" {
		cfg.BazarrPathMap = req.PathMap
	}
	if req.URL == "" || req.Key == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Enter the Bazarr URL and API key."})
		return
	}
	cl := bazarr.New(req.URL, req.Key)
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	st, err := cl.Status(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": friendlyBazarrError(err)})
		return
	}
	type pathOut struct {
		Title     string `json:"title"`
		Path      string `json:"path"`
		Mapped    string `json:"mapped"`
		Reachable bool   `json:"reachable"`
	}
	var paths []pathOut
	if items, err := cl.SamplePaths(ctx); err == nil {
		for _, it := range items {
			m := cfg.MapBazarrPath(it.Path)
			_, statErr := os.Stat(m)
			paths = append(paths, pathOut{Title: it.Title, Path: it.Path, Mapped: m, Reachable: statErr == nil})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "version": st.Version, "sonarr_version": st.SonarrVersion,
		"radarr_version": st.RadarrVersion, "paths": paths,
	})
}

// bazarrAction runs action against the Sonarr series / Radarr movie
// that owns item. Bazarr keys items by those ids, so a file with no
// arr_items row can't be addressed.
func bazarrAction(ctx context.Context, cl *bazarr.Client, item *store.ArrItem, action string) error {
	if item.Kind == "sonarr" {
		return cl.Series(ctx, item.ItemID, action)
	}
	return cl.Movie(ctx, item.ItemID, action)
}

// bazarrSearch asks Bazarr to search for missing subtitles on the
// file's series/movie (the missing_subs issue's button).
func (s *Server) bazarrSearch(w http.ResponseWriter, r *http.Request) {
	cl := s.bazarrClient()
	if cl == nil {
		fail(w, 400, fmt.Errorf("save a Bazarr URL and API key first"))
		return
	}
	item, err := s.st.ArrItemByFileID(pathID(r))
	if err != nil || item == nil {
		fail(w, 400, fmt.Errorf("this file isn't managed by Sonarr or Radarr, so Bazarr can't address it"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := bazarrAction(ctx, cl, item, bazarr.SearchMissing); err != nil {
		fail(w, 502, fmt.Errorf("%s", friendlyBazarrError(err)))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// BazarrRescanSubscriber returns an OnFinished subscriber: after a
// replace (or a new upscale copy), ask Bazarr to rescan that
// series/movie from disk so its subtitle state follows the new file and
// extension. Series are debounced like ArrRescanSubscriber's, so a whole
// season finishing sends one scan-disk call.
func (s *Server) BazarrRescanSubscriber() func(jobs.ReplacedEvent) {
	batch := newArrRescanBatcher()
	return func(ev jobs.ReplacedEvent) {
		switch ev.Kind {
		case "encode", "remux", "upscale-replace", "upscale-copy":
		default:
			return
		}
		if s.bazarrClient() == nil {
			return
		}
		item, err := s.st.ArrItemByFileID(ev.FileID)
		if err != nil || item == nil {
			return
		}
		scan := func([]arrPollRequest) {
			cl := s.bazarrClient()
			if cl == nil {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := bazarrAction(ctx, cl, item, bazarr.ScanDisk); err != nil {
				log.Printf("bazarr scan-disk %s %d: %v", item.Kind, item.ItemID, err)
			}
		}
		if item.Kind != "sonarr" {
			scan(nil)
			return
		}
		batch.schedule("bazarr:"+strconv.FormatInt(item.ItemID, 10), nil, scan)
	}
}
