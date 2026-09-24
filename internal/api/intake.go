package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"mediatrans/internal/arr"
	"mediatrans/internal/autopilot"
	"mediatrans/internal/encode"
	"mediatrans/internal/issues"
	"mediatrans/internal/notify"
	"mediatrans/internal/recs"
	"mediatrans/internal/store"
)

// intakePromoter runs one settle/confirm/queue pass over due intake rows
// Every side effect is behind a function field so tests can swap
// in a fake clock and fake nlink/recommendation behavior without a real
// file on disk; NewIntakePromoter wires the real ones.
type intakePromoter struct {
	now     func() time.Time
	reprobe func(path string) error
	getFile func(fileID int64) (*store.File, error)
	resolve func(f *store.File, override *encode.Settings) (encode.Settings, recs.Recommendation)
	enqueue func(f *store.File, st encode.Settings, origin, reason string) (*store.Job, error)
	// autopilot returns nil when autopilot is off (the plain
	// recommendation-only behavior below applies); non-nil is the
	// autopilot rule engine's decision for this file (the codec-penalty
	// gate must also be respected).
	autopilot func(f *store.File, rec recs.Recommendation, origin string) *autopilot.Decision
	// quickFix builds the video-copy/remux settings for an autopilot
	// "quick_fix" action.
	quickFix func(f *store.File) encode.Settings
	// forcePrune applies language pruning to settings for an autopilot
	// "queue_override" action whose rule set PruneLanguages,
	// regardless of the global/scoped apply mode. nil (as in tests that
	// don't care about it) means "do nothing".
	forcePrune func(f *store.File, st *encode.Settings)
	// forceSidecar applies a rule's subtitle-sidecar override (// RuleAction.SidecarMode) for a "queue_override" action. nil means
	// "do nothing".
	forceSidecar func(f *store.File, mode string, st *encode.Settings)
	// forceImageSubs applies a rule's image-subtitle override (// RuleAction.ImageSubsMode) for a "queue_override" action. nil means
	// "do nothing".
	forceImageSubs func(f *store.File, mode string, st *encode.Settings)
	// codecPenaltyBlocked reports whether f's settings must be held for
	// confirmation instead of queued: the owning arr instance has
	// an unacknowledged codec penalty and settings is a real HEVC/AV1
	// re-encode (not a remux). Never gates approveIntakeRow — that's an
	// explicit human decision, same as manual queueing elsewhere.
	codecPenaltyBlocked func(f *store.File, settings encode.Settings) (blocked bool, instanceName string)
	// estimateSeconds predicts wall-clock encode time from speed history,
	// used only to rank autopilot-origin jobs by value per GPU-second
	// wired to jobs.Engine.EstimateSeconds.
	estimateSeconds func(f *store.File, settings encode.Settings) (secs float64, estimated bool)
	// pressure reports whether disk-pressure mode is currently on
	// used to bias autopilotPriority toward quick fixes. nil
	// (as in every older test that builds this struct by literal)
	// means "not under pressure".
	pressure func() bool
}

func (s *Server) newIntakePromoter() *intakePromoter {
	return &intakePromoter{
		now:     time.Now,
		reprobe: s.scan.ProbeSingle,
		getFile: s.st.GetFile,
		resolve: s.resolve,
		enqueue: func(f *store.File, st encode.Settings, origin, reason string) (*store.Job, error) {
			return s.enqueue(f, st, false, origin, reason)
		},
		autopilot: s.autopilotDecision,
		quickFix: func(f *store.File) encode.Settings {
			cfg := s.cfg.Get()
			st := issues.QuickFix(f, cfg)
			applyLangPrune(f, cfg, &st)
			applySidecar(f, cfg, &st)
			applyImageSubs(f, cfg, &st)
			return st
		},
		forcePrune: func(f *store.File, st *encode.Settings) {
			forceLangPrune(f, s.cfg.Get(), st)
		},
		forceSidecar: func(f *store.File, mode string, st *encode.Settings) {
			forceSidecarMode(f, s.cfg.Get(), mode, st)
		},
		forceImageSubs: func(f *store.File, mode string, st *encode.Settings) {
			forceImageSubsMode(f, s.cfg.Get(), mode, st)
		},
		codecPenaltyBlocked: s.codecPenaltyBlocked,
		estimateSeconds:     s.eng.EstimateSeconds,
		pressure:            s.diskPressure.Load,
	}
}

