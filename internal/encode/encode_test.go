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
	if prim.Container != "mkv" {
		t.Errorf("PCM→FLAC can't live in mp4; got %s", prim.Container)
	}
	if strings.Contains(a, "-map 0:3") {
		t.Error("dropped track still mapped")
	}
	if !strings.Contains(a, "-c:a:1 flac") || prim.ExpectAudio != 2 || prim.ExpectSubs != 1 {
		t.Errorf("want pcm→flac on output #1, 2 audio, 1 sub: %s (%d/%d)", a, prim.ExpectAudio, prim.ExpectSubs)
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
