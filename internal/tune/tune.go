// Package tune finds, per file, the lowest-bitrate quality setting that
// still meets a VMAF target: short samples are encoded with the exact
// job pipeline and scored against the original. This replaces guessing
// from bitrate alone, which can't see grain, motion or how much damage
// the source compression already did.
package tune

import (
	"context"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/media"
)

// Sample placement: three 15 s spans spread through the file.
const sampleLen = 15.0

// SampleLen is the length of each measured sample, in seconds.
const SampleLen = sampleLen

var sampleAt = []float64{0.22, 0.50, 0.78}

// Step is one quality level that was tried.
type Step struct {
	Quality int              `json:"quality"`
	CRF     int              `json:"crf"`
	VMAF    media.VMAFResult `json:"vmaf"`
	Ratio   float64          `json:"ratio"` // encoded / source video bytes over the samples
	Pass    bool             `json:"pass"`
}

// Result is the outcome of a search.
type Result struct {
	Target  float64          `json:"target"`
	Quality int              `json:"quality"`
	VMAF    media.VMAFResult `json:"vmaf"`
	Ratio   float64          `json:"ratio"`
	Met     bool             `json:"met"` // false: even the best tried level missed the target
	Steps   []Step           `json:"steps"`
	Codec   string           `json:"codec"`
	Backend string           `json:"backend"`
}

// The target is the average VMAF (the standard used by ab-av1 et al.).
// The worst 5% of frames may dip further; grain and fast motion dip
// naturally, so this only rejects encodes with genuinely broken moments.
const p5Slack = 15.0

// Options for a search.
type Options struct {
	Settings encode.Settings // backend/node already resolved
	Probe    *media.Probe
	Target   float64
	WorkDir  string
	Acquire  func(key string) func() // GPU slot
	Progress func(msg string)
	// Keep the final level's encoded samples (preview uses them).
	KeepFinal bool
	// Cuts are pre-cut samples (from CutSamples) at Starts; nil = cut here.
	Cuts   []string
	Starts []float64
	// Cambi also scores banding severity (~50% more CPU per sample):
	// worth it on flat, gradient-heavy content where VMAF is known to
	// under-penalize banding, not worth it on everything.
	Cambi bool
}

// CutSamples decodes each span of the original once, accurately, into a
// lossless intermediate (x264 qp 0, no B-frames). Encodes are made from
// these and scored against them, so every decoder sees identical frames
// from frame 0: no seek drift, no keyframe/reorder mismatches.
func CutSamples(ctx context.Context, src *media.Probe, starts []float64, length float64, dir string, withAudio bool) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	pix := "yuv420p"
	if v := src.Video(); v != nil && v.BitDepth() > 8 {
		pix = "yuv420p10le"
	}
	var out []string
	for i, st := range starts {
		dst := filepath.Join(dir, fmt.Sprintf("cut-%d.mkv", i))
		args := []string{"-hide_banner", "-loglevel", "error", "-nostdin",
			"-ss", fmt.Sprintf("%.3f", st), "-i", src.Format.Filename, "-t", fmt.Sprintf("%.3f", length),
			"-map", "0:v:0", "-c:v", "libx264", "-qp", "0", "-preset", "ultrafast", "-bf", "0",
			"-pix_fmt", pix}
		if withAudio {
			args = append(args, "-map", "0:a:0?", "-c:a", "aac", "-b:a", "192k", "-ac", "2")
		}
		args = append(args, "-map_metadata", "-1", "-sn", "-dn", "-y", dst)
		if b, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("cut sample: %v: %s", err, strings.TrimSpace(string(b)))
		}
		out = append(out, dst)
	}
	return out, nil
}

// nearBlackPct excludes the bottom slice of the file's own byte-density
// distribution from window selection: a fade, a static title card or a
// logo bumper spends almost no bits and tells a quality search nothing
// about real content - worse, VMAF scores near-black spans oddly rather
// than easily (confirmed live: a 10th-percentile-by-bytes window scored
// the WORST VMAF of three candidates at a fixed CRF on a real file, not
// the best, despite having the lowest byte density). 20% gives a safer
// margin than the 10% a naive version of this idea starts from.
const nearBlackPct = 0.20

