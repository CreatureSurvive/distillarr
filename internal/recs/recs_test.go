// SPDX-License-Identifier: GPL-3.0-or-later

package recs

import (
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

func file(h int, br int64, codec string, year int) *store.File {
	w := h * 16 / 9
	return &store.File{Library: "movies", Title: "x", Year: year, Size: br * 7200 / 8 * 11 / 10,
		Duration: 7200, VideoCodec: codec, Width: w, Height: h, FPS: 24, BitDepth: 8,
		VideoBitrate: br, TotalBitrate: br * 11 / 10}
}

// TestAudioPlanUsesRules covers "Estimates: recs size estimates
// use the planned audio bitrates": a TrueHD 7.1 track's estimate should
// follow AudioRules' explicit bitrate_by_channels (1024k for 7.1), not
// the generic per-codec default (kbpsOf's own eac3 formula gives 896k
// for 8 channels) — and should shrink far below leaving the track alone.
func TestAudioPlanUsesRules(t *testing.T) {
	f := file(1080, 8_000_000, "h264", 2015)
	f.Audio = []store.AudioStream{{Index: 1, Codec: "truehd", Channels: 8, BitRate: 6_000_000}}
	f.TotalBitrate = f.VideoBitrate + 6_500_000 // large enough that "other" isn't clamped to 0 below
	rules := map[string]encode.AudioRule{
		"truehd": {Action: "convert", Target: "eac3", BitrateByChannels: map[int]int{6: 640, 8: 1024}},
	}
	s := encode.Settings{Codec: encode.HEVC, ContainerGoal: "mp4_required", AudioRules: rules}
	s.Normalize()

	plans := audioPlan(f, s)
	if len(plans) != 1 || plans[0].Action != "convert" || plans[0].Target != "eac3" || plans[0].Bitrate != 1024 {
		t.Fatalf("want truehd 7.1 -> eac3 1024k: %+v", plans)
	}

	cfg := config.Default()
	withRule := Estimate(f, s, cfg)

	sNoRule := s
	sNoRule.AudioRules = nil
	withoutRule := Estimate(f, sNoRule, cfg)
	if withRule.EstOut >= withoutRule.EstOut {
		t.Errorf("converting 6Mb/s TrueHD to 1024k EAC3 should shrink the estimate: with=%d without=%d",
			withRule.EstOut, withoutRule.EstOut)
	}

	sDefaultBitrate := s
	sDefaultBitrate.AudioRules = nil
	sDefaultBitrate.Audio = []encode.AudioTrack{{Index: 1, Action: "convert", Codec: "eac3"}} // no explicit bitrate
	viaGenericDefault := Estimate(f, sDefaultBitrate, cfg)
	if withRule.EstOut == viaGenericDefault.EstOut {
		t.Errorf("rule's explicit 1024k should differ from kbpsOf's generic eac3 default (896k for 8ch): got equal %d", withRule.EstOut)
	}
}

func TestQualityIsPerFile(t *testing.T) {
	cfg := config.Default()
	ResolveBackend = func(string, encode.Codec) encode.Backend { return encode.QSV }
	starved := Recommend(file(1080, 1_800_000, "h264", 2015), cfg)
	rich := Recommend(file(1080, 25_000_000, "h264", 2015), cfg)
	sd := Recommend(file(480, 1_500_000, "h264", 2005), cfg)
	if starved.Settings.Quality <= rich.Settings.Quality {
		t.Errorf("bit-starved source should get higher quality than rich: %d vs %d",
			starved.Settings.Quality, rich.Settings.Quality)
	}
	if sd.Settings.Quality <= cfg.DefaultQuality {
		t.Errorf("SD source should be raised above default: %d", sd.Settings.Quality)
	}
	if len(rich.Why) == 0 || rich.Action != "transcode" || rich.Savings < 50 {
		t.Errorf("rich H.264 should be a strong candidate with reasons: %+v", rich)
	}
}

func TestSkipsModernAndDV(t *testing.T) {
	cfg := config.Default()
	if r := Recommend(file(1080, 6_000_000, "hevc", 2020), cfg); r.Action != "skip" {
		t.Errorf("HEVC should skip: %s", r.Reason)
	}
	f := file(2160, 40_000_000, "h264", 2020)
	f.HDR = "dolby_vision"
	if r := Recommend(f, cfg); r.Action != "caution" {
		t.Errorf("DV should be caution: %s", r.Action)
	}
}

func TestCalibrationMovesEstimate(t *testing.T) {
	cal.m = map[string]*bucket{}
	cfg := config.Default()
	f := file(1080, 8_000_000, "h264", 2015)
	s := Recommend(f, cfg).Settings
	before := Estimate(f, s, cfg).EstOut
	pred := ModelRatio(f, s)
	for i := 0; i < 5; i++ {
		RecordObservation(f, s, pred*1.5) // reality: 50% bigger than predicted
	}
	after := Estimate(f, s, cfg)
	if after.EstOut <= before || after.Samples != 5 {
		t.Errorf("estimate should rise after observations: %d → %d (%d samples)", before, after.EstOut, after.Samples)
	}
}

func TestBarsUseActivePicture(t *testing.T) {
	cfg := config.Default()
	full := file(1080, 3_000_000, "h264", 2015)
	bars := *full
	bars.CropW, bars.CropH, bars.CropY, bars.CropChecked = 1920, 800, 140, true
	a, b := Recommend(full, cfg), Recommend(&bars, cfg)
	if b.SrcBPP <= a.SrcBPP {
		t.Errorf("bits are spread over the real picture only: %.4f vs %.4f", b.SrcBPP, a.SrcBPP)
	}
	if b.Settings.Crop != "" {
		t.Error("cropping is off by default")
	}
	cfg.CropBars = true
	if c := Recommend(&bars, cfg); c.Settings.Crop != "1920:800:0:140" {
		t.Errorf("crop rect not set: %q", c.Settings.Crop)
	}
}

func TestCurveIsContinuous(t *testing.T) {
	// No jumps at old class boundaries: nearby sizes get nearby values.
	a := targetBPP(1280*720, encode.HEVC, encode.QSV, 60)
	b := targetBPP(1366*768, encode.HEVC, encode.QSV, 60)
	if b >= a || a/b > 1.1 {
		t.Errorf("720p %.5f vs 768p %.5f should be close and decreasing", a, b)
	}
}

func TestH264Target(t *testing.T) {
	cfg := config.Default()
	cfg.DefaultCodec = "h264"
	if r := Recommend(file(1080, 8_000_000, "h264", 2015), cfg); r.Action != "skip" {
		t.Errorf("H.264 → H.264 must not be recommended: %s", r.Reason)
	}
	if r := Recommend(file(576, 6_000_000, "mpeg2video", 2005), cfg); r.Settings.Codec != encode.H264 || r.Settings.BitDepth != 8 {
		t.Errorf("legacy MPEG-2 → 8-bit H.264 expected: %+v", r.Settings)
	}
}
