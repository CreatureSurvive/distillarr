// SPDX-License-Identifier: GPL-3.0-or-later

// Package langprune decides which audio/subtitle tracks a file's language
// policy would keep or drop. Pure decision logic; nothing here
// touches a file — applying the decision is job.
package langprune

import "strings"

// codeAliases maps ISO 639-1 and 639-2/T codes to a canonical ISO 639-2/B
// form, so a user-entered "en"/"de"/"fr" and a stream's raw ffprobe tag
// ("eng"/"deu"/"fra" or "eng"/"ger"/"fre") compare equal regardless of
// which variant either side happens to use.
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

// Normalize maps a language code (ISO 639-1, or 639-2/T or /B) to a
// single canonical lowercase form. An unrecognized code passes through
// lowercased and trimmed, unchanged — still comparable to itself, just
// not aliased to anything.
func Normalize(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	if c, ok := codeAliases[code]; ok {
		return c
	}
	return code
}

// IsUndetermined reports whether a raw (not normalized) stream language
// tag means "no language known" rather than naming a real language.
func IsUndetermined(code string) bool {
	code = strings.ToLower(strings.TrimSpace(code))
	return code == "" || code == "und" || code == "unk" || code == "zxx"
}

// nameToCode maps Sonarr/Radarr's human-readable originalLanguage.name
// (their fixed list's English display names) to the same canonical codes
// Normalize produces.
var nameToCode = map[string]string{
	"english": "eng", "german": "ger", "french": "fre", "spanish": "spa",
	"italian": "ita", "japanese": "jpn", "korean": "kor", "chinese": "chi",
	"mandarin": "chi", "cantonese": "chi", "portuguese": "por", "russian": "rus",
	"dutch": "dut", "flemish": "dut", "swedish": "swe", "norwegian": "nor",
	"danish": "dan", "finnish": "fin", "polish": "pol", "turkish": "tur",
	"arabic": "ara", "hindi": "hin", "thai": "tha", "vietnamese": "vie",
	"czech": "cze", "greek": "gre", "hebrew": "heb", "hungarian": "hun",
	"romanian": "rum", "ukrainian": "ukr", "indonesian": "ind",
}

// NameToCode maps an arr_items.original_language display name (e.g.
// "English") to a canonical code, or "" when unrecognized.
func NameToCode(name string) string {
	return nameToCode[strings.ToLower(strings.TrimSpace(name))]
}
