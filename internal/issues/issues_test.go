package issues

import (
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

func TestDetect(t *testing.T) {
	cfg := config.Default()
	f := &store.File{VideoCodec: "hevc", Container: "mp4", VideoTag: "hev1", MetaChecked: true, Faststart: 0,
		Audio: []store.AudioStream{{Codec: "aac"}, {Codec: "pcm_s24le"}}}
	got := Encode(Detect(f, cfg, false, false, false, false, "", ""))
	if got != ",no_hvc1,no_faststart,pcm_audio," {
		t.Errorf("got %s", got)
	}
	avi := &store.File{VideoCodec: "mpeg4", Container: "avi", Faststart: -1, Interlaced: true}
	if got := Encode(Detect(avi, cfg, true, false, false, false, "", "")); got != ",legacy_container,legacy_codec,interlaced,worth_reencoding," {
		t.Errorf("got %s", got)
	}
	odd := &store.File{VideoCodec: "hevc", Container: "mp4", VideoTag: "[0][0][0][0]", MetaChecked: true, Faststart: 1}
	if got := Encode(Detect(odd, cfg, false, false, false, false, "", "")); got != ",no_hvc1," {
		t.Errorf("non-hvc1 tag in MP4: got %s", got)
	}
	unread := &store.File{VideoCodec: "hevc", Container: "mp4", VideoTag: "", MetaChecked: true, Faststart: 1}
	if got := Detect(unread, cfg, false, false, false, false, "", ""); len(got) != 0 {
		t.Errorf("unreadable tag should not be flagged: %v", got)
	}
	clean := &store.File{VideoCodec: "hevc", Container: "mkv", Faststart: -1, MetaChecked: true, VideoTag: "[0][0][0][0]"}
	if got := Detect(clean, cfg, false, false, false, false, "", ""); len(got) != 0 {
		t.Errorf("clean MKV should have no issues: %v", got)
	}
	pending := &store.File{VideoCodec: "hevc", Container: "mkv", Faststart: -1, MetaChecked: true, VideoTag: "[0][0][0][0]"}
	if got := Encode(Detect(pending, cfg, false, false, true, false, "", "")); got != ",upgrade_pending," {
		t.Errorf("upgrade pending: got %s", got)
	}
	forces := &store.File{VideoCodec: "hevc", Container: "mkv", Faststart: -1, MetaChecked: true, VideoTag: "[0][0][0][0]"}
	if got := Encode(Detect(forces, cfg, false, false, false, true, "", "")); got != ",forces_transcode," {
		t.Errorf("forces transcode: got %s", got)
	}
}

func TestAudioBlocksMP4(t *testing.T) {
	// No audio_rules: a TrueHD track in MKV isn't Apple-MP4-safe -> blocked.
	blocked := &store.File{VideoCodec: "hevc", Container: "mkv",
		Audio: []store.AudioStream{{Codec: "truehd", Channels: 8}}}
	if got := Detect(blocked, config.Default(), false, false, false, false, "", ""); !contains(got, "audio_blocks_mp4") {
		t.Errorf("TrueHD with no rule should block MP4: %v", got)
	}
	if s := QuickFix(blocked, config.Default()); s.Container != "auto" {
		t.Errorf("QuickFix should route audio_blocks_mp4 through auto, not stay mkv: %+v", s)
	}

	// A convert rule resolves the track -> not blocked.
	cfg := config.Default()
	cfg.AudioRules = map[string]encode.AudioRule{"truehd": {Action: "convert", Target: "eac3"}}
	if got := Detect(blocked, cfg, false, false, false, false, "", ""); contains(got, "audio_blocks_mp4") {
		t.Errorf("a matching convert rule should clear the block: %v", got)
	}

	// Already MP4, or already an MP4-safe codec: never flagged.
	safe := &store.File{VideoCodec: "hevc", Container: "mkv",
		Audio: []store.AudioStream{{Codec: "aac", Channels: 2}}}
	if got := Detect(safe, config.Default(), false, false, false, false, "", ""); contains(got, "audio_blocks_mp4") {
		t.Errorf("AAC audio shouldn't block MP4: %v", got)
	}
	alreadyMP4 := &store.File{VideoCodec: "hevc", Container: "mp4",
		Audio: []store.AudioStream{{Codec: "truehd", Channels: 8}}}
	if got := Detect(alreadyMP4, config.Default(), false, false, false, false, "", ""); contains(got, "audio_blocks_mp4") {
		t.Errorf("an MP4 file is never flagged by this issue: %v", got)
	}
	legacy := &store.File{VideoCodec: "h264", Container: "avi",
		Audio: []store.AudioStream{{Codec: "truehd", Channels: 8}}}
	if got := Detect(legacy, config.Default(), false, false, false, false, "", ""); contains(got, "audio_blocks_mp4") {
		t.Errorf("legacy_container already covers this case: %v", got)
	}
}

func TestExtraLanguages(t *testing.T) {
	f := &store.File{Duration: 100,
		Audio: []store.AudioStream{{Index: 0, Lang: "eng"}, {Index: 1, Lang: "jpn", BitRate: 128_000}},
		Subs:  []store.SubStream{{Index: 0, Lang: "eng"}},
	}
	// Policy off: never flagged.
	if has, _ := ExtraLanguages(f, config.Default(), "", ""); has {
		t.Errorf("policy off should never flag extra_languages")
	}
	if got := Detect(f, config.Default(), false, false, false, false, "", ""); contains(got, "extra_languages") {
		t.Errorf("policy off: Detect should not include extra_languages: %v", got)
	}

	// Audio mode on, jpn not in the keep list: flagged with the jpn
	// track's estimated bytes.
	cfg := config.Default()
	cfg.LangPolicy = config.LangPolicy{AudioMode: "report", AudioKeep: []string{"eng"}}
	has, saved := ExtraLanguages(f, cfg, "", "")
	if !has {
		t.Errorf("jpn track not in keep list should be flagged")
	}
	if want := int64(128_000) * 100 / 8; saved != want {
		t.Errorf("saved = %d, want %d", saved, want)
	}
	if got := Detect(f, cfg, false, false, false, false, "", ""); !contains(got, "extra_languages") {
		t.Errorf("Detect should include extra_languages: %v", got)
	}

	// Nothing to drop: not flagged.
	single := &store.File{Duration: 100, Audio: []store.AudioStream{{Index: 0, Lang: "eng"}}}
	if has, _ := ExtraLanguages(single, cfg, "", ""); has {
		t.Errorf("single eng track with eng kept should not be flagged")
	}
}

func TestExtraLanguagesScoping(t *testing.T) {
	f := &store.File{Library: "movies", Duration: 100,
		Audio: []store.AudioStream{{Index: 0, Lang: "eng"}, {Index: 1, Lang: "jpn", BitRate: 128_000}},
	}
	cfg := config.Default()
	cfg.LangPolicy = config.LangPolicy{AudioMode: "report", AudioKeep: []string{"eng"}}

	// A per-file exemption silences it, even with the mode on.
	exempt := *f
	exempt.LangPruneExempt = true
	if has, _ := ExtraLanguages(&exempt, cfg, "", ""); has {
		t.Errorf("an exempt file should never be flagged")
	}

	// A library override of "off" silences it for that library.
	cfg.LangLibraryOverrides = map[string]config.LangOverride{"movies": {Mode: "off"}}
	if has, _ := ExtraLanguages(f, cfg, "", ""); has {
		t.Errorf("a library-off override should silence extra_languages: %v", cfg)
	}
	cfg.LangLibraryOverrides = nil

	// An instance override keeping both languages silences it for that
	// instance only.
	cfg.LangInstanceOverrides = map[string]config.LangOverride{"Radarr 4K": {Mode: "custom", AudioKeep: []string{"eng", "jpn"}}}
	if has, _ := ExtraLanguages(f, cfg, "", "Radarr 4K"); has {
		t.Errorf("instance override keeping both languages should silence it")
	}
	if has, _ := ExtraLanguages(f, cfg, "", "Some Other Instance"); !has {
		t.Errorf("an unscoped instance should fall back to the global policy")
	}
}

func contains(keys []string, key string) bool {
	for _, k := range keys {
		if k == key {
			return true
		}
	}
	return false
}

func TestReasonCategory(t *testing.T) {
	cases := map[string]string{
		"ContainerNotSupported":        "container",
		"ContainerBitrateExceedsLimit": "bitrate",
		"AudioCodecNotSupported":       "audio_codec",
		"AudioChannelsNotSupported":    "audio_channels",
		"AudioBitrateNotSupported":     "audio_codec",
		"VideoCodecNotSupported":       "video_codec",
		"VideoProfileNotSupported":     "video_profile",
		"VideoLevelNotSupported":       "video_level",
		"VideoBitDepthNotSupported":    "bit_depth",
		"VideoResolutionNotSupported":  "video_codec",
		"SubtitleCodecNotSupported":    "subtitle",
		"DirectPlayError":              "video_codec",
		"SomethingUnknown":             "other",
	}
	for raw, want := range cases {
		if got := ReasonCategory(raw); got != want {
			t.Errorf("ReasonCategory(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestQuickFix(t *testing.T) {
	cfg := config.Default()
	s := QuickFix(&store.File{VideoCodec: "hevc", Container: "mp4"}, cfg)
	if !s.VideoCopy || s.Container != "mp4" {
		t.Errorf("MP4 fixes stay MP4 with video copied: %+v", s)
	}
	if s := QuickFix(&store.File{VideoCodec: "mpeg4", Container: "avi"}, cfg); s.Container != "auto" {
		t.Errorf("legacy container → auto: %+v", s)
	}
	if got := Decode(",a,b,"); len(got) != 2 || got[1] != "b" {
		t.Errorf("decode %v", got)
	}
}

func TestMissingSubs(t *testing.T) {
	cfg := config.Default()
	f := &store.File{Library: "movies",
		Subs:     []store.SubStream{{Lang: "eng"}},
		Sidecars: []store.Sidecar{{Name: "M.es.srt", Lang: "es"}}}
	if got := MissingSubs(f, cfg, ""); got != nil {
		t.Errorf("no SubsKeep list must mean off, got %v", got)
	}
	cfg.LangPolicy.SubsKeep = []string{"eng", "spa", "fre"}
	got := MissingSubs(f, cfg, "")
	if len(got) != 1 || got[0] != "fre" {
		t.Errorf("got %v, want [fre] (eng embedded, spa via 'es' sidecar)", got)
	}
	// The per-file pruning exemption doesn't hide missing subtitles.
	f.LangPruneExempt = true
	if got := Encode(Detect(f, cfg, false, false, false, false, "", "")); got != ",missing_subs," {
		t.Errorf("Detect = %s", got)
	}
}
