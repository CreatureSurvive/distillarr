package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"time"
)

// FFmpegVMAF is a static ffmpeg with libvmaf, used only for measuring.
var FFmpegVMAF = "ffmpeg-vmaf"

// VMAFResult summarises per-frame VMAF for one sample.
type VMAFResult struct {
	Mean float64 `json:"mean"`
	P5   float64 `json:"p5"`  // 5th percentile: how bad the worst moments get
	Min  float64 `json:"min"`
	// Cambi is mean banding severity (0 = none) when VMAFRef.Cambi asked
	// for it; 0 otherwise. Netflix's own guidance treats anything above
	// ~5 as visible banding, but that's context-dependent, not a rule
	// this app enforces - it's informational for now.
	Cambi float64 `json:"cambi,omitempty"`
}

// VMAFAvailable reports whether the metrics binary is installed.
func VMAFAvailable() bool {
	_, err := exec.LookPath(FFmpegVMAF)
	return err == nil
}

// VMAFRef describes the reference span inside the original file and how
// it must be shaped to match what the encode did to the picture.
type VMAFRef struct {
	Path        string
	Start, Dur  float64
	Crop        string // "w:h:x:y" applied to the reference too ("" = none)
	W, H        int    // reference picture size after crop
	Deinterlace bool   // the encode deinterlaced: do the same to the reference
	// Threads caps libvmaf's thread count (0 = the default, nearly every
	// core). Callers running several VMAF passes at once should divide
	// the core count between them, or each call fights the others for
	// the same cores instead of finishing any faster.
	Threads int
	// Cambi also scores banding severity in the same pass (~50% more
	// CPU work, measured). Worth asking for on flat, gradient-heavy
	// content - animation, HDR tonemap - where VMAF is known to under-
	// penalize banding; not worth the extra cost on everything.
	Cambi bool
}

// VMAF scores an encoded sample (starting at t=0) against the same span
// of the original. The encode is scaled back to the reference size (the
// standard way to score a downscaled encode).
func VMAF(ctx context.Context, distorted string, ref VMAFRef) (VMAFResult, error) {
	log, err := os.CreateTemp("", "vmaf-*.json")
	if err != nil {
		return VMAFResult{}, err
	}
	log.Close()
	defer os.Remove(log.Name())

	// libvmaf pairs frames by timestamp. MKV (ms) and MP4 (exact) round
	// 24 fps differently, which pairs every third frame with its
	// neighbour; renumbering both sides by frame index makes the pairing
	// exact (it must come after any filter that could change timing).
	const byIndex = "settb=AVTB,setpts=N*1000"
	refChain := "null"
	if ref.Crop != "" {
		refChain += ",crop=" + ref.Crop
	}
	if ref.Deinterlace {
		refChain += ",bwdif=mode=send_frame"
	}
	refChain += "," + byIndex
	threads := ref.Threads
	if threads <= 0 {
		threads = max(2, runtime.NumCPU()-2)
	}
	libvmaf := fmt.Sprintf("libvmaf=n_threads=%d:n_subsample=2:log_fmt=json:log_path=%s", threads, log.Name())
	if ref.Cambi {
		libvmaf += ":feature=name=cambi"
	}
	graph := fmt.Sprintf(
		"[0:v]scale=%d:%d:flags=bicubic,format=yuv420p,"+byIndex+"[d];"+
			"[1:v]%s,format=yuv420p[r];"+
			"[d][r]%s",
		ref.W, ref.H, refChain, libvmaf)

	cctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(cctx, FFmpegVMAF, "-hide_banner", "-nostdin", "-loglevel", "error",
		"-i", distorted,
		"-ss", fmt.Sprintf("%.3f", ref.Start), "-t", fmt.Sprintf("%.3f", ref.Dur), "-i", ref.Path,
		"-lavfi", graph, "-f", "null", "-").CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		return VMAFResult{}, fmt.Errorf("vmaf: %v: %s", err, msg)
	}

	raw, err := os.ReadFile(log.Name())
	if err != nil {
		return VMAFResult{}, err
	}
	var doc struct {
		Frames []struct {
			Metrics map[string]float64 `json:"metrics"`
		} `json:"frames"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return VMAFResult{}, fmt.Errorf("vmaf log: %w", err)
	}
	var v []float64
	var cambiSum float64
	var cambiN int
	for _, f := range doc.Frames {
		if x, ok := f.Metrics["vmaf"]; ok {
			v = append(v, x)
		}
		if x, ok := f.Metrics["cambi"]; ok {
			cambiSum += x
			cambiN++
		}
	}
	if len(v) == 0 {
		return VMAFResult{}, fmt.Errorf("vmaf: no frames scored")
	}
	var sum float64
	for _, x := range v {
		sum += x
	}
	sort.Float64s(v)
	res := VMAFResult{Mean: sum / float64(len(v)), P5: v[len(v)*5/100], Min: v[0]}
	if cambiN > 0 {
		res.Cambi = cambiSum / float64(cambiN)
	}
	return res, nil
}

// SpanVideoBytes sums the source's video packet sizes over a span: the
// exact number of bytes the original spends there (for measured ratios).
func SpanVideoBytes(ctx context.Context, path string, start, dur float64) (int64, error) {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	out, err := exec.CommandContext(cctx, FFprobe, "-v", "error", "-select_streams", "v:0",
		"-read_intervals", fmt.Sprintf("%.3f%%+%.3f", start, dur),
		"-show_entries", "packet=size", "-of", "csv=p=0", path).Output()
	if err != nil {
		return 0, fmt.Errorf("packet sizes: %w", err)
	}
	var total int64
	for _, line := range strings.Split(string(out), "\n") {
		var n int64
		if _, err := fmt.Sscan(strings.TrimSpace(strings.TrimSuffix(line, ",")), &n); err == nil {
			total += n
		}
	}
	return total, nil
}
