package media

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"time"
)

// sceneThresh is ffmpeg's usual floor for "this frame is a real cut",
// not just fast motion or a pan.
const sceneThresh = 0.3

var ptsTimeRe = regexp.MustCompile(`pts_time:([0-9.]+)`)

// DetectScenesNear returns real scene-cut timestamps within
// [center-radius, center+radius] (clamped at 0), for placing a sample
// on stable, representative content instead of an arbitrary fixed
// fraction of runtime. Only the window is decoded - a full-file decode
// just for cut points measured over 2 minutes on one 43-minute episode
// in this environment, which is too slow to pay on every measurement
// across a whole library queue. The decode is heavily downscaled (no
// audio, 160px wide) since only the cut points matter, not the
// picture. -copyts keeps showinfo's pts_time in the file's own
// timeline instead of relative to the seek.
func DetectScenesNear(ctx context.Context, path string, center, radius float64) ([]float64, error) {
	if radius <= 0 {
		return nil, nil
	}
	start := center - radius
	if start < 0 {
		start = 0
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	vf := fmt.Sprintf("scale=160:-2,select='gt(scene\\,%g)',showinfo", sceneThresh)
	out, err := exec.CommandContext(cctx, "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "info",
		"-ss", strconv.FormatFloat(start, 'f', 3, 64), "-i", path,
		"-t", strconv.FormatFloat(radius*2, 'f', 3, 64), "-copyts",
		"-map", "0:v:0", "-an", "-sn",
		"-vf", vf, "-f", "null", "-").CombinedOutput()
	if cctx.Err() != nil {
		return nil, cctx.Err()
	}
	// ffmpeg can exit non-zero on an otherwise-fine -f null pass (odd
	// trailing streams, etc.); showinfo already wrote what it found.
	_ = err
	return parseSceneTimes(string(out)), nil
}

func parseSceneTimes(s string) []float64 {
	var out []float64
	seen := map[float64]bool{}
	for _, m := range ptsTimeRe.FindAllStringSubmatch(s, -1) {
		v, err := strconv.ParseFloat(m[1], 64)
		if err != nil || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Float64s(out)
	return out
}
