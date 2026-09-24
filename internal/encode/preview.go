// SPDX-License-Identifier: GPL-3.0-or-later

package encode

import (
	"fmt"

	"github.com/CreatureSurvive/distillarr/internal/media"
)

// Preview samples use Build(..., &Clip{...}) so the "B" side is encoded
// by exactly the pipeline a real job would run.

// BuildSourceCut builds the "A" side sample: a stream copy of the span,
// or a visually lossless x264 proxy when browsers can't play the source.
func BuildSourceCut(src *media.Probe, outPath string, start, dur float64, proxy bool) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin",
		"-ss", fmt.Sprintf("%.3f", start), "-t", fmt.Sprintf("%.3f", dur),
		"-i", src.Format.Filename, "-map", fmt.Sprintf("0:%d", src.Video().Index), "-map", "0:a:0?"}
	if proxy {
		args = append(args, "-c:v", "libx264", "-preset", "veryfast", "-crf", "14", "-pix_fmt", "yuv420p")
	} else {
		args = append(args, "-c:v", "copy")
	}
	args = append(args, "-c:a", "aac", "-b:a", "192k", "-ac", "2", "-sn", "-dn",
		"-movflags", "+faststart", "-f", "mp4", "-y", outPath)
	return args
}

// SourceNeedsProxy reports whether the source video can't direct-play
// in browsers (so the "A" side needs an x264 proxy).
func SourceNeedsProxy(src *media.Probe) bool {
	v := src.Video()
	return v == nil || v.CodecName != "h264" || v.BitDepth() > 8
}
