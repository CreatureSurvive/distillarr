// Package encode builds ffmpeg command lines for every encoder
// backend (software x265/SVT-AV1, Intel QSV, VA-API, NVENC) from one
// quality knob, and derives fallbacks.
package encode

import (
	"fmt"
	"math"
	"strings"

	"mediatrans/internal/media"
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
)

// AllBackends is the preference-ordered default chain.
var AllBackends = []Backend{QSV, VAAPI, NVENC, SW}

// Settings fully describes one encode.
type Settings struct {
	Codec          Codec   `json:"codec"`
	Backend        Backend `json:"backend"`
	Quality        int     `json:"quality"`          // 0..100, 60 default
	Preset         string  `json:"preset,omitempty"` // "" = backend default
	MaxHeight      int     `json:"max_height,omitempty"` // 0 = keep resolution
	TonemapHDR     bool    `json:"tonemap_hdr,omitempty"`
	FilmGrain      int     `json:"film_grain,omitempty"` // SVT-AV1 only
	AudioPCMTarget string  `json:"audio_pcm_target,omitempty"` // flac|aac|eac3|copy
	Container      string  `json:"container,omitempty"` // mkv|mp4|auto (default auto)
	RenderNode     string  `json:"render_node,omitempty"` // /dev/dri/renderD128
}

// DefaultRenderNode is only a last-resort fallback — real node choice
// always comes from hwprobe results (on this host the Arc A380 is
// renderD129; the UHD 630 is renderD128 — verify via the probe, not
// stale docs).
const DefaultRenderNode = "/dev/dri/renderD129"

// CmdSpec is a complete ffmpeg invocation.
type CmdSpec struct {
	Args     []string // argv after the binary
	SemKey   string   // hw semaphore key: qsv|vaapi|nvenc|sw
	HWDecode bool     // uses hw decode → eligible for sw-decode fallback retry
}

// FFmpeg is the binary name (overridable for tests).
var FFmpeg = "ffmpeg"

