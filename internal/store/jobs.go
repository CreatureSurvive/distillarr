package store

import (
	"database/sql"
	"fmt"
)

// Job statuses.
const (
	StatusQueued    = "queued"
	StatusRunning   = "running"
	StatusVerifying = "verifying"
	StatusReplacing = "replacing"
	StatusDone      = "done"
	StatusFailed    = "failed"
	StatusCanceled  = "canceled"
)

// Job is one transcode queue entry.
type Job struct {
	ID          int64  `json:"id"`
	FileID      int64  `json:"file_id"`
	SrcPath     string `json:"src_path"`
	TempPath    string `json:"temp_path"`
	Status      string `json:"status"`
	Priority    int    `json:"priority"`
	RunNow      bool   `json:"run_now"`
	Backend     string `json:"backend"`
	Codec       string `json:"codec"`
	Quality     int    `json:"quality"`
	SettingsJSON string `json:"settings_json"`
	Attempts    int    `json:"attempts"`
	MaxAttempts int    `json:"max_attempts"`
	Error       string `json:"error,omitempty"`
	ErrorTail   string `json:"error_tail,omitempty"`
	ProgressJSON string `json:"progress_json,omitempty"`
	SrcStatJSON string `json:"src_stat_json,omitempty"`
	SrcSize     int64  `json:"src_size"`
	OutputSize  int64  `json:"output_size"`
	StartedAt   string `json:"started_at,omitempty"`
	FinishedAt  string `json:"finished_at,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`

	Cmd       string `json:"cmd,omitempty"`
	DestPath  string `json:"dest_path,omitempty"`

	// Origin is why this job exists ("manual", "issue-fix", "upscale", and
	// later "webhook" / "autopilot" / "playback"); Reason is a short
	// free-text note shown in the UI ("Autopilot: rule 'Old H.264'").
	Origin string `json:"origin,omitempty"`
	Reason string `json:"reason,omitempty"`

	// Joined for API convenience:
	FileTitle string `json:"file_title,omitempty"`
}

const jobCols = `id, file_id, src_path, temp_path, status, priority, run_now, backend, codec,
	quality, settings_json, attempts, max_attempts, error, error_tail, progress_json,
	src_stat_json, src_size, output_size, started_at, finished_at, created_at, cmd, dest_path,
	origin, reason`

func scanJob(row interface{ Scan(...any) error }) (*Job, error) {
	j := &Job{}
	var fileID sql.NullInt64
	var runNow int
	err := row.Scan(&j.ID, &fileID, &j.SrcPath, &j.TempPath, &j.Status, &j.Priority, &runNow,
		&j.Backend, &j.Codec, &j.Quality, &j.SettingsJSON, &j.Attempts, &j.MaxAttempts,
		&j.Error, &j.ErrorTail, &j.ProgressJSON, &j.SrcStatJSON, &j.SrcSize, &j.OutputSize,
		&j.StartedAt, &j.FinishedAt, &j.CreatedAt, &j.Cmd, &j.DestPath, &j.Origin, &j.Reason)
	if err != nil {
		return nil, err
	}
	if fileID.Valid {
		j.FileID = fileID.Int64
	}
	j.RunNow = runNow != 0
	return j, nil
}

// CreateJob enqueues a job.
func (s *Store) CreateJob(j *Job) error {
	origin := j.Origin
	if origin == "" {
		origin = "manual"
	}
	res, err := s.dbW.Exec(`INSERT INTO jobs(file_id, src_path, status, priority, run_now,
		backend, codec, quality, settings_json, attempts, max_attempts, src_size, origin, reason)
		VALUES(?,?,?,?,?,?,?,?,?,0,?,?,?,?)`,
		nullID(j.FileID), j.SrcPath, StatusQueued, j.Priority, b2i(j.RunNow),
		j.Backend, j.Codec, j.Quality, j.SettingsJSON, j.MaxAttempts, j.SrcSize, origin, j.Reason)
	if err != nil {
		return err
	}
	j.ID, err = res.LastInsertId()
	j.Origin = origin
	return err
}

func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// isNeural matches a job whose settings select the neural upscale tier (see
// isUpscale for why a LIKE on the compact JSON is enough).
const isNeural = `settings_json LIKE '%"upscale_tier":"neural"%'`