// codecPenaltyBlocked implements the codec-penalty gate: a real HEVC/AV1
// re-encode (VideoCopy=false — a remux is never gated) for a file whose
// owning arr instance has a stored codec-penalty report with at least
// one finding and hasn't acknowledged it (PenaltyAck==false) must not be
// queued unattended.
func (s *Server) codecPenaltyBlocked(f *store.File, settings encode.Settings) (bool, string) {
	if settings.VideoCopy || (settings.Codec != "hevc" && settings.Codec != "av1") {
		return false, ""
	}
	item, err := s.st.ArrItemByFileID(f.ID)
	if err != nil || item == nil {
		return false, ""
	}
	inst := s.arrInstanceByID(item.InstanceID)
	if inst == nil || inst.PenaltyAck {
		return false, ""
	}
	v, ok, _ := s.st.KVGet("arr_penalties_" + inst.ID)
	if !ok {
		return false, ""
	}
	var report arr.PenaltyReport
	if err := json.Unmarshal([]byte(v), &report); err != nil || len(report.Penalties) == 0 {
		return false, ""
	}
	return true, inst.Name
}

// autopilotDecision returns nil when autopilot is off; otherwise
// resolves the file's arr policy and tag names (autopilot.Evaluate is
// pure and DB-free, so that resolution has to happen here) and runs the
// rule engine.
func (s *Server) autopilotDecision(f *store.File, rec recs.Recommendation, origin string) *autopilot.Decision {
	cfg := s.cfg.Get()
	if !cfg.AutopilotEnabled {
		return nil
	}
	policy := recs.ArrPolicy(f.ID)
	ctx := autopilot.Context{Origin: origin, TagNames: s.arrTagNamesFor(f.ID)}
	d := autopilot.Evaluate(f, rec, policy, ctx, cfg.AutoRules, cfg.MinSavingsPct)
	return &d
}

// arrTagNamesFor resolves a file's owning arr instance's tags (if any)
// to their names, the form autopilot.Context.Tags matches against.
func (s *Server) arrTagNamesFor(fileID int64) []string {
	item, err := s.st.ArrItemByFileID(fileID)
	if err != nil || item == nil {
		return nil
	}
	names := s.st.ArrTags(item.InstanceID)
	var out []string
	for _, id := range item.TagIDs() {
		if n, ok := names[id]; ok {
			out = append(out, n)
		}
	}
	return out
}

// promoteOne re-probes the row's file, then walks the same gates for
// both a waiting row past its settle delay and a needs_confirmation row
// being rechecked: file gone -> dismissed; hardlinked -> needs_confirmation;
// otherwise autopilot (if on) or the plain recommendation decides
// queue / queue_override / quick_fix / ignore.
func (p *intakePromoter) promoteOne(st *store.Store, row store.Intake) {
	f, err := p.getFile(row.FileID)
	if err != nil || f == nil {
		_ = st.SetIntakeState(row.ID, store.IntakeDismissed, "file no longer exists")
		return
	}
	if err := p.reprobe(f.Path); err != nil {
		log.Printf("intake: reprobe %s: %v", f.Path, err)
		return // leave it for the next pass rather than guessing at stale data
	}
	f, err = p.getFile(row.FileID)
	if err != nil || f == nil {
		_ = st.SetIntakeState(row.ID, store.IntakeDismissed, "file no longer exists")
		return
	}
	if f.Nlink > 1 {
		_ = st.SetIntakeState(row.ID, store.IntakeNeedsConfirmation, "hardlinked")
		return
	}

	var override *encode.Settings
	if row.SettingsJSON != "" {
		var ov encode.Settings
		if err := json.Unmarshal([]byte(row.SettingsJSON), &ov); err == nil {
			override = &ov
		}
	}
	settings, rec := p.resolve(f, override)

	if p.autopilot != nil {
		if d := p.autopilot(f, rec, row.Origin); d != nil {
			switch d.Action {
			case "ignore":
				_ = st.SetIntakeState(row.ID, store.IntakeDismissed, d.Reason)
				return
			case "quick_fix":
				settings = p.quickFix(f)
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
				if d.PruneLanguages && p.forcePrune != nil {
					p.forcePrune(f, &settings)
				}
				if d.SidecarMode != "" && p.forceSidecar != nil {
					p.forceSidecar(f, d.SidecarMode, &settings)
				}
				if d.ImageSubsMode != "" && p.forceImageSubs != nil {
					p.forceImageSubs(f, d.ImageSubsMode, &settings)
				}
			}
			// The rule engine decided this, not whatever put the row in
			// intake, so the job says so — also what MoveJob keys
			// off to keep autopilot's ordering out of manual reordering.
			priority := 0
			if p.estimateSeconds != nil {
				secs, _ := p.estimateSeconds(f, settings)
				pressure := p.pressure != nil && p.pressure()
				priority = autopilotPriority(f.Size-rec.EstOut, secs, d.Action == "quick_fix", pressure)
			}
			p.enqueueNow(st, row, f, settings, "autopilot", priority)
			return
		}
	}

	if rec.Action == "skip" {
		_ = st.SetIntakeState(row.ID, store.IntakeDismissed, rec.Reason)
		return
	}
	p.enqueueNow(st, row, f, settings, row.Origin, 0)
}

