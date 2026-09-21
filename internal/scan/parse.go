// Package scan walks the media libraries and parses names into
// title/year/season/episode metadata following the library's
// Sonarr/Radarr-style naming.
package scan

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Parsed is the result of parsing one media filename.
type Parsed struct {
	Title      string // movie title or show title
	Year       int
	Season     int // -1 for movies, 0 for specials
	Episode    int // -1 for movies
	EpTitle    string
	QualityTag string // "Bluray-1080p", "WEBDL-1080p", ...
}

var (
	// "Show - S01E02 - Red Coast WEBDL-1080p" and "the.bear.s01e03..."
	epRe = regexp.MustCompile(`^(?P<show>.*?)[\s._-]*[Ss](?P<season>\d{1,2})[Ee](?P<ep>\d{1,4})(?P<rest>.*)$`)
	// "Season 1", "Season 01", "Specials"
	seasonDirRe = regexp.MustCompile(`(?i)^season[ ._]*(\d{1,2})$`)
	// "Movie Title (2008)" or "Movie Title 2008"
	yearRe = regexp.MustCompile(`[\s._(-]\(?((19|20)\d{2})\)?[\s._)-]*$`)
	// "Bluray-1080p", "WEB-DL 1080p", "1080p Proper" ...
	qualityRe = regexp.MustCompile(`(?i)\b((?:blu-?ray|bd|bdrip|brrip|webdl|web-?dl|web|webrip|hdtv|dvd|dvdrip|remux|hd-?rip|cam|screener|hddvd)[\s._-]*(\d{3,4}p|\d{3,4}i))\b(.*)$`)
	// Bare resolution: "Title 1080p" (trailing) or "1080p rest" (leading)
	bareResTailRe = regexp.MustCompile(`[\s._-]\(?((?:2160|1080|720|480)[pi])\)?$`)
	bareResHeadRe = regexp.MustCompile(`^((?:2160|1080|720|480)[pi])\b(.*)$`)
)

// VideoExts are media file extensions we scan.
var VideoExts = map[string]bool{
	".mkv": true, ".mp4": true, ".m4v": true, ".avi": true, ".mov": true,
	".wmv": true, ".mpg": true, ".mpeg": true, ".ts": true, ".m2ts": true,
	".flv": true, ".webm": true, ".mp2": false, ".vob": true,
}

// SubExts are external subtitle extensions.
var SubExts = map[string]bool{
	".srt": true, ".ass": true, ".ssa": true, ".sub": true, ".idx": true,
	".vtt": true, ".sup": true, ".smi": true, ".pjs": true, ".jss": true,
}

// IsVideoFile reports whether path has a video extension.
func IsVideoFile(name string) bool {
	return VideoExts[strings.ToLower(filepath.Ext(name))]
}

// IsSubFile reports whether path has a subtitle extension.
func IsSubFile(name string) bool {
	return SubExts[strings.ToLower(filepath.Ext(name))]
}

// SkipDir reports directories that never contain primary media.
func SkipDir(name string) bool {
	switch strings.ToLower(name) {
	case "trailers", "extras", "bonus", "featurettes", "featurette",
		"behind the scenes", "deleted scenes", "interviews", "scenes",
		"sample", "samples", ".recycle", "#recycle", ".trash":
		return true
	}
	return strings.HasPrefix(name, ".")
}

// SkipFile reports files that are never primary media.
func SkipFile(name, extLower string) bool {
	base := strings.ToLower(name)
	if strings.HasPrefix(base, "sample") || strings.HasPrefix(base, "theme.") {
		return true
	}
	switch extLower {
	case ".xml", ".nfo", ".jpg", ".jpeg", ".png", ".txt", ".json", ".mp3", ".torrent", ".!ut", ".part":
		return true
	}
	return false
}

// isSeasonDir reports "Season N" and "Specials" style directories.
func isSeasonDir(name string) bool {
	return seasonDirRe.MatchString(name) || strings.EqualFold(name, "specials")
}

