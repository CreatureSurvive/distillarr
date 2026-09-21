package encode

import (
	"fmt"
	"strings"

	"mediatrans/internal/media"
)

// BuildPreview constructs a short-sample encode with the exact video
// settings of a proposed job (input seek + duration), for A/B compare:
// browser-safe audio (AAC), no subtitles, mp4 output. The SemKey is
// the backend's; decode follows the same hw path as a real job.
func BuildPreview(s Settings, src *media.Probe, outPath string, start, dur float64) (primary, fallback *CmdSpec, err error) {
	if s.Quality == 0 {
		s.Quality = 60
	}
	scaleNeeded := s.MaxHeight > 0 && src.Video() != nil && src.Video().Height > s.MaxHeight
	crf := CRFForQuality(s.Quality)

	audioArgs := []string{"-map", "0:v:0", "-map", "0:a:0?", "-c:a", "aac", "-b:a", "192k", "-sn"}
	mp4Mux := []string{"-movflags", "+faststart", "-f", "mp4"}
	if s.Codec == HEVC {
		mp4Mux = []string{"-movflags", "+faststart", "-tag:v", "hvc1", "-f", "mp4"}
	}

	build := func(filters, video []string) []string {
		args := []string{"-hide_banner", "-loglevel", "error", "-nostdin",
			"-ss", fmt.Sprintf("%.3f", start), "-t", fmt.Sprintf("%.3f", dur)}
		// decoder/device flags are spliced before "-i" by withInputChain
		args = append(args, "-i", src.Format.Filename)
		if len(filters) > 0 {
			args = append(args, "-vf", strings.Join(filters, ","))
		}
		args = append(args, video...)
		args = append(args, audioArgs...)
		args = append(args, mp4Mux...)
		args = append(args, outPath)
		return args
	}

	videoFor := func() []string {
		switch {
		case s.Backend == QSV && s.Codec == HEVC:
			return []string{"-c:v", "hevc_qsv", "-rc_mode", "LA_ICQ",
				"-global_quality", itoa(crf + 3), "-look_ahead", "1",
				"-preset", presetOr(s.Preset, "medium"), "-profile:v", "main10"}
		case s.Backend == QSV && s.Codec == AV1:
			return []string{"-c:v", "av1_qsv", "-rc_mode", "ICQ",
				"-global_quality", itoa(crf + 3), "-async_depth", "4",
				"-preset", presetOr(s.Preset, "medium")}
		case s.Backend == VAAPI && s.Codec == HEVC:
			return []string{"-c:v", "hevc_vaapi", "-rc_mode", "CQP", "-qp", itoa(crf + 2),
				"-bf", "2", "-profile:v", "main10"}
		case s.Backend == VAAPI && s.Codec == AV1:
			return []string{"-c:v", "av1_vaapi", "-rc_mode", "CQP", "-qp", itoa(crf + 2)}
		case s.Backend == NVENC:
			return []string{"-c:v", string(s.Codec) + "_nvenc", "-preset", "p5", "-tune", "hq",
				"-rc", "vbr", "-cq", itoa(crf + 4), "-b:v", "0",
				"-spatial-aq", "1", "-temporal-aq", "1", "-pix_fmt", "p010le"}
		default: // SW
			if s.Codec == AV1 {
				v := []string{"-c:v", "libsvtav1", "-crf", itoa(clampMin(crf+11, 55)),
					"-preset", presetOr(s.Preset, "6"), "-pix_fmt", "yuv420p10le"}
				if s.FilmGrain > 0 {
					v = append(v, "-svtav1-params",
						fmt.Sprintf("film-grain=%d:film-grain-denoise=1", s.FilmGrain))
				}
				return v
			}
			return []string{"-c:v", "libx265", "-crf", itoa(crf), "-preset",
				presetOr(s.Preset, "medium"), "-pix_fmt", "p010le",
				"-x265-params", "log-level=error"}
		}
	}

	scalePart := ""
	if scaleNeeded {
		scalePart = fmt.Sprintf(":w=-2:h=%d", s.MaxHeight)
	}

	switch s.Backend {
	case SW:
		var filters []string
		if scaleNeeded {
			filters = append(filters, fmt.Sprintf("scale=-2:%d:flags=lanczos,format=p010le", s.MaxHeight))
		}
		return &CmdSpec{Args: build(filters, videoFor()), SemKey: "sw"}, nil, nil
	case QSV, VAAPI:
		hw := fmt.Sprintf("vaapi=va:%s", node(s))
		var initHW, decodeHW, filterHW, filterFB []string
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
		prim := &CmdSpec{Args: withInputChain(build(filterHW, videoFor()), initHW, decodeHW),
			SemKey: string(s.Backend), HWDecode: true}
		fb := &CmdSpec{Args: withInputChain(build(filterFB, videoFor()), initHW, nil),
			SemKey: string(s.Backend)}
		return prim, fb, nil
	case NVENC:
		var filterHW []string
		if scaleNeeded {
			filterHW = []string{fmt.Sprintf("scale_cuda=-2:%d:format=p010le", s.MaxHeight)}
		}
		prim := &CmdSpec{Args: withInputChain(build(filterHW, videoFor()),
			nil, []string{"-hwaccel", "cuda", "-hwaccel_output_format", "cuda"}),
			SemKey: "nvenc", HWDecode: true}
		return prim, nil, nil
	}
	return nil, nil, fmt.Errorf("unknown backend %q", s.Backend)
}

// withInputChain splices decoder/device flags in front of "-i SRC".
func withInputChain(args, initHW, decodeHW []string) []string {
	i := indexOf(args, "-i")
	if i < 0 {
		return args
	}
	pre := append([]string{}, args[:i]...)
	post := append([]string{}, args[i:]...)
	out := append(pre, initHW...)
	out = append(out, decodeHW...)
	return append(out, post...)
}

func indexOf(args []string, flag string) int {
	for i, a := range args {
		if a == flag {
			return i
		}
	}
	return -1
}

// BuildSourceCut builds a stream-copy sample of the source for the A/B
// left side. Falls back to a fast proxy re-encode for browsers.
func BuildSourceCut(src *media.Probe, outPath string, start, dur float64, proxy bool) []string {
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin",
		"-ss", fmt.Sprintf("%.3f", start), "-t", fmt.Sprintf("%.3f", dur),
		"-i", src.Format.Filename}
	if proxy {
		args = append(args, "-map", "0:v:0", "-map", "0:a:0?",
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "16", "-pix_fmt", "yuv420p",
			"-c:a", "aac", "-b:a", "192k", "-sn",
			"-movflags", "+faststart", "-f", "mp4")
	} else {
		args = append(args, "-map", "0:v:0", "-map", "0:a:0?",
			"-c", "copy", "-movflags", "+faststart", "-f", "mp4")
	}
	return append(args, outPath)
}

// SourceNeedsProxy reports whether the source video codec can't
// direct-play in browsers (so the left side needs an x264 proxy).
func SourceNeedsProxy(src *media.Probe) bool {
	v := src.Video()
	if v == nil {
		return true
	}
	switch v.CodecName {
	case "h264":
		return false // universally playable
	default:
		return true
	}
}
