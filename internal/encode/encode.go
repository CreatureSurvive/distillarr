// Package encode builds ffmpeg command lines for every encoder
// backend (software x265/SVT-AV1, Intel QSV, VA-API, NVENC) from one
// Settings value: explicit per-stream mapping, speed/quality/bit-depth,
// deinterlace, scaling, tone-mapping, and a software-decode fallback.
package encode

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"

	"mediatrans/internal/media"
	"mediatrans/internal/res"
	"mediatrans/internal/upscale"
)

// Backend selects the encoder family.
type Backend string

const (
	SW    Backend = "sw"    // libx265 / libsvtav1
	QSV   Backend = "qsv"   // Intel Quick Sync (oneVPL)
	VAAPI Backend = "vaapi" // VA-API (iHD)
	NVENC Backend = "nvenc" // NVIDIA
)

// Codec is the target video codec.
type Codec string

const (
	HEVC Codec = "hevc"
	AV1  Codec = "av1"
	H264 Codec = "h264" // maximum compatibility; always 8-bit
)

// AllBackends is the preference-ordered default chain.
var AllBackends = []Backend{QSV, VAAPI, NVENC, SW}

// Speeds, fastest → slowest. Mapped per encoder in speedFor.
var Speeds = []string{"faster", "fast", "medium", "slow", "slower"}

// AudioTrack is a per-source-stream audio decision.
type AudioTrack struct {
	Index    int    `json:"index"`              // source stream index
	Action   string `json:"action"`             // copy | convert | drop
	Codec    string `json:"codec,omitempty"`    // flac | aac | eac3 | opus (convert)
	Bitrate  int    `json:"bitrate,omitempty"`  // kbps, lossy convert only
	Channels int    `json:"channels,omitempty"` // 0 keep, 2 = downmix to stereo
}

// SubTrack is a per-source-stream subtitle decision.
type SubTrack struct {
	Index  int    `json:"index"`
	Action string `json:"action"` // keep | drop
}

// Settings fully describes one encode.
type Settings struct {
	Codec          Codec        `json:"codec"`
	Backend        Backend      `json:"backend"`
	Quality        int          `json:"quality"`                // 0..100
	Speed          string       `json:"speed,omitempty"`        // faster..slower
	BitDepth       int          `json:"bit_depth,omitempty"`    // 8 | 10 (default 10)
	MaxHeight      int          `json:"max_height,omitempty"`   // 0 = keep
	Deinterlace    string       `json:"deinterlace,omitempty"`  // auto | on | off
	TonemapHDR     bool         `json:"tonemap_hdr,omitempty"`  // HDR10/HLG → SDR BT.709
	FilmGrain      int          `json:"film_grain,omitempty"`   // SVT-AV1 synthesis 0..50
	Tune           string       `json:"tune,omitempty"`         // "" | animation | grain (x265)
	Container      string       `json:"container,omitempty"`    // auto | mkv | mp4
	RenderNode     string       `json:"render_node,omitempty"`  // resolved from probe
	AudioPCMTarget string       `json:"audio_pcm_target,omitempty"`
	Audio          []AudioTrack `json:"audio,omitempty"` // nil = policy default
	Subs           []SubTrack   `json:"subs,omitempty"`  // nil = keep all
	ExtraArgs      string       `json:"extra_args,omitempty"` // appended output options
	PreferMP4      bool         `json:"prefer_mp4,omitempty"` // auto container: MP4 whenever tracks fit
	Crop           string       `json:"crop,omitempty"`       // "w:h:x:y" black-bar crop ("" = none)
	// VideoCopy keeps the video bitstream as-is: a quick fix (remux,
	// hvc1 tag, faststart, audio conversion) with no re-encode.
	VideoCopy bool `json:"video_copy,omitempty"`
	// VMAFTarget > 0: before encoding, search samples for the smallest
	// quality that scores at least this VMAF (Quality is the start point).
	VMAFTarget float64 `json:"vmaf_target,omitempty"`

	// Upscaling. UpscaleTo is a resolution class (720 | 1080 | 2160) above
	// the source's; 0 = off. It is separate from MaxHeight, which only ever
	// downscales. Upscale jobs never use VMAF (scoring an upscale against
	// its own source is meaningless), so Normalize clears VMAFTarget.
	UpscaleTo     int                `json:"upscale_to,omitempty"`
	UpscaleTier   string             `json:"upscale_tier,omitempty"`   // shader | neural
	UpscalePreset string             `json:"upscale_preset,omitempty"` // upscale registry id
	UpscaleParams map[string]float64 `json:"upscale_params,omitempty"` // per-preset tunables
	UpscaleOutput string             `json:"upscale_output,omitempty"` // replace | copy ("" = replace); the API fills it from config
	// VulkanDevice is the Vulkan index the upscaler runs on, resolved from
	// hwprobe (Vulkan picks by index, not render node). Index 0 is valid.
	VulkanDevice int `json:"vulkan_device,omitempty"`
}