// ClaimNext atomically claims the next runnable job. Regular jobs need
// windowOpen and neural upscales need neuralOpen (they run in their own,
// usually overnight, window); run-now jobs are always claimable.
func (s *Store) ClaimNext(windowOpen, neuralOpen bool) (*Job, error) {
	wo, no := 0, 0
	if windowOpen {
		wo = 1
	}
	if neuralOpen {
		no = 1
	}
	row := s.dbW.QueryRow(`UPDATE jobs SET status='running', started_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),
		attempts=attempts+1, run_now=0
		WHERE id=(SELECT id FROM jobs WHERE status='queued'
			AND (run_now=1 OR (CASE WHEN `+isNeural+` THEN ? ELSE ? END)=1)
			ORDER BY priority, id LIMIT 1)
		RETURNING `+jobCols, no, wo)
	j, err := scanJob(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return j, err
}

// PauseJob returns a running job to the queue without counting the run as an
// attempt: a neural upscale that stops at the end of its window and resumes in
// the next is not failing, and must not use up its retries doing so.
func (s *Store) PauseJob(id int64) error {
	_, err := s.dbW.Exec(`UPDATE jobs SET status='queued', started_at='',
		attempts=MAX(attempts-1,0) WHERE id=?`, id)
	return err
}

// UpdateJobStatus sets status (and optionally error text) on a job.
func (s *Store) UpdateJobStatus(id int64, status, errMsg string) error {
	_, err := s.dbW.Exec(`UPDATE jobs SET status=?, error=? WHERE id=?`, status, errMsg, id)
	return err
}

// SetJobProgress stores throttled progress JSON.
func (s *Store) SetJobProgress(id int64, progressJSON string) error {
	_, err := s.dbW.Exec(`UPDATE jobs SET progress_json=? WHERE id=?`, progressJSON, id)
	return err
}

// FinishJob marks terminal state with output size.
func (s *Store) FinishJob(id int64, status string, outputSize int64, errMsg, errorTail string) error {
	_, err := s.dbW.Exec(`UPDATE jobs SET status=?, output_size=?, error=?, error_tail=?,
		finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?`,
		status, outputSize, errMsg, errorTail, id)
	return err
}

// FailJobKeepAttempt marks a job failed without spending the attempt that
// claimed it (ClaimNext already incremented attempts): used when the run
// never had a real chance to succeed, such as the source file changing
// underneath an in-progress encode.
func (s *Store) FailJobKeepAttempt(id int64, errMsg, errorTail string) error {
	_, err := s.dbW.Exec(`UPDATE jobs SET status=?, output_size=0, error=?, error_tail=?,
		finished_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'), attempts=MAX(attempts-1,0) WHERE id=?`,
		StatusFailed, errMsg, errorTail, id)
	return err
}

// SetJobTemp records the temp path and pre-encode stat snapshot.
func (s *Store) SetJobTemp(id int64, tempPath, srcStatJSON string) error {
	_, err := s.dbW.Exec(`UPDATE jobs SET temp_path=?, src_stat_json=? WHERE id=?`,
		tempPath, srcStatJSON, id)
	return err
}

// Requeue sends a job back to queued (used for user pause/requeue and
// crash recovery when the source is intact).
func (s *Store) Requeue(id int64) error {
	_, err := s.dbW.Exec(`UPDATE jobs SET status='queued', progress_json='',
		started_at='' WHERE id=?`, id)
	return err
}

// Reprioritize sets a new priority value.
func (s *Store) Reprioritize(id int64, priority int) error {
	_, err := s.dbW.Exec(`UPDATE jobs SET priority=? WHERE id=? AND status='queued'`, priority, id)
	return err
}

// SetJobCmd records the exact ffmpeg command line used.
func (s *Store) SetJobCmd(id int64, cmd string) error {
	_, err := s.dbW.Exec(`UPDATE jobs SET cmd=? WHERE id=?`, cmd, id)
	return err
}

// UpdateJobSettings stores the settings a job actually runs with (after
// the quality search picked its quality).
func (s *Store) UpdateJobSettings(id int64, quality int, settingsJSON string) error {
	_, err := s.dbW.Exec(`UPDATE jobs SET quality=?, settings_json=? WHERE id=?`, quality, settingsJSON, id)
	return err
}

// SetJobDest records where the output landed.
func (s *Store) SetJobDest(id int64, dest string) error {
	_, err := s.dbW.Exec(`UPDATE jobs SET dest_path=? WHERE id=?`, dest, id)
	return err
}

// MoveJob reorders the pending queue: job id is placed directly before
// beforeID (0 = end). Pending priorities are renumbered in gaps of 10.
// Autopilot-origin jobs are excluded from the reordered pool —
// their priority is computed from estimated value per GPU-second and
// must survive a manual drag elsewhere in the queue, not collapse into
// the 10/20/30… scheme every time a person reorders their own jobs.
func (s *Store) MoveJob(id, beforeID int64) error {
	tx, err := s.dbW.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id FROM jobs WHERE status='queued' AND id != ? AND origin != 'autopilot' ORDER BY priority, id`, id)
	if err != nil {
		return err
	}
	var order []int64
	for rows.Next() {
		var x int64
		if err := rows.Scan(&x); err != nil {
			rows.Close()
			return err
		}
		order = append(order, x)
	}
	rows.Close()
	placed := false
	var out []int64
	for _, x := range order {
		if x == beforeID {
			out = append(out, id)
			placed = true
		}
		out = append(out, x)
	}
	if !placed {
		out = append(out, id)
	}
	for i, x := range out {
		if _, err := tx.Exec(`UPDATE jobs SET priority=? WHERE id=? AND status='queued'`, (i+1)*10, x); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetRunNow promotes a job to run-now (bypasses schedule windows).
func (s *Store) SetRunNow(id int64) error {
	_, err := s.dbW.Exec(`UPDATE jobs SET run_now=1, priority=0 WHERE id=? AND status='queued'`, id)
	return err
}

// SetJobPriority overrides a queued job's priority — used once, right
// after an autopilot-origin job is created, to place it above
// 100000 (manual's flat default) ordered by estimated value per
// GPU-second, without touching the shared enqueue path every other
// origin uses.
func (s *Store) SetJobPriority(id int64, priority int) error {
	_, err := s.dbW.Exec(`UPDATE jobs SET priority=? WHERE id=? AND status='queued'`, priority, id)
	return err
}

// ListJobs lists jobs newest first, optionally filtered by status set.
func (s *Store) ListJobs(statuses []string, beforeID int64, limit int) ([]*Job, error) {
	where, args := "1=1", []any{}
	if len(statuses) > 0 {
		q := ""
		for _, st := range statuses {
			if q != "" {
				q += ","
			}
			q += "?"
			args = append(args, st)
		}
		where = "status IN (" + q + ")"
	}
	if beforeID > 0 {
		where += " AND id < ?"
		args = append(args, beforeID)
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	order := "id DESC"
	if len(statuses) == 1 && statuses[0] == StatusQueued {
		order = "run_now DESC, priority, id"
	}
	rows, err := s.dbR.Query(`SELECT `+jobCols+`, COALESCE((SELECT CASE WHEN library='tvshows'
			THEN title || ' · S' || printf('%02d', season) || 'E' || printf('%02d', episode) ELSE title END
			FROM files WHERE id=jobs.file_id),'')
		FROM jobs WHERE `+where+` ORDER BY `+order+` LIMIT ?`, append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Job{}
	for rows.Next() {
		j := &Job{}
		var fileID sql.NullInt64
		var runNow int
		if err := rows.Scan(&j.ID, &fileID, &j.SrcPath, &j.TempPath, &j.Status, &j.Priority, &runNow,
			&j.Backend, &j.Codec, &j.Quality, &j.SettingsJSON, &j.Attempts, &j.MaxAttempts,
			&j.Error, &j.ErrorTail, &j.ProgressJSON, &j.SrcStatJSON, &j.SrcSize, &j.OutputSize,
			&j.StartedAt, &j.FinishedAt, &j.CreatedAt, &j.Cmd, &j.DestPath, &j.Origin, &j.Reason,
			&j.FileTitle); err != nil {
			return nil, err
		}
		if fileID.Valid {
			j.FileID = fileID.Int64
		}
		j.RunNow = runNow != 0
		out = append(out, j)
	}
	return out, rows.Err()
}

// GetJob fetches one job.
func (s *Store) GetJob(id int64) (*Job, error) {
	row := s.dbR.QueryRow(`SELECT `+jobCols+` FROM jobs WHERE id=?`, id)
	j, err := scanJob(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return j, err
}

// ActiveJobs returns jobs in non-terminal states (for boot recovery
// and dashboard display).
func (s *Store) ActiveJobs() ([]*Job, error) {
	rows, err := s.dbR.Query(`SELECT `+jobCols+` FROM jobs
		WHERE status IN ('running','verifying','replacing') ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Job{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// CountJobsByStatus gives queue counts for the dashboard.
func (s *Store) CountJobsByStatus() (map[string]int, error) {
	rows, err := s.dbR.Query(`SELECT status, COUNT(*) FROM jobs GROUP BY status`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var st string
		var n int
		if err := rows.Scan(&st, &n); err != nil {
			return nil, err
		}
		out[st] = n
	}
	return out, rows.Err()
}

// RealizedSavings totals saved bytes across completed jobs whose output
// is still in place (excludes reverted jobs - the space isn't actually
// saved once the original came back).
func (s *Store) RealizedSavings() (int64, int, error) {
	var total int64
	var n int
	err := s.dbR.QueryRow(`SELECT COALESCE(SUM(src_size - output_size),0), COUNT(*)
		FROM jobs WHERE status='done' AND reverted_at='' AND src_size > output_size AND NOT (`+isUpscale+`)`).Scan(&total, &n)
	return total, n, err
}

// ClearJobHistory permanently deletes finished job records (done,
// failed, canceled) - active jobs are never touched. This is the data
// behind RealizedSavings/HistoryStats (the savings/history dashboard)
// and behind the "upscaled" badge and library filter, which look up
// the latest done upscale job per path: clearing history removes those
// badges too, not just the stats.
func (s *Store) ClearJobHistory() (int64, error) {
	res, err := s.dbW.Exec(`DELETE FROM jobs WHERE status IN ('done','failed','canceled')`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// MarkJobReverted flags a job's output as no longer kept, because its
// original was restored from trash. History/savings stats exclude it;
// the job's own status stays 'done' since it did complete.
func (s *Store) MarkJobReverted(id int64) error {
	if id <= 0 {
		return nil
	}
	_, err := s.dbW.Exec(`UPDATE jobs SET reverted_at=? WHERE id=? AND reverted_at=''`, nowRFC(), id)
	return err
}

// BackfillRevertedJobs runs once (guarded by a kv flag) to catch jobs
// that were restored before reverted_at existed to record it directly:
// a 'done' job whose source path currently holds a file the exact size
// of what the job started from is, for all practical purposes, back to
// its pre-job state. Exact size match after a real encode is not a
// coincidence worth worrying about.
func (s *Store) BackfillRevertedJobs() (int, error) {
	if done, _, _ := s.KVGet("reverted_backfill_done"); done == "1" {
		return 0, nil
	}
	res, err := s.dbW.Exec(`UPDATE jobs SET reverted_at=? WHERE status='done' AND reverted_at=''
		AND output_size>0 AND src_size>0 AND NOT (`+isUpscale+`)
		AND EXISTS (SELECT 1 FROM files f WHERE f.path=jobs.src_path AND f.missing=0 AND f.size=jobs.src_size)`,
		nowRFC())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if err := s.KVSet("reverted_backfill_done", "1"); err != nil {
		return int(n), err
	}
	return int(n), nil
}

// HasQueuedForFile reports whether a pending job exists for a path.
func (s *Store) HasQueuedForFile(path string) (bool, error) {
	var n int
	err := s.dbR.QueryRow(`SELECT COUNT(*) FROM jobs WHERE src_path=? AND status IN
		('queued','running','verifying','replacing')`, path).Scan(&n)
	return n > 0, err
}

// OpenJobIDsForFile lists every non-terminal job for a file (by id, not
// path — a webhook delete resolves to a file row first), for a caller to
// cancel one by one through jobs.Engine.Cancel (which needs to reach the
// engine's own in-flight cancel funcs, not just flip a status column).
func (s *Store) OpenJobIDsForFile(fileID int64) ([]int64, error) {
	rows, err := s.dbR.Query(`SELECT id FROM jobs WHERE file_id=? AND status IN
		('queued','running','verifying','replacing')`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// RetryJob requeues a failed/canceled job with fresh attempts.
func (s *Store) RetryJob(id int64) error {
	res, err := s.dbW.Exec(`UPDATE jobs SET status='queued', attempts=0, error='', error_tail='',
		finished_at='', progress_json='' WHERE id=? AND status IN ('failed','canceled')`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("job %d is not failed or canceled", id)
	}
	return nil
}

// ClearOldPath hides the row for a path that was renamed by a
// container change (the new path has its own row).
func (s *Store) ClearOldPath(path string) error {
	return s.MarkMissingByPath(path)
}
