package arr

import (
	"encoding/json"
	"os"
	"testing"
)

func cf(id int64, name string, specs ...CustomFormatSpec) CustomFormat {
	return CustomFormat{ID: id, Name: name, Specifications: specs}
}

func titleSpec(regex string) CustomFormatSpec {
	return CustomFormatSpec{Implementation: "ReleaseTitleSpecification",
		Fields: []CustomFormatField{{Name: "value", Value: regex}}}
}

func sizeSpec() CustomFormatSpec {
	return CustomFormatSpec{Implementation: "SizeSpecification",
		Fields: []CustomFormatField{{Name: "min", Value: 1.0}, {Name: "max", Value: 5.0}}}
}

// A TRaSH-style setup: a quality profile scores an HEVC-matching custom
// format around -10000. Must be flagged.
func TestAnalyzePenaltiesTrashLikeIsFlagged(t *testing.T) {
	cfs := []CustomFormat{cf(1, "HEVC (1080p)", titleSpec(`(((x|h)\.?265)|(HEVC))`))}
	profiles := []QualityProfile{{Name: "WEB-1080p", FormatItems: []FormatItem{{Format: 1, Score: -10000}}}}
	r := AnalyzePenalties(cfs, profiles, nil)
	if len(r.Penalties) != 1 {
		t.Fatalf("got %d penalties, want 1: %+v", len(r.Penalties), r)
	}
	p := r.Penalties[0]
	if p.Profile != "WEB-1080p" || p.CustomFormat != "HEVC (1080p)" || p.Score != -10000 || p.Matches != "release title" {
		t.Errorf("penalty = %+v", p)
	}
	if len(p.Terms) != 1 || p.Terms[0] != "h265" {
		t.Errorf("terms = %v, want [h265]", p.Terms)
	}
}

// No codec-related custom formats at all: clean.
func TestAnalyzePenaltiesNoCodecFormatsIsClean(t *testing.T) {
	cfs := []CustomFormat{cf(1, "RARBG", CustomFormatSpec{Implementation: "ReleaseGroupSpecification",
		Fields: []CustomFormatField{{Name: "value", Value: "(RARBG)"}}})}
	profiles := []QualityProfile{{Name: "Any", FormatItems: []FormatItem{{Format: 1, Score: -5}}}}
	r := AnalyzePenalties(cfs, profiles, nil)
	if !r.Clean() {
		t.Errorf("expected clean, got %+v", r)
	}
}

// A custom format that matches only on non-codec terms (e.g. a release
// group or size range) must not be flagged even with a negative score.
func TestAnalyzePenaltiesNonCodecTermNotFlagged(t *testing.T) {
	cfs := []CustomFormat{cf(1, "Small", sizeSpec())}
	profiles := []QualityProfile{{Name: "Any", FormatItems: []FormatItem{{Format: 1, Score: -100}}}}
	r := AnalyzePenalties(cfs, profiles, nil)
	if !r.Clean() {
		t.Errorf("expected clean (non-codec spec), got %+v", r)
	}
}

// A codec-matching format with a positive or zero score is not a penalty
// (it's neutral or actively preferred, not a re-download risk).
func TestAnalyzePenaltiesPositiveOrZeroScoreNotFlagged(t *testing.T) {
	cfs := []CustomFormat{cf(1, "H265", titleSpec(`(((x|h)\.?265)|(HEVC))`))}
	for _, score := range []int{0, 1, 4, 100} {
		profiles := []QualityProfile{{Name: "P", FormatItems: []FormatItem{{Format: 1, Score: score}}}}
		if r := AnalyzePenalties(cfs, profiles, nil); !r.Clean() {
			t.Errorf("score %d should not be a penalty, got %+v", score, r)
		}
	}
}

// A custom-format id referenced by a profile but not present in the
// custom-format list (deleted, or fetched inconsistently) must not panic
// and must not be flagged.
func TestAnalyzePenaltiesUnknownFormatIDIgnored(t *testing.T) {
	profiles := []QualityProfile{{Name: "P", FormatItems: []FormatItem{{Format: 999, Score: -50}}}}
	r := AnalyzePenalties(nil, profiles, nil)
	if !r.Clean() {
		t.Errorf("expected clean, got %+v", r)
	}
}

// SourceSpecification is also evaluated against current file state on
// rescan, so a codec term there must be caught too.
func TestAnalyzePenaltiesSourceSpecification(t *testing.T) {
	cfs := []CustomFormat{cf(1, "AV1 source", CustomFormatSpec{Implementation: "SourceSpecification",
		Fields: []CustomFormatField{{Name: "value", Value: "av1"}}})}
	profiles := []QualityProfile{{Name: "P", FormatItems: []FormatItem{{Format: 1, Score: -1}}}}
	r := AnalyzePenalties(cfs, profiles, nil)
	if len(r.Penalties) != 1 || r.Penalties[0].Matches != "source" {
		t.Errorf("got %+v", r)
	}
}