const (
	autopilotPriorityBase = 100000 // manual jobs' flat default (see Server.enqueue)
	autopilotPriorityMax  = 199999
)

// autopilotPriority ranks an autopilot-origin job by estimated value per
// GPU-second (bytes saved / estimated encode seconds): within autopilot's
// own share of the queue, the best value gets the priority number closest
// to autopilotPriorityBase, while every autopilot job still sorts after
// manual's flat 100000 default. 0 means "no override" — callers
// treat it as "leave CreateJob's default alone".
//
// Under disk pressure, quickFix jobs are confined to the front
// half of the range and everything else to the back half: a remux
// frees space in seconds, so it beats a multi-hour re-encode on space
// freed *now*, regardless of value per GPU-second. Outside pressure,
// quickFix is ignored and the full range is used exactly as before.
func autopilotPriority(savedBytes int64, secs float64, quickFix, pressure bool) int {
	lo, hi := autopilotPriorityBase, autopilotPriorityMax
	if pressure {
		mid := lo + (hi-lo)/2
		if quickFix {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	if savedBytes <= 0 || secs <= 0 {
		return hi
	}
	p := lo + int(1e9/(float64(savedBytes)/secs))
	if p <= lo {
		p = lo + 1
	}
	if p > hi {
		p = hi
	}
	return p
}

func (p *intakePromoter) enqueueNow(st *store.Store, row store.Intake, f *store.File, settings encode.Settings, origin string, priority int) {
	if p.codecPenaltyBlocked != nil {
		if blocked, _ := p.codecPenaltyBlocked(f, settings); blocked {
			_ = st.SetIntakeState(row.ID, store.IntakeNeedsConfirmation, "codec_penalty")
			return
		}
	}
	j, err := p.enqueue(f, settings, origin, row.Reason)
	if err != nil {
		log.Printf("intake: enqueue %s: %v", f.Path, err)
		return
	}
	if priority > 0 {
		_ = st.SetJobPriority(j.ID, priority)
	}
	_ = st.SetIntakeQueued(row.ID, j.ID)
}

// runIntakePass processes every currently-due row once.
func (p *intakePromoter) runIntakePass(st *store.Store) {
	due, err := st.DueIntake(p.now())
	if err != nil {
		log.Printf("intake: list due: %v", err)
		return
	}
	for _, row := range due {
		p.promoteOne(st, row)
	}
}

// IntakeLoop runs the promotion pass once a minute until stop is closed.
// A minute is frequent enough for a settle delay measured in tens of
// minutes, and cheap: a pass over zero or a handful of due rows is a
// handful of SELECTs.
func (s *Server) IntakeLoop(stop <-chan struct{}) {
	p := s.newIntakePromoter()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			before, _ := s.st.CountIntakeNeedsConfirmation()
			p.runIntakePass(s.st)
			if after, _ := s.st.CountIntakeNeedsConfirmation(); after > before {
				s.Notify.Send(notify.Event{Key: notify.NeedsConfirmation, Level: notify.Warning,
					Title: fmt.Sprintf("%d file(s) need confirmation", after-before),
					Body:  fmt.Sprintf("%d waiting in total in the Queue page's intake panel (hardlinked files or codec-penalty checks).", after),
					Link:  "#/queue", Group: "needs-confirmation updates"})
			}
		}
	}
}

