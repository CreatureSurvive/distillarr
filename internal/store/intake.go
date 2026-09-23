package store

import (
	"database/sql"
	"time"
)

// IntakeState is one row's position in the settle/confirm/queue pipeline.
type IntakeState string

const (
	IntakeWaiting           IntakeState = "waiting"
	IntakeNeedsConfirmation IntakeState = "needs_confirmation"
	IntakeQueued            IntakeState = "queued"
	IntakeDismissed         IntakeState = "dismissed"
)

// Intake is one candidate waiting for a settle delay or a human decision
// before it's queued unattended. settings_json is '' to mean "use
// the recommendation at promotion time" — the same nil-means-auto
// convention as encode.Settings elsewhere.
type Intake struct {
	ID           int64       `json:"id"`
	FileID       int64       `json:"file_id"`
	Origin       string      `json:"origin"`
	Reason       string      `json:"reason"`
	SettingsJSON string      `json:"settings_json,omitempty"`
	State        IntakeState `json:"state"`
	NotBefore    string      `json:"not_before,omitempty"`
	HoldReason   string      `json:"hold_reason,omitempty"`
	JobID        int64       `json:"job_id,omitempty"`
	CreatedAt    string      `json:"created_at"`
	UpdatedAt    string      `json:"updated_at"`
}

const intakeCols = `id, file_id, origin, reason, settings_json, state, not_before, hold_reason, job_id, created_at, updated_at`

func scanIntake(row *sql.Row) (*Intake, error) {
	var it Intake
	if err := row.Scan(&it.ID, &it.FileID, &it.Origin, &it.Reason, &it.SettingsJSON,
		&it.State, &it.NotBefore, &it.HoldReason, &it.JobID, &it.CreatedAt, &it.UpdatedAt); err != nil {
		return nil, err
	}
	return &it, nil
}

func scanIntakeRows(rows *sql.Rows) ([]Intake, error) {
	defer rows.Close()
	var out []Intake
	for rows.Next() {
		var it Intake
		if err := rows.Scan(&it.ID, &it.FileID, &it.Origin, &it.Reason, &it.SettingsJSON,
			&it.State, &it.NotBefore, &it.HoldReason, &it.JobID, &it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// UpsertIntake inserts a new waiting row for fileID, or — if one open
// (waiting/needs_confirmation) row already exists for that file — updates
// it in place instead of creating a second one: "at most one open row per
// file, the latest wins".
func (s *Store) UpsertIntake(fileID int64, origin, reason, settingsJSON string, notBefore time.Time) (*Intake, error) {
	now := nowRFC()
	var existingID int64
	err := s.dbW.QueryRow(`SELECT id FROM intake WHERE file_id=? AND state IN ('waiting','needs_confirmation')`, fileID).Scan(&existingID)
	switch {
	case err == nil:
		if _, err := s.dbW.Exec(`UPDATE intake SET origin=?, reason=?, settings_json=?, state=?, not_before=?, hold_reason='', updated_at=? WHERE id=?`,
			origin, reason, settingsJSON, IntakeWaiting, notBefore.UTC().Format(time.RFC3339), now, existingID); err != nil {
			return nil, err
		}
		return s.IntakeByID(existingID)
	case err == sql.ErrNoRows:
		res, err := s.dbW.Exec(`INSERT INTO intake(file_id, origin, reason, settings_json, state, not_before, created_at, updated_at)
			VALUES(?,?,?,?,?,?,?,?)`, fileID, origin, reason, settingsJSON, IntakeWaiting, notBefore.UTC().Format(time.RFC3339), now, now)
		if err != nil {
			return nil, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, err
		}
		return s.IntakeByID(id)
	default:
		return nil, err
	}
}

func (s *Store) IntakeByID(id int64) (*Intake, error) {
	row := s.dbR.QueryRow(`SELECT `+intakeCols+` FROM intake WHERE id=?`, id)
	it, err := scanIntake(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return it, nil
}

// ListIntake returns rows in a state ("" for every state), newest first.
func (s *Store) ListIntake(state string) ([]Intake, error) {
	if state == "" {
		rows, err := s.dbR.Query(`SELECT ` + intakeCols + ` FROM intake ORDER BY id DESC`)
		if err != nil {
			return nil, err
		}
		return scanIntakeRows(rows)
	}
	rows, err := s.dbR.Query(`SELECT `+intakeCols+` FROM intake WHERE state=? ORDER BY id DESC`, state)
	if err != nil {
		return nil, err
	}
	return scanIntakeRows(rows)
}

// DueIntake returns every row the promotion loop should look at this
// pass: waiting rows whose settle delay has elapsed, plus every
// needs_confirmation row (rechecked each pass in case its hold resolved
// on its own, e.g. a hardlink went away).
func (s *Store) DueIntake(now time.Time) ([]Intake, error) {
	rows, err := s.dbR.Query(`SELECT `+intakeCols+` FROM intake
		WHERE (state=? AND not_before<=?) OR state=? ORDER BY id`,
		IntakeWaiting, now.UTC().Format(time.RFC3339), IntakeNeedsConfirmation)
	if err != nil {
		return nil, err
	}
	return scanIntakeRows(rows)
}

// SetIntakeState moves a row to needs_confirmation or dismissed with a
// reason; use SetIntakeQueued for the queued transition, which also
// records the job id.
func (s *Store) SetIntakeState(id int64, state IntakeState, holdReason string) error {
	_, err := s.dbW.Exec(`UPDATE intake SET state=?, hold_reason=?, updated_at=? WHERE id=?`,
		state, holdReason, nowRFC(), id)
	return err
}

func (s *Store) SetIntakeQueued(id, jobID int64) error {
	_, err := s.dbW.Exec(`UPDATE intake SET state=?, job_id=?, hold_reason='', updated_at=? WHERE id=?`,
		IntakeQueued, jobID, nowRFC(), id)
	return err
}

// CountIntakeNeedsConfirmation is a cheap count for the nav badge.
func (s *Store) CountIntakeNeedsConfirmation() (int, error) {
	var n int
	err := s.dbR.QueryRow(`SELECT COUNT(*) FROM intake WHERE state=?`, IntakeNeedsConfirmation).Scan(&n)
	return n, err
}
