// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"encoding/json"
	"strings"
)

// UpscaleRecord says how a file came to be: the finished upscale job whose
// output it is. It is read from job history (a done job's dest_path and
// settings), so there is no per-file column to keep in step with rescans.
type UpscaleRecord struct {
	To     int    `json:"to"` // resolution class it was upscaled to
	Preset string `json:"preset"`
	Tier   string `json:"tier"`
	At     string `json:"at"`
}

// upscaleOutputs selects the paths finished upscale jobs produced: the new file
// in copy mode, the original path in replace mode.
const upscaleOutputs = `SELECT dest_path FROM jobs WHERE status='done' AND COALESCE(dest_path,'')!='' AND ` + isUpscale

// UpscaledFiles returns, for whichever of paths are the output of a finished
// upscale job, the record of the latest one.
func (s *Store) UpscaledFiles(paths []string) (map[string]UpscaleRecord, error) {
	out := map[string]UpscaleRecord{}
	if len(paths) == 0 {
		return out, nil
	}
	args := make([]any, len(paths))
	for i, p := range paths {
		args[i] = p
	}
	rows, err := s.dbR.Query(`SELECT dest_path, settings_json, finished_at FROM jobs
		WHERE status='done' AND dest_path IN (?`+strings.Repeat(",?", len(paths)-1)+`) AND `+isUpscale+`
		ORDER BY id`, args...) // later jobs win
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var dest, settings, at string
		if err := rows.Scan(&dest, &settings, &at); err != nil {
			return nil, err
		}
		var st struct {
			To     int    `json:"upscale_to"`
			Preset string `json:"upscale_preset"`
			Tier   string `json:"upscale_tier"`
		}
		if json.Unmarshal([]byte(settings), &st) == nil && st.To > 0 {
			out[dest] = UpscaleRecord{To: st.To, Preset: st.Preset, Tier: st.Tier, At: at}
		}
	}
	return out, rows.Err()
}
