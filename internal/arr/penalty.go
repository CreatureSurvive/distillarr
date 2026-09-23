package arr

import (
	"strings"
)

// codecNeedles maps a codec name to the substrings that identify it,
// checked against the *normalized* field text (letters/digits only,
// lowercased — see normalizeAlnum). Normalizing first, rather than
// matching a regex directly against the source text, is what makes this
// robust to how the source itself is escaped: Sonarr/Radarr's own
// generated regexes commonly write x264 as "(x|h)\.?264", which
// normalizes to "xh264" — containing "h264" as a substring — but would
// not match a pattern like `(x|h)\.?264` applied literally, since the
// characters between "h" and "264" in the *source* aren't ".", they're
// ")\.?" or similar. A term needs only one needle to hit.
var codecNeedles = []struct {
	name    string
	needles []string
}{
	{"h264", []string{"x264", "h264", "avc"}},
	{"h265", []string{"x265", "h265", "hevc"}},
	{"av1", []string{"av1"}},
	{"vp9", []string{"vp9"}},
}

// normalizeAlnum strips everything but letters and digits and lowercases
// the rest, collapsing away whatever punctuation a regex escapes with.
func normalizeAlnum(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r - 'A' + 'a')
		}
	}
	return b.String()
}

// specKindsSeenOnRescan are the specification implementations evaluated
// against the file's current name/attributes on a rescan, mapped to a human label. A
// custom format built from one of these can fire on a file Distillarr
// renamed, even though it never matched at import time.
var specKindsSeenOnRescan = map[string]string{
	"ReleaseTitleSpecification": "release title",
	"SourceSpecification":       "source",
}

// codecTermsIn returns which codec terms appear in a specification's
// text fields (a ReleaseTitleSpecification's regex, typically).
func codecTermsIn(spec CustomFormatSpec) []string {
	var terms []string
	seen := map[string]bool{}
	for _, f := range spec.Fields {
		s, ok := f.Value.(string)
		if !ok {
			continue
		}
		norm := normalizeAlnum(s)
		for _, ct := range codecNeedles {
			if seen[ct.name] {
				continue
			}
			for _, needle := range ct.needles {
				if strings.Contains(norm, needle) {
					seen[ct.name] = true
					terms = append(terms, ct.name)
					break
				}
			}
		}
	}
	return terms
}

// Penalty is one quality profile scoring a codec-matching custom format
// negatively — the concrete re-download-loop risk.
type Penalty struct {
	Profile      string   `json:"profile"`
	CustomFormat string   `json:"custom_format"`
	Score        int      `json:"score"`
	Matches      string   `json:"matches"` // "release title" | "source"
	Terms        []string `json:"terms"`
}

// NamingWarning flags a naming template that writes the codec into the
// filename — informational on its own (score 0 today), but it means a
// future scoring rule would immediately apply to every renamed file.
type NamingWarning struct {
	Field    string `json:"field"`
	Template string `json:"template"`
}

// PenaltyReport is AnalyzePenalties' result for one instance.
type PenaltyReport struct {
	Penalties []Penalty       `json:"penalties"`
	Naming    []NamingWarning `json:"naming_warnings"`
}

// Clean reports whether nothing to warn about was found.
func (r PenaltyReport) Clean() bool { return len(r.Penalties) == 0 && len(r.Naming) == 0 }

// AnalyzePenalties finds quality profiles that would score a Distillarr
// re-encode's likely codec negatively, and naming templates that would
// expose the codec to such a rule even at a score of 0 today. Pure and
// side-effect-free — every fact it needs is a parameter, nothing is
// fetched or assumed about any particular installation.
func AnalyzePenalties(cfs []CustomFormat, profiles []QualityProfile, naming *Naming) PenaltyReport {
	// Which custom formats look codec-related, and by what (merging
	// terms across every codec-matching specification of that format —
	// a compound format can have more than one).
	type codecFormat struct {
		matches string
		terms   map[string]bool
	}
	names := make(map[int64]string, len(cfs))
	codecCF := map[int64]*codecFormat{}
	for _, cf := range cfs {
		names[cf.ID] = cf.Name
		for _, spec := range cf.Specifications {
			kind, ok := specKindsSeenOnRescan[spec.Implementation]
			if !ok {
				continue
			}
			terms := codecTermsIn(spec)
			if len(terms) == 0 {
				continue
			}
			cur, ok := codecCF[cf.ID]
			if !ok {
				cur = &codecFormat{matches: kind, terms: map[string]bool{}}
				codecCF[cf.ID] = cur
			}
			for _, t := range terms {
				cur.terms[t] = true
			}
		}
	}

	var report PenaltyReport
	for _, p := range profiles {
		for _, fi := range p.FormatItems {
			if fi.Score >= 0 {
				continue
			}
			cf, ok := codecCF[fi.Format]
			if !ok {
				continue
			}
			terms := make([]string, 0, len(cf.terms))
			for t := range cf.terms {
				terms = append(terms, t)
			}
			report.Penalties = append(report.Penalties, Penalty{
				Profile: p.Name, CustomFormat: names[fi.Format], Score: fi.Score,
				Matches: cf.matches, Terms: terms,
			})
		}
	}

	if naming != nil {
		check := func(field, tmpl string) {
			if tmpl == "" {
				return
			}
			if strings.Contains(tmpl, "{MediaInfo VideoCodec}") || strings.Contains(tmpl, "{MediaInfo Full}") {
				report.Naming = append(report.Naming, NamingWarning{Field: field, Template: tmpl})
			}
		}
		check("standardEpisodeFormat", naming.StandardEpisodeFormat)
		check("dailyEpisodeFormat", naming.DailyEpisodeFormat)
		check("animeEpisodeFormat", naming.AnimeEpisodeFormat)
		check("standardMovieFormat", naming.StandardMovieFormat)
	}

	return report
}
