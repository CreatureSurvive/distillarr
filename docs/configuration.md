# Configuration reference

Everything below is set in the web UI (Settings) and stored in the app's
SQLite database, not in a file. The key in each table is the JSON name
used by `GET`/`PUT /api/v1/config`, which is handy for scripting: `PUT`
merges a partial object, so send only the keys you change.

Secrets (`jellyfin_api_key`, `plex_token`, `bazarr_key`, arr `api_key`,
notifier `url`) are never returned by `GET`; you get a `…_set: true` flag
instead, and a blank value in a `PUT` keeps the saved one.

## Environment variables

| Variable | Default | Effect |
|---|---|---|
| `DISTILLARR_LISTEN` | `:8080` | Address the HTTP server listens on inside the container. |
| `DISTILLARR_DB` | `/config/distillarr.db` | SQLite database path. |
| `DISTILLARR_PREVIEWS` | `/config/previews` | A/B preview clips. |
| `DISTILLARR_UPSCALE_WORK` | `/config/upscale` | Chunks of neural upscales in progress (they survive restarts). |
| `PUID` / `PGID` / `UMASK` | `1000` / `1000` / `022` | User, group and umask the app runs as (see `docs/packaging.md`). |
| `TZ` | `UTC` | Time zone for processing windows and the nightly summary. |
| `LIBVA_DRIVER_NAME` | driver default | VA-API driver (`iHD` for modern Intel, `radeonsi` for AMD). |

## Libraries and queue

| Key | Default | Effect |
|---|---|---|
| `libraries` | none | `[{name, path}]`. `name` is the library type: `movies` or `tvshows`. Paths are as the container sees them. |
| `paused` | `true` on a fresh install | No new job starts while paused. Resume from the Queue page. |
| `schedules` | none (always open) | Processing windows `{days, start, end}`; `days` is a Mon=1…Sun=64 bitmask, times are minutes after midnight and may wrap. Outside a window no new job starts; a running one finishes. |
| `workers` | `1` | Parallel encodes (1–8). |
| `max_attempts` | `3` | Tries per job before it's marked failed. |
| `measure_enabled` | on | Overnight VMAF measurements of files, to improve size estimates. |
| `measure_schedules` | 01:00–07:00 daily | When those measurements may run. |

## Encoding

