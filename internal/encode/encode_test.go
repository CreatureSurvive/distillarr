package encode

import (
	"strings"
	"testing"

	"mediatrans/internal/media"
)

func probe(file, vcodec, pix, trc string, audio []string, subs []string) *media.Probe {
	p := &media.Probe{Format: media.Format{Filename: file}}
	p.Streams = append(p.Streams, media.Stream{Index: 0, CodecType: "video", CodecName: vcodec,
		Width: 1920, Height: 1080, PixFmt: pix, ColorTransfer: trc, ColorPrimaries: "bt2020", ColorSpace: "bt2020nc"})
	for i, a := range audio {
		p.Streams = append(p.Streams, media.Stream{Index: 1 + i, CodecType: "audio", CodecName: a, Channels: 6})
	}
	for i, s := range subs {
		p.Streams = append(p.Streams, media.Stream{Index: 1 + len(audio) + i, CodecType: "subtitle", CodecName: s})
	}
	return p
}

func joined(t *testing.T, s Settings, p *media.Probe, clip *Clip) (string, *CmdSpec, *CmdSpec) {
	t.Helper()
	prim, fb, err := Build(s, p, "/out.tmp", clip)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(prim.Args, " "), prim, fb
}

func TestQSVTenAndEightBit(t *testing.T) {
	p := probe("/m/a.mkv", "h264", "yuv420p", "", []string{"ac3"}, nil)
	a, prim, fb := joined(t, Settings{Codec: HEVC, Backend: QSV, RenderNode: "/dev/dri/renderD129"}, p, nil)
	for _, want := range []string{"vaapi=va:/dev/dri/renderD129", "-hwaccel qsv", "vpp_qsv=format=p010le", "-profile:v main10", "-c:a:0 copy"} {
		if !strings.Contains(a, want) {
			t.Errorf("10-bit QSV missing %q in %s", want, a)
		}
	}
	if fb == nil || !prim.HWDecode || strings.Contains(strings.Join(fb.Args, " "), "-hwaccel qsv") {
		t.Error("QSV needs a software-decode fallback without -hwaccel")
	}
	a, _, _ = joined(t, Settings{Codec: HEVC, Backend: QSV, BitDepth: 8}, p, nil)
	if !strings.Contains(a, "format=nv12") || !strings.Contains(a, "-profile:v main ") {
		t.Errorf("8-bit QSV should use nv12/main: %s", a)
	}
}

func TestAudioPlanAndContainer(t *testing.T) {
	p := probe("/m/a.mp4", "h264", "yuv420p", "", []string{"aac", "pcm_s24le", "ac3"}, []string{"mov_text"})
	s := Settings{Codec: HEVC, Backend: SW, Audio: []AudioTrack{{Index: 3, Action: "drop"}}}
	a, prim, _ := joined(t, s, p, nil)
	if prim.Container != "mp4" {
		t.Errorf("PCM in an MP4 source should stay MP4 (as ALAC); got %s", prim.Container)
	}
	if strings.Contains(a, "-map 0:3") {
		t.Error("dropped track still mapped")
	}
	if !strings.Contains(a, "-c:a:1 alac") || prim.ExpectAudio != 2 || prim.ExpectSubs != 1 {
		t.Errorf("want pcm→alac on output #1, 2 audio, 1 sub: %s (%d/%d)", a, prim.ExpectAudio, prim.ExpectSubs)
	}
	p2 := probe("/m/b.mp4", "h264", "yuv420p", "", []string{"aac"}, []string{"mov_text"})
	_, prim, _ = joined(t, Settings{Codec: HEVC, Backend: SW}, p2, nil)
	if prim.Container != "mp4" {
		t.Errorf("clean mp4 should stay mp4, got %s", prim.Container)
	}
}

