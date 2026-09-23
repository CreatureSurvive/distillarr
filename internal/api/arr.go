package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"mediatrans/internal/arr"
	"mediatrans/internal/config"
	"mediatrans/internal/pathmap"
)

// arrInstanceOut is an ArrInstance as returned by the API: the key is
// never sent back, matching the Jellyfin key's masking convention.
type arrInstanceOut struct {
	config.ArrInstance
	APIKey    string `json:"api_key"`
	APIKeySet bool   `json:"api_key_set"`
}

func arrOut(inst config.ArrInstance) arrInstanceOut {
	keySet := inst.APIKey != ""
	inst.APIKey = ""
	return arrInstanceOut{ArrInstance: inst, APIKeySet: keySet}
}

var slugNonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slugify turns a display name into a lowercase, hyphenated id fragment.
// A stable id is needed (not the name itself) because webhook URLs and
// arr_items rows key on it, and the name can be edited later.
func slugify(name string) string {
	s := slugNonAlnum.ReplaceAllString(strings.ToLower(name), "-")
	s = strings.Trim(s, "-")
	if s == "" {
		s = "instance"
	}
	return s
}

func randomSuffix() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// mergeArrInstances resolves a PUT /config patch's arr_instances against
// the currently stored list. The patch is the complete desired list (an
// entry left out is removed, matching the rest of putConfig's
// object-replacement semantics for this field): each entry is matched by
// id, a blank api_key on an existing id keeps the stored key, and an
// entry with no id is new and gets a slug of its name (deduplicated
// against every id already in use, stored or freshly assigned).
func mergeArrInstances(stored []config.ArrInstance, patch json.RawMessage) ([]config.ArrInstance, error) {
	var incoming []config.ArrInstance
	if err := json.Unmarshal(patch, &incoming); err != nil {
		return nil, fmt.Errorf("arr_instances: %w", err)
	}
	byID := make(map[string]config.ArrInstance, len(stored))
	for _, s := range stored {
		byID[s.ID] = s
	}
	used := make(map[string]bool, len(incoming))
	out := make([]config.ArrInstance, 0, len(incoming))
	for _, inst := range incoming {
		if inst.ID == "" {
			base := slugify(inst.Name)
			id := base
			for used[id] || byID[id].ID != "" {
				id = base + "-" + randomSuffix()
			}
			inst.ID = id
		} else if old, ok := byID[inst.ID]; ok {
			if strings.TrimSpace(inst.APIKey) == "" {
				inst.APIKey = old.APIKey
			}
			// PenaltyAck has its own endpoint (arrAck): a plain settings
			// save can never flip it, in either direction — a bool has
			// no "not sent" state to distinguish from false.
			inst.PenaltyAck = old.PenaltyAck
		}
		if inst.TagAfterReencodeOn() && strings.TrimSpace(inst.ReencodeTag) == "" {
			return nil, fmt.Errorf("%s: enabling tag-after-reencode needs a tag name", inst.Name)
		}
		used[inst.ID] = true
		out = append(out, inst)
	}
	return out, nil
}

// arrKind maps the stored string to the client's Kind, defaulting to
// Radarr only because Kind must be something; callers always have a
// real value from either the stored instance or the request body.
func arrKind(s string) arr.Kind {
	if s == "sonarr" {
		return arr.Sonarr
	}
	return arr.Radarr
}

type rootFolderOut struct {
	Path      string `json:"path"`
	Mapped    string `json:"mapped"`
	Reachable bool   `json:"reachable"`
}

// arrTest checks connectivity and the path map for one instance, saved
// or not: {id} names a stored instance to fall back to for any field
// left blank in the body (so testing a saved instance doesn't require
// re-entering its key), or "new" for one that hasn't been saved yet.
// Nothing is persisted here — Settings saves through PUT /config as usual.
func (s *Server) arrTest(w http.ResponseWriter, r *http.Request) {
	var req config.ArrInstance
	_ = readJSON(r, &req)

	cfg := s.cfg.Get()
	id := r.PathValue("id")
	for _, inst := range cfg.ArrInstances {
		if inst.ID != id {
			continue
		}
		if req.URL == "" {
			req.URL = inst.URL
		}
		if req.APIKey == "" {
			req.APIKey = inst.APIKey
		}
		if req.PathMap == "" {
			req.PathMap = inst.PathMap
		}
		if req.Kind == "" {
			req.Kind = inst.Kind
		}
		break
	}
	if req.URL == "" || req.APIKey == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Enter the server URL and an API key."})
		return
	}

	cl := arr.New(req.URL, req.APIKey, arrKind(req.Kind))
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	st, err := cl.Test(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": arr.FriendlyError(err)})
		return
	}

	pm, _ := pathmap.Parse(req.PathMap)
	var roots []rootFolderOut
	if folders, err := cl.RootFolders(ctx); err == nil {
		for _, f := range folders {
			mapped := pm.ToLocal(f.Path)
			_, statErr := os.Stat(mapped)
			roots = append(roots, rootFolderOut{Path: f.Path, Mapped: mapped, Reachable: statErr == nil})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "version": st.Version, "app_name": st.AppName, "root_folders": roots,
	})
}

// GET /api/v1/arr/{id} — an instance's codec-penalty report from
// its last successful sync, and whether the user has acknowledged it.
func (s *Server) arrGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	inst := s.arrInstanceByID(id)
	if inst == nil {
		fail(w, 404, fmt.Errorf("no instance %q", id))
		return
	}
	var report arr.PenaltyReport
	if v, ok, _ := s.st.KVGet("arr_penalties_" + id); ok {
		_ = json.Unmarshal([]byte(v), &report)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "penalties": report, "penalty_ack": inst.PenaltyAck,
	})
}

// POST /api/v1/arr/{id}/ack — acknowledge this instance's codec-penalty
// warning. Its own endpoint, deliberately outside the general config
// save path (see mergeArrInstances): a plain settings save can never
// flip a bool that has no "not sent" state.
func (s *Server) arrAck(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	found := false
	err := s.cfg.Update(func(c *config.Config) {
		for i := range c.ArrInstances {
			if c.ArrInstances[i].ID == id {
				c.ArrInstances[i].PenaltyAck = true
				found = true
				break
			}
		}
	})
	if err != nil {
		fail(w, 500, err)
		return
	}
	if !found {
		fail(w, 404, fmt.Errorf("no instance %q", id))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
