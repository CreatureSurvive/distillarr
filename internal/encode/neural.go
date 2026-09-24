// SPDX-License-Identifier: GPL-3.0-or-later

package encode

import (
	"fmt"
	"strings"

	"github.com/CreatureSurvive/distillarr/internal/media"
	"github.com/CreatureSurvive/distillarr/internal/upscale"
)

// The neural tier can't be one ffmpeg command: the upscaler is a separate
// program working on image files. A job is instead cut into chunks of frames:
// each chunk is decoded to PNGs, upscaled, and encoded straight to the final
// codec as its own small file, and the chunks are concatenated and muxed with
// the original audio and subtitles at the end. Finished chunks are kept, so a
// job survives a closed schedule window or a restart.

// NeuralPlan is the geometry of a neural upscale of one source.
type NeuralPlan struct {
	Scale        int // the model's native scale factor
	InW, InH     int // frame size fed to the upscaler (after crop)
	ModelW       int // size the model produces (InW*Scale, InH*Scale)
	ModelH       int
	OutW, OutH   int     // exact final size (res.Up), resized down from the model's
	FPS          string  // source rate as a rational, e.g. 24000/1001
	FPSVal       float64 // the same as a number
	Frames       int     // estimated total frames
	ChunkFrames  int     // frames per chunk
	Model        string
	EstimateSecs float64
}

// ChunkSeconds is the target length of one chunk. Long enough that the per-chunk
// startup (model load, encoder init) is a few percent of the work; short enough
// that a stop loses little.
const ChunkSeconds = 20

// PlanNeural works out the chunking and the upscaler settings for a source.
func PlanNeural(s Settings, v *media.Stream, durationSec float64) (*NeuralPlan, error) {
	s.Normalize()
	p, ok := upscale.Get(s.UpscalePreset)
	if !ok || !p.Neural() {
		return nil, fmt.Errorf("%q is not a neural upscale preset", s.UpscalePreset)
	}
	outW, outH, ok := UpscaleSize(s, v)
	if !ok {
		return nil, fmt.Errorf("nothing to upscale: the source is already %dx%d", v.Width, v.Height)
	}
	inW, inH := v.Width, v.Height
	if cw, ch, _, _, crop := parseCrop(s.Crop, v.Width, v.Height); crop {
		inW, inH = cw, ch
	}
	fps := v.FPS()
	if fps <= 0 || v.AvgFrameRate == "" || strings.HasPrefix(v.AvgFrameRate, "0/") {
		return nil, fmt.Errorf("no usable frame rate to chunk by (%q)", v.AvgFrameRate)
	}
	sc := p.PickScale(inW, outW)
	frames := int(durationSec*fps + 0.5)
	return &NeuralPlan{
		Scale: sc, InW: inW, InH: inH, ModelW: inW * sc, ModelH: inH * sc,
		OutW: outW, OutH: outH, FPS: v.AvgFrameRate, FPSVal: fps,
		Frames: frames, ChunkFrames: max(1, int(fps*ChunkSeconds+0.5)), Model: p.Model(),
		EstimateSecs: p.EstimateSeconds(float64(frames), inW, inH),
	}, nil
}

// Chunks is how many chunks cover the source.
func (n *NeuralPlan) Chunks() int { return (n.Frames + n.ChunkFrames - 1) / n.ChunkFrames }

// ChunkWindow is the span of source time belonging to chunk k. The boundaries
// sit half a frame before a frame's timestamp, so no frame falls on a boundary:
// every frame lands in exactly one chunk, with none dropped or duplicated, and
// the chunks add up to the source's duration without drift. The last chunk is
// open-ended (dur < 0) and stops at the end of the file.
func (n *NeuralPlan) ChunkWindow(k int) (start, dur float64) {
	half := 0.5 / n.FPSVal
	span := float64(n.ChunkFrames) / n.FPSVal
	if k == 0 {
		start = 0
	} else {
		start = float64(k)*span - half
	}
	if k >= n.Chunks()-1 {
		return start, -1
	}
	end := float64(k+1)*span - half
	return start, end - start
}

// neuralPre is the CPU-side prefiltering shared with the software path in Build:
// crop, deinterlace, tone-map. Frames are decoded in software, since they go to
// image files.
func neuralPre(s Settings, v *media.Stream) []string {
	var pre []string
	if cw, ch, cx, cy, crop := parseCrop(s.Crop, v.Width, v.Height); crop {
		pre = append(pre, fmt.Sprintf("crop=%d:%d:%d:%d", cw, ch, cx, cy))
	}
	if s.Deinterlace == "on" || (s.Deinterlace == "auto" && v.Interlaced()) {
		pre = append(pre, "bwdif=mode=send_frame")
	}
	if s.TonemapHDR && v.HDRType() != "" && v.HDRType() != "dolby_vision" {
		pre = append(pre, "zscale=t=linear:npl=100", "format=gbrpf32le", "zscale=p=bt709",
			"tonemap=tonemap=hable:desat=0", "zscale=t=bt709:m=bt709:r=tv")
	}
	// Frames go to RGB PNGs. swscale assumes BT.601 unless told otherwise, which
	// is right for SD and visibly wrong for HD: decode with the matrix the source
	// really uses (the same guess libplacebo and players make).
	return append(pre, "scale=in_color_matrix="+srcMatrix(v), "format=rgb24")
}

