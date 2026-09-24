// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
)

// speedSampleCap bounds how many recent samples a key keeps: enough for
// a stable rolling median without the row growing forever.
const speedSampleCap = 20

// SpeedKey identifies one (backend, device, codec, resolution class)
// bucket — a device name (not just the backend string) matters because
// two GPUs on the same host can back the same backend at very different
// speeds.
func SpeedKey(backend, device, codec string, resClass int) string {
	return fmt.Sprintf("%s|%s|%s|%d", backend, device, codec, resClass)
}

// RecordSpeedSample appends one realized encode speed (source seconds
// encoded per wall-clock second, i.e. the same "×realtime" figure
// encode.Progress.Speed already reports) to key's rolling window.
func (s *Store) RecordSpeedSample(key string, speed float64) error {
	if speed <= 0 {
		return nil
	}
	var samples []float64
	row := s.dbW.QueryRow(`SELECT samples FROM speed_stats WHERE key=?`, key)
	var raw string
	switch err := row.Scan(&raw); {
	case err == nil:
		_ = json.Unmarshal([]byte(raw), &samples)
	case err != sql.ErrNoRows:
		return err
	}
	samples = append(samples, speed)
	if len(samples) > speedSampleCap {
		samples = samples[len(samples)-speedSampleCap:]
	}
	b, err := json.Marshal(samples)
	if err != nil {
		return err
	}
	_, err = s.dbW.Exec(`INSERT INTO speed_stats(key, samples, updated_at) VALUES(?,?,?)
		ON CONFLICT(key) DO UPDATE SET samples=excluded.samples, updated_at=excluded.updated_at`,
		key, string(b), nowRFC())
	return err
}

// SpeedMedian returns key's rolling median speed and how many samples
// back it, or ok=false when there's no history yet — the caller falls
// back to a conservative default in that case.
func (s *Store) SpeedMedian(key string) (median float64, count int, ok bool) {
	var raw string
	err := s.dbR.QueryRow(`SELECT samples FROM speed_stats WHERE key=?`, key).Scan(&raw)
	if err != nil {
		return 0, 0, false
	}
	var samples []float64
	if json.Unmarshal([]byte(raw), &samples) != nil || len(samples) == 0 {
		return 0, 0, false
	}
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		median = sorted[n/2]
	} else {
		median = (sorted[n/2-1] + sorted[n/2]) / 2
	}
	return median, n, true
}