func TestHDRPassthroughAndTonemap(t *testing.T) {
	p := probe("/m/h.mkv", "hevc", "yuv420p10le", "smpte2084", []string{"eac3"}, []string{"hdmv_pgs_subtitle"})
	a, _, _ := joined(t, Settings{Codec: HEVC, Backend: QSV}, p, nil)
	if !strings.Contains(a, "-color_trc smpte2084") || !strings.Contains(a, "-color_primaries bt2020") {
		t.Errorf("HDR10 signalling not carried: %s", a)
	}
	a, _, fb := joined(t, Settings{Codec: HEVC, Backend: QSV, TonemapHDR: true}, p, nil)
	if fb != nil || strings.Contains(a, "-hwaccel qsv") || !strings.Contains(a, "tonemap=") || !strings.Contains(a, "-color_trc bt709") {
		t.Errorf("tone-map must be sw-decode → CPU tonemap → QSV encode, tagged bt709: %s", a)
	}
}

func TestPreviewClip(t *testing.T) {
	p := probe("/m/a.mkv", "h264", "yuv420p", "", []string{"truehd", "ac3"}, []string{"subrip"})
	a, prim, _ := joined(t, Settings{Codec: AV1, Backend: SW, Speed: "slow"}, p, &Clip{Start: 100, Dur: 20})
	if prim.Container != "mp4" || !strings.Contains(a, "-ss 100.000 -t 20.000 -i") {
		t.Errorf("clip must be mp4 with input seek: %s", a)
	}
	if !strings.Contains(a, "-c:a aac") || !strings.Contains(a, "-sn") || !strings.Contains(a, "-preset 5") {
		t.Errorf("clip audio/subs/speed wrong: %s", a)
	}
}

func TestSplitArgs(t *testing.T) {
	got := SplitArgs(`-x265-params "aq-mode=3:psy-rd=2" -g 240`)
	if len(got) != 4 || got[1] != "aq-mode=3:psy-rd=2" {
		t.Errorf("SplitArgs: %q", got)
	}
}

func TestPreferMP4(t *testing.T) {
	mkv := probe("/m/a.mkv", "h264", "yuv420p", "", []string{"ac3", "aac"}, []string{"subrip"})
	a, prim, _ := joined(t, Settings{Codec: HEVC, Backend: SW, PreferMP4: true}, mkv, nil)
	if prim.Container != "mp4" || !strings.Contains(a, "-tag:v hvc1") || !strings.Contains(a, "+faststart") || !strings.Contains(a, "-c:s mov_text") {
		t.Errorf("MKV with MP4-safe tracks should become MP4 + hvc1 + faststart: %s", a)
	}
	pgs := probe("/m/b.mkv", "h264", "yuv420p", "", []string{"ac3"}, []string{"hdmv_pgs_subtitle"})
	if _, prim, _ = joined(t, Settings{Codec: HEVC, Backend: SW, PreferMP4: true}, pgs, nil); prim.Container != "mkv" {
		t.Error("image subtitles must keep MKV")
	}
	thd := probe("/m/c.mkv", "h264", "yuv420p", "", []string{"truehd"}, nil)
	if _, prim, _ = joined(t, Settings{Codec: HEVC, Backend: SW, PreferMP4: true}, thd, nil); prim.Container != "mkv" {
		t.Error("copied TrueHD must keep MKV")
	}
}

func TestResolutionCapUsesClass(t *testing.T) {
	scope4k := probe("/m/s.mkv", "h264", "yuv420p", "", []string{"ac3"}, nil)
	scope4k.Streams[0].Width, scope4k.Streams[0].Height = 3840, 1600
	a, _, _ := joined(t, Settings{Codec: HEVC, Backend: QSV, MaxHeight: 1080}, scope4k, nil)
	if !strings.Contains(a, "vpp_qsv=format=p010le:w=1920:h=800") {
		t.Errorf("3840×1600 capped at 1080p should fit 1920×800: %s", a)
	}
	scope := probe("/m/b.mkv", "h264", "yuv420p", "", []string{"ac3"}, nil)
	scope.Streams[0].Width, scope.Streams[0].Height = 1920, 802
	a, _, _ = joined(t, Settings{Codec: HEVC, Backend: SW, MaxHeight: 1080}, scope, nil)
	if strings.Contains(a, "scale=") {
		t.Errorf("1920×802 is 1080p: a 1080p cap must not scale it: %s", a)
	}
}

