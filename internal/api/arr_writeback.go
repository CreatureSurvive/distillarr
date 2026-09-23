package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"mediatrans/internal/arr"
	"mediatrans/internal/jobs"
)

// ArrWriteBackSubscriber returns a jobs.OnFinished subscriber for // optional write-back: on a successful encode/remux/upscale-replace,
// apply the configured tag to the owning series/movie (creating it on
// the instance first if needed) and/or unmonitor the specific
// episode/movie. Both are per-instance toggles, off by default — this is
// a no-op for every instance until the user opts in. Unlike
// ArrRescanSubscriber, upscale-copy is excluded: nothing was replaced,
// so there's nothing yet to mark as done.
func (s *Server) ArrWriteBackSubscriber() func(jobs.ReplacedEvent) {
	return func(ev jobs.ReplacedEvent) {
		switch ev.Kind {
		case "encode", "remux", "upscale-replace":
		default:
			return
		}
		item, err := s.st.ArrItemByFileID(ev.FileID)
		if err != nil || item == nil {
			return
		}
		inst := s.arrInstanceByID(item.InstanceID)
		if inst == nil || (!inst.TagAfterReencodeOn() && !inst.UnmonitorAfterReencodeOn()) {
			return
		}

		cl := arr.New(inst.URL, inst.APIKey, arrKind(inst.Kind))
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		if inst.TagAfterReencodeOn() && strings.TrimSpace(inst.ReencodeTag) != "" {
			if err := tagArrItem(ctx, cl, arrKind(inst.Kind), item.ItemID, inst.ReencodeTag); err != nil {
				log.Printf("arr write-back %s: tag: %v", inst.ID, err)
			}
		}
		if inst.UnmonitorAfterReencodeOn() {
			if err := unmonitorArrItem(ctx, cl, arrKind(inst.Kind), item.ItemID, item.FileRecID); err != nil {
				log.Printf("arr write-back %s: unmonitor: %v", inst.ID, err)
			}
		}
	}
}

func tagArrItem(ctx context.Context, cl *arr.Client, kind arr.Kind, itemID int64, tagName string) error {
	tagID, err := cl.EnsureTag(ctx, tagName)
	if err != nil {
		return fmt.Errorf("ensure tag %q: %w", tagName, err)
	}
	if kind == arr.Sonarr {
		return cl.TagSeries(ctx, itemID, tagID)
	}
	return cl.TagMovie(ctx, itemID, tagID)
}

// POST /api/v1/arr/{id}/rename-tag — Settings' "Rename the existing tag
// in Sonarr/Radarr too" offer, when the user edits reencode_tag. This is
// a live write, but only ever runs from that explicit click, never
// automatically. old_name is matched case-insensitively; nothing happens
// if no such tag exists yet on the instance (there's nothing to rename).
func (s *Server) arrRenameTag(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OldName string `json:"old_name"`
		NewName string `json:"new_name"`
	}
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err)
		return
	}
	req.NewName = strings.TrimSpace(req.NewName)
	if strings.TrimSpace(req.OldName) == "" || req.NewName == "" {
		fail(w, 400, fmt.Errorf("old_name and new_name are required"))
		return
	}
	inst := s.arrInstanceByID(r.PathValue("id"))
	if inst == nil {
		fail(w, 404, fmt.Errorf("no instance %q", r.PathValue("id")))
		return
	}
	cl := arr.New(inst.URL, inst.APIKey, arrKind(inst.Kind))
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	tags, err := cl.Tags(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": arr.FriendlyError(err)})
		return
	}
	for _, t := range tags {
		if strings.EqualFold(t.Label, req.OldName) {
			if err := cl.RenameTag(ctx, t.ID, req.NewName); err != nil {
				writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": arr.FriendlyError(err)})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "renamed": true})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "renamed": false})
}

// unmonitorArrItem unmonitors the specific episode/movie, not the whole
// series — for Sonarr that means finding the episode whose episode file
// id matches fileRecID first, since arr_items only tracks the series id.
func unmonitorArrItem(ctx context.Context, cl *arr.Client, kind arr.Kind, itemID, fileRecID int64) error {
	if kind != arr.Sonarr {
		return cl.UnmonitorMovie(ctx, itemID)
	}
	episodes, err := cl.Episodes(ctx, itemID)
	if err != nil {
		return fmt.Errorf("list episodes: %w", err)
	}
	for _, e := range episodes {
		if e.EpisodeFileID == fileRecID {
			return cl.UnmonitorEpisode(ctx, e.ID)
		}
	}
	return fmt.Errorf("no episode found with file id %d", fileRecID)
}
