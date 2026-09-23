package issues

import (
	"testing"

	"mediatrans/internal/config"
	"mediatrans/internal/store"
)

func TestDetect(t *testing.T) {
	f := &store.File{VideoCodec: "hevc", Container: "mp4", VideoTag: "hev1", MetaChecked: true, Faststart: 0,
		Audio: []store.AudioStream{{Codec: "aac"}, {Codec: "pcm_s24le"}}}
	got := Encode(Detect(f, false, false, false))
	if got != ",no_hvc1,no_faststart,pcm_audio," {
		t.Errorf("got %s", got)
	}
	avi := &store.File{VideoCodec: "mpeg4", Container: "avi", Faststart: -1, Interlaced: true}
	if got := Encode(Detect(avi, true, false, false)); got != ",legacy_container,legacy_codec,interlaced,worth_reencoding," {
		t.Errorf("got %s", got)
	}
	odd := &store.File{VideoCodec: "hevc", Container: "mp4", VideoTag: "[0][0][0][0]", MetaChecked: true, Faststart: 1}
	if got := Encode(Detect(odd, false, false, false)); got != ",no_hvc1," {
		t.Errorf("non-hvc1 tag in MP4: got %s", got)
	}
	unread := &store.File{VideoCodec: "hevc", Container: "mp4", VideoTag: "", MetaChecked: true, Faststart: 1}
	if got := Detect(unread, false, false, false); len(got) != 0 {
		t.Errorf("unreadable tag should not be flagged: %v", got)
	}
	clean := &store.File{VideoCodec: "hevc", Container: "mkv", Faststart: -1, MetaChecked: true, VideoTag: "[0][0][0][0]"}
	if got := Detect(clean, false, false, false); len(got) != 0 {
		t.Errorf("clean MKV should have no issues: %v", got)
	}
	pending := &store.File{VideoCodec: "hevc", Container: "mkv", Faststart: -1, MetaChecked: true, VideoTag: "[0][0][0][0]"}
	if got := Encode(Detect(pending, false, false, true)); got != ",upgrade_pending," {
		t.Errorf("upgrade pending: got %s", got)
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