func TestCropPaths(t *testing.T) {
	p := probe("/m/lb.mkv", "h264", "yuv420p", "", []string{"ac3"}, nil)
	s := Settings{Codec: HEVC, Backend: QSV, Crop: "1920:800:0:140"}
	a, _, fb := joined(t, s, p, nil)
	if !strings.Contains(a, "-hwaccel qsv") || !strings.Contains(a, ":cw=1920:ch=800:cx=0:cy=140") {
		t.Errorf("QSV should crop on the GPU: %s", a)
	}
	fa := strings.Join(fb.Args, " ")
	if !strings.Contains(fa, "crop=1920:800:0:140") || strings.Contains(fa, "cw=") {
		t.Errorf("QSV fallback crops once, on the CPU: %s", fa)
	}
	a, _, fb = joined(t, Settings{Codec: HEVC, Backend: VAAPI, Crop: "1920:800:0:140"}, p, nil)
	if fb != nil || strings.Contains(a, "-hwaccel vaapi") || !strings.Contains(a, "crop=1920:800:0:140,format=nv12,hwupload") {
		t.Errorf("VA-API crops before upload: %s", a)
	}
	a, _, _ = joined(t, Settings{Codec: HEVC, Backend: SW, Crop: "1920:800:0:140", MaxHeight: 720}, p, nil)
	if !strings.Contains(a, "crop=1920:800:0:140,scale=1280:532") {
		t.Errorf("crop then scale the picture: %s", a)
	}
	if a, _, _ = joined(t, Settings{Codec: HEVC, Backend: SW, Crop: "1920:1200:0:0"}, p, nil); strings.Contains(a, "crop=") {
		t.Error("an out-of-frame crop must be ignored")
	}
}

func TestH264AndCopy(t *testing.T) {
	p := probe("/m/a.mkv", "mpeg2video", "yuv420p", "", []string{"ac3"}, nil)
	a, _, _ := joined(t, Settings{Codec: H264, Backend: QSV}, p, nil)
	if !strings.Contains(a, "h264_qsv") || !strings.Contains(a, "format=nv12") || strings.Contains(a, "p010") {
		t.Errorf("H.264 must be 8-bit: %s", a)
	}
	hev1 := probe("/m/b.mp4", "hevc", "yuv420p10le", "", []string{"aac", "pcm_s16le"}, nil)
	a, prim, fb := joined(t, Settings{VideoCopy: true, Container: "mp4"}, hev1, nil)
	if fb != nil || !strings.Contains(a, "-c:v copy") || !strings.Contains(a, "-tag:v hvc1") ||
		!strings.Contains(a, "+faststart") || !strings.Contains(a, "-c:a:1 alac") || prim.Container != "mp4" {
		t.Errorf("quick fix should copy video, tag hvc1, faststart, pcm→alac: %s", a)
	}
	avi := probe("/m/c.avi", "vc1", "yuv420p", "", []string{"ac3"}, nil)
	if _, prim, _ = joined(t, Settings{VideoCopy: true, PreferMP4: true}, avi, nil); prim.Container != "mkv" {
		t.Errorf("VC-1 can't be copied into MP4: %s", prim.Container)
	}
}

