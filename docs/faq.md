# FAQ

Common questions. Every setting is listed in
[configuration.md](configuration.md).

## Will Sonarr/Radarr re-download files Distillarr re-encodes?

Possibly, if your Sonarr or Radarr scores the encoded format negatively
in the quality profile a series or movie uses. This isn't unusual: the
[TRaSH Guides](https://github.com/TRaSH-Guides/Guides)'s recommended
quality profiles score 1080p x265 heavily negative by design, since
scene-encoded x265 releases have historically been inconsistent quality.
Distillarr's own re-encodes don't have that problem — they're built with
a measured VMAF quality target — but Sonarr/Radarr can't tell the
difference between "a good x265 encode" and "a scene release we don't
trust," because a quality profile scores by *codec*, not by source.

Distillarr checks each connected instance for this after every sync
(read-only: it reads your custom formats, quality profiles and naming
config, never anything of your media). If your setup would score a
re-encode's codec negatively, or if your naming format would write the
codec into the filename (which exposes it to a scoring rule you might
add later even if none exists today), Settings shows a warning on that
instance's card explaining exactly which profile and custom format,
matched by which term. Two independent things have to both be true for
this to actually matter in practice:

1. A quality profile actually in use by that series/movie scores a
   codec-matching custom format negatively.
2. Something in that custom format matches Distillarr's output — most
   such formats match on `ReleaseTitleSpecification` or
   `SourceSpecification`, which Sonarr/Radarr evaluate against the
   file's *current* name on a rescan, not the original release name. If
   Distillarr's rename never puts a codec token in the filename (Sonarr/
   Radarr write it if your naming format includes
   `{MediaInfo VideoCodec}` or `{MediaInfo Full}`; Distillarr's own
   rename only ever changes the file extension), nothing to match on
   ever appears there.

**The fix**: tag a re-encoded item with a
name you choose, and exclude that tag from the custom format's score in
Sonarr/Radarr (or in Recyclarr's config, under that custom format's
`quality_profiles` score override). Either way, the warning is
informational — Distillarr won't auto-queue anything under a setup like
this without an explicit "I understand," and that decision is never made
for you.

## Why does replacing a hardlinked file need confirmation?

If a file shares its data with another link — almost always a torrent
client still seeding the exact file your library entry points at —
replacing it doesn't free any disk space until that other link goes
away too, and the new encode sits on disk *in addition to* what's being
seeded rather than instead of it. Distillarr detects this (a file's
hardlink count, refreshed on every scan) and asks you to confirm before
queueing, or before replacing if a link appears mid-encode. Retention of
the replaced original always survives regardless of hardlink status.

## Why does Atmos/DTS:X sometimes come back as "just" 5.1/7.1?

Because of an audio rule. Converting TrueHD Atmos or DTS:X to E-AC-3
(for example to fit MP4) drops their object/height metadata, because
ffmpeg has no encoder for Dolby's Atmos-in-EAC3 variant. Distillarr
warns about this on the file's plan before it's queued rather than
silently downmixing; keep those tracks with a "copy" rule or MKV if
that matters to you.

## What does a VMAF target actually mean?

The number is a 0–100 quality score from Netflix's VMAF model, compared
against the original. Distillarr's default target (93) is considered
visually indistinguishable from the source at normal viewing distance
for a typical audience; measurements below that are visibly softer to
at least some viewers on close inspection. Turning the target up
produces a bigger file for the same source; turning it off encodes at a
fixed quality setting instead of searching for one.

## Which GPU does it use, and can I choose?

At startup (and from Settings → System → Hardware) Distillarr runs a short
test encode on every encoder and GPU it can see: each render node for
VA-API and QSV, NVENC, and the software encoders. Only combinations that
actually produce a valid file are used, so a GPU that can decode but not
encode AV1, say, simply isn't offered for AV1. With several GPUs, each
codec goes to the best one that passed (the Arc before the iGPU, for
instance); the file page shows which device a job will use.

To pin a backend, set Settings → Encoding → Encoder (QSV, VA-API, NVENC or
CPU) instead of Auto. Device numbering (`renderD128`, `renderD129`) isn't
stable between hosts or even boots, so Distillarr never relies on it.
Upscaling picks a Vulkan device the same way.

If a hardware encoder fails twice in 30 minutes it's marked degraded and
jobs fall back to another encoder until you reset it on the Hardware card.

## Why does it prefer MP4?

Because more devices direct-play MP4. Apple TV, iPhone/iPad, most smart
TVs and web browsers play HEVC and AAC/E-AC-3 in MP4 without a server-side
transcode, while MKV often forces a remux or transcode on those clients.
MP4 has limits, though: no image subtitles (PGS/VobSub), no styled ASS,
and no TrueHD/DTS without conversion. With the default "Prefer MP4", a file
becomes MP4 only when everything you keep fits; otherwise it stays MKV.
"MP4 required" converts what doesn't fit (per your audio rules and
subtitle settings), and "Keep" leaves the container alone.

HEVC in MP4 must be tagged `hvc1` and have its index at the start
(`faststart`) for Apple devices; Distillarr always writes it that way and
refuses a result that isn't. Changing the extension (`.mkv` to `.mp4`) is
handled: Sonarr/Radarr are asked to rescan, and it checks they picked up
the new path.