// DefaultRenderNode is a last-resort fallback for when no probe has chosen a
// node: the first render node present (renderD128 on most single-GPU
// systems). Real node choice is probe-driven (hwprobe.NodeFor), because
// numbering says nothing about which GPU is which.
func DefaultRenderNode() string {
	if m, _ := filepath.Glob("/dev/dri/renderD*"); len(m) > 0 {
		sort.Strings(m)
		return m[0]
	}
	return "/dev/dri/renderD128"
}

// CmdSpec is a complete ffmpeg invocation plus what it will produce.
type CmdSpec struct {
	Args        []string `json:"args"`
	SemKey      string   `json:"sem_key"`
	HWDecode    bool     `json:"hw_decode"`
	Container   string   `json:"container"`
	ExpectAudio int      `json:"expect_audio"`
	ExpectSubs  int      `json:"expect_subs"`
}

// Clip restricts a build to a span (previews). Zero value = whole file.
type Clip struct {
	Start, Dur float64 // Dur == 0: no -ss/-t (the input is already exactly the span)
	NoAudio    bool    // video only (quality-search samples)

	// Neural-upscale chunks encode a numbered PNG sequence rather than a file:
	InputArgs []string // input options placed before -i (e.g. -framerate)
	ScaleW    int      // > 0: resize to exactly ScaleW×ScaleH instead of fitting a class
	ScaleH    int
	RGBInput  bool   // frames are RGB: convert to BT.709 YUV explicitly, not swscale's BT.601 default
	Container string // overrides the preview default (mp4); chunks use mkv so they concatenate cleanly
}

// FFmpeg is the binary name.
var FFmpeg = "ffmpeg"

