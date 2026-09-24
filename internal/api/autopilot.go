package api

import (
	"encoding/json"
	"net/http"
	"sort"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/autopilot"
	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/issues"
	"github.com/CreatureSurvive/distillarr/internal/recs"
	"github.com/CreatureSurvive/distillarr/internal/store"
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

// autopilotBacklogFile is one file the backlog run picked (or would pick).
type autopilotBacklogFile struct {
	FileID     int64   `json:"file_id"`
	Title      string  `json:"title"`
	Path       string  `json:"path"`
	RuleID     string  `json:"rule_id,omitempty"`
	Rule       string  `json:"rule,omitempty"`
	Action     string  `json:"action"`
	EstSeconds float64 `json:"est_seconds"`
	EstSavedGB float64 `json:"est_saved_gb"`
}

func (c autopilotBacklogFile) valuePerSec() float64 {
	if c.EstSeconds <= 0 {
		return 0
	}
	return c.EstSavedGB / c.EstSeconds
}

// POST /api/v1/autopilot/backlog?dry_run=true (the default) — evaluates
// the whole library against the current rules like autopilotPreview, but
// sorts by estimated value per GPU-second and greedily selects files
// until the configured budget (0 = unlimited) runs out, the same way one
// processing window's autopilot spend should be prioritized. A
// dry run reports the selection without creating anything; dry_run=false
// opens one intake row per selected file (origin "autopilot", due now),
// so the existing intake promotion pass re-checks hardlinks and re-runs
// the rule engine fresh at the moment it actually queues — this handler
// never enqueues a job directly.
func (s *Server) autopilotBacklog(w http.ResponseWriter, r *http.Request) {
	dryRun := r.URL.Query().Get("dry_run") != "false"
	cfg := s.cfg.Get()
	pressure := s.diskPressure.Load()
	mult := 1.0
	if pressure {
		mult = cfg.PressureBudgetX()
	}
	budgetBytes := int64(cfg.AutopilotBudgetGB * mult * (1 << 30))
	budgetSecs := cfg.AutopilotBudgetHours * mult * 3600

	var candidates []autopilotBacklogFile
	err := s.st.EachFile(func(f *store.File) error {
		if f.RecJSON == "" || f.Missing {
			return nil
		}
		var rec recs.Recommendation
		if json.Unmarshal([]byte(f.RecJSON), &rec) != nil || rec.Action != "transcode" {
			return nil
		}
		if queued, _ := s.st.HasQueuedForFile(f.Path); queued {
			return nil
		}
		policy := recs.ArrPolicy(f.ID)
		ctx := autopilot.Context{Origin: "autopilot", TagNames: s.arrTagNamesFor(f.ID)}
		d := autopilot.Evaluate(f, rec, policy, ctx, cfg.AutoRules, cfg.MinSavingsPct)
		if d.Action == "ignore" {
			return nil
		}
		settings := rec.Settings
		switch d.Action {
		case "quick_fix":
			settings = issues.QuickFix(f, cfg)
			applyLangPrune(f, cfg, &settings)
			applySidecar(f, cfg, &settings)
			applyImageSubs(f, cfg, &settings)
		case "queue_override":
			if d.Codec != "" {
				settings.Codec = encode.Codec(d.Codec)
			}
			if d.Quality > 0 {
				settings.Quality = d.Quality
			}
			if d.AudioRules != nil {
				settings.AudioRules = d.AudioRules
			}
			if d.PruneLanguages {
				forceLangPrune(f, cfg, &settings)
			}
			if d.SidecarMode != "" {
				forceSidecarMode(f, cfg, d.SidecarMode, &settings)
			}
			if d.ImageSubsMode != "" {
				forceImageSubsMode(f, cfg, d.ImageSubsMode, &settings)
			}
		}
		if blocked, _ := s.codecPenaltyBlocked(f, settings); blocked {
			return nil // would just land in needs_confirmation, not real backlog work
		}
		secs, _ := s.eng.EstimateSeconds(f, settings)
		saved := f.Size - rec.EstOut
		candidates = append(candidates, autopilotBacklogFile{
			FileID: f.ID, Title: f.Title, Path: f.Path,
			RuleID: d.RuleID, Rule: d.Rule, Action: d.Action,
			EstSeconds: secs, EstSavedGB: float64(saved) / (1 << 30),
		})
		return nil
	})
	if err != nil {
		fail(w, 500, err)
		return
	}

	// Under disk pressure, quick fixes (remuxes, done in seconds) sort
	// ahead of everything else regardless of value per GPU-second — see
	// — with value per GPU-second still
	// deciding order within each group.
	sort.SliceStable(candidates, func(i, j int) bool {
		if pressure {
			qi := candidates[i].Action == "quick_fix"
			qj := candidates[j].Action == "quick_fix"
			if qi != qj {
				return qi
			}
		}
		return candidates[i].valuePerSec() > candidates[j].valuePerSec()
	})

	var picked []autopilotBacklogFile
	var spentBytes int64
	var spentSecs float64
	for _, c := range candidates {
		saved := int64(c.EstSavedGB * (1 << 30))
		if budgetBytes > 0 && spentBytes+saved > budgetBytes {
			break
		}
		if budgetSecs > 0 && spentSecs+c.EstSeconds > budgetSecs {
			break
		}
		picked = append(picked, c)
		spentBytes += saved
		spentSecs += c.EstSeconds
	}

	queued := 0
	if !dryRun {
		now := time.Now()
		for _, c := range picked {
			reason := "Backlog: built-in default"
			if c.Rule != "" {
				reason = "Backlog: rule '" + c.Rule + "'"
			}
			if _, err := s.st.UpsertIntake(c.FileID, "autopilot", reason, "", now); err == nil {
				queued++
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"dry_run":           dryRun,
		"budget_gb":         cfg.AutopilotBudgetGB,
		"budget_hours":      cfg.AutopilotBudgetHours,
		"disk_pressure":     pressure,
		"budget_multiplier": mult,
		"total_candidates":  len(candidates),
		"selected_count":    len(picked),
		"est_saved_gb":      float64(spentBytes) / (1 << 30),
		"est_hours":         spentSecs / 3600,
		"queued_intake":     queued,
		"selected":          picked,
	})
}
