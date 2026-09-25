// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

func TestSeasonUniform(t *testing.T) {
	cfg := config.Config{ContainerGoal: "prefer_mp4"}
	aac := []store.AudioStream{{Codec: "aac", Channels: 2}}
	files := []*store.File{
		{ID: 1, Season: 1, VideoCodec: "h264", Container: "mkv", Audio: aac},  // worth: queued
		{ID: 2, Season: 1, VideoCodec: "h264", Container: "mkv", Audio: aac},  // not worth: needs the encode
		{ID: 3, Season: 1, VideoCodec: "hevc", Container: "mkv", Audio: aac},  // right codec, wrong container: remux
		{ID: 4, Season: 1, VideoCodec: "hevc", Container: "mp4", Audio: aac},  // already matches
		{ID: 5, Season: 1, VideoCodec: "h264", Container: "mkv", Audio: aac},  // already re-encoded by us: left alone
		{ID: 6, Season: 2, VideoCodec: "h264", Container: "mkv", Audio: aac},  // season with nothing queued: untouched
	}
	enc := encode.Settings{Codec: encode.HEVC, Quality: 55, Container: "auto"}
	plans := make([]episodePlan, len(files))
	for i := range files {
		plans[i] = episodePlan{FileID: files[i].ID, Action: "skip", Setting: enc}
	}
	plans[0].Action, plans[0].Worth = "transcode", true
	queued := func(i int) bool { return i == 0 }
	reencoded := func(id int64) bool { return id == 5 }

	pinned, extra, left := seasonUniform(cfg, files, plans, queued, reencoded)
	if p := pinned[0]; p.Container != "mp4" {
		t.Errorf("queued episode not pinned to the season container: %+v", p)
	}
	if e, ok := extra[1]; !ok || e.VideoCopy || e.Codec != encode.HEVC || e.Container != "mp4" {
		t.Errorf("h264 episode should be re-encoded to match: %+v ok=%v", e, ok)
	}
	if e, ok := extra[2]; !ok || !e.VideoCopy || e.Container != "mp4" {
		t.Errorf("hevc/mkv episode should be remuxed to mp4: %+v ok=%v", e, ok)
	}
	if _, ok := extra[3]; ok {
		t.Error("matching episode must not be queued")
	}
	if _, ok := extra[4]; ok || left != 1 {
		t.Errorf("re-encoded episode must be left alone and counted: left=%d", left)
	}
	if _, ok := extra[5]; ok {
		t.Error("a season with nothing queued must be untouched")
	}

	// A PGS subtitle anywhere in the season pins it to MKV.
	files[3].Subs = []store.SubStream{{Codec: "hdmv_pgs_subtitle"}}
	pinned, extra, _ = seasonUniform(cfg, files, plans, queued, reencoded)
	if pinned[0].Container != "mkv" {
		t.Errorf("season with image subs should pin mkv: %+v", pinned[0])
	}
	if e, ok := extra[3]; !ok || !e.VideoCopy {
		t.Errorf("hevc/mp4 episode should be remuxed to the season's mkv: %+v ok=%v", e, ok)
	}
}