| Key | Default | Effect |
|---|---|---|
| `default_codec` | `hevc` | `hevc`, `av1` or `h264`. |
| `preferred_backend` | `auto` | `auto`, `qsv`, `vaapi`, `nvenc` or `sw`. `auto` picks the best encoder that passed the hardware probe. |
| `vmaf_target` | `93` | Quality search target: `91`, `93`, `95`, or `0` for a fixed quality. |
| `default_quality` | `60` | 0–100 quality knob (start point of the search, or the fixed quality when the target is off). |
| `default_speed` | `medium` | Encoder preset. |
| `min_savings_pct` | `30` | Files that would shrink less than this are recommended "keep". |
| `recompress_hevc` | off | Also re-encode HEVC with unusually high bitrate. |
| `max_height` | `0` (keep) | Cap output height (e.g. `1080`). |
| `tonemap_hdr` | off | Tone-map HDR to SDR by default (off keeps HDR10/HLG). |
| `crop_bars` | off | Crop detected black bars from the output. |
| `container_goal` | `prefer_mp4` | `prefer_mp4` (MP4 when every kept track fits), `mp4_required` (convert whatever doesn't fit), `keep`. |
| `audio_rules` | built-in table | Per-codec action (`keep`/`convert`/`drop`) and target, keyed by codec name plus the virtual `pcm`. |
| `audio_pcm_target` | `flac` | Legacy PCM conversion target. |
| `add_stereo_compat` | off | Add an AAC 2.0 track when no stereo track would remain. |
| `defer_while_transcoding` | on | Don't start encodes while Jellyfin/Plex is transcoding for someone. |
| `hold_replace_while_playing` | on | Wait for playback to stop before swapping a file in. |

## Tracks and subtitles

| Key | Default | Effect |
|---|---|---|
| `lang_policy` | off | `{audio_mode, subs_mode, audio_keep, subs_keep, keep_undetermined, keep_commentary, best_per_language, keep_sdh}`. Modes: `off`, `report` (issue only), `apply`. Keep lists are ISO 639 codes. `subs_keep` also drives the `missing_subs` issue. |
| `lang_library_overrides` / `lang_instance_overrides` | none | The same policy scoped to one library or one Sonarr/Radarr instance. |
| `subs_sidecar_mode` | off | Text subtitles: `extract_keep` or `extract_remove` into `.srt`/`.ass` sidecars. |
| `image_subs_mode` | keep | PGS/VobSub: `sidecar` (`.sup`/`.idx+.sub`) or `ocr` (to SRT). |
| `image_subs_keep_original` | on | Keep the image track in the file after OCR. |
| `ocr_min_confidence` | `80` | OCR below this leaves the track untouched. |

## Storage and trash

| Key | Default | Effect |
|---|---|---|
| `trash_enabled` | on | Keep replaced originals. |
| `trash_days` | `14` | Retention before originals are deleted. |
| `trash_dir` | blank | Blank: `.distillarr-trash` at the root of each library's filesystem (or in the library folder). Set a path only if every library is on that filesystem. |
| `allow_trash_copy` | off | When the trash is on another filesystem, copy the original instead of failing the job. |
| `disk_pressure_pct` | `0` (off) | Free-space percentage at which disk-pressure mode starts. |
| `disk_pressure_budget_x` | `2` | Autopilot budget multiplier under disk pressure. |

## Upscaling

| Key | Default | Effect |
|---|---|---|
| `upscale_output` | `copy` | `copy` writes a new file beside the source; `replace` swaps it in (original to trash). |
| `upscale_schedules` | 22:00–07:00 daily | Window for neural upscales (hours per file; paused when it closes). |

## Integrations

| Key | Default | Effect |
|---|---|---|
| `jellyfin_url`, `jellyfin_api_key`, `jellyfin_path_map` | blank | Jellyfin connection. Path map is `jellyfin=here`, e.g. `/data=/media`. |
| `plex_url`, `plex_token`, `plex_path_map` | blank | Plex connection. |
| `plex_keep_added_at` | on | Restore Plex's "date added" after a refresh. |
| `bazarr_url`, `bazarr_key`, `bazarr_path_map` | blank | Bazarr connection (rescans, subtitle search). |
| `arr_instances` | none | Sonarr/Radarr connections, see below. |
| `webhook_base_url` | blank | Address Sonarr/Radarr reach this app at; also the base for links in notifications. |
| `upgrade_loop_days` | `14` | A download replacing a file re-encoded this recently counts as an upgrade loop. |

### `arr_instances[]`

| Key | Default | Effect |
|---|---|---|
| `name`, `kind`, `url`, `api_key`, `path_map` | — | `kind` is `sonarr` or `radarr`; path map as above. |
| `enabled` | on | |
| `skip_upgrade_pending` | on | Skip monitored items whose quality cutoff isn't met. |
| `tag_policy_enabled` | on | Read the steering tags below. |
| `skip_tag`, `av1_tag`, `hevc_tag`, `remux_only_tag` | `mt-skip`, `mt-av1`, `mt-hevc`, `mt-remux-only` | Tags that skip a file, force a codec, or allow quick fixes only. |
| `rescan_after_replace` | on | Ask the instance to rescan after a replace. |
| `tag_after_reencode`, `reencode_tag` | off | Tag the item after a re-encode (and on an upgrade loop, with the skip tag). |
| `unmonitor_after_reencode` | off | Unmonitor the episode/movie after a re-encode. |
| `webhook_intake`, `webhook_settle_minutes` | off, `30` | Queue imports from the webhook after a settle delay. |

## Automation

| Key | Default | Effect |
|---|---|---|
| `autopilot_enabled` | off | Rule-driven queueing of new and existing files. |
| `auto_rules` | none | Ordered rules; first match wins (see the Automation tab). |
| `autopilot_budget_gb`, `autopilot_budget_hours` | `0` (unlimited) | Per-window cap on what autopilot queues. |

## Notifications

`notifiers[]`: `{name, url, enabled, events, min_level, quiet_start,
quiet_end}`. `url` is a [shoutrrr](https://github.com/nicholas-fedor/shoutrrr/tree/main/docs/services)
service URL. `events` lists event keys (`GET /api/v1/notify/events`);
`min_level` is `info`, `warning` or `error`; quiet hours are minutes after
midnight (equal values: none).

## Security

| Key | Default | Effect |
|---|---|---|
| `auth_mode` | `required` | `required`, `lan_bypass` (no login from `auth_cidrs`), `proxy_header` (trust `proxy_header` from `trusted_proxies`), or `disabled`. |
| `auth_cidrs` | private ranges | Ranges `lan_bypass` trusts. |
| `trusted_proxies` | none | Reverse-proxy addresses allowed to set the username header. |
| `proxy_header` | `Remote-User` | Header carrying the username (Authelia, Authentik). |
| `metrics_public` | off | Serve `/metrics` without auth. Otherwise send an API key in `X-Api-Key`. |

The admin account, sessions and API keys live in the database (Settings →
Security), not in the config object.
