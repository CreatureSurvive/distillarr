// Package recs recommends per-file encode settings with written
// reasons, and estimates output size with a bits-per-pixel model that
// self-calibrates from finished jobs and preview samples.
package recs

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"

	"mediatrans/internal/config"
	"mediatrans/internal/encode"
	"mediatrans/internal/store"
)

// AudioPlan describes what happens to one audio track, for display.
type AudioPlan struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec"`
	Channels int    `json:"channels"`
	Lang     string `json:"lang,omitempty"`
	Action   string `json:"action"` // copy | convert | drop
	Target   string `json:"target,omitempty"`
	Note     string `json:"note,omitempty"`
}

// Recommendation is the computed advice for one file.
type Recommendation struct {
	Action   string          `json:"action"` // transcode | skip | caution
	Reason   string          `json:"reason"`
	Settings encode.Settings `json:"settings"`
	Why      []string        `json:"why,omitempty"` // why these settings
	Notes    []string        `json:"notes,omitempty"`
	Audio    []AudioPlan     `json:"audio,omitempty"`
	EstOut   int64           `json:"est_out_bytes"`
	EstLow   int64           `json:"est_low_bytes"`
	EstHigh  int64           `json:"est_high_bytes"`
	Savings  float64         `json:"savings_pct"`
	Worth    bool            `json:"worth"`
	Score    float64         `json:"score"`
	SrcBPP   float64         `json:"src_bpp"`
	Headroom float64         `json:"headroom"`   // source bits vs what the target needs
	Samples  int             `json:"calibration_samples"` // real results behind the estimate
}

// JSON serializes for files.rec_json caching.
func (r Recommendation) JSON() string {
	b, _ := json.Marshal(r)
	return string(b)
}

// Hooks wired by main.
var (
	// ResolveBackend maps "auto"/preference to a working backend.
	ResolveBackend = func(pref string, c encode.Codec) encode.Backend {
		if pref == "" || pref == "auto" {
			return encode.SW
		}
		return encode.Backend(pref)
	}
	// Genres returns Jellyfin genres for a path (nil when unknown).
	Genres = func(path string) []string { return nil }
)

// ---- size model ----

func fpsOr(f float64) float64 {
	if f <= 1 || f > 240 {
		return 24
	}
	return f
}

func resBucket(h int) string {
	switch {
	case h <= 576:
		return "sd"
	case h <= 800:
		return "720"
	case h <= 1200:
		return "1080"
	}
	return "2160"
}

// targetBPP is the "reference" bits/pixel of the target encoder at
// quality q. Output is modelled as the geometric mean of this and the
// source's H.264-equivalent bits (content complexity carries through).
// Constants fitted to measured Arc A380 QSV HEVC encodes (1080p
// H.264 at ~0.05 bpp → video 60% smaller at quality 66) and refined
// per bucket by calibration.
func targetBPP(h int, c encode.Codec, b encode.Backend, q int) float64 {
	base := map[string]float64{"sd": 0.018, "720": 0.011, "1080": 0.0075, "2160": 0.0045}[resBucket(h)]
	switch b {
	case encode.QSV:
		base *= 1.2
	case encode.VAAPI:
		base *= 1.3
	case encode.NVENC:
		base *= 1.25
	}
	if c == encode.AV1 {
		base *= 0.75
	}
	return base * math.Pow(1.3, float64(q-60)/10)
}

// codecEff converts source bits to "H.264-equivalent" bits.
func codecEff(c string) float64 {
	switch c {
	case "hevc", "vp9":
		return 1.6
	case "av1":
		return 2.0
	case "h264":
		return 1.0
	}
	return 0.6 // mpeg2/mpeg4/vc1/wmv: inefficient, bits are worth less
}

func srcFamily(c string) string {
	switch c {
	case "h264", "hevc", "av1":
		return c
	}
	return "legacy"
}

// CalibKey identifies a calibration bucket.
func CalibKey(b encode.Backend, c encode.Codec, srcCodec string, h int) string {
	return fmt.Sprintf("%s|%s|%s|%s", b, c, srcFamily(srcCodec), resBucket(h))
}

