package imagesubs

import (
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

func testSubs() []store.SubStream {
	return []store.SubStream{
		{Index: 4, Codec: "subrip", Lang: "eng", IsText: true},
		{Index: 8, Codec: CodecPGS, Lang: "eng", Default: true},
		{Index: 9, Codec: CodecPGS, Lang: "ger"},
		{Index: 10, Codec: CodecVobSub, Lang: "fre"},
	}
}

func TestIsImageSubtitle(t *testing.T) {
	if !IsImageSubtitle(CodecPGS) || !IsImageSubtitle(CodecVobSub) {
		t.Error("PGS and VobSub should both be image subtitle codecs")
	}
	if IsImageSubtitle("subrip") {
		t.Error("subrip is text, not image")
	}
}

func TestPrimaryTrackPrefersOriginalLanguage(t *testing.T) {
	got, ok := PrimaryTrack(testSubs(), "ger")
	if !ok || got.Index != 9 {
		t.Errorf("got index %d (ok=%v), want 9 (matches original language)", got.Index, ok)
	}
}

func TestPrimaryTrackFallsBackToDefault(t *testing.T) {
	got, ok := PrimaryTrack(testSubs(), "spa") // no track matches, no such language present
	if !ok || got.Index != 8 {
		t.Errorf("got index %d (ok=%v), want 8 (the default-flagged track)", got.Index, ok)
	}
}

func TestPrimaryTrackFallsBackToFirst(t *testing.T) {
	subs := []store.SubStream{
		{Index: 20, Codec: CodecVobSub, Lang: "dut"},
		{Index: 21, Codec: CodecPGS, Lang: "swe"},
	}
	got, ok := PrimaryTrack(subs, "")
	if !ok || got.Index != 20 {
		t.Errorf("got index %d (ok=%v), want 20 (first image sub, source order)", got.Index, ok)
	}
}

func TestPrimaryTrackNoneFound(t *testing.T) {
	subs := []store.SubStream{{Index: 4, Codec: "subrip", Lang: "eng", IsText: true}}
	if _, ok := PrimaryTrack(subs, ""); ok {
		t.Error("no image subtitle tracks present, expected ok=false")
	}
}

func TestDropEntriesKeepOriginalDropsNothing(t *testing.T) {
	if got := DropEntries(testSubs(), Sidecar, true, 0, false, nil); got != nil {
		t.Errorf("keepOriginal=true should drop nothing: %v", got)
	}
}

func TestDropEntriesSidecarModeDropsAllImageTracks(t *testing.T) {
	got := DropEntries(testSubs(), Sidecar, false, 0, false, nil)
	if len(got) != 3 {
		t.Fatalf("got %+v, want 3 drops (indices 8, 9, 10 — not the text sub at 4)", got)
	}
	for _, d := range got {
		if d.Action != "drop" || d.Index == 4 {
			t.Errorf("unexpected drop entry: %+v", d)
		}
	}
}

func TestDropEntriesOCRModeDropsOnlyTheOCRdTrack(t *testing.T) {
	got := DropEntries(testSubs(), OCR, false, 8, true, nil)
	if len(got) != 1 || got[0].Index != 8 {
		t.Errorf("got %+v, want exactly index 8 dropped (the successfully OCR'd track)", got)
	}
}

func TestDropEntriesOCRModeFailedAttemptDropsNothing(t *testing.T) {
	got := DropEntries(testSubs(), OCR, false, 8, false, nil)
	if got != nil {
		t.Errorf("a failed/low-confidence OCR attempt must never drop the original track: %v", got)
	}
}

func TestDropEntriesSkipsAlreadyDroppedIndices(t *testing.T) {
	existing := []encode.SubTrack{{Index: 8, Action: "drop"}}
	got := DropEntries(testSubs(), Sidecar, false, 0, false, existing)
	if len(got) != 2 {
		t.Errorf("got %+v, want 2 (index 8 already covered)", got)
	}
	for _, d := range got {
		if d.Index == 8 {
			t.Errorf("index 8 should not be duplicated: %+v", got)
		}
	}
}

func TestNormalizeAndCodeTables(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"de", "ger"}, {"deu", "ger"}, {"ger", "ger"},
		{"cs", "cze"}, {"ces", "cze"}, {"EL", "gre"},
	} {
		if got := Normalize(tc.in); got != tc.want {
			t.Errorf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if got := ietf2For("ger"); got != "de" {
		t.Errorf("ietf2For(ger) = %q, want de", got)
	}
	if got := tess3For("ger"); got != "deu" {
		t.Errorf("tess3For(ger) = %q, want deu (tesseract uses the T-code, not the B-code)", got)
	}
	// An unmapped code passes through rather than failing outright.
	if got := ietf2For("xyz"); got != "xyz" {
		t.Errorf("ietf2For(xyz) = %q, want passthrough xyz", got)
	}
}

func TestOCRResultRoundTrip(t *testing.T) {
	r := OCRResult{TrackIndex: 8, Lang: "eng", Confidence: 87.3, SRTName: "Movie.eng.srt", AttemptedAt: "2026-09-24T00:00:00Z"}
	got, ok := UnmarshalResult(MarshalResult(r))
	if !ok || got != r {
		t.Errorf("round trip mismatch: got %+v, want %+v", got, r)
	}
	if _, ok := UnmarshalResult(""); ok {
		t.Error("empty string should not round-trip to ok=true")
	}
	if _, ok := UnmarshalResult("not json"); ok {
		t.Error("garbage should not round-trip to ok=true")
	}
}
