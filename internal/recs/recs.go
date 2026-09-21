// Package recs recommends encoding settings and size estimates for
// files, seasons, and libraries. Recommendations are transparent:
// every one carries a human-readable rationale.
package recs

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"mediatrans/internal/config"
	"mediatrans/internal/encode"
	"mediatrans/internal/store"
)

// Recommendation is the computed advice for one file.
type Recommendation struct {
	Action   string          `json:"action"` // transcode | skip | caution
	Reason   string          `json:"reason"`
	Settings encode.Settings `json:"settings"`
	EstOut   int64           `json:"est_out_bytes"`
	EstLow   int64           `json:"est_low_bytes"`
	EstHigh  int64           `json:"est_high_bytes"`
	Savings  float64         `json:"savings_pct"` // 0..100
	Worth    bool            `json:"worth"`
	Score    float64         `json:"score"` // 0..100 candidate score
	Notes    []string        `json:"notes,omitempty"`
}

// JSON serializes for files.rec_json caching.
func (r Recommendation) JSON() string {
	b, _ := json.Marshal(r)
	return string(b)
}

// ratioFor maps (source codec → target codec, quality) to an expected
// bitrate ratio. Derived from typical encode outcomes: modern HEVC at
// CRF~21–24 needs roughly 40–55% of x264's bitrate for the same
// perceived quality; AV1 needs ~75% of HEVC; ancient codecs far more.
func ratioFor(srcCodec string, target encode.Codec, quality int) float64 {
	// quality 50 = base ratio; each 10 knob points ≈ ±15% ratio
	q := float64(quality-50) / 10.0
	base := 0.50
	switch srcCodec {
	case "hevc":
		base = 0.72 // re-compressing HEVC yields less
	case "av1":
		base = 0.85
	case "mpeg2video", "mpeg4", "vc1", "wmv3", "msmpeg4v3", "flv1", "vp6":
		base = 0.25
	case "h264", "vp9":
		base = 0.50
	default:
		base = 0.45
	}
	if target == encode.AV1 {
		base *= 0.75
	}
	r := base * math.Pow(0.87, q)
	if r < 0.08 {
		r = 0.08
	}
	if r > 0.95 {
		r = 0.95
	}
	return r
}

// minBitrateFor is the sensible floor by resolution so tiny-but-long
// files don't get absurd estimates.
func minBitrateFor(height int) int64 {
	switch {
	case height >= 2000:
		return 2_500_000
	case height >= 1000:
		return 1_000_000
	case height >= 700:
		return 600_000
	default:
		return 300_000
	}
}

// Recommend computes the advice for one file under the given config.
func Recommend(f *store.File, cfg config.Config) Recommendation {
	r := Recommendation{Action: "skip", Settings: defaultSettings(cfg)}

	if f == nil || f.Size <= 0 || f.Duration <= 0 || f.VideoCodec == "" {
		r.Reason = "File has no readable video stream."
		return r
	}

	// Dolby Vision: re-encoding risks losing DV layers/metadata.
	if f.HDR == "dolby_vision" {
		r.Action = "caution"
		r.Reason = "Dolby Vision source — re-encoding can break the DV layer. Skipped by default; enable manually only if you accept losing DV."
		return r
	}

	target := encode.Codec(cfg.DefaultCodec)
	if target != encode.AV1 {
		target = encode.HEVC
	}

	// Already modern?
	already := f.VideoCodec == "hevc" || f.VideoCodec == "av1"
	if already && (!cfg.RecompressHEVC || f.VideoCodec == "av1") {
		r.Reason = fmt.Sprintf("Already %s — no re-encode recommended.", f.VideoCodec)
		return r
	}
	if already && cfg.RecompressHEVC && f.VideoCodec == "hevc" {
		// Only worth it when bitrate is anomalously high for the resolution.
		if f.VideoBitrate < anomalyBitrate(f.Height) {
			r.Reason = fmt.Sprintf("Already HEVC at %.1f Mbps — bitrate is reasonable for %dp.",
				float64(f.VideoBitrate)/1e6, f.Height)
			return r
		}
		r.Notes = append(r.Notes, "Existing HEVC has an unusually high bitrate; recompression enabled.")
	}

	// Audio: PCM handling.
	var pcmBits int64
	for _, a := range f.Audio {
		if isPCMLike(a.Codec) {
			// FLAC compresses PCM to roughly 50–60%.
			pcmBits += a.BitRate
		}
	}
	if pcmBits > 0 && cfg.AudioPCMTarget != "copy" {
		r.Notes = append(r.Notes,
			fmt.Sprintf("Uncompressed %s audio will be converted to %s (lossless).", "PCM", cfg.AudioPCMTarget))
	}

	// Video estimate.
	ratio := ratioFor(f.VideoCodec, target, cfg.DefaultQuality)
	estVideo := int64(float64(f.VideoBitrate) * ratio)
	if floor := minBitrateFor(f.Height); estVideo < floor {
		estVideo = floor
	}
	// Audio + subtitle + container overhead carried over from source.
	otherBits := f.TotalBitrate - f.VideoBitrate
	if otherBits < 0 {
		otherBits = 0
	}
	if pcmBits > 0 && cfg.AudioPCMTarget != "copy" {
		otherBits -= pcmBits / 2 // PCM → FLAC roughly halves
	}
	estTotal := estVideo + otherBits
	estOut := int64(float64(estTotal) * f.Duration / 8)
	if estOut < 1024*512 {
		estOut = 1024 * 512
	}

	r.Settings = encode.Settings{
		Codec:          target,
		Backend:        encode.Backend(cfg.PreferredBackend),
		Quality:        cfg.DefaultQuality,
		AudioPCMTarget: cfg.AudioPCMTarget,
		Container:      "auto",
		MaxHeight:      cfg.MaxHeight,
		TonemapHDR:     cfg.TonemapHDR && f.HDR != "",
	}
	if f.HDR == "hdr10" {
		r.Notes = append(r.Notes, "HDR10: color metadata passes through to the HEVC encode.")
	}
	if f.HDR == "hlg" {
		r.Notes = append(r.Notes, "HLG: color metadata passes through; verify playback after encode.")
	}
	if f.BitDepth == 8 {
		r.Notes = append(r.Notes, "8-bit source → 10-bit output (better compression, no banding cost).")
	}

	savings := (1 - float64(estOut)/float64(f.Size)) * 100
	r.EstOut = estOut
	r.EstLow = int64(float64(estOut) * 0.85)
	r.EstHigh = int64(float64(estOut) * 1.25)
	r.Savings = math.Max(0, savings)

	minPct := float64(cfg.MinSavingsPct)
	if r.Savings < minPct {
		r.Action = "skip"
		r.Reason = fmt.Sprintf("Estimated savings %.0f%% is below the %.0f%% threshold.", r.Savings, minPct)
		return r
	}

	r.Action = "transcode"
	r.Worth = true
	r.Reason = fmt.Sprintf("%s %dp @ %.1f Mbps → %s 10-bit: est. %.1f→%.1f GB (−%.0f%%).",
		codecLabel(f.VideoCodec), f.Height, float64(f.VideoBitrate)/1e6,
		string(target), float64(f.Size)/1e9, float64(estOut)/1e9, r.Savings)
	r.Score = score(r.Savings, f.Size)
	return r
}