// CRFForQuality maps the 0..100 knob to the software-CRF scale.
func CRFForQuality(q int) int {
	q = clamp(q, 0, 100)
	return clamp(23-int(math.Round(float64(q-50)/5)), 12, 34)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func (s Settings) node() string {
	if s.RenderNode != "" {
		return s.RenderNode
	}
	return DefaultRenderNode()
}

func (s Settings) tenBit() bool { return s.BitDepth != 8 }

// Normalize fills defaults.
func (s *Settings) Normalize() {
	if s.Quality == 0 {
		s.Quality = 60
	}
	if s.Codec == "" {
		s.Codec = HEVC
	}
	if s.Speed == "" {
		s.Speed = "medium"
	}
	if s.BitDepth != 8 {
		s.BitDepth = 10
	}
	if s.Codec == H264 {
		s.BitDepth = 8 // 10-bit H.264 barely plays anywhere
	}
	if s.Deinterlace == "" {
		s.Deinterlace = "auto"
	}
	if s.Container == "" {
		s.Container = "auto"
	}
	if s.AudioPCMTarget == "" {
		s.AudioPCMTarget = "flac"
	}
	if s.UpscaleTo > 0 {
		s.VMAFTarget = 0
		s.MaxHeight = 0 // a downscale cap and an upscale target contradict
		if s.UpscalePreset == "" {
			s.UpscalePreset = upscale.DefaultPreset
		}
		// The preset decides the tier; the field only records it. An unknown
		// preset is left as a shader job so Build reports the unknown name.
		s.UpscaleTier = upscale.TierShader
		if p, ok := upscale.Get(s.UpscalePreset); ok {
			s.UpscaleTier = p.Tier
		}
	}
}

// Build constructs the primary command and (for hw-decode pipelines) a
// software-decode fallback. clip != nil builds a browser-playable
// preview sample (mp4, first audio track as AAC stereo, no subtitles).
func Build(s Settings, src *media.Probe, outPath string, clip *Clip) (primary, fallback *CmdSpec, err error) {
	s.Normalize()
	v := src.Video()
	if v == nil {
		return nil, nil, fmt.Errorf("no video stream")
	}
	container := "mp4"
	if clip != nil && clip.Container != "" {
		container = clip.Container
	}
	if clip == nil {
		if container, err = ChooseContainer(s, src); err != nil {
			return nil, nil, err
		}
	}

	streams := planStreams(s, src, container, clip != nil)
	if clip != nil && clip.NoAudio {
		streams = streamPlan{maps: []string{"-map", fmt.Sprintf("0:%d", v.Index)}, codecs: []string{"-an", "-sn", "-dn"}}
	}
	if s.VideoCopy {
		if s.UpscaleTo > 0 {
			return nil, nil, fmt.Errorf("upscaling needs a re-encode; it can't be combined with video copy")
		}
		return buildCopy(s, src, v, outPath, container, streams), nil, nil
	}
	video, err := videoArgs(s)
	if err != nil {
		return nil, nil, err
	}

	// MaxHeight is a resolution class cap (1080 = fit inside 1920×1080),
	// so letterboxed 1920×802 is never "above" a 1080p cap.
	cw, ch, cx, cy, crop := parseCrop(s.Crop, v.Width, v.Height)
	fitW, fitH := v.Width, v.Height
	if crop {
		fitW, fitH = cw, ch
	}
	scaleW, scaleH, scale := res.Fit(fitW, fitH, s.MaxHeight)
	if clip != nil && clip.ScaleW > 0 {
		scaleW, scaleH, scale = clip.ScaleW, clip.ScaleH, true
	}
	upW, upH, up := UpscaleSize(s, v) // ok=false: already at/above target, plain encode
	deint := s.Deinterlace == "on" || (s.Deinterlace == "auto" && v.Interlaced())
	srcTen := v.BitDepth() >= 10
	tonemap := s.TonemapHDR && v.HDRType() != "" && v.HDRType() != "dolby_vision"
	if up && v.HDRType() != "" && !tonemap {
		return nil, nil, fmt.Errorf("HDR sources can't be upscaled unless tone-mapped to SDR")
	}
	if up {
		// The upscaler's RGB is encoded as BT.709 whatever the source used, so
		// the source's own colour tags (e.g. BT.601 SD) would be wrong.
		video = append(video, "-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709")
	} else {
		video = append(video, colorArgs(s, v)...)
	}

	var pre []string
	if clip != nil && clip.Dur > 0 {
		pre = []string{"-ss", fmt.Sprintf("%.3f", clip.Start), "-t", fmt.Sprintf("%.3f", clip.Dur)}
	}

	assemble := func(devices, decode, filters []string, hw bool) *CmdSpec {
		args := []string{"-hide_banner", "-loglevel", "warning", "-nostdin",
			"-progress", "pipe:1", "-nostats", "-stats_period", "0.5"}
		args = append(args, devices...)
		args = append(args, decode...)
		args = append(args, pre...)
		if clip != nil {
			args = append(args, clip.InputArgs...)
		}
		args = append(args, "-i", src.Format.Filename)
		args = append(args, streams.maps...)
		if len(filters) > 0 {
			args = append(args, "-vf", strings.Join(filters, ","))
		}
		args = append(args, video...)
		args = append(args, streams.codecs...)
		args = append(args, muxArgs(container)...)
		if s.Codec == HEVC && container == "mp4" {
			args = append(args, "-tag:v", "hvc1")
		}
		if clip == nil {
			args = append(args, SplitArgs(s.ExtraArgs)...)
		}
		args = append(args, "-y", outPath)
		return &CmdSpec{Args: args, SemKey: string(s.Backend), HWDecode: hw,
			Container: container, ExpectAudio: streams.nAudio, ExpectSubs: streams.nSubs}
	}

	// An upscale's own denoise param (see upscale.DenoiseFilter): cleaning grain
	// at the source's resolution, before crop/tonemap are done and the picture
	// is handed to the upscaler.
	var denoise float64
	if up {
		if p, ok := upscale.Get(s.UpscalePreset); ok && p.Tier == upscale.TierShader {
			denoise = p.Resolve(s.UpscaleParams)["denoise"]
		}
	}

	// Software pre-filters shared by every sw-decode path.
	swPre := []string{}
	if clip != nil && clip.RGBInput {
		// RGB in: go to YUV with BT.709 (and tag it) before anything else, so no
		// later stage falls back to swscale's BT.601 default. 4:4:4 keeps the
		// chroma until the encoder's own format step.
		swPre = append(swPre, "scale=out_color_matrix=bt709:out_range=tv", "format=yuv444p")
	}
	if crop {
		swPre = append(swPre, fmt.Sprintf("crop=%d:%d:%d:%d", cw, ch, cx, cy))
	}
	if deint && (s.Backend == SW || up) {
		swPre = append(swPre, "bwdif=mode=send_frame") // upscaling always filters on the CPU side
	}
	if f := upscale.DenoiseFilter(denoise); f != "" {
		swPre = append(swPre, f)
	}
	if tonemap {
		swPre = append(swPre,
			"zscale=t=linear:npl=100", "format=gbrpf32le", "zscale=p=bt709",
			"tonemap=tonemap=hable:desat=0", "zscale=t=bt709:m=bt709:r=tv")
	}

	if up {
		return buildUpscale(s, upscale.Spec{W: upW, H: upH, Preset: s.UpscalePreset, Params: s.UpscaleParams},
			assemble, swPre, srcTen || tonemap, crop || tonemap || deint || denoise > 0)
	}

	switch s.Backend {
	case SW, "":
		s.Backend = SW
		f := append([]string{}, swPre...)
		if scale {
			f = append(f, fmt.Sprintf("scale=%d:%d:flags=lanczos", scaleW, scaleH))
		}
		return assemble(nil, nil, f, false), nil, nil

	case QSV, VAAPI:
		devices := []string{"-init_hw_device", "vaapi=va:" + s.node()}
		var decode, hwFilter, fbFilter []string
		fmtOut := "nv12"
		if s.tenBit() {
			fmtOut = "p010le"
		}
		upFmt := "nv12"
		if srcTen || tonemap {
			upFmt = "p010le"
		}
		if s.Backend == QSV {
			devices = append(devices, "-init_hw_device", "qsv=qsv@va", "-filter_hw_device", "qsv")
			decode = []string{"-hwaccel", "qsv", "-hwaccel_output_format", "qsv"}
			vpp := "vpp_qsv=format=" + fmtOut
			if scale {
				vpp += fmt.Sprintf(":w=%d:h=%d", scaleW, scaleH)
			}
			if deint {
				vpp += ":deinterlace=2"
			}
			// Hardware path crops on the GPU; the fallback already cropped
			// on the CPU before upload.
			fbFilter = []string{vpp}
			if crop {
				vpp += fmt.Sprintf(":cw=%d:ch=%d:cx=%d:cy=%d", cw, ch, cx, cy)
			}
			hwFilter = []string{vpp}
		} else {
			devices = append(devices, "-filter_hw_device", "va")
			decode = []string{"-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi"}
			if deint {
				hwFilter = append(hwFilter, "deinterlace_vaapi")
			}
			sc := "scale_vaapi=format=" + fmtOut
			if scale {
				sc += fmt.Sprintf(":w=%d:h=%d", scaleW, scaleH)
			}
			hwFilter = append(hwFilter, sc)
			fbFilter = hwFilter
		}
		upload := append(append([]string{}, swPre...), "format="+upFmt, "hwupload=extra_hw_frames=64")
		fb := assemble(devices, nil, append(upload, fbFilter...), false)
		if tonemap || (crop && s.Backend == VAAPI) {
			// Tone-mapping (and cropping for VA-API) are CPU filters:
			// the sw-decode path IS the primary.
			return fb, nil, nil
		}
		return assemble(devices, decode, hwFilter, true), fb, nil

	case NVENC:
		fmtOut := "yuv420p"
		if s.tenBit() {
			fmtOut = "p010le"
		}
		var hwf []string
		if deint {
			hwf = append(hwf, "yadif_cuda")
		}
		sc := "scale_cuda=format=" + fmtOut
		if scale {
			sc = fmt.Sprintf("scale_cuda=%d:%d:format=%s", scaleW, scaleH, fmtOut)
		}
		hwf = append(hwf, sc)
		upload := append(append([]string{}, swPre...), "format="+fmtOut, "hwupload_cuda")
		if scale {
			upload = append(upload, fmt.Sprintf("scale_cuda=%d:%d", scaleW, scaleH))
		}
		fb := assemble([]string{"-init_hw_device", "cuda=cu", "-filter_hw_device", "cu"}, nil, upload, false)
		if tonemap || crop {
			return fb, nil, nil
		}
		return assemble(nil, []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"}, hwf, true), fb, nil
	}
	return nil, nil, fmt.Errorf("unknown backend %q", s.Backend)
}

// UpscaleSize is the output size of an upscale job: the source (or its
// black-bar crop) fitted to class s.UpscaleTo. ok is false when there is
// nothing to upscale, i.e. the source already meets the target.
func UpscaleSize(s Settings, v *media.Stream) (w, h int, ok bool) {
	fw, fh := v.Width, v.Height
	if cw, ch, _, _, crop := parseCrop(s.Crop, v.Width, v.Height); crop {
		fw, fh = cw, ch
	}
	return res.Up(fw, fh, s.UpscaleTo)
}

// StillFilters returns the -vf filter lists for a single-frame A/B: a is the
// standard software Lanczos resize (the baseline any upscaler must beat), b
// runs the real upscale chain and needs `-init_hw_device vulkan=vk:N
// -filter_hw_device vk`. Both apply the same crop and deinterlace as Build,
// and both produce w×h. Stills are 8-bit SDR; they judge detail, not colour.
func StillFilters(s Settings, v *media.Stream) (a, b []string, w, h int, err error) {
	s.Normalize()
	w, h, ok := UpscaleSize(s, v)
	if !ok {
		return nil, nil, 0, 0, fmt.Errorf("nothing to upscale: the source already meets the %dp target", s.UpscaleTo)
	}
	var pre []string
	if cw, ch, cx, cy, crop := parseCrop(s.Crop, v.Width, v.Height); crop {
		pre = append(pre, fmt.Sprintf("crop=%d:%d:%d:%d", cw, ch, cx, cy))
	}
	if s.Deinterlace == "on" || (s.Deinterlace == "auto" && v.Interlaced()) {
		pre = append(pre, "bwdif=mode=send_frame")
	}
	// A is the plain, un-denoised baseline throughout — like every other B-only
	// tuning knob (sigmoid, deband, sharpness), denoise only ever affects B.
	a = append(append([]string{}, pre...),
		fmt.Sprintf("scale=%d:%d:flags=lanczos:in_color_matrix=%s", w, h, srcMatrix(v)), "format=rgb24")
	if s.UpscaleTier == upscale.TierNeural {
		return a, nil, w, h, nil // b comes from the neural upscaler (see NeuralStill)
	}
	bPre := pre
	if p, ok := upscale.Get(s.UpscalePreset); ok {
		if f := upscale.DenoiseFilter(p.Resolve(s.UpscaleParams)["denoise"]); f != "" {
			bPre = append(append([]string{}, pre...), f)
		}
	}
	// Both sides stay RGB into the PNG, skipping a lossy YUV round trip.
	chain, err := upscale.Spec{W: w, H: h, Preset: s.UpscalePreset, Params: s.UpscaleParams}.RGBChain("nv12", "rgba")
	if err != nil {
		return nil, nil, 0, 0, err
	}
	b = append(append(append([]string{}, bPre...), chain...), "format=rgb24")
	return a, b, w, h, nil
}

// srcMatrix is the YCbCr matrix a source is decoded with: its own tag when it
// has one, otherwise the same guess libplacebo and players make (BT.709 from
// 1280 wide or above 576 lines, BT.601 below).
func srcMatrix(v *media.Stream) string {
	switch v.ColorSpace {
	case "", "unknown", "reserved":
		if v.Width >= 1280 || v.Height > 576 {
			return "bt709"
		}
		return "bt601"
	}
	return "auto"
}

// buildUpscale is the fifth chain shape: the picture goes through libplacebo
// on a Vulkan device, so unlike the other backends the scaler isn't part of
// the encoder's own filter. Vulkan frames can't be handed back to VA-API/QSV
// surfaces on current Mesa, so frames cross system memory around the Vulkan
// stage (see upscale.Spec.Chain).
//
// The primary decodes on the GPU; the fallback decodes in software. When any
// CPU filter is needed (crop, tone-map, deinterlace) the software path is the
// only one, as with tone-mapping on the other backends. srcTen says the frames
// entering the upscaler are 10-bit; cpuFilters says a CPU-side filter is needed.
func buildUpscale(s Settings, spec upscale.Spec,
	assemble func(devices, decode, filters []string, hw bool) *CmdSpec,
	swPre []string, srcTen, cpuFilters bool) (primary, fallback *CmdSpec, err error) {

	if s.UpscaleTier != upscale.TierShader {
		return nil, nil, fmt.Errorf("%s upscaling is not built by encode.Build", s.UpscaleTier)
	}
	inFmt, outFmt := "nv12", "nv12"
	if srcTen {
		inFmt = "p010le"
	}
	if s.tenBit() {
		outFmt = "p010le"
	}
	chain, err := spec.Chain(inFmt, outFmt, s.Backend == VAAPI)
	if err != nil {
		return nil, nil, err
	}

	// Encoder device(s) first, then the Vulkan device the filters run on.
	var devices, decode []string
	switch s.Backend {
	case QSV:
		devices = []string{"-init_hw_device", "vaapi=va:" + s.node(), "-init_hw_device", "qsv=qsv@va"}
		decode = []string{"-hwaccel", "qsv", "-hwaccel_device", "qsv", "-hwaccel_output_format", "qsv"}
	case VAAPI:
		devices = []string{"-init_hw_device", "vaapi=va:" + s.node()}
		decode = []string{"-hwaccel", "vaapi", "-hwaccel_device", "va", "-hwaccel_output_format", "vaapi"}
	case NVENC:
		devices = []string{"-init_hw_device", "cuda=cu"}
		decode = []string{"-hwaccel", "cuda", "-hwaccel_device", "cu", "-hwaccel_output_format", "cuda"}
	}
	devices = append(devices, "-init_hw_device", "vulkan=vk:"+itoa(s.VulkanDevice), "-filter_hw_device", "vk")

	fb := assemble(devices, nil, append(append([]string{}, swPre...), chain...), false)
	fb.SemKey = "vulkan"
	if cpuFilters || decode == nil {
		return fb, nil, nil
	}
	prim := assemble(devices, decode, append([]string{"hwdownload"}, chain...), true)
	prim.SemKey = "vulkan"
	return prim, fb, nil
}

// buildCopy is a quick fix: video copied bit-exact, audio/subtitles per
// the stream plan, re-muxed with hvc1 + faststart where MP4.
func buildCopy(s Settings, src *media.Probe, v *media.Stream, out, container string, st streamPlan) *CmdSpec {
	args := []string{"-hide_banner", "-loglevel", "warning", "-nostdin",
		"-progress", "pipe:1", "-nostats", "-stats_period", "0.5",
		"-fflags", "+genpts", // old AVI/MPEG files often lack usable timestamps
		"-i", src.Format.Filename}
	args = append(args, st.maps...)
	args = append(args, "-c:v", "copy")
	args = append(args, st.codecs...)
	args = append(args, muxArgs(container)...)
	if v.CodecName == "hevc" && container == "mp4" {
		args = append(args, "-tag:v", "hvc1")
	}
	args = append(args, "-y", out)
	return &CmdSpec{Args: args, SemKey: "copy", Container: container,
		ExpectAudio: st.nAudio, ExpectSubs: st.nSubs}
}

// parseCrop validates a "w:h:x:y" crop against the frame.
func parseCrop(spec string, fw, fh int) (w, h, x, y int, ok bool) {
	if spec == "" {
		return 0, 0, 0, 0, false
	}
	if n, err := fmt.Sscanf(spec, "%d:%d:%d:%d", &w, &h, &x, &y); err != nil || n != 4 {
		return 0, 0, 0, 0, false
	}
	if w <= 0 || h <= 0 || x < 0 || y < 0 || x+w > fw || y+h > fh || (w == fw && h == fh) {
		return 0, 0, 0, 0, false
	}
	return w &^ 1, h &^ 1, x, y, true
}

// videoArgs returns the encoder + rate-control args.
func videoArgs(s Settings) ([]string, error) {
	crf := CRFForQuality(s.Quality)
	sp := speedFor(s.Backend, s.Codec, s.Speed)
	switch {
	case s.Backend == SW && s.Codec == HEVC:
		pix := "yuv420p10le"
		if !s.tenBit() {
			pix = "yuv420p"
		}
		a := []string{"-c:v", "libx265", "-crf", itoa(crf), "-preset", sp, "-pix_fmt", pix}
		if s.Tune == "animation" || s.Tune == "grain" {
			a = append(a, "-tune", s.Tune)
		}
		return append(a, "-x265-params", "log-level=error"), nil
	case s.Backend == SW && s.Codec == AV1:
		pix := "yuv420p10le"
		if !s.tenBit() {
			pix = "yuv420p"
		}
		a := []string{"-c:v", "libsvtav1", "-crf", itoa(clamp(crf+11, 1, 63)), "-preset", sp, "-pix_fmt", pix}
		if s.FilmGrain > 0 {
			a = append(a, "-svtav1-params", fmt.Sprintf("film-grain=%d:film-grain-denoise=1", clamp(s.FilmGrain, 1, 50)))
		}
		return a, nil
	case s.Backend == SW && s.Codec == H264:
		return []string{"-c:v", "libx264", "-crf", itoa(crf), "-preset", sp, "-pix_fmt", "yuv420p",
			"-profile:v", "high"}, nil
	case s.Backend == QSV && s.Codec == H264:
		return []string{"-c:v", "h264_qsv", "-rc_mode", "LA_ICQ", "-global_quality", itoa(crf),
			"-look_ahead", "1", "-look_ahead_depth", "40", "-bf", "3", "-preset", sp, "-profile:v", "high"}, nil
	case s.Backend == VAAPI && s.Codec == H264:
		return []string{"-c:v", "h264_vaapi", "-rc_mode", "ICQ", "-global_quality", itoa(crf),
			"-bf", "2", "-profile:v", "high"}, nil
	case s.Backend == QSV && s.Codec == HEVC:
		prof := "main10"
		if !s.tenBit() {
			prof = "main"
		}
		// Constant quality with a 40-frame lookahead: bits follow scene
		// complexity (action gets more, static scenes less). No bitrate
		// cap. global_quality == CRF-equivalent: measured on the Arc,
		// q60 (21) averages VMAF ~93 on grainy 1080p H.264 sources.
		// (adaptive_i/adaptive_b are ignored in LA_ICQ, so not passed.)
		return []string{"-c:v", "hevc_qsv", "-rc_mode", "LA_ICQ", "-global_quality", itoa(crf),
			"-look_ahead", "1", "-look_ahead_depth", "40", "-bf", "7",
			"-preset", sp, "-profile:v", prof}, nil
	case s.Backend == QSV && s.Codec == AV1:
		return []string{"-c:v", "av1_qsv", "-rc_mode", "ICQ", "-global_quality", itoa(crf),
			"-async_depth", "4", "-preset", sp}, nil
	case s.Backend == VAAPI && s.Codec == HEVC:
		prof := "main10"
		if !s.tenBit() {
			prof = "main"
		}
		// ICQ (constant quality, bitrate follows content), not CQP.
		return []string{"-c:v", "hevc_vaapi", "-rc_mode", "ICQ", "-global_quality", itoa(crf),
			"-bf", "2", "-profile:v", prof}, nil
	case s.Backend == VAAPI && s.Codec == AV1:
		return []string{"-c:v", "av1_vaapi", "-rc_mode", "ICQ", "-global_quality", itoa(crf)}, nil
	case s.Backend == NVENC:
		return []string{"-c:v", string(s.Codec) + "_nvenc", "-preset", sp, "-tune", "hq",
			"-rc", "vbr", "-cq", itoa(crf + 1), "-b:v", "0", "-spatial-aq", "1", "-temporal-aq", "1",
			"-rc-lookahead", "32"}, nil
	}
	return nil, fmt.Errorf("unsupported backend/codec %s/%s", s.Backend, s.Codec)
}

// speedFor maps the generic speed to each encoder's preset vocabulary.
func speedFor(b Backend, c Codec, speed string) string {
	i := 2
	for k, v := range Speeds {
		if v == speed {
			i = k
		}
	}
	switch {
	case b == SW && c == AV1:
		return []string{"10", "8", "6", "5", "4"}[i]
	case b == NVENC:
		return []string{"p3", "p4", "p5", "p6", "p7"}[i]
	case b == VAAPI:
		return "" // VA-API has no preset
	}
	return Speeds[i] // x265 + QSV share names
}

// colorArgs tags the output with the source's colour description so
// HDR10/HLG signalling survives (unless tone-mapped to SDR).
func colorArgs(s Settings, v *media.Stream) []string {
	if s.TonemapHDR && v.HDRType() != "" {
		return []string{"-color_primaries", "bt709", "-color_trc", "bt709", "-colorspace", "bt709"}
	}
	var a []string
	if v.ColorPrimaries != "" && v.ColorPrimaries != "unknown" {
		a = append(a, "-color_primaries", v.ColorPrimaries)
	}
	if v.ColorTransfer != "" && v.ColorTransfer != "unknown" {
		a = append(a, "-color_trc", v.ColorTransfer)
	}
	if v.ColorSpace != "" && v.ColorSpace != "unknown" {
		a = append(a, "-colorspace", v.ColorSpace)
	}
	return a
}

// streamPlan is the concrete mapping + per-output-stream codec args.
type streamPlan struct {
	maps   []string
	codecs []string
	nAudio int
	nSubs  int
}

// AudioDecision resolves what happens to one source audio stream.
func AudioDecision(s Settings, a media.Stream, container string) AudioTrack {
	for _, t := range s.Audio {
		if t.Index == a.Index {
			if t.Action == "" {
				t.Action = "copy"
			}
			return t
		}
	}
	if a.IsPCM() && s.AudioPCMTarget != "copy" {
		c := s.AudioPCMTarget
		if c == "flac" && container == "mp4" {
			c = "alac" // lossless too, and MP4/Apple-native
		}
		return AudioTrack{Index: a.Index, Action: "convert", Codec: c}
	}
	if container == "mp4" && !isMP4AudioSafe(a.CodecName) {
		return AudioTrack{Index: a.Index, Action: "convert", Codec: "aac"}
	}
	return AudioTrack{Index: a.Index, Action: "copy"}
}

func subKept(s Settings, sub media.Stream, container string) bool {
	for _, t := range s.Subs {
		if t.Index == sub.Index && t.Action == "drop" {
			return false
		}
	}
	return container != "mp4" || sub.IsTextSubtitle()
}

func planStreams(s Settings, src *media.Probe, container string, preview bool) streamPlan {
	p := streamPlan{}
	v := src.Video()
	p.maps = append(p.maps, "-map", fmt.Sprintf("0:%d", v.Index))

	if preview {
		// Browser sample: first non-dropped audio → AAC stereo, no subs.
		for _, a := range src.Audios() {
			if AudioDecision(s, a, container).Action != "drop" {
				p.maps = append(p.maps, "-map", fmt.Sprintf("0:%d", a.Index))
				p.codecs = append(p.codecs, "-c:a", "aac", "-b:a", "192k", "-ac", "2")
				p.nAudio = 1
				break
			}
		}
		p.codecs = append(p.codecs, "-sn", "-dn")
		return p
	}

	for _, a := range src.Audios() {
		d := AudioDecision(s, a, container)
		if d.Action == "drop" {
			continue
		}
		k := itoa(p.nAudio)
		p.maps = append(p.maps, "-map", fmt.Sprintf("0:%d", a.Index))
		if d.Action == "copy" {
			p.codecs = append(p.codecs, "-c:a:"+k, "copy")
		} else {
			codec := d.Codec
			if codec == "" {
				codec = "flac"
			}
			if codec == "opus" {
				codec = "libopus"
			}
			p.codecs = append(p.codecs, "-c:a:"+k, codec)
			ch := a.Channels
			if d.Channels > 0 {
				ch = d.Channels
				p.codecs = append(p.codecs, "-ac:a:"+k, itoa(d.Channels))
			}
			if codec != "flac" {
				br := d.Bitrate
				if br <= 0 {
					br = defaultAudioKbps(codec, ch)
				}
				p.codecs = append(p.codecs, "-b:a:"+k, fmt.Sprintf("%dk", br))
			}
		}
		p.nAudio++
	}

	for _, sub := range src.Subtitles() {
		if !subKept(s, sub, container) {
			continue
		}
		p.maps = append(p.maps, "-map", fmt.Sprintf("0:%d", sub.Index))
		p.nSubs++
	}
	if p.nSubs > 0 {
		if container == "mp4" {
			p.codecs = append(p.codecs, "-c:s", "mov_text")
		} else {
			p.codecs = append(p.codecs, "-c:s", "copy")
		}
	}
	if container == "mkv" {
		p.maps = append(p.maps, "-map", "0:t?")
		p.codecs = append(p.codecs, "-c:t", "copy")
	}
	return p
}

// defaultAudioKbps picks a transparent bitrate by codec and channels.
func defaultAudioKbps(codec string, ch int) int {
	if ch <= 0 {
		ch = 2
	}
	per := map[string]int{"aac": 96, "libopus": 64, "eac3": 112}[codec]
	if per == 0 {
		per = 96
	}
	br := per * ch
	if br < 128 {
		br = 128
	}
	if codec == "eac3" && br > 1024 {
		br = 1024
	}
	return br
}

// muxArgs returns container-level args.
func muxArgs(container string) []string { return muxArgsFrom(container, 0) }

// muxArgsFrom is muxArgs with metadata and chapters taken from input idx (the
// neural mux reads them from the original source, its second input).
func muxArgsFrom(container string, idx int) []string {
	i := itoa(idx)
	if container == "mp4" {
		return []string{"-map_metadata", i, "-map_chapters", i, "-movflags", "+faststart", "-f", "mp4"}
	}
	return []string{"-map_metadata", i, "-map_chapters", i, "-default_mode", "infer_no_subs", "-f", "matroska"}
}

// ChooseContainer resolves "auto". With PreferMP4 (Apple-friendly: HEVC
// tagged hvc1, moov atom first) any source goes to MP4 when every kept
// track fits; otherwise the source container is kept (MP4 only when the
// source is MP4). Bitmap subs, styled ASS subs and copied TrueHD/DTS/FLAC
// audio force MKV rather than being silently degraded.
func ChooseContainer(s Settings, src *media.Probe) (string, error) {
	if src.Video() == nil {
		return "", fmt.Errorf("no video stream")
	}
	switch s.Container {
	case "mkv":
		return "mkv", nil
	case "mp4":
		return "mp4", nil // incompatible streams are converted/dropped by planStreams
	}
	name := strings.ToLower(src.Format.Filename)
	srcMP4 := strings.HasSuffix(name, ".mp4") || strings.HasSuffix(name, ".m4v")
	if !srcMP4 && !s.PreferMP4 {
		return "mkv", nil
	}
	if !fitsMP4(s, src) {
		return "mkv", nil
	}
	return "mp4", nil
}

func fitsMP4(s Settings, src *media.Probe) bool {
	if s.VideoCopy {
		switch src.Video().CodecName {
		case "h264", "hevc", "av1", "mpeg4":
		default:
			return false // e.g. VC-1, WMV, MPEG-2 can't be copied into MP4
		}
	}
	for _, sub := range src.Subtitles() {
		if !subKept(s, sub, "mkv") {
			continue
		}
		switch sub.CodecName {
		case "subrip", "srt", "mov_text", "webvtt", "text":
		default:
			return false // PGS/VobSub images or styled ASS
		}
	}
	for _, a := range src.Audios() {
		d := AudioDecision(s, a, "mkv")
		if d.Action == "copy" && !isMP4AudioSafe(a.CodecName) {
			return false
		}
		if d.Action == "convert" && d.Codec == "opus" {
			return false
		}
	}
	return true
}

func isMP4AudioSafe(codec string) bool {
	switch codec {
	case "aac", "mp3", "ac3", "eac3", "alac", "opus":
		return true
	}
	return false
}

// SplitArgs splits user-supplied extra args with simple shell quoting.
func SplitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	has := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, has = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if has {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	if has {
		out = append(out, cur.String())
	}
	return out
}

// CommandString renders argv as a copy-pasteable shell command.
func CommandString(args []string) string {
	parts := []string{FFmpeg}
	for _, a := range args {
		if a == "" || strings.ContainsAny(a, " '\"$`\\|&;()<>*?[]#~!") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		parts = append(parts, a)
	}
	return strings.Join(parts, " ")
}

func itoa(i int) string { return fmt.Sprintf("%d", i) }
