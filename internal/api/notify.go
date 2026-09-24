// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/notify"
)

// notifierOut masks a target's URL (it carries the service's token).
type notifierOut struct {
	config.Notifier
	URL     string `json:"url"`
	URLSet  bool   `json:"url_set"`
	Service string `json:"service"` // URL scheme, for the UI's icon/help link
}

func notifierOutOf(n config.Notifier) notifierOut {
	return notifierOut{Notifier: n, URLSet: n.URL != "", Service: notify.Scheme(n.URL)}
}

// mergeNotifiers applies a PUT's notifiers list: the list replaces the
// stored one wholesale (an id left out is removed), a blank URL on an
// existing id keeps the saved one, and new targets get an id.
func mergeNotifiers(stored []config.Notifier, patch json.RawMessage) ([]config.Notifier, error) {
	var incoming []config.Notifier
	if err := json.Unmarshal(patch, &incoming); err != nil {
		return nil, fmt.Errorf("notifiers: %w", err)
	}
	byID := make(map[string]config.Notifier, len(stored))
	for _, n := range stored {
		byID[n.ID] = n
	}
	used := map[string]bool{}
	out := make([]config.Notifier, 0, len(incoming))
	for _, n := range incoming {
		if old, ok := byID[n.ID]; ok && n.ID != "" {
			if strings.TrimSpace(n.URL) == "" {
				n.URL = old.URL
			}
		} else {
			base := slugify(n.Name)
			if base == "" {
				base = "target"
			}
			id := base
			for used[id] || byID[id].ID != "" {
				id = base + "-" + randomSuffix()
			}
			n.ID = id
		}
		switch n.MinLevel {
		case "", notify.Info, notify.Warning, notify.Error:
		default:
			return nil, fmt.Errorf("%s: unknown minimum level %q", n.Name, n.MinLevel)
		}
		if n.QuietStart < 0 || n.QuietStart >= 1440 || n.QuietEnd < 0 || n.QuietEnd >= 1440 {
			return nil, fmt.Errorf("%s: quiet hours must be minutes within a day", n.Name)
		}
		used[n.ID] = true
		out = append(out, n)
	}
	return out, nil
}

// notifyTest sends a test message to a saved target ({id}) or, with a
// url in the body, to an unsaved one ({id} = "new" or a saved id whose
// URL is being edited).
func (s *Server) notifyTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL string `json:"url"`
	}
	_ = readJSON(r, &req)
	id := r.PathValue("id")
	url := strings.TrimSpace(req.URL)
	saved := false
	if url == "" {
		for _, n := range s.cfg.Get().Notifiers {
			if n.ID == id {
				url, saved = n.URL, true
			}
		}
	}
	if url == "" {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": "Enter the target URL."})
		return
	}
	svc := s.Notify
	if svc == nil {
		svc = notify.New(s.cfg.Get, notify.Shoutrrr)
	}
	recordID := ""
	if saved {
		recordID = id
	}
	if err := svc.Test(recordID, url); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// notifyStatus returns each target's last delivery outcome.
func (s *Server) notifyStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Notify.Statuses())
}