// Parse derives metadata from a file path: the show title prefers the
// show directory (canonical grouping key), falling back to the
// filename's own show name; movie title/year from the file stem with
// the directory as fallback.
func Parse(library, path string) Parsed {
	p := Parsed{Season: -1, Episode: -1}
	dir, base := filepath.Split(path)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	parent := filepath.Base(strings.TrimSuffix(strings.TrimSuffix(dir, "/"), "/"))
	grandparent := filepath.Base(strings.TrimSuffix(
		filepath.Dir(strings.TrimSuffix(dir, "/")), "/"))

	if library == "tvshows" {
		// Canonical show title: the directory above "Season N"/"Specials"
		// (or the immediate parent when episodes sit in the show root).
		showDir := parent
		if isSeasonDir(parent) {
			showDir = grandparent
		}
		showTitle := titleFromYearDir(showDir)

		if m := epRe.FindStringSubmatch(stem); m != nil {
			p.Title = firstNonEmpty(showTitle, cleanTitle(m[1]))
			p.Season, _ = strconv.Atoi(m[2])
			p.Episode, _ = strconv.Atoi(m[3])
			rest := strings.TrimLeft(strings.TrimSpace(m[4]), "- _.")
			p.EpTitle, p.QualityTag = splitQuality(rest)
		} else if sd := seasonDirRe.FindStringSubmatch(parent); sd != nil {
			p.Title = showTitle
			p.Season, _ = strconv.Atoi(sd[1])
			p.EpTitle, p.QualityTag = splitQuality(stem)
		} else if strings.EqualFold(parent, "specials") {
			p.Title = showTitle
			p.Season = 0
			p.EpTitle, p.QualityTag = splitQuality(stem)
		} else {
			p.Title = firstNonEmpty(showTitle, cleanTitle(stem))
			p.EpTitle, p.QualityTag = "", ""
		}
		return p
	}

	// Movies: prefer the stem, fall back to the directory for title/year.
	t, y, q := splitMovieStem(stem)
	if t == "" {
		t = titleFromYearDir(parent)
	}
	if y == 0 {
		if m := yearRe.FindStringSubmatch(parent); m != nil {
			y, _ = strconv.Atoi(m[1])
		}
	}
	p.Title, p.Year, p.QualityTag = t, y, q
	return p
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// normTag normalizes a quality tag: separators → spaces, collapsed, upper.
func normTag(s string) string {
	s = strings.ReplaceAll(s, "-", " ")
	s = strings.ReplaceAll(s, ".", " ")
	s = strings.ReplaceAll(s, "_", " ")
	return strings.ToUpper(strings.Join(strings.Fields(s), " "))
}

// splitQuality splits "Red Coast WEBDL-1080p" into title + quality tag.
func splitQuality(rest string) (string, string) {
	if m := qualityRe.FindStringSubmatch(rest); m != nil {
		title := strings.TrimRight(rest[:strings.Index(rest, m[0])], " -_.")
		return title, normTag(m[1] + " " + m[3])
	}
	if m := bareResHeadRe.FindStringSubmatch(rest); m != nil {
		return cleanTitle(m[2]), normTag(m[1])
	}
	if i := bareResTailRe.FindStringIndex(rest); i != nil {
		return strings.TrimSpace(rest[:i[0]]), normTag(rest[i[0]+1:])
	}
	return strings.TrimSpace(rest), ""
}

// splitMovieStem splits "Iron Man (2008) Bluray-1080p" → title/year/tag.
func splitMovieStem(stem string) (string, int, string) {
	quality := ""
	if m := qualityRe.FindStringSubmatch(stem); m != nil {
		quality = m[1] + " " + m[3]
		stem = strings.TrimRight(stem[:strings.Index(stem, m[0])], " -_.")
	} else if m := bareResHeadRe.FindStringSubmatch(stem); m != nil {
		quality = m[1]
		stem = m[2]
	} else if i := bareResTailRe.FindStringIndex(stem); i != nil {
		quality = stem[i[0]+1:]
		stem = strings.TrimSpace(stem[:i[0]])
	}
	year := 0
	if m := yearRe.FindStringSubmatch(stem); m != nil {
		year, _ = strconv.Atoi(m[1])
		stem = strings.TrimRight(stem[:strings.Index(stem, m[1])], " -_()")
	}
	return cleanTitle(stem), year, normTag(quality)
}

// titleFromYearDir parses "Show Name (2024)" → "Show Name".
func titleFromYearDir(dir string) string {
	if m := yearRe.FindStringSubmatch(dir); m != nil {
		return cleanTitle(strings.TrimRight(dir[:strings.Index(dir, m[1])], " -_()"))
	}
	return cleanTitle(dir)
}

// cleanTitle normalizes dots/underscores used as spaces in scene names.
func cleanTitle(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, ".", " ")
	s = strings.ReplaceAll(s, "_", " ")
	s = strings.Join(strings.Fields(s), " ")
	return s
}