// ModelRatio predicts output/input VIDEO bitrate before calibration.
func ModelRatio(f *store.File, s encode.Settings) float64 {
	if f.VideoBitrate <= 0 || f.Width <= 0 || f.Height <= 0 {
		return 0.6
	}
	outH := f.Height
	if s.MaxHeight > 0 && f.Height > s.MaxHeight {
		outH = s.MaxHeight
	}
	scale := float64(outH) / float64(f.Height)
	pixIn := float64(f.Width*f.Height) * fpsOr(f.FPS)
	pixOut := pixIn * scale * scale
	target := targetBPP(outH, s.Codec, s.Backend, s.Quality) * pixOut
	srcEq := float64(f.VideoBitrate) * codecEff(f.VideoCodec)
	r := 0.88 * math.Sqrt(target/srcEq)
	return math.Max(0.1, math.Min(0.97, r))
}

// ---- calibration ----

type bucket struct {
	N      int     `json:"n"`
	SumLog float64 `json:"sum_log"`
}

var cal = struct {
	sync.Mutex
	m  map[string]*bucket
	st *store.Store
}{m: map[string]*bucket{}}

// InitCalibration loads persisted calibration.
func InitCalibration(st *store.Store) {
	cal.Lock()
	defer cal.Unlock()
	cal.st = st
	var m map[string]*bucket
	if ok, _ := store.KVLoad(st, "calibration", &m); ok && m != nil {
		cal.m = m
	}
}

// RecordObservation stores a measured output/input video ratio for the
// settings that produced it; future estimates in that bucket shift.
func RecordObservation(f *store.File, s encode.Settings, observed float64) {
	if observed <= 0.02 || observed > 3 {
		return
	}
	pred := ModelRatio(f, s)
	key := CalibKey(s.Backend, s.Codec, f.VideoCodec, f.Height)
	cal.Lock()
	b := cal.m[key]
	if b == nil {
		b = &bucket{}
		cal.m[key] = b
	}
	// Cap history so the model keeps adapting.
	if b.N >= 50 {
		b.SumLog *= 49.0 / float64(b.N)
		b.N = 49
	}
	b.N++
	b.SumLog += math.Log(observed / pred)
	m := cal.m
	st := cal.st
	cal.Unlock()
	if st != nil {
		_ = store.KVJSON(st, "calibration", &m)
	}
}

func calFactor(key string) (float64, int) {
	cal.Lock()
	defer cal.Unlock()
	b := cal.m[key]
	if b == nil || b.N == 0 {
		return 1, 0
	}
	w := float64(b.N) / float64(b.N+2)
	return math.Exp(b.SumLog / float64(b.N) * w), b.N
}

// CalibrationSummary exposes buckets for the settings page.
func CalibrationSummary() map[string]map[string]float64 {
	cal.Lock()
	defer cal.Unlock()
	out := map[string]map[string]float64{}
	for k, b := range cal.m {
		if b.N > 0 {
			out[k] = map[string]float64{"samples": float64(b.N), "factor": math.Exp(b.SumLog / float64(b.N))}
		}
	}
	return out
}

// ---- recommendation ----

