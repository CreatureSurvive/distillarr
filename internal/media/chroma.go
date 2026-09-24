// SPDX-License-Identifier: GPL-3.0-or-later

package media

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// FFmpegBin is the ffmpeg used for sampling frame statistics.
var FFmpegBin = "ffmpeg"

var chromaRe = regexp.MustCompile(`lavfi\.signalstats\.([UV])AVG=([0-9.]+)`)

// MeanChroma averages the U and V means over every frame in ffmpeg
// signalstats output (metadata=mode=print). ok is false when either channel
// is missing.
func MeanChroma(out string) (u, v float64, ok bool) {
	var su, sv float64
	var nu, nv int
	for _, m := range chromaRe.FindAllStringSubmatch(out, -1) {
		f, err := strconv.ParseFloat(m[2], 64)
		if err != nil {
			continue
		}
		if m[1] == "U" {
			su += f
			nu++
		} else {
			sv += f
			nv++
		}
	}
	if nu == 0 || nv == 0 {
		return 0, 0, false
	}
	return su / float64(nu), sv / float64(nv), true
}

// SampleChroma decodes frames from each position in ats and returns the mean
// U and V of the picture on an 8-bit scale (128 = neutral). A corrupt render,
// such as a plane that came back all zeros, shows up as a mean far from the
// source's, which a clean exit code alone would never reveal.
func SampleChroma(ctx context.Context, path string, ats []float64, frames int) (u, v float64, err error) {
	var su, sv float64
	for _, at := range ats {
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		out, rerr := exec.CommandContext(cctx, FFmpegBin, "-hide_banner", "-loglevel", "error", "-nostdin",
			"-ss", fmt.Sprintf("%.2f", at), "-i", path, "-map", "0:v:0", "-frames:v", strconv.Itoa(frames),
			"-vf", "format=yuv420p,signalstats,metadata=mode=print:file=-", "-f", "null", "-").CombinedOutput()
		cancel()
		if rerr != nil {
			return 0, 0, fmt.Errorf("sample %.0fs of %s: %s", at, path, strings.TrimSpace(tailLine(string(out), rerr)))
		}
		cu, cv, ok := MeanChroma(string(out))
		if !ok {
			return 0, 0, fmt.Errorf("no frames decoded at %.0fs of %s", at, path)
		}
		su += cu
		sv += cv
	}
	n := float64(len(ats))
	return su / n, sv / n, nil
}

func tailLine(out string, err error) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if s := strings.TrimSpace(lines[len(lines)-1]); s != "" {
		return s
	}
	return err.Error()
}
