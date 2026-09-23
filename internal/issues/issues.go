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

// ForcesTranscodeLookbackDays is the forces_transcode issue's window: a
// non-direct playback session inside this many days counts.
const ForcesTranscodeLookbackDays = 30

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
	{"audio_blocks_mp4", "Audio blocks MP4", Quick, "The video (and any subtitles) already fit MP4, but the audio — TrueHD, DTS, FLAC or PCM with no matching conversion rule — would force MKV. Fixed by remuxing to MP4 and converting just the audio, per your audio rules.", "file"},
	{"mixed_season", "Mixed formats in a season", Reencode, "Episodes in the same season use different codecs, containers or resolutions, which can cause inconsistent playback or transcoding.", "season"},
	{"forces_transcode", "Forces client transcode", Info, "A Jellyfin or Plex session played this file with a transcode in the last 30 days, so at least one client couldn't play it directly. See the file page for the reasons.", "file"},
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
// from its current recommendation; forcesTranscode comes from the
// playback-events table; cfg supplies AudioRules for
// audio_blocks_mp4.
func Detect(f *store.File, cfg config.Config, worth, measuredMiss, upgradePending, forcesTranscode bool) []string {
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
	if audioBlocksMP4(f, cfg) {
		out = append(out, "audio_blocks_mp4")
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
	if forcesTranscode {
		out = append(out, "forces_transcode")
	}
	return out
}

// ReasonCategory buckets a raw transcode reason from either server into
// one of a small set of canonical keys, so playback_events and
// the forces_transcode detail stay comparable across Jellyfin's many
// TranscodeReasons enum values and Plex's decision fields:
//   - container, audio_codec, audio_channels: a quick fix (remux, or
//     an audio rule conversion) resolves it;
//   - video_codec, video_profile, video_level, bit_depth: needs a
//     re-encode;
//   - subtitle: image-subtitle burn-in, not automated yet (P4.x);
//   - bitrate: the client's own bitrate cap forced a transcode
//     (Jellyfin's ContainerBitrateExceedsLimit) — not a container
//     incompatibility, so it must not be lumped in with "container"; no
//     fix here re-encodes to a lower bitrate on request, not proactively.
func ReasonCategory(raw string) string {
	r := strings.ToLower(raw)
	switch {
	case strings.Contains(r, "subtitle"):
		return "subtitle"
	case strings.Contains(r, "bitrateexceedslimit"), strings.Contains(r, "containerbitrate"):
		return "bitrate"
	case strings.Contains(r, "container"):
		return "container"
	case strings.Contains(r, "audiochannel"):
		return "audio_channels"
	case strings.Contains(r, "audiocodec"), strings.Contains(r, "audiobitrate"),
		strings.Contains(r, "audiosamplerate"), strings.Contains(r, "secondaryaudio"):
		return "audio_codec"
	case strings.Contains(r, "videoprofile"):
		return "video_profile"
	case strings.Contains(r, "videolevel"):
		return "video_level"
	case strings.Contains(r, "bitdepth"):
		return "bit_depth"
	case strings.Contains(r, "video"), strings.Contains(r, "directplayerror"):
		return "video_codec"
	default:
		return "other"
	}
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

// mp4SafeVideoCodecs mirrors fitsMP4's VideoCopy check (encode.go) for the
// codecs ffmpeg can copy straight into an MP4 container.
var mp4SafeVideoCodecs = map[string]bool{"h264": true, "hevc": true, "av1": true, "mpeg4": true}

// audioBlocksMP4 reports whether f's video already fits MP4 but its audio
// would force MKV under cfg's AudioRules — "surface files held in MKV
// only by their audio". Computed from persisted store.File fields
// (mirroring fitsMP4's audio loop in encode.go) rather than a live
// media.Probe: store.File has no per-subtitle codec, but that's fine here
// since a subtitle-forced MKV isn't this issue's concern, and a legacy
// container is already its own issue (legacy_container's quick fix
// already routes through "auto", so it isn't blocked the same way).
func audioBlocksMP4(f *store.File, cfg config.Config) bool {
	if !mp4SafeVideoCodecs[f.VideoCodec] || legacyContainers[f.Container] || isMP4(f.Container) {
		return false
	}
	for _, a := range f.Audio {
		if t, ok := encode.ResolveAudioRule(cfg.AudioRules, a.Codec, a.Channels, "mp4"); ok {
			if t.Action == "convert" && t.Codec == "opus" {
				return true // opus itself doesn't mux MP4-safe here (fitsMP4 parity)
			}
			continue
		}
		if !encode.IsMP4AudioSafe(a.Codec) {
			return true
		}
	}
	return false
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
		AudioRules:     cfg.AudioRules,
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
	case audioBlocksMP4(f, cfg):
		s.Container = "auto" // audio_blocks_mp4: the rules resolve it, so let ChooseContainer pick MP4
	default:
		s.Container = "mkv"
	}
	return s
}
