// Package issues finds things wrong with a file (or a season) against
// the library's target: Apple/streaming compatibility, legacy formats,
// wasteful audio, and files worth re-encoding. Each issue says whether a
// quick fix (remux, video untouched) resolves it or a re-encode is needed.
package issues

import (
	"strings"

	"mediatrans/internal/config"
	"mediatrans/internal/encode"
	"mediatrans/internal/store"
)

// Fix kinds.
const (
	Quick    = "quick"    // remux / audio-only: video copied bit-exact, minutes
	Reencode = "reencode" // needs a full video encode
	Info     = "info"     // nothing to run; worth knowing
)

// Type describes one kind of issue.
type Type struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Fix   string `json:"fix"`
	Help  string `json:"help"`
	Scope string `json:"scope"` // file | season
}

// Types lists every issue in display order.
var Types = []Type{
	{"no_hvc1", "HEVC not tagged hvc1 (Apple)", Quick, "Apple devices (and some TVs) only play HEVC in MP4 when it's tagged hvc1; hev1 and other tags won't play. Fixed by re-tagging; the video isn't touched.", "file"},
	{"no_faststart", "Index at the end (no faststart)", Quick, "The MP4 index sits after the video, so players must read the whole file before starting and seeking is slow over the network. Fixed by rewriting the file with the index first.", "file"},
	{"pcm_audio", "Uncompressed PCM audio", Quick, "Raw PCM is several times larger than lossless FLAC/ALAC for identical sound. Fixed by converting only the audio.", "file"},
	{"legacy_container", "Legacy container", Quick, "AVI, WMV, TS, MPG and similar containers have poor seeking, no modern subtitle support and patchy player support. Fixed by moving the streams into MKV/MP4.", "file"},
	{"legacy_codec", "Legacy video codec", Reencode, "MPEG-2, MPEG-4 ASP (Xvid/DivX), VC-1, WMV and similar need far more space and often can't direct-play. Needs a re-encode.", "file"},
	{"interlaced", "Interlaced video", Reencode, "Interlaced video shows combing on modern screens unless the player deinterlaces it. Re-encoding deinterlaces once, properly.", "file"},
	{"worth_reencoding", "Worth re-encoding", Reencode, "Would shrink by at least your savings threshold at your quality target.", "file"},
	{"quality_limited", "Can't reach quality target", Info, "Measured: no tested setting reached your VMAF target, usually because the source is already heavily compressed. Best left as is.", "file"},
	{"upgrade_pending", "Upgrade pending", Info, "A connected Sonarr/Radarr instance monitors this file and its quality cutoff isn't met yet, so a better release is probably coming. Skipped so a re-encode isn't wasted on a file about to be replaced.", "file"},
	{"mixed_season", "Mixed formats in a season", Reencode, "Episodes in the same season use different codecs, containers or resolutions, which can cause inconsistent playback or transcoding.", "season"},
}

// ByKey finds a type.
func ByKey(k string) (Type, bool) {
	for _, t := range Types {
		if t.Key == k {
			return t, true
		}
	}
	return Type{}, false
}

var legacyContainers = map[string]bool{
	"avi": true, "wmv": true, "asf": true, "flv": true, "mpg": true, "mpeg": true, "ts": true,
	"m2ts": true, "vob": true, "divx": true, "ogm": true, "rm": true, "rmvb": true, "3gp": true,
}

var legacyCodecs = map[string]bool{
	"mpeg1video": true, "mpeg2video": true, "mpeg4": true, "msmpeg4v1": true, "msmpeg4v2": true,
	"msmpeg4v3": true, "wmv1": true, "wmv2": true, "wmv3": true, "vc1": true, "h263": true,
	"rv30": true, "rv40": true, "vp6": true, "vp6f": true, "vp8": true, "theora": true, "flv1": true,
}

func isMP4(container string) bool {
	return container == "mp4" || container == "m4v" || container == "mov"
}

// Detect returns the issue keys for one file. worth/measuredMiss come
// from its current recommendation.
func Detect(f *store.File, worth, measuredMiss, upgradePending bool) []string {
	var out []string
	// Any tag other than hvc1 (hev1, blank-in-MP4 "[0][0][0][0]", ...);
	// "" means the tag couldn't be read, so don't guess.
	if f.MetaChecked && f.VideoCodec == "hevc" && isMP4(f.Container) && f.VideoTag != "" && f.VideoTag != "hvc1" {
		out = append(out, "no_hvc1")
	}
	if isMP4(f.Container) && f.Faststart == 0 {
		out = append(out, "no_faststart")
	}
	for _, a := range f.Audio {
		if strings.HasPrefix(a.Codec, "pcm_") || a.Codec == "lpcm" {
			out = append(out, "pcm_audio")
			break
		}
	}
	if legacyContainers[f.Container] {
		out = append(out, "legacy_container")
	}
	if legacyCodecs[f.VideoCodec] {
		out = append(out, "legacy_codec")
	}
	if f.Interlaced {
		out = append(out, "interlaced")
	}
	if worth {
		out = append(out, "worth_reencoding")
	}
	if measuredMiss {
		out = append(out, "quality_limited")
	}
	if upgradePending {
		out = append(out, "upgrade_pending")
	}
	return out
}

// Encode joins keys for storage (",a,b," so LIKE '%,a,%' matches exactly).
func Encode(keys []string) string {
	if len(keys) == 0 {
		return ""
	}
	return "," + strings.Join(keys, ",") + ","
}

// Decode splits the stored form.
func Decode(s string) []string {
	var out []string
	for _, k := range strings.Split(strings.Trim(s, ","), ",") {
		if k != "" {
			out = append(out, k)
		}
	}
	return out
}

// HasQuick reports whether any of a file's issues has a quick fix.
func HasQuick(keys []string) bool {
	for _, k := range keys {
		if t, ok := ByKey(k); ok && t.Fix == Quick {
			return true
		}
	}
	return false
}

// QuickFix returns remux-only settings that resolve every quick issue on
// the file at once (video copied bit-exact).
func QuickFix(f *store.File, cfg config.Config) encode.Settings {
	s := encode.Settings{
		VideoCopy:      true,
		Codec:          encode.Codec(f.VideoCodec),
		AudioPCMTarget: cfg.AudioPCMTarget,
		PreferMP4:      cfg.MP4(),
		Container:      f.Container,
		Backend:        encode.SW,
	}
	if s.AudioPCMTarget == "copy" {
		s.AudioPCMTarget = "flac"
	}
	switch {
	case legacyContainers[f.Container]:
		s.Container = "auto" // MP4 when everything fits, else MKV
	case isMP4(f.Container):
		s.Container = "mp4"
	default:
		s.Container = "mkv"
	}
	return s
}
