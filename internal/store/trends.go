// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"encoding/json"
	"time"
)

// TrendSnapshot is one day of library history.
type TrendSnapshot struct {
	Day             string           `json:"day"` // YYYY-MM-DD, local time
	LibBytes        int64            `json:"lib_bytes"`
	BytesByCodec    map[string]int64 `json:"bytes_by_codec"`
	FreeBytesByFS   map[string]int64 `json:"free_bytes_by_fs"`
	SavedCumulative int64            `json:"saved_cumulative"`
}

// UpsertTrend writes (or replaces) one day's snapshot.
func (s *Store) UpsertTrend(t TrendSnapshot) error {
	codecs, _ := json.Marshal(t.BytesByCodec)
	free, _ := json.Marshal(t.FreeBytesByFS)
	_, err := s.dbW.Exec(`INSERT INTO trend_snapshots(day, lib_bytes, bytes_by_codec_json, free_bytes_by_fs_json, saved_cumulative)
		VALUES(?,?,?,?,?) ON CONFLICT(day) DO UPDATE SET lib_bytes=excluded.lib_bytes,
		bytes_by_codec_json=excluded.bytes_by_codec_json, free_bytes_by_fs_json=excluded.free_bytes_by_fs_json,
		saved_cumulative=excluded.saved_cumulative`, t.Day, t.LibBytes, string(codecs), string(free), t.SavedCumulative)
	return err
}

// ListTrends returns every snapshot, oldest first.
func (s *Store) ListTrends() ([]TrendSnapshot, error) {
	rows, err := s.dbR.Query(`SELECT day, lib_bytes, bytes_by_codec_json, free_bytes_by_fs_json, saved_cumulative
		FROM trend_snapshots ORDER BY day`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TrendSnapshot{}
	for rows.Next() {
		var t TrendSnapshot
		var codecs, free string
		if err := rows.Scan(&t.Day, &t.LibBytes, &codecs, &free, &t.SavedCumulative); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(codecs), &t.BytesByCodec)
		_ = json.Unmarshal([]byte(free), &t.FreeBytesByFS)
		out = append(out, t)
	}
	return out, rows.Err()
}

// HasTrend reports whether day already has a snapshot.
func (s *Store) HasTrend(day string) bool {
	var n int
	_ = s.dbR.QueryRow(`SELECT COUNT(*) FROM trend_snapshots WHERE day=?`, day).Scan(&n)
	return n > 0
}

// LibraryBytesByCodec sums present files' sizes per video codec.
func (s *Store) LibraryBytesByCodec() (map[string]int64, error) {
	rows, err := s.dbR.Query(`SELECT COALESCE(NULLIF(video_codec,''),'unknown'), SUM(size) FROM files WHERE missing=0 GROUP BY 1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			return nil, err
		}
		out[k] = n
	}
	return out, rows.Err()
}

// SavedByDay returns bytes saved per local day (YYYY-MM-DD) by done,
// unreverted, non-upscale jobs, for backfilling saved_cumulative.
func (s *Store) SavedByDay(loc *time.Location) (map[string]int64, error) {
	rows, err := s.dbR.Query(`SELECT finished_at, src_size - output_size FROM jobs
		WHERE status='done' AND reverted_at='' AND src_size > output_size AND output_size > 0 AND finished_at != '' AND NOT (` + isUpscale + `)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var at string
		var n int64
		if err := rows.Scan(&at, &n); err != nil {
			return nil, err
		}
		t, err := time.Parse(time.RFC3339Nano, at)
		if err != nil {
			continue
		}
		out[t.In(loc).Format("2006-01-02")] += n
	}
	return out, rows.Err()
}

// ClearTrends deletes every snapshot; the next snapshot backfills
// savings from the remaining job history.
func (s *Store) ClearTrends() error {
	_, err := s.dbW.Exec(`DELETE FROM trend_snapshots`)
	return err
}
