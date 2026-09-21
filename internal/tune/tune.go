// Package tune finds, per file, the lowest-bitrate quality setting that
// still meets a VMAF target: short samples are encoded with the exact
// job pipeline and scored against the original. This replaces guessing
// from bitrate alone, which can't see grain, motion or how much damage
// the source compression already did.
package tune

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"mediatrans/internal/encode"
	"mediatrans/internal/media"
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

// Spans returns the sample start times for a duration.
func Spans(dur float64) []float64 {
	var out []float64
	for _, f := range sampleAt {
		st := dur*f - sampleLen/2
		if st < 0 {
			st = 0
		}
		if st+sampleLen > dur {
			st = max(0, dur-sampleLen)
		}
		out = append(out, st)
	}
	return out
}

func qualityForCRF(crf int) int { return 50 + (23-crf)*5 }

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
		starts = Spans(dur)
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
		var encBytes int64
		minP5, sumMean := 100.0, 0.0
		var paths []string
		for i, cut := range cuts {
			out := filepath.Join(o.WorkDir, fmt.Sprintf("c%d-s%d.mp4", crf, i))
			prim, fb, err := encode.Build(ss, cutProbes[i], out, &encode.Clip{Start: 0, Dur: sampleLen + 1, NoAudio: !o.KeepFinal})
			if err != nil {
				return nil, err
			}
			// The lossless intermediate isn't hardware-decodable; decode it on
			// the CPU and encode with the job's real encoder.
			spec := prim
			if fb != nil {
				spec = fb
			}
			release := o.Acquire(prim.SemKey)
			_, err = encode.Runner(ctx, *spec, 0, nil)
			release()
			if err != nil {
				return nil, fmt.Errorf("sample encode: %w", err)
			}
			if n, err := media.SpanVideoBytes(ctx, out, 0, sampleLen+1); err == nil {
				encBytes += n
			}
			vr, err := media.VMAF(ctx, out, media.VMAFRef{Path: cut, Start: 0, Dur: sampleLen + 1,
				Crop: crop, W: refW, H: refH, Deinterlace: deint})
			if err != nil {
				return nil, err
			}
			sumMean += vr.Mean
			minP5 = min(minP5, vr.P5)
			step.VMAF.Min = min(step.VMAF.Min, vr.Min)
			paths = append(paths, out)
		}
		step.VMAF.Mean = sumMean / float64(len(cuts))
		step.VMAF.P5 = minP5
		if srcBytes > 0 {
			step.Ratio = float64(encBytes) / float64(srcBytes)
		}
		step.Pass = step.VMAF.Mean >= o.Target && step.VMAF.P5 >= o.Target-p5Slack
		tried[crf] = step
		files[crf] = paths
		res.Steps = append(res.Steps, *step)
		return step, nil
	}

	// Start from the settings' own quality; walk toward the boundary,
	// then stop at the highest CRF (smallest file) that passes.
	crf := encode.CRFForQuality(s.Quality)
	lo, hi := 14, 32
	first, err := try(crf)
	if err != nil {
		return res, nil, err
	}
	best := -1
	if first.Pass {
		best = crf
		for c := crf + 2; c <= hi && len(tried) < 6; c += 2 {
			st, err := try(c)
			if err != nil {
				return res, nil, err
			}
			if !st.Pass {
				if st2, err := try(c - 1); err == nil && st2.Pass {
					best = c - 1
				}
				break
			}
			best = c
		}
	} else {
		for c := crf - 2; c >= lo && len(tried) < 6; c -= 2 {
			st, err := try(c)
			if err != nil {
				return res, nil, err
			}
			if st.Pass {
				best = c
				if st2, err := try(c + 1); err == nil && st2.Pass {
					best = c + 1
				}
				break
			}
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