// Spans returns len(sampleAt) sample start times for a duration. Fixed
// fractions alone can land squarely on an unrepresentative 15s window -
// e.g. an episode where the beginning/middle/climax points all happen
// to fall on easy dialogue, or all happen to fall on the one grainy
// night scene - which is exactly the kind of noise that makes the
// quality search fit to 45 seconds instead of the whole file (seen
// live: sibling episodes of the same show landing on VMAF-target
// quality anywhere from 25 to 70). Each sample is chosen from the
// file's own packet-size index (no decode: the container's index
// alone, ~9s for a 2.5-hour Bluray remux in testing) as the
// keyframe-starting window whose byte density is closest to the file's
// own median, within an equal time slice of the runtime - typical
// complexity for that part of the film, not an accidental outlier in
// either direction, landing cleanly on a keyframe for free. A file
// whose index can't be read, or too short to have a keyframe in every
// slice, falls back to plain fixed fractions.
func Spans(ctx context.Context, path string, dur float64) []float64 {
	if dur > sampleLen*4 {
		if pkts, err := media.PacketSizes(ctx, path); err == nil {
			if spans := windowScan(pkts, dur); len(spans) == len(sampleAt) {
				return spans
			}
		}
	}
	return fixedSpans(dur)
}

func fixedSpans(dur float64) []float64 {
	out := make([]float64, 0, len(sampleAt))
	for _, f := range sampleAt {
		out = append(out, clampSpan(dur*f-sampleLen/2, dur))
	}
	return out
}

type window struct {
	start float64
	bytes int64
}

// windowScan finds len(sampleAt) candidate windows and picks one per
// equal time slice of the runtime. Each keyframe packet seeds one
// candidate window summing every packet's bytes within sampleLen of it
// (bounded work: real keyframe spacing is seconds, and each inner scan
// stops as soon as it passes sampleLen, so this is nowhere near the
// O(n²) it might look like). Returns nil if any slice has nothing
// usable, so the caller falls back to fixed fractions for all of them
// rather than mixing strategies.
func windowScan(pkts []media.Packet, dur float64) []float64 {
	var all []window
	n := len(pkts)
	for i, p := range pkts {
		if !p.Key || p.PTS < 0 || p.PTS+sampleLen > dur {
			continue
		}
		var sum int64
		for j := i; j < n && pkts[j].PTS-p.PTS < sampleLen; j++ {
			sum += pkts[j].Size
		}
		all = append(all, window{p.PTS, sum})
	}
	if len(all) < len(sampleAt) {
		return nil
	}
	bySize := append([]window(nil), all...)
	sort.Slice(bySize, func(i, j int) bool { return bySize[i].bytes < bySize[j].bytes })
	floor := bySize[int(float64(len(bySize))*nearBlackPct)].bytes
	median := bySize[len(bySize)/2].bytes

	out := make([]float64, 0, len(sampleAt))
	slice := dur / float64(len(sampleAt))
	for i := range sampleAt {
		lo, hi := slice*float64(i), slice*float64(i+1)
		best, bestDist := -1, math.MaxFloat64
		for k, w := range all {
			if w.start < lo || w.start >= hi || w.bytes < floor || tooClose(w.start, out) {
				continue
			}
			if d := math.Abs(float64(w.bytes - median)); d < bestDist {
				best, bestDist = k, d
			}
		}
		if best < 0 {
			return nil
		}
		out = append(out, all[best].start)
	}
	return out
}

func tooClose(t float64, chosen []float64) bool {
	for _, c := range chosen {
		if math.Abs(t-c) < sampleLen*1.1 {
			return true
		}
	}
	return false
}

func clampSpan(st, dur float64) float64 {
	if st < 0 {
		st = 0
	}
	if st+sampleLen > dur {
		st = max(0, dur-sampleLen)
	}
	return st
}

func qualityForCRF(crf int) int { return 50 + (23-crf)*5 }

// vmafPerCRF is the standard rule of thumb for x264/x265 in the 88-96
// VMAF range: about 2 VMAF points per CRF step. It doesn't need to be
// exact - crfStep re-derives the step from a fresh measurement every
// try, so an imprecise slope on real content only costs an extra try
// or two, never a wrong answer.
const vmafPerCRF = 2.0

// crfStep sizes the next CRF probe from how far the last try's mean
// VMAF sat from target, clamped so one step can never leap further
// than a handful of quality levels. Lower CRF is higher quality, so a
// positive gap (below target) must lower CRF: callers do crf-step.
func crfStep(gapToTarget float64) int {
	step := int(math.Round(gapToTarget / vmafPerCRF))
	switch {
	case step > 4:
		step = 4
	case step < -4:
		step = -4
	case step == 0:
		if gapToTarget >= 0 {
			step = 1
		} else {
			step = -1
		}
	}
	return step
}

