package recs

import (
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/imagesubs"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

func TestRecommendFoldsInImageSubsSidecarMode(t *testing.T) {
	f := file(1080, 8_000_000, "h264", 2015)
	f.Subs = []store.SubStream{{Index: 8, Codec: imagesubs.CodecPGS, Lang: "eng", Default: true}}
	cfg := config.Default()
	cfg.ImageSubsMode = "sidecar"
	off := false
	cfg.ImageSubsKeepOriginal = &off

	r := Recommend(f, cfg)
	if r.Settings.ImageSubsMode != "sidecar" {
		t.Errorf("Settings.ImageSubsMode = %q, want sidecar", r.Settings.ImageSubsMode)
	}
	dropped := false
	for _, tr := range r.Settings.Subs {
		if tr.Index == 8 && tr.Action == "drop" {
			dropped = true
		}
	}
	if !dropped {
		t.Errorf("sidecar mode with keep_original off should drop the PGS track: %+v", r.Settings.Subs)
	}
}

func TestRecommendImageSubsKeepOriginalDropsNothing(t *testing.T) {
	f := file(1080, 8_000_000, "h264", 2015)
	f.Subs = []store.SubStream{{Index: 8, Codec: imagesubs.CodecPGS, Lang: "eng", Default: true}}
	cfg := config.Default()
	cfg.ImageSubsMode = "sidecar" // ImageSubsKeepOriginal left nil = default on

	r := Recommend(f, cfg)
	for _, tr := range r.Settings.Subs {
		if tr.Action == "drop" {
			t.Errorf("default keep_original=true must not drop the image track: %+v", r.Settings.Subs)
		}
	}
}

func TestRecommendOCRModeUsesCachedResult(t *testing.T) {
	f := file(1080, 8_000_000, "h264", 2015)
	f.Subs = []store.SubStream{{Index: 8, Codec: imagesubs.CodecPGS, Lang: "eng", Default: true}}
	f.OCRJSON = imagesubs.MarshalResult(imagesubs.OCRResult{TrackIndex: 8, Confidence: 90, SRTName: "x.srt"})
	cfg := config.Default()
	cfg.ImageSubsMode = "ocr"
	off := false
	cfg.ImageSubsKeepOriginal = &off

	r := Recommend(f, cfg)
	dropped := false
	for _, tr := range r.Settings.Subs {
		if tr.Index == 8 && tr.Action == "drop" {
			dropped = true
		}
	}
	if !dropped {
		t.Errorf("a successful cached OCR result with keep_original off should drop track 8: %+v", r.Settings.Subs)
	}
}

func TestRecommendOCRModeFailedResultKeepsTrack(t *testing.T) {
	f := file(1080, 8_000_000, "h264", 2015)
	f.Subs = []store.SubStream{{Index: 8, Codec: imagesubs.CodecPGS, Lang: "eng", Default: true}}
	f.OCRJSON = imagesubs.MarshalResult(imagesubs.OCRResult{TrackIndex: 8, Confidence: 40, Failed: true})
	cfg := config.Default()
	cfg.ImageSubsMode = "ocr"
	off := false
	cfg.ImageSubsKeepOriginal = &off

	r := Recommend(f, cfg)
	for _, tr := range r.Settings.Subs {
		if tr.Action == "drop" {
			t.Errorf("a failed OCR result must never drop the original track: %+v", r.Settings.Subs)
		}
	}
}