// A naming template embedding the codec is its own, separate warning —
// informational, not a Penalty, and independent of any score.
func TestAnalyzePenaltiesNamingEmbedsCodec(t *testing.T) {
	n := &Naming{StandardEpisodeFormat: "{Series Title} - S{season:00}E{episode:00} {Quality Full} {MediaInfo VideoCodec}"}
	r := AnalyzePenalties(nil, nil, n)
	if len(r.Naming) != 1 || r.Naming[0].Field != "standardEpisodeFormat" {
		t.Errorf("got %+v", r)
	}
	if len(r.Penalties) != 0 {
		t.Errorf("naming warnings must not also produce Penalties, got %+v", r.Penalties)
	}
}

func TestAnalyzePenaltiesNamingWithoutCodecTokenIsClean(t *testing.T) {
	n := &Naming{StandardEpisodeFormat: "{Series Title} - S{season:00}E{episode:00} {Quality Full}",
		StandardMovieFormat: "{Movie Title} ({Release Year}) {Quality Full}"}
	r := AnalyzePenalties(nil, nil, n)
	if !r.Clean() {
		t.Errorf("expected clean, got %+v", r)
	}
}

// Regression test: an escaped regex like "(x|h)\.?264" — Sonarr/Radarr's
// own generated form for a plain x264 format — must be recognized even
// with no literal fallback word (e.g. "AVC") present. A naive regex
// match of a codec pattern against this literal text fails (the
// characters between "h" and "264" in the source are ")\.?", not "."),
// which is why detection normalizes first instead.
func TestAnalyzePenaltiesEscapedRegexWithoutFallbackWord(t *testing.T) {
	cfs := []CustomFormat{cf(1, "H264", titleSpec(`(x|h)\.?264`))}
	profiles := []QualityProfile{{Name: "P", FormatItems: []FormatItem{{Format: 1, Score: -1}}}}
	r := AnalyzePenalties(cfs, profiles, nil)
	if len(r.Penalties) != 1 || len(r.Penalties[0].Terms) != 1 || r.Penalties[0].Terms[0] != "h264" {
		t.Fatalf("got %+v, want a single h264 penalty", r)
	}
}

// A compound format with more than one codec-matching specification
// merges the terms rather than only reporting the first one found.
func TestAnalyzePenaltiesMergesTermsAcrossSpecs(t *testing.T) {
	cfs := []CustomFormat{cf(1, "x264 or x265", titleSpec(`(x|h)\.?264`), titleSpec(`(((x|h)\.?265)|(HEVC))`))}
	profiles := []QualityProfile{{Name: "P", FormatItems: []FormatItem{{Format: 1, Score: -1}}}}
	r := AnalyzePenalties(cfs, profiles, nil)
	if len(r.Penalties) != 1 || len(r.Penalties[0].Terms) != 2 {
		t.Errorf("got %+v", r)
	}
}

// Real-world testdata (captured from a live instance) must come back
// clean: its only negative-scored format items are non-codec ones, and
// the codec-matching formats it does have (H264/H265) are scored
// positive or zero everywhere. A regression here means either the real
// data changed in a way worth knowing about, or the analyzer broke.
func TestAnalyzePenaltiesAgainstThisHostsRealData(t *testing.T) {
	for _, kind := range []string{"sonarr", "radarr"} {
		cfs := loadCustomFormats(t, kind)
		profiles := loadQualityProfiles(t, kind)
		naming := loadNaming(t, kind)
		r := AnalyzePenalties(cfs, profiles, naming)
		if !r.Clean() {
			t.Errorf("%s: expected the real-world config to be clean, got %+v", kind, r)
		}
	}
}

func loadCustomFormats(t *testing.T, kind string) []CustomFormat {
	t.Helper()
	b, err := os.ReadFile("testdata/" + kind + "_customformat.json")
	if err != nil {
		t.Fatal(err)
	}
	var out []CustomFormat
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func loadQualityProfiles(t *testing.T, kind string) []QualityProfile {
	t.Helper()
	b, err := os.ReadFile("testdata/" + kind + "_qualityprofile.json")
	if err != nil {
		t.Fatal(err)
	}
	var out []QualityProfile
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func loadNaming(t *testing.T, kind string) *Naming {
	t.Helper()
	b, err := os.ReadFile("testdata/" + kind + "_naming.json")
	if err != nil {
		t.Fatal(err)
	}
	var out Naming
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}
