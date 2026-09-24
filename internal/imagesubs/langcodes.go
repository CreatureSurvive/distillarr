package imagesubs

import "strings"

// langInfo maps a canonical ISO 639-2/B code (this project's convention,
// see internal/langprune/langcodes.go — kept as a separate small table
// here rather than importing that package, to avoid a cross-package
// dependency for 28 static rows) to the two codes OCR pipeline
// needs: ietf2 (pgsrip's --language flag, e.g. "en") and tess3
// (tesseract's traineddata filename stem, ISO 639-2/T — usually the same
// as the B-code, but German/French/Czech/Greek/Chinese/Romanian differ).
type langInfo struct{ ietf2, tess3 string }

var langTable = map[string]langInfo{
	"eng": {"en", "eng"},
	"ger": {"de", "deu"},
	"fre": {"fr", "fra"},
	"spa": {"es", "spa"},
	"ita": {"it", "ita"},
	"jpn": {"ja", "jpn"},
	"kor": {"ko", "kor"},
	"chi": {"zh", "chi_sim"},
	"por": {"pt", "por"},
	"rus": {"ru", "rus"},
	"dut": {"nl", "nld"},
	"swe": {"sv", "swe"},
	"nor": {"no", "nor"},
	"dan": {"da", "dan"},
	"fin": {"fi", "fin"},
	"pol": {"pl", "pol"},
	"tur": {"tr", "tur"},
	"ara": {"ar", "ara"},
	"hin": {"hi", "hin"},
	"tha": {"th", "tha"},
	"vie": {"vi", "vie"},
	"cze": {"cs", "ces"},
	"gre": {"el", "ell"},
	"heb": {"he", "heb"},
	"hun": {"hu", "hun"},
	"rum": {"ro", "ron"},
	"ukr": {"uk", "ukr"},
	"ind": {"id", "ind"},
}

// codeAliases: the ISO 639-1/639-2 variants a raw ffprobe tag might use,
// mapped to this table's canonical B-code key. Deliberately duplicated
// from internal/langprune rather than imported, per langTable's comment.
var codeAliases = map[string]string{
	"en": "eng", "eng": "eng",
	"de": "ger", "ger": "ger", "deu": "ger",
	"fr": "fre", "fre": "fre", "fra": "fre",
	"es": "spa", "spa": "spa",
	"it": "ita", "ita": "ita",
	"ja": "jpn", "jpn": "jpn",
	"ko": "kor", "kor": "kor",
	"zh": "chi", "chi": "chi", "zho": "chi",
	"pt": "por", "por": "por",
	"ru": "rus", "rus": "rus",
	"nl": "dut", "dut": "dut", "nld": "dut",
	"sv": "swe", "swe": "swe",
	"no": "nor", "nor": "nor",
	"da": "dan", "dan": "dan",
	"fi": "fin", "fin": "fin",
	"pl": "pol", "pol": "pol",
	"tr": "tur", "tur": "tur",
	"ar": "ara", "ara": "ara",
	"hi": "hin", "hin": "hin",
	"th": "tha", "tha": "tha",
	"vi": "vie", "vie": "vie",
	"cs": "cze", "cze": "cze", "ces": "cze",
	"el": "gre", "gre": "gre", "ell": "gre",
	"he": "heb", "heb": "heb",
	"hu": "hun", "hun": "hun",
	"ro": "rum", "rum": "rum", "ron": "rum",
	"uk": "ukr", "ukr": "ukr",
	"id": "ind", "ind": "ind",
}

// Normalize maps a raw stream language tag to this table's canonical
// ISO 639-2/B code. An unrecognized code passes through lowercased and
// trimmed — still comparable to itself, just without a lang table entry.
func Normalize(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if c, ok := codeAliases[code]; ok {
		return c
	}
	return code
}

// ietf2For returns pgsrip's expected --language value for a normalized
// code, defaulting to the code itself (pgsrip/babelfish understands a
// wide range of forms; an unmapped code is passed through as a
// best-effort guess rather than failing outright).
func ietf2For(normalized string) string {
	if li, ok := langTable[normalized]; ok {
		return li.ietf2
	}
	return normalized
}

// tess3For returns tesseract's traineddata filename stem (without
// ".traineddata") for a normalized code, same best-effort fallback.
func tess3For(normalized string) string {
	if li, ok := langTable[normalized]; ok {
		return li.tess3
	}
	return normalized
}