func defaultSettings(cfg config.Config) encode.Settings {
	return encode.Settings{
		Codec:          encode.Codec(cfg.DefaultCodec),
		Backend:        encode.Backend(cfg.PreferredBackend),
		Quality:        cfg.DefaultQuality,
		AudioPCMTarget: cfg.AudioPCMTarget,
		Container:      "auto",
		MaxHeight:      cfg.MaxHeight,
	}
}

// score blends savings % with absolute size (bigger = more impactful).
func score(savings float64, size int64) float64 {
	if savings <= 0 || size <= 0 {
		return 0
	}
	sizeFactor := math.Log2(float64(size)/(500*1024*1024)) // 0 at 500 MB
	if sizeFactor < 0 {
		sizeFactor = 0
	}
	sc := savings*0.7 + math.Min(30, sizeFactor*6)*1.0
	if sc > 100 {
		sc = 100
	}
	if sc < 1 {
		sc = 1
	}
	return sc
}

// anomalyBitrate is the "too high for its resolution" HEVC threshold.
func anomalyBitrate(height int) int64 {
	switch {
	case height >= 2000:
		return 25_000_000
	case height >= 1000:
		return 12_000_000
	default:
		return 7_000_000
	}
}

func codecLabel(c string) string {
	switch c {
	case "h264":
		return "H.264"
	case "hevc":
		return "HEVC"
	case "av1":
		return "AV1"
	case "mpeg2video":
		return "MPEG-2"
	case "vc1":
		return "VC-1"
	case "mpeg4":
		return "MPEG-4"
	}
	return c
}

// SeasonRec aggregates a set of episode files (one season or a whole
// show) into a single recommendation with projected totals.
type SeasonRec struct {
	Episodes     int              `json:"episodes"`
	TotalSize    int64            `json:"total_size"`
	EstOutTotal  int64            `json:"est_out_total"`
	SavingsPct   float64          `json:"savings_pct"`
	Settings     encode.Settings  `json:"settings"`
	WorthCount   int              `json:"worth_count"`
	SkipCount    int              `json:"skip_count"`
	CautionCount int              `json:"caution_count"`
	AvgBitrate   int64            `json:"avg_bitrate"`
	Height       int              `json:"height"`
	Notes        []string         `json:"notes,omitempty"`
}

// Aggregate computes one recommendation for a group of episode files.
func Aggregate(files []*store.File, cfg config.Config) SeasonRec {
	out := SeasonRec{Settings: defaultSettings(cfg), Height: 0}
	if len(files) == 0 {
		return out
	}
	var bitrates []int64
	var worthBytes, worthEst int64
	heightCounts := map[int]int{}
	for _, f := range files {
		out.Episodes++
		out.TotalSize += f.Size
		if f.VideoBitrate > 0 {
			bitrates = append(bitrates, f.VideoBitrate)
		}
		heightCounts[f.Height]++
		rec := Recommend(f, cfg)
		switch rec.Action {
		case "transcode":
			out.WorthCount++
			worthBytes += f.Size
			worthEst += rec.EstOut
		case "caution":
			out.CautionCount++
		default:
			out.SkipCount++
		}
	}
	sort.Slice(bitrates, func(i, j int) bool { return bitrates[i] < bitrates[j] })
	if len(bitrates) > 0 {
		out.AvgBitrate = bitrates[len(bitrates)/2]
	}
	bestH, bestN := 0, 0
	for h, n := range heightCounts {
		if n > bestN {
			bestH, bestN = h, n
		}
	}
	out.Height = bestH
	if worthBytes > 0 {
		out.EstOutTotal = worthEst
		out.SavingsPct = math.Max(0, (1-float64(worthEst)/float64(worthBytes))*100)
	}
	if out.CautionCount > 0 {
		out.Notes = append(out.Notes,
			fmt.Sprintf("%d episode(s) are Dolby Vision — excluded from the projection.", out.CautionCount))
	}
	return out
}

// isPCMLike mirrors media's PCM detection for the stored audio summary.
func isPCMLike(codec string) bool {
	return codec == "lpcm" ||
		strings.HasPrefix(codec, "pcm_") ||
		strings.Contains(codec, "adpcm")
}
