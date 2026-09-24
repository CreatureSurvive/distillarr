// SPDX-License-Identifier: GPL-3.0-or-later

package sidecar

import (
	"os"
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

func testSubs() []store.SubStream {
	return []store.SubStream{
		{Index: 4, Codec: "subrip", Lang: "eng", IsText: true},
		{Index: 5, Codec: "hdmv_pgs_subtitle", Lang: "fre"}, // image: never a sidecar candidate
		{Index: 6, Codec: "ass", Lang: "jpn", IsText: true},
	}
}

func TestDropEntriesOffAndKeepDropNothing(t *testing.T) {
	for _, mode := range []string{Off, ExtractKeep, "bogus"} {
		if got := DropEntries(testSubs(), mode, nil); got != nil {
			t.Errorf("mode %q: got %v, want nil (only extract_remove drops)", mode, got)
		}
	}
}

func TestDropEntriesExtractRemoveDropsTextOnly(t *testing.T) {
	got := DropEntries(testSubs(), ExtractRemove, nil)
	if len(got) != 2 {
		t.Fatalf("got %+v, want 2 drops (indices 4 and 6, not the image sub at 5)", got)
	}
	for _, d := range got {
		if d.Action != "drop" || (d.Index != 4 && d.Index != 6) {
			t.Errorf("unexpected drop entry: %+v", d)
		}
	}
}

func TestDropEntriesSkipsAlreadyDroppedIndices(t *testing.T) {
	existing := []encode.SubTrack{{Index: 4, Action: "drop"}} // e.g. already dropped by language pruning
	got := DropEntries(testSubs(), ExtractRemove, existing)
	if len(got) != 1 || got[0].Index != 6 {
		t.Errorf("got %+v, want only index 6 (4 already covered)", got)
	}
}

func TestSidecarNamingClashGetsNumericSuffix(t *testing.T) {
	dir := t.TempDir()
	touch := func(name string) {
		if err := os.WriteFile(dir+"/"+name, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	touch("Movie.eng.srt")
	touch("Movie.eng.1.srt")

	got := sidecarName(dir, "Movie", "eng", ".srt")
	if got != "Movie.eng.2.srt" {
		t.Errorf("sidecarName = %q, want Movie.eng.2.srt (both prior names taken)", got)
	}
}

func TestSidecarNamingNoLang(t *testing.T) {
	dir := t.TempDir()
	got := sidecarName(dir, "Movie", "", ".srt")
	if got != "Movie.srt" {
		t.Errorf("sidecarName = %q, want Movie.srt", got)
	}
}
