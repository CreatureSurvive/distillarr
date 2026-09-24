// SPDX-License-Identifier: GPL-3.0-or-later

package langprune

import (
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

func bp(b bool) *bool { return &b }

func TestSelectOff(t *testing.T) {
	audio := []store.AudioStream{{Index: 0, Lang: "eng"}, {Index: 1, Lang: "jpn"}}
	subs := []store.SubStream{{Index: 0, Lang: "eng"}}
	res := Select(audio, subs, "", 100, config.LangPolicy{}) // AudioMode/SubsMode both "" (off)
	if len(res.DropAudio) != 0 || len(res.DropSubs) != 0 {
		t.Errorf("policy off should drop nothing: %+v", res)
	}
	if len(res.KeepAudio) != 2 || len(res.KeepSubs) != 1 {
		t.Errorf("policy off should keep everything: %+v", res)
	}
}

func TestSelectMultiAudio(t *testing.T) {
	audio := []store.AudioStream{{Index: 0, Lang: "eng"}, {Index: 1, Lang: "jpn"}}
	p := config.LangPolicy{AudioMode: "report", AudioKeep: []string{"en"}}
	res := Select(audio, nil, "eng", 100, p)
	if !containsInt(res.KeepAudio, 0) || !containsInt(res.DropAudio, 1) {
		t.Errorf("expected eng kept, jpn dropped: %+v", res)
	}
}

func TestSelectUndetermined(t *testing.T) {
	audio := []store.AudioStream{{Index: 0, Lang: "eng"}, {Index: 1, Lang: "und"}}
	p := config.LangPolicy{AudioMode: "report", AudioKeep: []string{"en"}}
	// Default KeepUndetermined (nil -> on): the und track is kept too.
	res := Select(audio, nil, "eng", 100, p)
	if !containsInt(res.KeepAudio, 1) {
		t.Errorf("und track should be kept by default: %+v", res)
	}
	// KeepUndetermined off: und track drops (eng still kept, so no safety-net rescue).
	p.KeepUndetermined = bp(false)
	res = Select(audio, nil, "eng", 100, p)
	if !containsInt(res.DropAudio, 1) {
		t.Errorf("und track should drop when KeepUndetermined is off: %+v", res)
	}
}

func TestSelectCommentary(t *testing.T) {
	audio := []store.AudioStream{
		{Index: 0, Lang: "eng"},
		{Index: 1, Lang: "eng", Commentary: true},
	}
	p := config.LangPolicy{AudioMode: "report", AudioKeep: []string{"eng"}}
	// KeepCommentary off (default): the commentary track drops even though eng matches.
	res := Select(audio, nil, "eng", 100, p)
	if !containsInt(res.DropAudio, 1) {
		t.Errorf("commentary track should drop by default: %+v", res)
	}
	// KeepCommentary on: it's treated like any other eng track.
	p.KeepCommentary = true
	res = Select(audio, nil, "eng", 100, p)
	if !containsInt(res.KeepAudio, 1) {
		t.Errorf("commentary track should be kept when KeepCommentary is on: %+v", res)
	}
}

func TestSelectForcedSubtitleInKeptAudioLanguage(t *testing.T) {
	audio := []store.AudioStream{{Index: 0, Lang: "eng"}}
	subs := []store.SubStream{
		{Index: 0, Lang: "eng", Forced: true}, // not in SubsKeep, but forced + kept audio lang
		{Index: 1, Lang: "eng", Forced: false},
	}
	p := config.LangPolicy{
		AudioMode: "report", AudioKeep: []string{"eng"},
		SubsMode: "report", SubsKeep: []string{"fre"}, // eng not wanted for subs
	}
	res := Select(audio, subs, "eng", 100, p)
	if !containsInt(res.KeepSubs, 0) {
		t.Errorf("forced sub in a kept audio language must always be kept: %+v", res)
	}
	if !containsInt(res.DropSubs, 1) {
		t.Errorf("non-forced eng sub should drop (eng not in SubsKeep): %+v", res)
	}
}

func TestSelectSDH(t *testing.T) {
	subs := []store.SubStream{{Index: 0, Lang: "eng", SDH: true}}
	p := config.LangPolicy{SubsMode: "report", SubsKeep: []string{"eng"}}
	// Default KeepSDH (nil -> on): kept.
	res := Select(nil, subs, "eng", 100, p)
	if !containsInt(res.KeepSubs, 0) {
		t.Errorf("SDH track should be kept by default: %+v", res)
	}
	// KeepSDH off: dropped even though eng matches.
	p.KeepSDH = bp(false)
	res = Select(nil, subs, "eng", 100, p)
	if !containsInt(res.DropSubs, 0) {
		t.Errorf("SDH track should drop when KeepSDH is off: %+v", res)
	}
}

func TestSelectBestPerLanguage(t *testing.T) {
	audio := []store.AudioStream{
		{Index: 0, Lang: "eng", Channels: 6, BitRate: 640_000},
		{Index: 1, Lang: "eng", Channels: 2, BitRate: 128_000},
	}
	p := config.LangPolicy{AudioMode: "report", AudioKeep: []string{"eng"}, BestPerLanguage: true}
	res := Select(audio, nil, "eng", 100, p)
	if !containsInt(res.KeepAudio, 0) || !containsInt(res.DropAudio, 1) {
		t.Errorf("BestPerLanguage should keep only the 6-channel track: %+v", res)
	}
}

func TestSelectNoOriginal(t *testing.T) {
	// original is "" and the first (only) audio track's own language is
	// also undetermined, so orig can never resolve to a real code. None
	// of the tracks match AudioKeep, so the safety net falls back to
	// keeping track 0 (the first track, since no language identifies
	// "the original").
	audio := []store.AudioStream{{Index: 0, Lang: ""}, {Index: 1, Lang: "fre"}}
	p := config.LangPolicy{AudioMode: "report", AudioKeep: []string{"eng"}, KeepUndetermined: bp(false)}
	res := Select(audio, nil, "", 100, p)
	if len(res.KeepAudio) != 1 || res.KeepAudio[0] != 0 {
		t.Errorf("no original: expected track 0 kept as the fallback, got %+v", res)
	}
}

func TestSelectEveryTrackForeignKeepsOriginal(t *testing.T) {
	// AudioKeep wants only English; every track is foreign. The original
	// language (French) must still survive via the safety net.
	audio := []store.AudioStream{
		{Index: 0, Lang: "fre"},
		{Index: 1, Lang: "jpn"},
		{Index: 2, Lang: "ger"},
	}
	p := config.LangPolicy{AudioMode: "report", AudioKeep: []string{"eng"}}
	res := Select(audio, nil, "fre", 100, p)
	if len(res.KeepAudio) != 1 || res.KeepAudio[0] != 0 {
		t.Errorf("expected only the original-language (fre) track kept, got %+v", res)
	}
	if len(res.DropAudio) != 2 {
		t.Errorf("expected the other two foreign tracks dropped, got %+v", res)
	}
}

func TestSelectSavedBytes(t *testing.T) {
	audio := []store.AudioStream{
		{Index: 0, Lang: "eng", BitRate: 640_000},
		{Index: 1, Lang: "jpn", BitRate: 320_000},
	}
	p := config.LangPolicy{AudioMode: "report", AudioKeep: []string{"eng"}}
	res := Select(audio, nil, "eng", 60, p) // 60s at 320kbps
	want := int64(320_000) * 60 / 8
	if res.SavedBytes != want {
		t.Errorf("SavedBytes = %d, want %d", res.SavedBytes, want)
	}
}

func TestNormalizeAndUndetermined(t *testing.T) {
	cases := map[string]string{"en": "eng", "eng": "eng", "DE": "ger", "fra": "fre", "xx": "xx"}
	for in, want := range cases {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
	for _, u := range []string{"", "und", "UND", "unk", "zxx"} {
		if !IsUndetermined(u) {
			t.Errorf("IsUndetermined(%q) = false, want true", u)
		}
	}
	if IsUndetermined("eng") {
		t.Errorf("IsUndetermined(eng) = true, want false")
	}
}

func TestNameToCode(t *testing.T) {
	if NameToCode("English") != "eng" {
		t.Errorf("NameToCode(English) = %q, want eng", NameToCode("English"))
	}
	if NameToCode("Klingon") != "" {
		t.Errorf("NameToCode(Klingon) = %q, want \"\"", NameToCode("Klingon"))
	}
}

func containsInt(xs []int, v int) bool {
	for _, x := range xs {
		if x == v {
			return true
		}
	}
	return false
}
