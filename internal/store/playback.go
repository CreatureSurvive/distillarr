// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"strings"
	"time"
)

// RecordPlaybackEvent upserts one playback-session observation for a
// tracked file. Bucketed to the hour (UTC) so a session the
// poller sees every 15s for an hour still produces one row; the latest
// observation in that hour wins. reasons is empty when direct is true.
func (s *Store) RecordPlaybackEvent(fileID int64, server string, at time.Time, direct bool, reasons []string) error {
	hour := at.UTC().Truncate(time.Hour).Format(time.RFC3339)
	d := 0
	if direct {
		d = 1
	}
	_, err := s.dbW.Exec(`INSERT INTO playback_events(file_id, server, at, direct, reasons) VALUES(?,?,?,?,?)
		ON CONFLICT(file_id, server, at) DO UPDATE SET direct=excluded.direct, reasons=excluded.reasons`,
		fileID, server, hour, d, strings.Join(reasons, ","))
	return err
}

// ForcesTranscodeFileIDs returns every file id with at least one
// non-direct playback session in the last `days` days — the
// forces_transcode issue, computed once for a bulk pass like
// RefreshRecs instead of a query per file.
func (s *Store) ForcesTranscodeFileIDs(days int) (map[int64]bool, error) {
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UTC().Format(time.RFC3339)
	rows, err := s.dbR.Query(`SELECT DISTINCT file_id FROM playback_events WHERE direct=0 AND at>=?`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ForcesTranscode is ForcesTranscodeFileIDs for a single file, for a
// one-off probe.
func (s *Store) ForcesTranscode(fileID int64, days int) (bool, error) {
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UTC().Format(time.RFC3339)
	var n int
	err := s.dbR.QueryRow(`SELECT COUNT(1) FROM playback_events WHERE file_id=? AND direct=0 AND at>=?`, fileID, cutoff).Scan(&n)
	return n > 0, err
}

// ForcesTranscodeDetail is the file page detail: reason category
// counts, per-server session counts, and the most recent event, within
// the last `days` days. Returns nil, nil when there's nothing.
type ForcesTranscodeDetail struct {
	Reasons map[string]int `json:"reasons"`
	Servers map[string]int `json:"servers"`
	Last    string         `json:"last"`
}

func (s *Store) ForcesTranscodeDetail(fileID int64, days int) (*ForcesTranscodeDetail, error) {
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UTC().Format(time.RFC3339)
	rows, err := s.dbR.Query(`SELECT server, reasons, at FROM playback_events WHERE file_id=? AND direct=0 AND at>=?`, fileID, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	d := &ForcesTranscodeDetail{Reasons: map[string]int{}, Servers: map[string]int{}}
	for rows.Next() {
		var server, reasons, at string
		if err := rows.Scan(&server, &reasons, &at); err != nil {
			return nil, err
		}
		d.Servers[server]++
		for _, r := range strings.Split(reasons, ",") {
			if r != "" {
				d.Reasons[r]++
			}
		}
		if at > d.Last {
			d.Last = at
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(d.Servers) == 0 {
		return nil, nil
	}
	return d, nil
}
