package api

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"mediatrans/internal/encode"
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
	}
}

// promoteOne re-probes the row's file, then walks the same three gates
// for both a waiting row past its settle delay and a needs_confirmation
// row being rechecked: hardlinked -> needs_confirmation, the
// recommendation says skip -> dismissed, otherwise -> queued.
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
	if rec.Action == "skip" {
		_ = st.SetIntakeState(row.ID, store.IntakeDismissed, rec.Reason)
		return
	}

	j, err := p.enqueue(f, settings, row.Origin, row.Reason)
	if err != nil {
		log.Printf("intake: enqueue %s: %v", f.Path, err)
		return
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
			p.runIntakePass(s.st)
		}
	}
}

type intakeOut struct {
	store.Intake
	FilePath  string `json:"file_path,omitempty"`
	FileTitle string `json:"file_title,omitempty"`
}

func (s *Server) intakeOut(it store.Intake) intakeOut {
	out := intakeOut{Intake: it}
	if f, err := s.st.GetFile(it.FileID); err == nil && f != nil {
		out.FilePath = f.Path
		out.FileTitle = f.Title
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