// Recommend picks settings for one file under the config.
func Recommend(f *store.File, cfg config.Config) Recommendation {
	s := baseSettings(cfg)
	r := Recommendation{Action: "skip", Settings: s}
	if f == nil || f.Size <= 0 || f.Duration <= 0 || f.VideoCodec == "" {
		r.Reason = "No readable video stream."
		return r
	}
	if f.HDR == "dolby_vision" {
		r.Action = "caution"
		r.Reason = "Dolby Vision source: re-encoding drops the Dolby Vision layer. Skipped unless you queue it by hand."
		return r
	}
	if f.VideoCodec == "av1" || (f.VideoCodec == "hevc" && !cfg.RecompressHEVC) {
		r.Reason = fmt.Sprintf("Already %s. Nothing to gain.", label(f.VideoCodec))
		r.Audio = audioPlan(f, s)
		return r
	}

	pixRate := float64(f.Width*f.Height) * fpsOr(f.FPS)
	r.SrcBPP = float64(f.VideoBitrate) / pixRate
	r.Headroom = r.SrcBPP * codecEff(f.VideoCodec) / targetBPP(f.Height, s.Codec, s.Backend, 60)
	if f.VideoCodec == "hevc" && r.Headroom < 30 {
		r.Reason = fmt.Sprintf("Already HEVC at %.1f Mb/s, which is reasonable for %dp.", float64(f.VideoBitrate)/1e6, f.Height)
		return r
	}

	// ---- choose quality from the source ----
	q := cfg.DefaultQuality
	why := []string{}
	switch h := r.Headroom; {
	case h < 4.5:
		q += 6
		why = append(why, fmt.Sprintf("Source is already bit-starved (%.3f bits/pixel), so quality is raised +6 to avoid stacking compression artifacts.", r.SrcBPP))
	case h < 8:
		q += 3
		why = append(why, fmt.Sprintf("Modest source bitrate (%.3f bits/pixel), so quality is raised +3.", r.SrcBPP))
	case h > 25:
		q -= 4
		why = append(why, fmt.Sprintf("High-bitrate source (%.3f bits/pixel) has plenty of detail headroom, so quality is lowered −4 and stays transparent.", r.SrcBPP))
	default:
		why = append(why, fmt.Sprintf("Healthy source bitrate (%.3f bits/pixel), so the default quality fits.", r.SrcBPP))
	}
	switch {
	case f.Height <= 576:
		q += 4
		why = append(why, "Standard-definition source: small frames show artifacts sooner (+4).")
	case f.Height >= 1800:
		q -= 3
		why = append(why, "4K frames hide fine compression well (−3).")
	}
	if isAnimation(f) {
		q -= 5
		why = append(why, "Animation compresses very efficiently: flat colour, clean lines (−5).")
		if s.Backend == encode.SW && s.Codec == encode.HEVC {
			s.Tune = "animation"
		}
	} else if f.Library == "movies" && f.Year > 0 && f.Year < 1990 {
		q += 3
		why = append(why, fmt.Sprintf("Older film (%d) usually carries grain, which is costly to preserve (+3).", f.Year))
		if s.Codec == encode.AV1 {
			s.FilmGrain = 8
			why = append(why, "AV1 film-grain synthesis enabled to re-create grain cheaply.")
		}
	}
	if f.Interlaced {
		s.Deinterlace = "on"
		why = append(why, "Interlaced source: deinterlacing (bwdif / vpp) is enabled.")
	}
	s.Quality = clampInt(q, 35, 85)

	notes := []string{}
	switch f.HDR {
	case "hdr10":
		if s.TonemapHDR {
			notes = append(notes, "HDR10 will be tone-mapped to SDR (CPU filter).")
		} else {
			notes = append(notes, "HDR10: colour signalling is carried into the 10-bit encode.")
		}
	case "hlg":
		notes = append(notes, "HLG: colour signalling carried; verify playback after encode.")
	}
	if f.BitDepth <= 8 && s.BitDepth == 10 {
		notes = append(notes, "8-bit to 10-bit: better compression and less banding at the same size.")
	}
	if s.MaxHeight > 0 && f.Height > s.MaxHeight {
		notes = append(notes, fmt.Sprintf("Downscaled %dp to %dp (Settings cap).", f.Height, s.MaxHeight))
	}
	r.Audio = audioPlan(f, s)
	for _, a := range r.Audio {
		if a.Action == "convert" {
			notes = append(notes, fmt.Sprintf("Audio #%d %s → %s (%s).", a.Index, a.Codec, a.Target, a.Note))
		}
	}

	r.Settings = s
	r.Why = why
	r.Notes = notes
	fillEstimate(&r, f, s, cfg)
	if r.Savings < float64(cfg.MinSavingsPct) || f.Size-r.EstOut < 50<<20 {
		r.Action = "skip"
		r.Worth = false
		r.Reason = fmt.Sprintf("Estimated savings of %.0f%% are below your %d%% threshold.", r.Savings, cfg.MinSavingsPct)
		return r
	}
	r.Action = "transcode"
	r.Worth = true
	r.Reason = fmt.Sprintf("%s %dp at %.1f Mb/s → %s %d-bit, quality %d: about %s → %s (−%.0f%%).",
		label(f.VideoCodec), f.Height, float64(f.VideoBitrate)/1e6, label(string(s.Codec)), s.BitDepth,
		s.Quality, gb(f.Size), gb(r.EstOut), r.Savings)
	r.Score = score(r.Savings, f.Size)
	return r
}