// NeuralDecodeArgs decodes one chunk's frames to numbered PNGs (outPattern is
// e.g. dir/%06d.png). dur < 0 reads to the end of the file.
func NeuralDecodeArgs(s Settings, src *media.Probe, start, dur float64, outPattern string) ([]string, error) {
	s.Normalize()
	v := src.Video()
	if v == nil {
		return nil, fmt.Errorf("no video stream")
	}
	if v.HDRType() != "" && !(s.TonemapHDR && v.HDRType() != "dolby_vision") {
		return nil, fmt.Errorf("HDR sources can't be upscaled unless tone-mapped to SDR")
	}
	a := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	if start > 0 {
		a = append(a, "-ss", fmt.Sprintf("%.4f", start))
	}
	a = append(a, "-i", src.Format.Filename, "-map", fmt.Sprintf("0:%d", v.Index))
	if dur > 0 {
		a = append(a, "-t", fmt.Sprintf("%.4f", dur))
	}
	if pre := neuralPre(s, v); len(pre) > 0 {
		a = append(a, "-vf", strings.Join(pre, ","))
	}
	// Passthrough: exactly the frames the source has in this window, no
	// duplicates or drops from ffmpeg's frame-rate matching.
	return append(a, "-an", "-sn", "-dn", "-fps_mode", "passthrough", "-start_number", "1", "-y", outPattern), nil
}

// BuildNeuralChunk encodes one chunk of upscaled PNGs (inPattern, e.g.
// dir/%06d.png, at the model's output size) to the job's codec as a standalone
// Matroska file at exactly the target size. It reuses Build, so every backend's
// filter chain and rate control stays in one place, and uses the software-decode
// variant since image files aren't hardware-decodable.
func BuildNeuralChunk(s Settings, n *NeuralPlan, inPattern, outPath string) (*CmdSpec, error) {
	s.UpscaleTo, s.MaxHeight, s.VMAFTarget, s.VideoCopy = 0, 0, 0, false
	probe := &media.Probe{
		Format: media.Format{Filename: inPattern},
		Streams: []media.Stream{{
			Index: 0, CodecType: "video", CodecName: "png", PixFmt: "rgb24",
			Width: n.ModelW, Height: n.ModelH,
			ColorSpace: "bt709", ColorPrimaries: "bt709", ColorTransfer: "bt709",
		}},
	}
	clip := &Clip{
		NoAudio: true, Container: "mkv", RGBInput: true, ScaleW: n.OutW, ScaleH: n.OutH,
		InputArgs: []string{"-framerate", n.FPS, "-start_number", "1"},
	}
	prim, fb, err := Build(s, probe, outPath, clip)
	if err != nil {
		return nil, err
	}
	if fb != nil {
		return fb, nil
	}
	return prim, nil
}

// BuildNeuralMux joins the chunk list (an ffmpeg concat file) with the original
// file's audio, subtitles, attachments, chapters and metadata into the final
// output, copying every stream. Its CmdSpec carries the container and stream
// counts Verify expects, exactly as Build's does for a normal encode.
func BuildNeuralMux(s Settings, src *media.Probe, listPath, outPath string) (*CmdSpec, error) {
	s.Normalize()
	v := src.Video()
	if v == nil {
		return nil, fmt.Errorf("no video stream")
	}
	container, err := ChooseContainer(s, src)
	if err != nil {
		return nil, err
	}
	plan := planStreams(s, src, container, false)

	args := []string{"-hide_banner", "-loglevel", "warning", "-nostdin",
		"-progress", "pipe:1", "-nostats", "-stats_period", "0.5",
		"-f", "concat", "-safe", "0", "-i", listPath, "-i", src.Format.Filename,
		"-map", "0:v:0"}
	// planStreams maps from the source as input 0, and its first pair is the
	// video; here the video comes from the chunks (input 0) and everything else
	// from the source (input 1).
	for i := 2; i+1 < len(plan.maps); i += 2 {
		args = append(args, plan.maps[i], "1:"+strings.TrimPrefix(plan.maps[i+1], "0:"))
	}
	args = append(args, "-c:v", "copy")
	args = append(args, plan.codecs...)
	args = append(args, muxArgsFrom(container, 1)...)
	if s.Codec == HEVC && container == "mp4" {
		args = append(args, "-tag:v", "hvc1")
	}
	args = append(args, "-y", outPath)
	return &CmdSpec{Args: args, SemKey: "copy", Container: container,
		ExpectAudio: plan.nAudio, ExpectSubs: plan.nSubs}, nil
}

// NeuralStill is what a single-frame neural preview needs: the model and native
// scale to run, and the CPU prefilters (crop, deinterlace, tone-map) to apply to
// the decoded frame first, so it matches what a real job would upscale.
func NeuralStill(s Settings, v *media.Stream) (model string, scale int, pre []string, err error) {
	s.Normalize()
	p, ok := upscale.Get(s.UpscalePreset)
	if !ok || !p.Neural() {
		return "", 0, nil, fmt.Errorf("%q is not a neural upscale preset", s.UpscalePreset)
	}
	outW, _, ok := UpscaleSize(s, v)
	if !ok {
		return "", 0, nil, fmt.Errorf("nothing to upscale: the source is already %dx%d", v.Width, v.Height)
	}
	if v.HDRType() != "" && !(s.TonemapHDR && v.HDRType() != "dolby_vision") {
		return "", 0, nil, fmt.Errorf("HDR sources can't be upscaled unless tone-mapped to SDR")
	}
	inW := v.Width
	if cw, _, _, _, crop := parseCrop(s.Crop, v.Width, v.Height); crop {
		inW = cw
	}
	return p.Model(), p.PickScale(inW, outW), neuralPre(s, v), nil
}
