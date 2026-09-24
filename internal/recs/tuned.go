// SPDX-License-Identifier: GPL-3.0-or-later

package recs

import (
	"encoding/json"

	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/media"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// TuneRecord is a file's last VMAF quality search, stored in
// files.tune_json. It only applies to the same codec, encoder, target,
// crop and resolution cap it was measured with.
type TuneRecord struct {
	Target    float64          `json:"target"`
	Codec     string           `json:"codec"`
	Backend   string           `json:"backend"`
	Crop      string           `json:"crop,omitempty"`
	MaxHeight int              `json:"max_height,omitempty"`
	Quality   int              `json:"quality"`
	Ratio     float64          `json:"ratio"`
	VMAF      media.VMAFResult `json:"vmaf"`
	Met       bool             `json:"met"`
	At        string           `json:"at"`
}

// TunedFor returns the stored measurement if it matches settings s.
func TunedFor(f *store.File, s encode.Settings) *TuneRecord {
	if f == nil || f.TuneJSON == "" || s.VMAFTarget <= 0 {
		return nil
	}
	var t TuneRecord
	if json.Unmarshal([]byte(f.TuneJSON), &t) != nil || t.Quality == 0 {
		return nil
	}
	if t.Target != s.VMAFTarget || t.Codec != string(s.Codec) || t.Backend != string(s.Backend) ||
		t.Crop != s.Crop || t.MaxHeight != s.MaxHeight {
		return nil
	}
	return &t
}