// Estimate recomputes size/savings for explicit settings (UI tuning).
func Estimate(f *store.File, s encode.Settings, cfg config.Config) Recommendation {
	s.Normalize()
	if s.Backend == "" || s.Backend == "auto" {
		s.Backend = ResolveBackend("auto", s.Codec)
	}
	r := Recommendation{Action: "custom", Settings: s}
	pixRate := float64(f.Width*f.Height) * fpsOr(f.FPS)
	if pixRate > 0 {
		r.SrcBPP = float64(f.VideoBitrate) / pixRate
	}
	r.Audio = audioPlan(f, s)
	fillEstimate(&r, f, s, cfg)
	r.Worth = r.Savings >= float64(cfg.MinSavingsPct)
	return r
}

func fillEstimate(r *Recommendation, f *store.File, s encode.Settings, cfg config.Config) {
	factor, n := calFactor(CalibKey(s.Backend, s.Codec, f.VideoCodec, f.Height))
	ratio := math.Max(0.06, math.Min(1.1, ModelRatio(f, s)*factor))
	other := f.TotalBitrate - f.VideoBitrate
	if other < 0 {
		other = 0
	}
	for _, a := range r.Audio {
		switch {
		case a.Action == "drop":
			other -= trackBits(f, a.Index)
		case a.Action == "convert" && a.Target == "flac":
			other -= trackBits(f, a.Index) / 2
		case a.Action == "convert":
			other -= trackBits(f, a.Index) - int64(kbpsOf(a.Target, a.Channels))*1000
		}
	}
	if other < 0 {
		other = 0
	}
	vb := float64(f.VideoBitrate) * ratio
	r.EstOut = int64((vb + float64(other)) * f.Duration / 8)
	// Uncertainty narrows as real samples accumulate.
	spread := 0.3 / math.Sqrt(float64(n)+1)
	r.EstLow = int64(float64(r.EstOut) * (1 - spread))
	r.EstHigh = int64(float64(r.EstOut) * (1 + spread))
	r.Savings = math.Max(0, (1-float64(r.EstOut)/float64(f.Size))*100)
	r.Samples = n
}

func trackBits(f *store.File, idx int) int64 {
	for _, a := range f.Audio {
		if a.Index == idx {
			if a.BitRate > 0 {
				return a.BitRate
			}
			if isPCMLike(a.Codec) {
				return int64(a.Channels) * 48000 * 24
			}
			return 640000
		}
	}
	return 0
}

func kbpsOf(target string, ch int) int {
	if ch <= 0 {
		ch = 2
	}
	switch target {
	case "aac":
		return max(128, 96*ch)
	case "opus":
		return max(128, 64*ch)
	case "eac3":
		return min(1024, max(128, 112*ch))
	}
	return 256
}

// audioPlan resolves per-track actions (explicit settings first, then
// the PCM → lossless policy).
func audioPlan(f *store.File, s encode.Settings) []AudioPlan {
	out := []AudioPlan{}
	for _, a := range f.Audio {
		p := AudioPlan{Index: a.Index, Codec: a.Codec, Channels: a.Channels, Lang: a.Lang, Action: "copy"}
		explicit := false
		for _, t := range s.Audio {
			if t.Index == a.Index {
				explicit = true
				p.Action = t.Action
				p.Target = t.Codec
				if t.Channels > 0 {
					p.Channels = t.Channels
				}
			}
		}
		if !explicit && isPCMLike(a.Codec) && s.AudioPCMTarget != "copy" {
			p.Action, p.Target = "convert", s.AudioPCMTarget
		}
		switch {
		case p.Action == "convert" && p.Target == "flac":
			p.Note = "lossless, about half the size"
		case p.Action == "convert":
			p.Note = "lossy"
		case p.Action == "drop":
			p.Note = "removed"
		case strings.Contains(a.Codec, "truehd") || a.Codec == "dts":
			p.Note = "bit-exact (keeps Atmos/DTS:X)"
		default:
			p.Note = "bit-exact"
		}
		out = append(out, p)
	}
	return out
}

