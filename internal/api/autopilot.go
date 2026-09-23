package api

import (
	"encoding/json"
	"net/http"

	"mediatrans/internal/autopilot"
	"mediatrans/internal/recs"
	"mediatrans/internal/store"
)

const autopilotPreviewSampleSize = 8

type autopilotPreviewFile struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Path  string `json:"path"`
}

// autopilotPreviewGroup is one (rule, action) bucket: every file the
// dry run would hand that rule's decision to.
type autopilotPreviewGroup struct {
	RuleID     string                 `json:"rule_id,omitempty"` // "" is the built-in default
	Rule       string                 `json:"rule,omitempty"`
	Action     string                 `json:"action"`
	Count      int                    `json:"count"`
	EstSavedGB float64                `json:"est_saved_gb"`
	Sample     []autopilotPreviewFile `json:"sample"`
}

// GET /api/v1/autopilot/preview — a dry run: evaluates every cached
// "worth transcoding" recommendation against the current rules (whether
// autopilot is on or off — this previews what turning it on, or editing
// the rules, would do) and groups the results per rule, without
// queueing anything. Uses each file's already-cached rec_json instead
// of recomputing recs.Recommend, so a full-library preview is cheap.
func (s *Server) autopilotPreview(w http.ResponseWriter, r *http.Request) {
	cfg := s.cfg.Get()
	groups := map[string]*autopilotPreviewGroup{}
	var order []string

	err := s.st.EachFile(func(f *store.File) error {
		if f.RecJSON == "" {
			return nil
		}
		var rec recs.Recommendation
		if json.Unmarshal([]byte(f.RecJSON), &rec) != nil {
			return nil
		}
		if rec.Action != "transcode" {
			return nil
		}
		policy := recs.ArrPolicy(f.ID)
		ctx := autopilot.Context{TagNames: s.arrTagNamesFor(f.ID)}
		d := autopilot.Evaluate(f, rec, policy, ctx, cfg.AutoRules, cfg.MinSavingsPct)

		key := d.RuleID + "\x00" + d.Action
		g, ok := groups[key]
		if !ok {
			g = &autopilotPreviewGroup{RuleID: d.RuleID, Rule: d.Rule, Action: d.Action}
			groups[key] = g
			order = append(order, key)
		}
		g.Count++
		if saved := f.Size - rec.EstOut; saved > 0 {
			g.EstSavedGB += float64(saved) / (1 << 30)
		}
		if len(g.Sample) < autopilotPreviewSampleSize {
			g.Sample = append(g.Sample, autopilotPreviewFile{ID: f.ID, Title: f.Title, Path: f.Path})
		}
		return nil
	})
	if err != nil {
		fail(w, 500, err)
		return
	}

	out := make([]*autopilotPreviewGroup, 0, len(order))
	for _, k := range order {
		out = append(out, groups[k])
	}
	writeJSON(w, http.StatusOK, map[string]any{"groups": out})
}