type intakeOut struct {
	store.Intake
	FilePath     string `json:"file_path,omitempty"`
	FileTitle    string `json:"file_title,omitempty"`
	InstanceID   string `json:"instance_id,omitempty"`
	InstanceName string `json:"instance_name,omitempty"`
}

func (s *Server) intakeOut(it store.Intake) intakeOut {
	out := intakeOut{Intake: it}
	if f, err := s.st.GetFile(it.FileID); err == nil && f != nil {
		out.FilePath = f.Path
		out.FileTitle = f.Title
	}
	if item, err := s.st.ArrItemByFileID(it.FileID); err == nil && item != nil {
		out.InstanceID = item.InstanceID
		if inst := s.arrInstanceByID(item.InstanceID); inst != nil {
			out.InstanceName = inst.Name
		}
	}
	return out
}

// GET /api/v1/intake?state= — "" lists every state.
func (s *Server) intakeList(w http.ResponseWriter, r *http.Request) {
	rows, err := s.st.ListIntake(r.URL.Query().Get("state"))
	if err != nil {
		fail(w, 500, err)
		return
	}
	out := make([]intakeOut, len(rows))
	for i, it := range rows {
		out[i] = s.intakeOut(it)
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": out})
}

// approveIntakeRow enqueues row regardless of a hardlink hold (the user
// just confirmed it), but still respects a genuine skip recommendation —
// approving a stale hold shouldn't force a re-encode Sonarr/Radarr no
// longer wants.
func (s *Server) approveIntakeRow(id int64) error {
	row, err := s.st.IntakeByID(id)
	if err != nil {
		return err
	}
	if row == nil {
		return fmt.Errorf("no intake row %d", id)
	}
	f, err := s.st.GetFile(row.FileID)
	if err != nil || f == nil {
		_ = s.st.SetIntakeState(id, store.IntakeDismissed, "file no longer exists")
		return fmt.Errorf("file not found")
	}
	var override *encode.Settings
	if row.SettingsJSON != "" {
		var ov encode.Settings
		if err := json.Unmarshal([]byte(row.SettingsJSON), &ov); err == nil {
			override = &ov
		}
	}
	settings, rec := s.resolve(f, override)
	if rec.Action == "skip" {
		_ = s.st.SetIntakeState(id, store.IntakeDismissed, rec.Reason)
		return fmt.Errorf("no longer recommended: %s", rec.Reason)
	}
	settings.ConfirmedHardlinked = true
	j, err := s.enqueue(f, settings, false, row.Origin, row.Reason)
	if err != nil {
		return err
	}
	return s.st.SetIntakeQueued(id, j.ID)
}

func (s *Server) intakeApprove(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if err := s.approveIntakeRow(id); err != nil {
		fail(w, http.StatusConflict, err)
		return
	}
	s.eng.Kick()
	s.hub.Broadcast("intake", map[string]any{"id": id, "state": store.IntakeQueued})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) intakeDismiss(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if err := s.st.SetIntakeState(id, store.IntakeDismissed, "dismissed by user"); err != nil {
		fail(w, 500, err)
		return
	}
	s.hub.Broadcast("intake", map[string]any{"id": id, "state": store.IntakeDismissed})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type intakeBulkReq struct {
	IDs []int64 `json:"ids"`
}

func (s *Server) intakeApproveBulk(w http.ResponseWriter, r *http.Request) {
	var req intakeBulkReq
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err)
		return
	}
	ok, failed := 0, 0
	for _, id := range req.IDs {
		if err := s.approveIntakeRow(id); err != nil {
			failed++
			continue
		}
		ok++
	}
	s.eng.Kick()
	s.hub.Broadcast("intake", map[string]any{"bulk": "approve"})
	writeJSON(w, http.StatusOK, map[string]any{"approved": ok, "failed": failed})
}

func (s *Server) intakeDismissBulk(w http.ResponseWriter, r *http.Request) {
	var req intakeBulkReq
	if err := readJSON(r, &req); err != nil {
		fail(w, 400, err)
		return
	}
	ok := 0
	for _, id := range req.IDs {
		if err := s.st.SetIntakeState(id, store.IntakeDismissed, "dismissed by user"); err == nil {
			ok++
		}
	}
	s.hub.Broadcast("intake", map[string]any{"bulk": "dismiss"})
	writeJSON(w, http.StatusOK, map[string]any{"dismissed": ok})
}
