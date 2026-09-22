package store

// HistoryTotals summarizes finished jobs.
type HistoryTotals struct {
	Done        int     `json:"done"`
	Failed      int     `json:"failed"`
	Canceled    int     `json:"canceled"`
	Upscaled    int     `json:"upscaled"` // finished upscale jobs (not part of the savings below)
	SrcBytes    int64   `json:"src_bytes"`
	OutBytes    int64   `json:"out_bytes"`
	Saved       int64   `json:"saved"`
	EncodeHours float64 `json:"encode_hours"`
}

// StatGroup is one breakdown row (kind, codec, library, week).
type StatGroup struct {
	Key      string  `json:"key"`
	Jobs     int     `json:"jobs"`
	SrcBytes int64   `json:"src_bytes"`
	OutBytes int64   `json:"out_bytes"`
	Saved    int64   `json:"saved"`
	Hours    float64 `json:"hours"`
}

// TopSaver is one of the biggest wins.
type TopSaver struct {
	JobID    int64  `json:"job_id"`
	Path     string `json:"path"`
	SrcBytes int64  `json:"src_bytes"`
	OutBytes int64  `json:"out_bytes"`
	Finished string `json:"finished_at"`
}

// HistoryStats is the whole history dashboard.
type HistoryStats struct {
	Totals  HistoryTotals `json:"totals"`
	Kinds   []StatGroup   `json:"kinds"`   // encode | remux
	Codecs  []StatGroup   `json:"codecs"`  // output codec (encodes only)
	Library []StatGroup   `json:"library"` // movies | tvshows
	Weeks   []StatGroup   `json:"weeks"`   // YYYY-MM-DD of week start (Monday), last 16
	Top     []TopSaver    `json:"top"`
}

// isUpscale matches a job's settings_json (compact JSON: an upscale job carries
// "upscale_to":N, omitted otherwise). Upscales grow files on purpose, so every
// "space saved" aggregate excludes them: they would show as negative savings,
// or as false ones when a low-bitrate upscale lands smaller than a rich source.
const isUpscale = `settings_json LIKE '%"upscale_to"%'`

const doneRows = `FROM jobs j LEFT JOIN files f ON f.id=j.file_id WHERE j.status='done' AND j.reverted_at='' AND j.output_size>0 AND NOT (j.` + isUpscale + `)`
const hoursExpr = `COALESCE(SUM(CASE WHEN j.started_at!='' AND j.finished_at!=''
	THEN (julianday(j.finished_at)-julianday(j.started_at))*24 ELSE 0 END),0)`

func (s *Store) statGroups(keyExpr, extra string) ([]StatGroup, error) {
	rows, err := s.dbR.Query(`SELECT `+keyExpr+` AS k, COUNT(*), COALESCE(SUM(j.src_size),0),
		COALESCE(SUM(j.output_size),0), COALESCE(SUM(j.src_size-j.output_size),0), `+hoursExpr+`
		`+doneRows+extra+` GROUP BY k ORDER BY k`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StatGroup{}
	for rows.Next() {
		var g StatGroup
		if err := rows.Scan(&g.Key, &g.Jobs, &g.SrcBytes, &g.OutBytes, &g.Saved, &g.Hours); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// HistoryStats aggregates the job history.
func (s *Store) HistoryStats() (HistoryStats, error) {
	var h HistoryStats
	// Done (and the upscaled share of it) counts only kept output - a
	// job that was later restored from trash didn't keep anything.
	if err := s.dbR.QueryRow(`SELECT COALESCE(SUM(status='done' AND reverted_at=''),0), COALESCE(SUM(status='failed'),0),
		COALESCE(SUM(status='canceled'),0),
		COALESCE(SUM(status='done' AND reverted_at='' AND `+isUpscale+`),0) FROM jobs`).Scan(&h.Totals.Done, &h.Totals.Failed, &h.Totals.Canceled, &h.Totals.Upscaled); err != nil {
		return h, err
	}
	if err := s.dbR.QueryRow(`SELECT COALESCE(SUM(j.src_size),0), COALESCE(SUM(j.output_size),0),
		COALESCE(SUM(j.src_size-j.output_size),0), `+hoursExpr+` `+doneRows).
		Scan(&h.Totals.SrcBytes, &h.Totals.OutBytes, &h.Totals.Saved, &h.Totals.EncodeHours); err != nil {
		return h, err
	}
	var err error
	if h.Kinds, err = s.statGroups(`CASE WHEN j.backend='remux' THEN 'remux' ELSE 'encode' END`, ""); err != nil {
		return h, err
	}
	if h.Codecs, err = s.statGroups(`j.codec`, " AND j.backend!='remux'"); err != nil {
		return h, err
	}
	if h.Library, err = s.statGroups(`COALESCE(f.library, CASE WHEN j.src_path LIKE '%/tvshows/%' THEN 'tvshows' ELSE 'movies' END)`, ""); err != nil {
		return h, err
	}
	// Week buckets keyed by their Monday (local time).
	if h.Weeks, err = s.statGroups(`date(j.finished_at,'localtime','weekday 0','-6 days')`,
		" AND j.finished_at >= date('now','-112 days')"); err != nil {
		return h, err
	}
	rows, err := s.dbR.Query(`SELECT j.id, j.src_path, j.src_size, j.output_size, j.finished_at ` + doneRows +
		` ORDER BY j.src_size-j.output_size DESC LIMIT 8`)
	if err != nil {
		return h, err
	}
	defer rows.Close()
	h.Top = []TopSaver{}
	for rows.Next() {
		var t TopSaver
		if err := rows.Scan(&t.JobID, &t.Path, &t.SrcBytes, &t.OutBytes, &t.Finished); err != nil {
			return h, err
		}
		h.Top = append(h.Top, t)
	}
	return h, rows.Err()
}
