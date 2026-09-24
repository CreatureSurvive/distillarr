// SPDX-License-Identifier: GPL-3.0-or-later

package media

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"time"
)

// Crop is the active picture inside a frame.
type Crop struct{ W, H, X, Y int }

var cropRe = regexp.MustCompile(`crop=(-?\d+):(-?\d+):(-?\d+):(-?\d+)`)

// DetectCrop finds black bars encoded into the picture by running
// cropdetect on a few keyframes at six points through the file. The
// result is the union of every usable sample, so bars are only reported
// where they are present in all of them (a dark scene can't shrink it,
// and a wider scene anywhere widens it back). ok=false means no bars
// worth reporting (under 8 px a side) or too few usable samples.
func DetectCrop(ctx context.Context, path string, w, h int, dur float64) (c Crop, ok bool, err error) {
	if w <= 0 || h <= 0 || dur <= 30 {
		return Crop{}, false, nil
	}
	x0, y0, x1, y1 := w, h, 0, 0
	valid := 0
	for _, frac := range []float64{0.08, 0.24, 0.40, 0.56, 0.72, 0.88} {
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		out, runErr := exec.CommandContext(cctx, "ffmpeg", "-hide_banner", "-nostdin", "-loglevel", "info",
			"-skip_frame", "nokey", "-ss", fmt.Sprintf("%.1f", dur*frac), "-i", path,
			"-map", "0:v:0", "-frames:v", "4", "-an", "-sn",
			"-vf", "cropdetect=limit=0.094:round=2:reset=0", "-f", "null", "-").CombinedOutput()
		cancel()
		if ctx.Err() != nil {
			return Crop{}, false, ctx.Err()
		}
		if runErr != nil {
			continue
		}
		m := cropRe.FindAllStringSubmatch(string(out), -1)
		if len(m) == 0 {
			continue
		}
		last := m[len(m)-1]
		cw, _ := strconv.Atoi(last[1])
		ch, _ := strconv.Atoi(last[2])
		cx, _ := strconv.Atoi(last[3])
		cy, _ := strconv.Atoi(last[4])
		// A near-black frame reports a tiny or negative box: not evidence.
		if cw <= 0 || ch <= 0 || cw*ch < w*h/4 {
			continue
		}
		valid++
		x0, y0 = min(x0, cx), min(y0, cy)
		x1, y1 = max(x1, cx+cw), max(y1, cy+ch)
	}
	if valid < 3 {
		return Crop{}, false, nil
	}
	c = Crop{W: (x1 - x0) &^ 1, H: (y1 - y0) &^ 1, X: x0 &^ 1, Y: y0 &^ 1}
	if w-c.W < 16 && h-c.H < 16 {
		return Crop{}, false, nil // no meaningful bars
	}
	return c, true, nil
}