func TestUpscaleChains(t *testing.T) {
	src := probe("/m/a.mkv", "h264", "yuv420p", "", []string{"ac3"}, nil)
	src.Streams[0].Width, src.Streams[0].Height = 854, 480
	base := Settings{Codec: HEVC, UpscaleTo: 1080, UpscalePreset: "film-lanczos", VulkanDevice: 0, RenderNode: "/dev/dri/renderD129"}

	v := base
	v.Backend = VAAPI
	a, prim, fb := joined(t, v, src, nil)
	for _, want := range []string{"vaapi=va:/dev/dri/renderD129", "vulkan=vk:0", "-filter_hw_device vk",
		"-hwaccel vaapi -hwaccel_device va", "-vf hwdownload,format=nv12,hwupload,libplacebo=w=1920:h=1080",
		":format=x2bgr10le,hwdownload,format=x2bgr10le,scale=out_color_matrix=bt709:out_range=tv,format=p010le,hwupload_vaapi",
		"-color_primaries bt709 -color_trc bt709 -colorspace bt709", "hevc_vaapi"} {
		if !strings.Contains(a, want) {
			t.Errorf("VA-API upscale missing %q in %s", want, a)
		}
	}
	// The probe helper tags sources bt2020: upscaled RGB is BT.709 whatever the source said.
	if strings.Contains(a, "bt2020") {
		t.Errorf("an upscale must not inherit the source's colour tags: %s", a)
	}
	if prim.SemKey != "vulkan" || !prim.HWDecode || fb == nil || fb.HWDecode || strings.Contains(strings.Join(fb.Args, " "), "-hwaccel") {
		t.Errorf("want a vulkan-keyed hw-decode primary with a software-decode fallback: %+v / %+v", prim, fb)
	}

	q := base
	q.Backend = QSV
	a, _, _ = joined(t, q, src, nil)
	for _, want := range []string{"qsv=qsv@va", "-hwaccel qsv -hwaccel_device qsv", "hevc_qsv", "vulkan=vk:0"} {
		if !strings.Contains(a, want) {
			t.Errorf("QSV upscale missing %q in %s", want, a)
		}
	}
	if strings.Contains(a, "hwupload_vaapi") || strings.Contains(a, "vpp_qsv") {
		t.Errorf("QSV takes software frames from the upscaler; no VA-API upload or vpp: %s", a)
	}

	w := base
	w.Backend, w.VulkanDevice = SW, 1
	a, prim, fb = joined(t, w, src, nil)
	if fb != nil || prim.HWDecode || prim.SemKey != "vulkan" || !strings.Contains(a, "vulkan=vk:1") || strings.Contains(a, "-hwaccel") {
		t.Errorf("SW upscale is software decode only, on the requested Vulkan device: %s", a)
	}

	// CPU-side filters force the software path, as tone-mapping does elsewhere.
	c := base
	c.Backend, c.Crop = QSV, "854:356:0:62"
	a, _, fb = joined(t, c, src, nil)
	if fb != nil || strings.Contains(a, "-hwaccel") || !strings.Contains(a, "crop=854:356:0:62,format=nv12,hwupload,libplacebo=w=1920:h=800") {
		t.Errorf("crop must run on the CPU before the upscaler, and size the target from the cropped frame: %s", a)
	}

	// 10-bit source keeps 10-bit surfaces through the round trip.
	ten := probe("/m/t.mkv", "hevc", "yuv420p10le", "", []string{"ac3"}, nil)
	ten.Streams[0].Width, ten.Streams[0].Height = 1280, 720
	u := base
	u.Backend, u.UpscaleTo = QSV, 2160
	a, _, _ = joined(t, u, ten, nil)
	if !strings.Contains(a, "hwdownload,format=p010le,hwupload,libplacebo=w=3840:h=2160") {
		t.Errorf("10-bit source must stay p010le through the upscaler: %s", a)
	}
}

func TestUpscaleStillFilters(t *testing.T) {
	sd := probe("/m/a.mkv", "h264", "yuv420p", "", nil, nil)
	sd.Streams[0].Width, sd.Streams[0].Height = 720, 400
	sd.Streams[0].ColorSpace, sd.Streams[0].ColorPrimaries = "", ""
	a, b, w, h, err := StillFilters(Settings{UpscaleTo: 1080, UpscalePreset: "film-lanczos"}, &sd.Streams[0])
	if err != nil || w != 1920 || h != 1066 {
		t.Fatalf("want 1920x1066, got %dx%d %v", w, h, err)
	}
	if got := strings.Join(a, ","); got != "scale=1920:1066:flags=lanczos:in_color_matrix=bt601,format=rgb24" {
		t.Errorf("baseline must decode SD with BT.601 and land in RGB: %s", got)
	}
	if got := strings.Join(b, ","); !strings.HasPrefix(got, "format=nv12,hwupload,libplacebo=w=1920:h=1066") ||
		!strings.HasSuffix(got, ":format=rgba,hwdownload,format=rgba,format=rgb24") || strings.Contains(got, "scale=out_color") {
		t.Errorf("upscaled still stays RGB end to end: %s", got)
	}
	hd := probe("/m/b.mkv", "h264", "yuv420p", "", nil, nil)
	hd.Streams[0].ColorSpace = ""
	a, _, _, _, _ = StillFilters(Settings{UpscaleTo: 2160}, &hd.Streams[0])
	if !strings.Contains(strings.Join(a, ","), "in_color_matrix=bt709") {
		t.Errorf("an untagged HD source is BT.709: %v", a)
	}
	tagged := probe("/m/c.mkv", "h264", "yuv420p", "", nil, nil) // helper tags it bt2020nc
	a, _, _, _, _ = StillFilters(Settings{UpscaleTo: 2160}, &tagged.Streams[0])
	if !strings.Contains(strings.Join(a, ","), "in_color_matrix=auto") {
		t.Errorf("a tagged source keeps its own matrix: %v", a)
	}
	if _, _, _, _, err := StillFilters(Settings{UpscaleTo: 1080}, &hd.Streams[0]); err == nil {
		t.Error("a 1080p source has nothing to upscale to 1080p")
	}
}

