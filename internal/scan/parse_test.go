// SPDX-License-Identifier: GPL-3.0-or-later

package scan

import (
	"path/filepath"
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

func TestParseTV(t *testing.T) {
	cases := []struct {
		path         string
		title        string
		season, ep   int
		epTitle, tag string
	}{
		{"/srv/media/tvshows/3 Body Problem (2024)/Season 1/3 Body Problem - S01E02 - Red Coast WEBDL-1080p.mp4",
			"3 Body Problem", 1, 2, "Red Coast", "WEBDL 1080P"},
		{"/srv/media/tvshows/Fargo (2014)/Season 02/Fargo - S02E07 - Did you do this.mp4",
			"Fargo", 2, 7, "Did you do this", ""},
		{"/srv/media/tvshows/The Bear (2022)/Season 1/the.bear.s01e03.1080p.web.h264.mkv",
			"The Bear", 1, 3, "web h264", "1080P"},
		{"/srv/media/tvshows/Adventure Time (2010)/Season 05/Adventure Time - S05E12 - Something WEBRip-720p Proper.mkv",
			"Adventure Time", 5, 12, "Something", "WEBRIP 720P PROPER"},
		{"/srv/media/tvshows/Dexter's Laboratory (1996)/Specials/Dexter - S00E01 - Pilot.avi",
			"Dexter's Laboratory", 0, 1, "Pilot", ""},
		{"/srv/media/tvshows/The Wild Thornberrys (1998)/Season 1/The WILD Thornberrys - S01 E01 - Flood Warning (480p - DVDRip).mp4",
			"The Wild Thornberrys", 1, 1, "Flood Warning (480p - DVDRip)", ""},
		{"/srv/media/tvshows/Show (2001)/Season 2/Show.S02.E05.Title.mkv",
			"Show", 2, 5, "Title", ""},
		{"/srv/media/tvshows/Show (2001)/Season 3/Show - 3x07 - Title.mkv",
			"Show", 3, 7, "Title", ""},
		{"/srv/media/tvshows/MythBusters (2003)/Season 02/MythBusters.S2004E05.Buried.in.Concrete.mp4",
			"MythBusters", 2, 5, "Buried.in.Concrete", ""},
		{"/srv/media/tvshows/King of the Hill (1997)/Season 01/101 - Pilot.mkv",
			"King of the Hill", 1, 1, "Pilot", ""},
		{"/srv/media/tvshows/King of the Hill (1997)/Season 12/1203 - Title.mkv",
			"King of the Hill", 12, 3, "Title", ""},
		{"/srv/media/tvshows/Show (2001)/Season 01/2012 Special.mkv",
			"Show", 1, -1, "2012 Special", ""},
	}
	for _, c := range cases {
		p := Parse("tvshows", c.path)
		if p.Title != c.title || p.Season != c.season || p.Episode != c.ep ||
			p.EpTitle != c.epTitle || p.QualityTag != c.tag {
			t.Errorf("Parse(%q)\n got {%-q %d %d %-q %-q}\nwant {%-q %d %d %-q %-q}",
				c.path, p.Title, p.Season, p.Episode, p.EpTitle, p.QualityTag,
				c.title, c.season, c.ep, c.epTitle, c.tag)
		}
	}
}

func TestParseMovie(t *testing.T) {
	cases := []struct {
		path, title string
		year        int
		tag         string
	}{
		{"/srv/media/movies/Iron Man (2008)/Iron Man (2008) Bluray-1080p.mp4", "Iron Man", 2008, "BLURAY 1080P"},
		{"/srv/media/movies/Swapped (2026)/Swapped (2026) WEBDL-2160p.mkv", "Swapped", 2026, "WEBDL 2160P"},
		{"/srv/media/movies/12 Angry Men (1957)/12 Angry Men (1957) DVD-480p.avi", "12 Angry Men", 1957, "DVD 480P"},
	}
	for _, c := range cases {
		p := Parse("movies", c.path)
		if p.Title != c.title || p.Year != c.year || p.QualityTag != c.tag {
			t.Errorf("Parse(%q) got {%q %d %q} want {%q %d %q}",
				c.path, p.Title, p.Year, p.QualityTag, c.title, c.year, c.tag)
		}
	}
}

func TestSplitQuality(t *testing.T) {
	title, tag := splitQuality("Red Coast WEBDL-1080p")
	if title != "Red Coast" || tag != "WEBDL 1080P" {
		t.Errorf("got %q / %q", title, tag)
	}
}

func TestSkipRules(t *testing.T) {
	if !SkipDir("Trailers") || !SkipDir("extras") || !SkipDir(".git") {
		t.Error("SkipDir failed")
	}
	if SkipDir("Season 1") {
		t.Error("Season dirs must not be skipped")
	}
	if !SkipFile("theme.mp3", ".mp3") || !SkipFile("sample.mkv", ".mkv") {
		t.Error("SkipFile failed")
	}
	if !IsVideoFile("x.MKV") || IsVideoFile("x.nfo") {
		t.Error("IsVideoFile failed")
	}
	if !IsSubFile("Movie.en.srt") {
		t.Error("IsSubFile failed")
	}
}

func TestReparseNames(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.NewManager(st)
	f := &store.File{Path: "/tv/The Wild Thornberrys (1998)/Season 1/The WILD Thornberrys - S01 E07 - Vacant Lot.mp4",
		Library: "tvshows", Title: "The Wild Thornberrys", Season: 1, Episode: -1}
	if err := st.UpsertFile(f, nil); err != nil {
		t.Fatal(err)
	}
	New(st, cfg).ReparseNames()
	got, _ := st.GetFile(f.ID)
	if got.Season != 1 || got.Episode != 7 || got.EpTitle != "Vacant Lot" {
		t.Errorf("after reparse: %+v", got)
	}
	if v, _, _ := st.KVGet("parser_version"); v != ParserVersion {
		t.Errorf("version not recorded: %q", v)
	}
}
