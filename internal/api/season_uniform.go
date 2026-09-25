// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// seasonUniform makes a season queue leave the season uniform, so it
// doesn't come back as a mixed_season issue (codec or container
// differing between episodes). queued[i] reports whether files[i] is
// already going to be queued with plans[i].Setting. It returns:
//   - settings to use for every queued episode (the container pinned
//     for the whole season), keyed by index;
//   - extra jobs for episodes that weren't going to be queued but would
//     otherwise stand out: a re-encode when their codec differs, a
//     remux when only the container does;
//   - how many episodes it had to leave alone (already re-encoded, or
//     flagged "caution", e.g. Dolby Vision), which stay different.
func seasonUniform(cfg config.Config, files []*store.File, plans []episodePlan, queued func(i int) bool,
	reencoded func(fileID int64) bool) (pinned map[int]encode.Settings, extra map[int]encode.Settings, leftAlone int) {
	pinned, extra = map[int]encode.Settings{}, map[int]encode.Settings{}
	bySeason := map[int][]int{}
	for i, f := range files {
		bySeason[f.Season] = append(bySeason[f.Season], i)
	}
	for _, idx := range bySeason {
		var target encode.Settings
		found := false
		codecCount := map[encode.Codec]int{}
		for _, i := range idx {
			if queued(i) && !plans[i].Setting.VideoCopy {
				codecCount[plans[i].Setting.Codec]++
				if !found || codecCount[plans[i].Setting.Codec] > codecCount[target.Codec] {
					target, found = plans[i].Setting, true
				}
			}
		}
		if !found {
			continue // no re-encode in this season: nothing to harmonise
		}
		container := seasonContainer(cfg, files, idx)
		for _, i := range idx {
			f := files[i]
			if queued(i) {
				st := plans[i].Setting
				if container != "" {
					st.Container = container
				}
				pinned[i] = st
				continue
			}
			sameCodec := f.VideoCodec == string(target.Codec)
			sameContainer := container == "" || f.Container == container || (container == "mp4" && f.Container == "m4v")
			if sameCodec && sameContainer {
				continue
			}
			if reencoded(f.ID) || plans[i].Action == "caution" {
				leftAlone++
				continue
			}
			st := plans[i].Setting
			if container != "" {
				st.Container = container
			}
			if sameCodec {
				st.VideoCopy = true // container only: a remux, video untouched
			} else {
				st.VideoCopy = false
				st.Codec = target.Codec
				st.Quality = target.Quality
				st.VMAFTarget = target.VMAFTarget
			}
			extra[i] = st
		}
	}
	return pinned, extra, leftAlone
}

// seasonContainer is the container every episode of a season should end
// up in: under prefer_mp4, MP4 only when every episode fits it (from the
// stored streams, like the audio_blocks_mp4 issue), else MKV;
// mp4_required is always MP4; "keep" doesn't pin anything ("").
func seasonContainer(cfg config.Config, files []*store.File, idx []int) string {
	switch cfg.ContainerGoal {
	case "mp4_required":
		return "mp4"
	case "keep":
		return ""
	}
	for _, i := range idx {
		if !storedFitsMP4(cfg, files[i]) {
			return "mkv"
		}
	}
	return "mp4"
}

func storedFitsMP4(cfg config.Config, f *store.File) bool {
	for _, s := range f.Subs {
		switch s.Codec {
		case "subrip", "srt", "mov_text", "webvtt", "text":
		default:
			return false
		}
	}
	for _, a := range f.Audio {
		if t, ok := encode.ResolveAudioRule(cfg.AudioRules, a.Codec, a.Channels, "mp4"); ok {
			if t.Action == "convert" && t.Codec == "opus" {
				return false
			}
			continue
		}
		if !encode.IsMP4AudioSafe(a.Codec) {
			return false
		}
	}
	return true
}