func TestUpscaleDenoise(t *testing.T) {
	sd := probe("/m/a.mkv", "h264", "yuv420p", "", []string{"ac3"}, nil)
	sd.Streams[0].Width, sd.Streams[0].Height = 854, 480

	// Off by default: identical to a build with no denoise param at all.
	off, _, err := Build(Settings{Codec: HEVC, Backend: QSV, UpscaleTo: 1080, UpscalePreset: "film-lanczos",
		RenderNode: "/dev/dri/renderD129", VulkanDevice: 0}, sd, "/o.tmp", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(off.Args, " "), "hqdn3d") {
		t.Errorf("denoise must be off by default: %s", strings.Join(off.Args, " "))
	}

	on := Settings{Codec: HEVC, Backend: QSV, UpscaleTo: 1080, UpscalePreset: "film-lanczos",
		UpscaleParams: map[string]float64{"denoise": 2}, RenderNode: "/dev/dri/renderD129", VulkanDevice: 0}
	prim, fb, err := Build(on, sd, "/o.tmp", nil)
	if err != nil {
		t.Fatal(err)
	}
	// swPre (and hence hqdn3d) never reaches the hw-decode primary (encode.go:
	// buildUpscale only applies it via the sw-decode path), so requesting
	// denoise must force sw-decode-only, same as crop/tonemap/deinterlace do.
	if fb != nil || prim.HWDecode || strings.Contains(strings.Join(prim.Args, " "), "-hwaccel") {
		t.Errorf("denoise must force the sw-decode-only path: fb=%v prim=%+v", fb, prim)
	}
	a := strings.Join(prim.Args, " ")
	if !strings.Contains(a, "-vf hqdn3d=8.0:6.0:0:0,format=nv12,hwupload,libplacebo=") {
		t.Errorf("hqdn3d must run before the upscaler, with no temporal component: %s", a)
	}

	// Neural presets aren't shader presets (isn't resolved via upscale.Get's
	// Tier check), so a denoise param alongside one must not change Build's
	// existing "not built by Build" refusal into some other error.
	_, _, err = Build(Settings{Codec: HEVC, Backend: QSV, UpscaleTo: 1080, UpscalePreset: "neural-anime",
		UpscaleParams: map[string]float64{"denoise": 2}}, sd, "/o.tmp", nil)
	if err == nil || !strings.Contains(err.Error(), "not built by encode.Build") {
		t.Errorf("want the usual neural-tier refusal, got: %v", err)
	}
}