func baseSettings(cfg config.Config) encode.Settings {
	c := encode.Codec(cfg.DefaultCodec)
	s := encode.Settings{
		Codec:          c,
		Backend:        ResolveBackend(cfg.PreferredBackend, c),
		Quality:        cfg.DefaultQuality,
		Speed:          cfg.DefaultSpeed,
		MaxHeight:      cfg.MaxHeight,
		TonemapHDR:     cfg.TonemapHDR,
		AudioPCMTarget: cfg.AudioPCMTarget,
		Container:      "auto",
		PreferMP4:      cfg.MP4(),
	}
	s.Normalize()
	return s
}

func isAnimation(f *store.File) bool {
	for _, g := range Genres(f.Path) {
		g = strings.ToLower(g)
		if g == "animation" || g == "anime" {
			return true
		}
	}
	return false
}

// score blends savings % with absolute bytes saved.
func score(savings float64, size int64) float64 {
	if savings <= 0 || size <= 0 {
		return 0
	}
	sizeFactor := math.Max(0, math.Log2(float64(size)/(500<<20)))
	return math.Max(1, math.Min(100, savings*0.7+math.Min(30, sizeFactor*6)))
}

func label(c string) string {
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
	return strings.ToUpper(c)
}

func gb(b int64) string { return fmt.Sprintf("%.1f GB", float64(b)/1e9) }

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func isPCMLike(codec string) bool {
	return codec == "lpcm" || strings.HasPrefix(codec, "pcm_") || strings.Contains(codec, "adpcm")
}

// ---- season / show aggregation ----

// SeasonRec aggregates a group of episodes into one plan.
type SeasonRec struct {
	Episodes     int             `json:"episodes"`
	TotalSize    int64           `json:"total_size"`
	WorthSize    int64           `json:"worth_size"`
	EstOutTotal  int64           `json:"est_out_total"`
	SavingsPct   float64         `json:"savings_pct"`
	Settings     encode.Settings `json:"settings"`
	WorthCount   int             `json:"worth_count"`
	SkipCount    int             `json:"skip_count"`
	CautionCount int             `json:"caution_count"`
	AvgBitrate   int64           `json:"avg_bitrate"`
	Height       int             `json:"height"`
	QualityMin   int             `json:"quality_min"`
	QualityMax   int             `json:"quality_max"`
	Why          []string        `json:"why,omitempty"`
	Notes        []string        `json:"notes,omitempty"`
}

// Aggregate computes one plan for a group of episode files: each
// episode keeps its own tuned quality; the summary reports the range.
func Aggregate(files []*store.File, cfg config.Config) SeasonRec {
	out := SeasonRec{Settings: baseSettings(cfg), QualityMin: 100}
	if len(files) == 0 {
		return out
	}
	var bitrates []int64
	heights := map[int]int{}
	var whyFirst []string
	for _, f := range files {
		out.Episodes++
		out.TotalSize += f.Size
		if f.VideoBitrate > 0 {
			bitrates = append(bitrates, f.VideoBitrate)
		}
		heights[f.Height]++
		rec := Recommend(f, cfg)
		switch rec.Action {
		case "transcode":
			out.WorthCount++
			out.WorthSize += f.Size
			out.EstOutTotal += rec.EstOut
			out.QualityMin = min(out.QualityMin, rec.Settings.Quality)
			out.QualityMax = max(out.QualityMax, rec.Settings.Quality)
			if whyFirst == nil {
				whyFirst = rec.Why
				out.Settings = rec.Settings
			}
		case "caution":
			out.CautionCount++
		default:
			out.SkipCount++
		}
	}
	if out.QualityMin == 100 {
		out.QualityMin = 0
	}
	sort.Slice(bitrates, func(i, j int) bool { return bitrates[i] < bitrates[j] })
	if len(bitrates) > 0 {
		out.AvgBitrate = bitrates[len(bitrates)/2]
	}
	for h, n := range heights {
		if n > heights[out.Height] {
			out.Height = h
		}
	}
	if out.WorthSize > 0 {
		out.SavingsPct = math.Max(0, (1-float64(out.EstOutTotal)/float64(out.WorthSize))*100)
	}
	out.Why = whyFirst
	if out.QualityMax > out.QualityMin {
		out.Notes = append(out.Notes, fmt.Sprintf("Each episode is tuned individually: quality %d to %d.", out.QualityMin, out.QualityMax))
	}
	if out.CautionCount > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d Dolby Vision episode(s) are left out.", out.CautionCount))
	}
	return out
}
