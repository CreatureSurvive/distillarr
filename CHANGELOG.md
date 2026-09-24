# Changelog

All notable changes to Distillarr are listed here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions
follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.1.0]

First public release.

### Added
- Library scanner with per-file HEVC/AV1/H.264 recommendations, size
  estimates and issue detection (Apple compatibility, legacy formats, PCM
  audio, mixed seasons, extra languages, missing subtitles).
- Queue with processing windows, per-backend hardware encoding (Intel QSV,
  VA-API, NVIDIA NVENC, software), VMAF-targeted quality search, A/B
  previews and verified, crash-safe in-place replacement with a trash.
- Sonarr/Radarr integration: policies from tags, rescans, write-back,
  webhooks with settle-delay intake, codec-penalty checks, autopilot
  rules with a nightly budget.
- Jellyfin and Plex: refresh after replace, "date added" kept, playback
  awareness, forced-transcode detection.
- Audio policies, language pruning, text and image subtitle sidecars
  with optional OCR, Bazarr rescans.
- Upscaling with shader (libplacebo) and neural (Real-ESRGAN) tiers.
- Notifications through shoutrrr, Prometheus `/metrics`, library trends.
- Authentication: admin account, API keys, LAN-bypass, proxy-header and
  disabled modes; first-run setup wizard.
