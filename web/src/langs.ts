// SPDX-License-Identifier: GPL-3.0-or-later

// ISO 639-2/B codes matched to display names, for language pruning's
// keep-list editors. Keep this list in sync with
// internal/langprune/langcodes.go's codeAliases/nameToCode canonical
// codes — it's the same set, display-name side.
export const LANGUAGES: { code: string; name: string }[] = [
  { code: "eng", name: "English" },
  { code: "ger", name: "German" },
  { code: "fre", name: "French" },
  { code: "spa", name: "Spanish" },
  { code: "ita", name: "Italian" },
  { code: "jpn", name: "Japanese" },
  { code: "kor", name: "Korean" },
  { code: "chi", name: "Chinese" },
  { code: "por", name: "Portuguese" },
  { code: "rus", name: "Russian" },
  { code: "dut", name: "Dutch" },
  { code: "swe", name: "Swedish" },
  { code: "nor", name: "Norwegian" },
  { code: "dan", name: "Danish" },
  { code: "fin", name: "Finnish" },
  { code: "pol", name: "Polish" },
  { code: "tur", name: "Turkish" },
  { code: "ara", name: "Arabic" },
  { code: "hin", name: "Hindi" },
  { code: "tha", name: "Thai" },
  { code: "vie", name: "Vietnamese" },
  { code: "cze", name: "Czech" },
  { code: "gre", name: "Greek" },
  { code: "heb", name: "Hebrew" },
  { code: "hun", name: "Hungarian" },
  { code: "rum", name: "Romanian" },
  { code: "ukr", name: "Ukrainian" },
  { code: "ind", name: "Indonesian" },
];

const byCode = new Map(LANGUAGES.map((l) => [l.code, l.name]));

// langName looks up a display name for a raw track language code/tag
// ("" or "und"/"unk"/"zxx" show as "Undetermined"); an unrecognized code
// falls back to itself, uppercased, so it's still legible.
export function langName(code: string): string {
  const c = (code || "").toLowerCase().trim();
  if (c === "" || c === "und" || c === "unk" || c === "zxx") return "Undetermined";
  return byCode.get(c) || code.toUpperCase();
}