// Search runs the quality search. It returns the result plus, when
// KeepFinal, the paths of the final level's samples in span order.
func Search(ctx context.Context, o Options) (Result, []string, error) {
	s := o.Settings
	s.Normalize()
	p := o.Probe
	v := p.Video()
	if v == nil {
		return Result{}, nil, fmt.Errorf("no video")
	}
	if s.TonemapHDR && v.HDRType() != "" {
		return Result{}, nil, fmt.Errorf("tone-mapped output can't be scored against an HDR source")
	}
	dur := p.DurationSec()
	if dur < sampleLen*2 {
		return Result{}, nil, fmt.Errorf("file too short to sample")
	}
	if err := os.MkdirAll(o.WorkDir, 0o755); err != nil {
		return Result{}, nil, err
	}

	// Reference shape: what the encode does to the picture.
	refW, refH := v.Width, v.Height
	crop := ""
	if cw, ch, ok := parseCrop(s.Crop, v.Width, v.Height); ok {
		refW, refH, crop = cw, ch, s.Crop
	}
	deint := s.Deinterlace == "on" || (s.Deinterlace == "auto" && v.Interlaced())

	cuts, starts := o.Cuts, o.Starts
	if cuts == nil {
		var err error
		starts = Spans(ctx, p.Format.Filename, dur)
		if cuts, err = CutSamples(ctx, p, starts, sampleLen, o.WorkDir, o.KeepFinal); err != nil {
			return Result{}, nil, err
		}
		defer func() {
			for _, c := range cuts {
				_ = os.Remove(c)
			}
		}()
	}
	cutProbes := make([]*media.Probe, len(cuts))
	var srcBytes int64
	for i, c := range cuts {
		cp, err := media.ProbeFile(ctx, c)
		if err != nil {
			return Result{}, nil, err
		}
		cutProbes[i] = cp
		// Size baseline: what the ORIGINAL spends on this span.
		n, err := media.SpanVideoBytes(ctx, p.Format.Filename, starts[i], sampleLen)
		if err != nil {
			return Result{}, nil, err
		}
		srcBytes += n
	}

	res := Result{Target: o.Target, Codec: string(s.Codec), Backend: string(s.Backend)}
	tried := map[int]*Step{}
	files := map[int][]string{}

	// Each sample's VMAF pass wants nearly every core by default (see
	// media.VMAF); running len(cuts) of them at once needs the cores
	// split between them; or they just fight each other for the same
	// ones instead of finishing any faster.
	vmafThreads := max(2, runtime.NumCPU()/max(1, len(cuts)))

	try := func(crf int) (*Step, error) {
		if st, ok := tried[crf]; ok {
			return st, nil
		}
		q := qualityForCRF(crf)
		if o.Progress != nil {
			o.Progress(fmt.Sprintf("testing quality %d", q))
		}
		ss := s
		ss.Quality = q
		step := &Step{Quality: q, CRF: crf, VMAF: media.VMAFResult{Min: 100}}

		type sample struct {
			encBytes int64
			vmaf     media.VMAFResult
			path     string
		}
		results := make([]sample, len(cuts))
		errs := make([]error, len(cuts))
		var wg sync.WaitGroup
		for i, cut := range cuts {
			wg.Add(1)
			go func(i int, cut string) {
				defer wg.Done()
				out := filepath.Join(o.WorkDir, fmt.Sprintf("c%d-s%d.mp4", crf, i))
				prim, fb, err := encode.Build(ss, cutProbes[i], out, &encode.Clip{Start: 0, Dur: sampleLen + 1, NoAudio: !o.KeepFinal})
				if err != nil {
					errs[i] = err
					return
				}
				// The lossless intermediate isn't hardware-decodable; decode it
				// on the CPU and encode with the job's real encoder.
				spec := prim
				if fb != nil {
					spec = fb
				}
				release := o.Acquire(prim.SemKey)
				_, err = encode.Runner(ctx, *spec, 0, nil)
				release()
				if err != nil {
					errs[i] = fmt.Errorf("sample encode: %w", err)
					return
				}
				var encBytes int64
				if n, err := media.SpanVideoBytes(ctx, out, 0, sampleLen+1); err == nil {
					encBytes = n
				}
				vr, err := media.VMAF(ctx, out, media.VMAFRef{Path: cut, Start: 0, Dur: sampleLen + 1,
					Crop: crop, W: refW, H: refH, Deinterlace: deint, Threads: vmafThreads, Cambi: o.Cambi})
				if err != nil {
					errs[i] = err
					return
				}
				results[i] = sample{encBytes: encBytes, vmaf: vr, path: out}
			}(i, cut)
		}
		wg.Wait()
		for _, err := range errs {
			if err != nil {
				return nil, err
			}
		}

		var encBytes int64
		minP5, sumMean, sumCambi := 100.0, 0.0, 0.0
		paths := make([]string, len(cuts))
		for i, r := range results {
			encBytes += r.encBytes
			sumMean += r.vmaf.Mean
			sumCambi += r.vmaf.Cambi
			minP5 = min(minP5, r.vmaf.P5)
			step.VMAF.Min = min(step.VMAF.Min, r.vmaf.Min)
			paths[i] = r.path
		}
		step.VMAF.Mean = sumMean / float64(len(cuts))
		step.VMAF.P5 = minP5
		if o.Cambi {
			step.VMAF.Cambi = sumCambi / float64(len(cuts))
		}
		if srcBytes > 0 {
			step.Ratio = float64(encBytes) / float64(srcBytes)
		}
		step.Pass = step.VMAF.Mean >= o.Target && step.VMAF.P5 >= o.Target-p5Slack
		tried[crf] = step
		files[crf] = paths
		res.Steps = append(res.Steps, *step)
		return step, nil
	}

	// Start from the settings' own quality and walk toward the boundary,
	// sizing each step from how far the last try's VMAF sat from target
	// instead of a blind fixed step - a source that's passing by 10
	// points can jump straight for a much higher CRF instead of
	// spending tries crossing the gap one point at a time.
	crf := encode.CRFForQuality(s.Quality)
	lo, hi := 14, 32
	cur, err := try(crf)
	if err != nil {
		return res, nil, err
	}
	for len(tried) < 6 {
		next := crf - crfStep(o.Target-cur.VMAF.Mean)
		if next == crf || next < lo || next > hi {
			break
		}
		st, err := try(next)
		if err != nil {
			return res, nil, err
		}
		if st.Pass != cur.Pass {
			// Crossed the pass/fail boundary. A bigger adaptive step can
			// leave a wider gap than the old fixed step of 2 ever could,
			// so narrow it for real instead of only checking one CRF back.
			passSide, otherSide := crf, next
			if !cur.Pass {
				passSide, otherSide = next, crf
			}
			for len(tried) < 6 {
				lo2, hi2 := passSide, otherSide
				if lo2 > hi2 {
					lo2, hi2 = hi2, lo2
				}
				if hi2-lo2 <= 1 {
					break
				}
				mid := (lo2 + hi2) / 2
				mst, err := try(mid)
				if err != nil {
					return res, nil, err
				}
				if mst.Pass {
					passSide = mid
				} else {
					otherSide = mid
				}
			}
			break
		}
		crf, cur = next, st
	}

	best := -1
	for c, st := range tried {
		if st.Pass && c > best {
			best = c
		}
	}
	res.Met = best >= 0
	if !res.Met {
		// Nothing tried met the target: take the best-scoring level.
		var crfs []int
		for c := range tried {
			crfs = append(crfs, c)
		}
		sort.Ints(crfs)
		best = crfs[0]
	}
	st := tried[best]
	res.Quality, res.VMAF, res.Ratio = st.Quality, st.VMAF, st.Ratio
	sort.Slice(res.Steps, func(i, j int) bool { return res.Steps[i].Quality > res.Steps[j].Quality })

	// Clean up everything except (optionally) the chosen level.
	var keep []string
	for c, ps := range files {
		for _, f := range ps {
			if c == best && o.KeepFinal {
				continue
			}
			_ = os.Remove(f)
		}
	}
	if o.KeepFinal {
		keep = files[best]
	}
	return res, keep, nil
}

func parseCrop(spec string, fw, fh int) (w, h int, ok bool) {
	if spec == "" {
		return 0, 0, false
	}
	var x, y int
	if n, err := fmt.Sscanf(spec, "%d:%d:%d:%d", &w, &h, &x, &y); err != nil || n != 4 {
		return 0, 0, false
	}
	if w <= 0 || h <= 0 || x+w > fw || y+h > fh {
		return 0, 0, false
	}
	return w, h, true
}