// CRFForQuality maps the 0..100 knob to the software-CRF scale.
func CRFForQuality(q int) int {
	if q < 0 {
		q = 0
	}
	if q > 100 {
		q = 100
	}
	return clamp(23 - int(math.Round(float64(q-50)/5)), 14, 30)
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

func node(s Settings) string {
	if s.RenderNode != "" {
		return s.RenderNode
	}
	return DefaultRenderNode
}

// Build constructs the primary command plus (for hw-decode pipelines)
// a software-decode fallback variant. src is the source probe.
func Build(s Settings, src *media.Probe, outPath string) (primary, fallback *CmdSpec, err error) {
	if s.Quality == 0 {
		s.Quality = 60
	}
	if s.AudioPCMTarget == "" {
		s.AudioPCMTarget = "flac"
	}
	container, err := ChooseContainer(s, src)
	if err != nil {
		return nil, nil, err
	}
	mux := muxArgs(container)
	scaleNeeded := s.MaxHeight > 0 && src.Video() != nil && src.Video().Height > s.MaxHeight

	switch s.Backend {
	case SW:
		spec, err := buildSW(s, src, outPath, container, scaleNeeded, mux)
		if err != nil {
			return nil, nil, err
		}
		return spec, nil, nil
	case QSV, VAAPI:
		spec, fb, err := buildIntel(s, src, outPath, container, scaleNeeded, mux)
		if err != nil {
			return nil, nil, err
		}
		return spec, fb, nil
	case NVENC:
		spec, fb, err := buildNVENC(s, src, outPath, container, scaleNeeded, mux)
		if err != nil {
			return nil, nil, err
		}
		return spec, fb, nil
	}
	return nil, nil, fmt.Errorf("unknown backend %q", s.Backend)
}

// buildSW builds libx265 / libsvtav1 invocations.
func buildSW(s Settings, src *media.Probe, out, container string, scale bool, mux []string) (*CmdSpec, error) {
	if s.TonemapHDR && scale {
		return nil, fmt.Errorf("tonemap + scale combination not supported on sw yet")
	}
	var filters []string
	pixFmt := "p010le"
	if s.Codec == AV1 {
		pixFmt = "yuv420p10le"
	}
	if s.TonemapHDR {
		filters = append(filters,
			"zscale=t=linear:npl=100",
			"format=gbrpf32le",
			"zscale=p=bt709",
			"tonemap=tonemap=hable:desat=0",
			"zscale=t=bt709:m=bt709:r=tv",
			"format="+pixFmt)
	} else if scale {
		filters = append(filters, fmt.Sprintf("scale=-2:%d:flags=lanczos,format=%s", s.MaxHeight, pixFmt))
	}

	crf := CRFForQuality(s.Quality)
	var video []string
	switch s.Codec {
	case HEVC:
		video = []string{"-c:v", "libx265", "-crf", itoa(crf), "-preset", presetOr(s.Preset, "medium"),
			"-pix_fmt", pixFmt, "-x265-params", "log-level=error"}
	case AV1:
		video = []string{"-c:v", "libsvtav1", "-crf", itoa(clampMin(crf+11, 55)),
			"-preset", presetOr(s.Preset, "6"), "-pix_fmt", pixFmt}
		if s.FilmGrain > 0 {
			video = append(video, "-svtav1-params",
				fmt.Sprintf("film-grain=%d:film-grain-denoise=1", s.FilmGrain))
		}
	}
	return assemble(s, src, out, container, nil, filters, video, mux)
}

func clampMin(v, min int) int {
	if v < min {
		return min
	}
	return v
}

// buildIntel builds QSV and VA-API pipelines with hw decode primary
// and software-decode fallback.
func buildIntel(s Settings, src *media.Probe, out, container string, scale bool, mux []string) (*CmdSpec, *CmdSpec, error) {
	if s.TonemapHDR {
		return nil, nil, fmt.Errorf("HDR tone-mapping requires the software backend")
	}

	hw := fmt.Sprintf("va:%s", node(s))
	scalePart := ""
	if scale {
		scalePart = fmt.Sprintf(":w=-2:h=%d", s.MaxHeight)
	}
	crf := CRFForQuality(s.Quality)

	var video []string
	switch {
	case s.Backend == QSV && s.Codec == HEVC:
		video = []string{"-c:v", "hevc_qsv", "-rc_mode", "LA_ICQ",
			"-global_quality", itoa(crf + 3), "-look_ahead", "1",
			"-preset", presetOr(s.Preset, "medium"), "-profile:v", "main10"}
	case s.Backend == QSV && s.Codec == AV1:
		video = []string{"-c:v", "av1_qsv", "-rc_mode", "ICQ",
			"-global_quality", itoa(crf + 3), "-async_depth", "4",
			"-preset", presetOr(s.Preset, "medium")}
	case s.Backend == VAAPI && s.Codec == HEVC:
		video = []string{"-c:v", "hevc_vaapi", "-rc_mode", "CQP",
			"-qp", itoa(crf + 2), "-bf", "2", "-profile:v", "main10"}
	case s.Backend == VAAPI && s.Codec == AV1:
		video = []string{"-c:v", "av1_vaapi", "-rc_mode", "CQP", "-qp", itoa(crf + 2)}
	default:
		return nil, nil, fmt.Errorf("unsupported backend/codec %s/%s", s.Backend, s.Codec)
	}

	var decodeHW, initHW []string
	var filterHW, filterFB []string
	if s.Backend == QSV {
		initHW = []string{"-init_hw_device", hw, "-init_hw_device", "qsv=qsv@va", "-filter_hw_device", "qsv"}
		decodeHW = []string{"-hwaccel", "qsv", "-hwaccel_output_format", "qsv"}
		filterHW = []string{fmt.Sprintf("vpp_qsv=format=p010le%s", scalePart)}
		filterFB = []string{"format=nv12|p010le", "hwupload=extra_hw_frames=64",
			fmt.Sprintf("vpp_qsv=format=p010le%s", scalePart)}
	} else {
		initHW = []string{"-init_hw_device", hw, "-filter_hw_device", "va"}
		decodeHW = []string{"-hwaccel", "vaapi", "-hwaccel_output_format", "vaapi"}
		filterHW = []string{fmt.Sprintf("scale_vaapi=format=p010le%s", scalePart)}
		filterFB = []string{"format=p010le", "hwupload=extra_hw_frames=32",
			fmt.Sprintf("scale_vaapi=format=p010le%s", scalePart)}
	}

	primary, err := assemble(s, src, out, container, initHW, mustConcat(decodeHW, filterHW), video, mux)
	if err != nil {
		return nil, nil, err
	}
	primary.HWDecode = true
	fb, err := assemble(s, src, out, container, initHW, filterFB, video, mux)
	if err != nil {
		return nil, nil, err
	}
	return primary, fb, nil
}

func mustConcat(a, b []string) []string {
	if len(a) == 0 {
		return b
	}
	return append(append([]string{}, a...), b...)
}

// buildNVENC builds CUDA pipelines.
func buildNVENC(s Settings, src *media.Probe, out, container string, scale bool, mux []string) (*CmdSpec, *CmdSpec, error) {
	if s.TonemapHDR {
		return nil, nil, fmt.Errorf("HDR tone-mapping requires the software backend")
	}
	crf := CRFForQuality(s.Quality)
	video := []string{"-c:v", string(s.Codec) + "_nvenc", "-preset", presetOr(s.Preset, "p5"),
		"-tune", "hq", "-rc", "vbr", "-cq", itoa(crf + 4), "-b:v", "0",
		"-spatial-aq", "1", "-temporal-aq", "1", "-pix_fmt", "p010le"}
	var filterHW, filterFB []string
	if scale {
		filterHW = []string{fmt.Sprintf("scale_cuda=-2:%d:format=p010le", s.MaxHeight)}
		filterFB = []string{fmt.Sprintf("format=p010le,hwupload_cuda,scale_cuda=-2:%d", s.MaxHeight)}
	}
	primary, err := assemble(s, src, out, container, nil, mustConcat([]string{"-hwaccel", "cuda",
		"-hwaccel_output_format", "cuda"}, filterHW), video, mux)
	if err != nil {
		return nil, nil, err
	}
	primary.HWDecode = true
	fb, err := assemble(s, src, out, container, nil, filterFB, video, mux)
	if err != nil {
		return nil, nil, err
	}
	return primary, fb, nil
}

// assemble composes the common scaffold.
func assemble(s Settings, src *media.Probe, out, container string, preInput []string,
	filters, video, mux []string) (*CmdSpec, error) {

	args := []string{"-hide_banner", "-loglevel", "warning", "-nostdin",
		"-progress", "pipe:1", "-nostats", "-stats_period", "0.5"}
	args = append(args, preInput...)
	args = append(args, "-i", src.Format.Filename)

	// Stream mapping: first video, all audio, subtitles (optional),
	// attachments for mkv (fonts for ASS subs).
	args = append(args, "-map", "0:v:0", "-map", "0:a", "-map", "0:s?")
	if container == "mkv" {
		args = append(args, "-map", "0:t?")
	}
	if len(filters) > 0 {
		args = append(args, "-vf", strings.Join(filters, ","))
	}
	args = append(args, video...)

	// Audio: copy by default, per-output-stream override for PCM.
	args = append(args, "-c:a", "copy")
	pcmTarget := s.AudioPCMTarget
	if pcmTarget == "" || pcmTarget == "copy" {
		pcmTarget = "flac"
	}
	for ord, a := range src.Audios() {
		if a.IsPCM() {
			switch pcmTarget {
			case "flac":
				args = append(args, "-c:a:"+itoa(ord), "flac")
			case "aac":
				args = append(args, "-c:a:"+itoa(ord), "aac",
					"-b:a:"+itoa(ord), itoa(channelsBitrate(a.Channels, 96000)))
			case "eac3":
				args = append(args, "-c:a:"+itoa(ord), "eac3",
					"-b:a:"+itoa(ord), itoa(clampInt(channelsBitrate(a.Channels, 128000), 96000, 1536000)))
			}
		}
	}

	// Subtitles: copy in mkv; mov_text in mp4 (text subs only — bitmap
	// subs force mkv in ChooseContainer).
	if container == "mp4" {
		args = append(args, "-c:s", "mov_text")
		if s.Codec == HEVC {
			args = append(args, "-tag:v", "hvc1")
		}
	} else {
		args = append(args, "-c:s", "copy")
	}

	args = append(args, mux...)
	args = append(args, out)
	return &CmdSpec{Args: args, SemKey: string(s.Backend)}, nil
}

func channelsBitrate(ch, perCh int) int {
	if ch <= 0 {
		ch = 2
	}
	br := ch * perCh
	if br < 192000 {
		br = 192000
	}
	return br
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// muxArgs returns muxer args for the chosen container.
func muxArgs(container string) []string {
	if container == "mp4" {
		return []string{"-map_metadata", "0", "-map_chapters", "0",
			"-movflags", "+faststart", "-f", "mp4"}
	}
	return []string{"-map_metadata", "0", "-map_chapters", "0",
		"-default_mode", "infer_no_subs", "-f", "matroska"}
}

// ChooseContainer resolves the "auto" policy: keep mp4 only when every
// stream is mp4-compatible, else mkv.
func ChooseContainer(s Settings, src *media.Probe) (string, error) {
	switch s.Container {
	case "mkv":
		return "mkv", nil
	case "mp4":
		for _, sub := range src.Subtitles() {
			if !sub.IsTextSubtitle() {
				return "mkv", nil // silently fall back rather than fail
			}
		}
		return "mp4", nil
	}
	// auto
	if src.Video() == nil {
		return "", fmt.Errorf("no video stream")
	}
	if src.Format.FormatName != "" && strings.Contains(src.Format.FormatName, "mp4") &&
		strings.HasSuffix(strings.ToLower(src.Format.Filename), ".mp4") &&
		src.Video().CodecName != "" && isMP4VideoSafe(src.Video().CodecName) {
		for _, sub := range src.Subtitles() {
			if !sub.IsTextSubtitle() {
				return "mkv", nil
			}
		}
		for _, a := range src.Audios() {
			if !isMP4AudioSafe(a.CodecName) && !a.IsPCM() {
				return "mkv", nil
			}
		}
		return "mp4", nil
	}
	return "mkv", nil
}

func isMP4VideoSafe(codec string) bool {
	switch codec {
	case "h264", "hevc", "av1":
		return true
	}
	return false
}

func isMP4AudioSafe(codec string) bool {
	switch codec {
	case "aac", "mp3", "ac3", "eac3", "alac":
		return true
	}
	return false
}

func presetOr(p, def string) string {
	if p == "" {
		return def
	}
	return p
}

func itoa(i int) string { return fmt.Sprintf("%d", i) }