func TestUpscaleStillDenoise(t *testing.T) {
	sd := probe("/m/a.mkv", "h264", "yuv420p", "", nil, nil)
	sd.Streams[0].Width, sd.Streams[0].Height = 854, 480

	a0, b0, _, _, err := StillFilters(Settings{UpscaleTo: 1080, UpscalePreset: "film-lanczos"}, &sd.Streams[0])
	if err != nil || strings.Contains(strings.Join(b0, ","), "hqdn3d") {
		t.Fatalf("denoise off by default: %v %v", b0, err)
	}
	a1, b1, _, _, err := StillFilters(Settings{UpscaleTo: 1080, UpscalePreset: "film-lanczos",
		UpscaleParams: map[string]float64{"denoise": 1}}, &sd.Streams[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(a0, ",") != strings.Join(a1, ",") {
		t.Errorf("A is the honest baseline: denoise must never touch it: %v vs %v", a0, a1)
	}
	bj := strings.Join(b1, ",")
	if !strings.HasPrefix(bj, "hqdn3d=4.0:3.0:0:0,format=nv12,hwupload,") {
		t.Errorf("B must denoise before the upscale chain: %s", bj)
	}
}

func TestUpscaleRejectsHDR(t *testing.T) {
	hdr := probe("/m/h.mkv", "hevc", "yuv420p10le", "smpte2084", []string{"eac3"}, nil)
	hdr.Streams[0].Width, hdr.Streams[0].Height = 1280, 720
	if _, _, err := Build(Settings{Codec: HEVC, Backend: QSV, UpscaleTo: 2160}, hdr, "/o.tmp", nil); err == nil {
		t.Error("HDR can't be upscaled without tone-mapping: RGB output would be tagged SDR")
	}
	a, _, _ := joined(t, Settings{Codec: HEVC, Backend: QSV, UpscaleTo: 2160, TonemapHDR: true}, hdr, nil)
	if !strings.Contains(a, "tonemap=") || !strings.Contains(a, "libplacebo=") || !strings.Contains(a, "-colorspace bt709") {
		t.Errorf("tone-mapped HDR may be upscaled, tagged bt709: %s", a)
	}
}

func TestUpscaleNoOpsAndErrors(t *testing.T) {
	fhd := probe("/m/a.mkv", "h264", "yuv420p", "", []string{"ac3"}, nil) // 1920x1080
	a, prim, _ := joined(t, Settings{Codec: HEVC, Backend: QSV, UpscaleTo: 1080}, fhd, nil)
	if strings.Contains(a, "libplacebo") || prim.SemKey != "qsv" {
		t.Errorf("a source already at the target class is a plain encode: %s", a)
	}
	sd := probe("/m/b.mkv", "h264", "yuv420p", "", []string{"ac3"}, nil)
	sd.Streams[0].Width, sd.Streams[0].Height = 854, 480
	if _, _, err := Build(Settings{Codec: HEVC, Backend: QSV, UpscaleTo: 1080, UpscalePreset: "neural-anime"}, sd, "/o.tmp", nil); err == nil {
		t.Error("the neural tier must not be built by Build")
	}
	if _, _, err := Build(Settings{Codec: HEVC, Backend: QSV, UpscaleTo: 1080, UpscalePreset: "nope"}, sd, "/o.tmp", nil); err == nil {
		t.Error("an unknown preset must error")
	}
	s := Settings{UpscaleTo: 1080, MaxHeight: 720}
	s.Normalize()
	if s.MaxHeight != 0 || s.UpscalePreset == "" {
		t.Errorf("upscale clears the downscale cap and defaults a preset: %+v", s)
	}
}

func TestUpscaleSettings(t *testing.T) {
	s := Settings{UpscaleTo: 1080, VMAFTarget: 93}
	s.Normalize()
	if s.VMAFTarget != 0 {
		t.Error("an upscale job must not carry a VMAF target: scoring against its own source is meaningless")
	}
	if s.UpscaleTier != "shader" || s.UpscaleOutput != "" {
		t.Errorf("upscale defaults: tier %q; output must be left for the API to fill from config, got %q", s.UpscaleTier, s.UpscaleOutput)
	}
	off := Settings{VMAFTarget: 93}
	off.Normalize()
	if off.VMAFTarget != 93 || off.UpscaleTier != "" || off.UpscaleOutput != "" {
		t.Errorf("a non-upscale job must be left alone: %+v", off)
	}

	p := probe("/m/a.mkv", "h264", "yuv420p", "", []string{"ac3"}, nil)
	if _, _, err := Build(Settings{VideoCopy: true, UpscaleTo: 1080}, p, "/out.tmp", nil); err == nil {
		t.Error("video copy + upscale must be rejected")
	}
}
