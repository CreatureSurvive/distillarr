// SPDX-License-Identifier: GPL-3.0-or-later

// Package config holds runtime configuration, persisted as JSON in the
// kv table. Every mutation broadcasts a change so the scheduler and
// dispatcher pick up new windows / worker counts without a restart.
package config

import (
	"log"
	"strings"
	"sync"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/pathmap"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// Library is one media root.
type Library struct {
	Name string `json:"name"` // "movies" | "tvshows" (display + grouping key)
	Path string `json:"path"` // absolute, as seen by the container
}

// Schedule is one processing window. Days is a weekday bitmap,
// Monday=bit0 … Sunday=bit6 (0x7F = every day). Start/End are minutes
// since midnight, local time; End may wrap past midnight (e.g. 60→360
// means 01:00–06:00, 1380→300 means 23:00–05:00).
type Schedule struct {
	ID    int    `json:"id"`
	Label string `json:"label,omitempty"`
	Days  int    `json:"days"` // Mon=1<<0 .. Sun=1<<6
	Start int    `json:"start"` // minutes from midnight
	End   int    `json:"end"`   // minutes from midnight (may wrap)
}

// LangPolicy decides which audio/subtitle tracks langprune.Select keeps
// AudioMode/SubsMode are independent: "off" (default, untouched)
// | "report" (compute the extra_languages issue, change nothing) |
// "apply" (acts on the decision). Deliberately no default keep list —
// the API rejects report/apply while the matching list is empty.
type LangPolicy struct {
	AudioMode string   `json:"audio_mode,omitempty"` // off|report|apply
	SubsMode  string   `json:"subs_mode,omitempty"`  // off|report|apply
	AudioKeep []string `json:"audio_keep,omitempty"` // ISO 639-2 codes, user-entered
	SubsKeep  []string `json:"subs_keep,omitempty"`
	// KeepUndetermined keeps tracks with no/"und" language tag rather than
	// dropping them as unmatched. Default on: safer when the tag is just
	// missing metadata, not really a foreign track.
	KeepUndetermined *bool `json:"keep_undetermined,omitempty"`
	// KeepCommentary keeps commentary tracks that match the language list
	// like any other track. Default off: commentary is usually redundant
	// even in a kept language.
	KeepCommentary bool `json:"keep_commentary,omitempty"`
	// BestPerLanguage keeps only the single best track per language
	// (audio: most channels then highest bitrate; subtitles: the default
	// track) instead of every matching track.
	BestPerLanguage bool `json:"best_per_language,omitempty"`
	// KeepSDH keeps hearing-impaired subtitle tracks that match the
	// language list like any other track. Default on.
	KeepSDH *bool `json:"keep_sdh,omitempty"`
}

// KeepUndeterminedOn reports the effective setting (default on).
func (p LangPolicy) KeepUndeterminedOn() bool {
	return p.KeepUndetermined == nil || *p.KeepUndetermined
}

// KeepSDHOn reports the effective setting (default on).
func (p LangPolicy) KeepSDHOn() bool {
	return p.KeepSDH == nil || *p.KeepSDH
}

// LangOverride scopes LangPolicy to one library or arr instance.
// "inherit" (the zero value) changes nothing. "off" disables pruning for
// files in this scope regardless of the global policy. "custom" uses
// this override's own AudioKeep/SubsKeep in place of the global list —
// but a side left empty here simply inherits the global list for that
// side, so a custom override can never accidentally empty a list and
// have Select drop everything in it (see validateLangPolicy, which
// guards the same hazard for the global policy).
type LangOverride struct {
	Mode      string   `json:"mode,omitempty"` // inherit|off|custom
	AudioKeep []string `json:"audio_keep,omitempty"`
	SubsKeep  []string `json:"subs_keep,omitempty"`
}

func applyLangOverride(p *LangPolicy, ov LangOverride) {
	switch ov.Mode {
	case "off":
		// Clear the keep lists too, not just the modes: ForceDrops and
		// the dry-run report both re-derive "is this side active" from
		// whether a keep list is non-empty (so a rule or a preview can
		// force pruning on even when the global mode is "report"/"off").
		// If AudioKeep/SubsKeep survived an "off" override untouched,
		// that re-derivation would silently undo the override.
		p.AudioMode, p.SubsMode = "off", "off"
		p.AudioKeep, p.SubsKeep = nil, nil
	case "custom":
		if len(ov.AudioKeep) > 0 {
			p.AudioKeep = ov.AudioKeep
		}
		if len(ov.SubsKeep) > 0 {
			p.SubsKeep = ov.SubsKeep
		}
	}
}

// EffectiveLangPolicy resolves LangPolicy for one file: the instance
// override (if any) applies first, then the library override on top of
// that (more specific — a file's storage location is the more direct
// signal for what it should keep), then a per-file exemption always
// wins last by forcing both modes off.
func (c Config) EffectiveLangPolicy(library, instanceName string, exempt bool) LangPolicy {
	p := c.LangPolicy
	if instanceName != "" {
		if ov, ok := c.LangInstanceOverrides[instanceName]; ok {
			applyLangOverride(&p, ov)
		}
	}
	if ov, ok := c.LangLibraryOverrides[library]; ok {
		applyLangOverride(&p, ov)
	}
	if exempt {
		p.AudioMode, p.SubsMode = "off", "off"
	}
	return p
}

// EffectiveSidecarMode resolves subs_sidecar_mode for one file: a
// per-file override (fileOverride) always wins — it's the user's most
// specific, explicit choice; an autopilot rule override (ruleOverride,
// RuleAction.SidecarMode) wins next; otherwise the global default
// applies. "" resolves to "off".
func (c Config) EffectiveSidecarMode(fileOverride, ruleOverride string) string {
	switch {
	case fileOverride != "":
		return fileOverride
	case ruleOverride != "":
		return ruleOverride
	case c.SubsSidecarMode != "":
		return c.SubsSidecarMode
	default:
		return "off"
	}
}

// EffectiveImageSubsMode resolves image_subs_mode the same way
// EffectiveSidecarMode does: file override > rule override > global
// default. "" resolves to "keep" (do nothing).
func (c Config) EffectiveImageSubsMode(fileOverride, ruleOverride string) string {
	switch {
	case fileOverride != "":
		return fileOverride
	case ruleOverride != "":
		return ruleOverride
	case c.ImageSubsMode != "":
		return c.ImageSubsMode
	default:
		return "keep"
	}
}

// ImageSubsKeepOriginalOn reports the effective setting (default on).
func (c Config) ImageSubsKeepOriginalOn() bool {
	return c.ImageSubsKeepOriginal == nil || *c.ImageSubsKeepOriginal
}

// OCRMinConfidenceOn reports the effective threshold (default 80).
func (c Config) OCRMinConfidenceOn() float64 {
	if c.OCRMinConfidence == nil {
		return 80
	}
	return *c.OCRMinConfidence
}

// Config is the whole persisted configuration.
type Config struct {
	Libraries []Library `json:"libraries"`
	Workers   int       `json:"workers"`
	Paused    bool      `json:"paused"`
	Schedules []Schedule `json:"schedules"`

	// Overnight measuring: VMAF quality searches on candidates while
	// the GPU is otherwise idle, so estimates become measurements.
	MeasureEnabled   *bool      `json:"measure_enabled"`
	MeasureSchedules []Schedule `json:"measure_schedules"`

	DefaultCodec       string `json:"default_codec"`        // "hevc" | "av1"
	DefaultQuality     int    `json:"default_quality"`      // 0..100
	PreferredBackend   string `json:"preferred_backend"`   // auto|qsv|vaapi|nvenc|sw
	MinSavingsPct      int    `json:"min_savings_pct"`     // gate for "worth it"
	AudioPCMTarget     string `json:"audio_pcm_target"`    // flac|aac|eac3
	TrashEnabled       bool   `json:"trash_enabled"`
	TrashDays          int    `json:"trash_days"`
	MaxAttempts        int    `json:"max_attempts"`
	RecompressHEVC     bool   `json:"recompress_hevc"`     // allow re-encoding existing HEVC
	MaxHeight          int    `json:"max_height"`          // 0 = keep; e.g. 1080 caps output
	TonemapHDR         bool   `json:"tonemap_hdr"`         // HDR10→SDR profile off by default
	DefaultSpeed       string `json:"default_speed"`       // faster|fast|medium|slow|slower
	// TrashDir is a global trash override; "" (the default for new
	// installs) keeps each library's trash on its own filesystem (// see jobs.TrashDirFor). AllowTrashCopy lets retention copy when the
	// trash ends up on another filesystem instead of failing the job.
	AllowTrashCopy     bool   `json:"allow_trash_copy,omitempty"`
	TrashDir           string `json:"trash_dir"`           // on the media pool → hardlinks, no copy
	// PreferMP4 is deprecated: kept only so normalize() can
	// migrate an old stored value into ContainerGoal the first time this
	// config loads after the upgrade (true/nil → prefer_mp4, false →
	// keep). New code reads ContainerGoal, never this field.
	PreferMP4 *bool `json:"prefer_mp4"`
	// ContainerGoal: prefer_mp4 (default: "auto" output goes MP4 — HEVC
	// tagged hvc1, moov first — whenever every kept track fits, else
	// MKV) | mp4_required (always MP4; AudioRules convert whatever would
	// otherwise force MKV) | keep (MP4 stays MP4 if it still fits;
	// anything else stays non-MP4). Left "" until normalize() resolves
	// it (from PreferMP4 on first load, "prefer_mp4" after), so an
	// upgrading host's explicit choice survives.
	ContainerGoal string `json:"container_goal,omitempty"`
	// AudioRules is per-source-codec audio policy, keyed by
	// ffprobe codec_name ("truehd", "dts", "opus", "vorbis", "flac", ...)
	// plus the virtual key "pcm" covering every PCM variant. Empty means
	// only AudioPCMTarget and the built-in MP4-safety fallback apply, as
	// before audio rules existed. See encode.AudioRule.
	AudioRules map[string]encode.AudioRule `json:"audio_rules,omitempty"`
	// AddStereoCompat adds an AAC 2.0 track, downmixed with a
	// centre-weighted pan filter, whenever a file would otherwise keep no
	// stereo/mono track at all. Off by default. See encode.Settings.AddStereoCompat.
	AddStereoCompat bool `json:"add_stereo_compat,omitempty"`
	// CropBars crops detected black bars out of the output. Off by
	// default: films that switch aspect ratio (IMAX scenes) could lose
	// picture if the sampled frames missed the wider scenes.
	CropBars bool `json:"crop_bars"`
	// VMAFTarget is the quality every encode is tuned to (0 = use the
	// quality number as-is). 93 ≈ indistinguishable at normal viewing.
	VMAFTarget *float64 `json:"vmaf_target"`
	// UpscaleOutput is what an upscale job does with the source: "copy"
	// writes a new file beside it and never touches the original (default:
	// upscaling is subjective and its result can't be undone by re-encoding),
	// "replace" swaps it in place with the original kept in trash.
	UpscaleOutput string `json:"upscale_output"`
	// UpscaleSchedules is when neural upscales may run. They take hours per file
	// and hold a worker throughout, so they get their own window (overnight by
	// default) instead of the encode schedule. Empty means never on their own;
	// "Upscale now" always runs.
	UpscaleSchedules []Schedule `json:"upscale_schedules"`

	// LangPolicy: which audio/subtitle tracks to drop by language.
	// Everything off/empty by default — deliberately no default keep list.
	LangPolicy LangPolicy `json:"lang_policy,omitempty"`
	// LangLibraryOverrides / LangInstanceOverrides scope LangPolicy per
	// library (keyed by Library.Name) or per Sonarr/Radarr instance
	// (keyed by ArrInstance.Name, like RuleMatch.Instances). See
	// EffectiveLangPolicy.
	LangLibraryOverrides  map[string]LangOverride `json:"lang_library_overrides,omitempty"`
	LangInstanceOverrides map[string]LangOverride `json:"lang_instance_overrides,omitempty"`

	// SubsSidecarMode: "off" (default) | "extract_keep" |
	// "extract_remove". Off by default — embedded text subtitles already
	// fit MP4 as mov_text, so this is only for users who want sidecar
	// files too. Resolved per file by EffectiveSidecarMode.
	SubsSidecarMode string `json:"subs_sidecar_mode,omitempty"`

	// ImageSubsMode: "" (keep, default) | "sidecar" | "ocr". PGS
	// and VobSub sidecar files are not known to be selectable in Jellyfin
	// or Plex, so "sidecar" is
	// gated by a UI warning. Resolved per file
	// by EffectiveImageSubsMode (file override > rule override > this).
	ImageSubsMode string `json:"image_subs_mode,omitempty"`
	// ImageSubsKeepOriginal keeps the source image subtitle track in the
	// output alongside whatever sidecar/OCR mode produces. Default on
	// (the safer choice — dropping is a one-way loss of the original
	// bitmap); see ImageSubsKeepOriginalOn.
	ImageSubsKeepOriginal *bool `json:"image_subs_keep_original,omitempty"`
	// OCRMinConfidence gates whether an OCR result is used: below this,
	// the track stays untouched and the file gets the
	// subs_ocr_low_confidence info issue instead. Default 80 (see
	// OCRMinConfidenceOn); real tesseract word
	// confidence measures in the high 80s/low 90s for clean PGS tracks.
	OCRMinConfidence *float64 `json:"ocr_min_confidence,omitempty"`

	JellyfinURL    string `json:"jellyfin_url"`
	JellyfinAPIKey string `json:"jellyfin_api_key"`
	// JellyfinPathMap rewrites Jellyfin's view of paths to ours,
	// "from=to" (e.g. "/data=/srv/media" when Jellyfin mounts the library as /data).
	JellyfinPathMap string `json:"jellyfin_path_map"`

	// Plex: same shape as the Jellyfin fields above. No default
	// path map is set — unlike Jellyfin's, which predates the public
	// Distillarr release, this one shouldn't bake in one host's mount
	// layout.
	PlexURL     string `json:"plex_url"`
	PlexToken   string `json:"plex_token"`
	PlexPathMap string `json:"plex_path_map"`
	// PlexKeepAddedAt restores an item's "date added" after a
	// post-replace refresh if Plex changed it. Default on, like
	// Jellyfin already does unconditionally; see PlexKeepAddedAtOn.
	PlexKeepAddedAt *bool `json:"plex_keep_added_at,omitempty"`
	// Bazarr: optional. Bazarr stays the subtitle source; these
	// let Distillarr ask it to rescan after a replace and to search for
	// missing subtitles. The key is masked like plex_token.
	BazarrURL     string `json:"bazarr_url"`
	BazarrKey     string `json:"bazarr_key"`
	BazarrPathMap string `json:"bazarr_path_map"`
	// Jellystat: optional playback-history backfill. The key is
	// masked like bazarr_key.
	JellystatURL string `json:"jellystat_url,omitempty"`
	JellystatKey string `json:"jellystat_key,omitempty"`

	// DeferWhileTranscoding stops new encode jobs from starting while any
	// Jellyfin/Plex session is transcoding video; it can only take
	// effect once a server is connected. Default on; see
	// DeferWhileTranscodingOn.
	DeferWhileTranscoding *bool `json:"defer_while_transcoding,omitempty"`
	// HoldReplaceWhilePlaying waits for a file's playback session to end
	// before swapping the finished encode in, rather than
	// replacing a file someone is watching. Default on; see
	// HoldReplaceWhilePlayingOn.
	HoldReplaceWhilePlaying *bool `json:"hold_replace_while_playing,omitempty"`

	// ArrInstances: any number of Sonarr and Radarr connections. See
	// internal/arr for the client and for the design.
	ArrInstances []ArrInstance `json:"arr_instances"`

	// WebhookBaseURL overrides the host:port Settings shows in front of
	// each instance's webhook path. Blank means "use whatever
	// origin the browser is loaded from" (computed client-side) — only
	// needed when that's wrong for Sonarr/Radarr, e.g. they see this
	// container under a different Docker-network hostname.
	WebhookBaseURL string `json:"webhook_base_url,omitempty"`

	// Notifiers are notification targets, merged by id in
	// putConfig like arr_instances; URL is secret (masked, url_set).
	// Notification links are built from WebhookBaseURL (see PublicURL).
	Notifiers []Notifier `json:"notifiers,omitempty"`

	// Authentication. AuthMode: "required" (log in always),
	// "lan_bypass" (no login from AuthCIDRs, default the private ranges),
	// "proxy_header" (trust ProxyHeader from TrustedProxies, e.g.
	// Authelia/Authentik), or "disabled" (no auth at all). "" = required;
	// see AuthModeOn. Accounts, sessions and API keys live in the DB.
	AuthMode       string   `json:"auth_mode,omitempty"`
	AuthCIDRs      []string `json:"auth_cidrs,omitempty"`
	TrustedProxies []string `json:"trusted_proxies,omitempty"`
	ProxyHeader    string   `json:"proxy_header,omitempty"`
	// MetricsPublic leaves /metrics reachable without auth.
	MetricsPublic bool `json:"metrics_public,omitempty"`
	// UpgradeLoopDays: a Sonarr/Radarr download replacing a file
	// Distillarr re-encoded within this many days counts as an upgrade
	// loop. 0 = default 14; see UpgradeLoopDaysOn.
	UpgradeLoopDays int `json:"upgrade_loop_days,omitempty"`

	// AutopilotEnabled turns on rule-driven unattended queueing,
	// off by default. AutoRules are evaluated in order, first match
	// wins; see internal/autopilot.Evaluate, which takes these directly
	// (the types live here, not in internal/autopilot, since autopilot
	// imports internal/recs which imports this package — the types
	// can't live on the far side of that edge without a cycle).
	AutopilotEnabled bool       `json:"autopilot_enabled,omitempty"`
	AutoRules        []AutoRule `json:"auto_rules,omitempty"`
	// AutopilotBudgetGB / AutopilotBudgetHours cap what one processing
	// window occurrence spends on autopilot-origin work; 0 means
	// unlimited, which is also the zero-value default, so no accessor is
	// needed the way the default-on settings above need one.
	AutopilotBudgetGB    float64 `json:"autopilot_budget_gb,omitempty"`
	AutopilotBudgetHours float64 `json:"autopilot_budget_hours,omitempty"`
	// AutopilotOrder: "" (value, the default: bytes saved per GPU-second)
	// or "popular" (most-watched first, value breaking ties).
	AutopilotOrder string `json:"autopilot_order,omitempty"`

	// DiskPressurePct turns on disk-pressure mode once any
	// library's filesystem free space drops to this percentage or
	// below; 0 (the zero-value default) is off.
	DiskPressurePct float64 `json:"disk_pressure_pct,omitempty"`
	// DiskPressureBudgetX multiplies the autopilot budget while under
	// pressure. nil uses the default of 2x — a default-on multiplier,
	// so it needs the pointer+accessor pattern like PreferMP4/VMAFTarget.
	DiskPressureBudgetX *float64 `json:"disk_pressure_budget_x,omitempty"`
}

// RuleMatch: every set field must match (AND) for the rule to apply. An
// empty/zero field means "don't care" — except Animation, which is a
// real tri-state (nil = don't care, non-nil = must equal).
type RuleMatch struct {
	Libraries     []string `json:"libraries,omitempty"`  // "movies" | "tvshows"
	Instances     []string `json:"instances,omitempty"`  // arr instance display names
	Tags          []string `json:"tags,omitempty"`       // resolved arr tag names
	Origins       []string `json:"origins,omitempty"`    // intake origin: webhook | autopilot | playback | manual
	SrcCodecs     []string `json:"src_codecs,omitempty"` // h264, hevc, ...
	ResClasses    []int    `json:"res_classes,omitempty"` // 480/576/720/1080/2160
	MinSavingsPct int      `json:"min_savings_pct,omitempty"`
	MinAgeDays    int      `json:"min_age_days,omitempty"`
	IssueKeys     []string `json:"issue_keys,omitempty"`
	Animation     *bool    `json:"animation,omitempty"`
	// Popularity, from playback history (the sessions poller,
	// Jellystat backfill) and Plex view counts. MaxPlays is a pointer so
	// "at most 0 plays" (never watched) can be expressed.
	MinPlays      int  `json:"min_plays,omitempty"`
	MaxPlays      *int `json:"max_plays,omitempty"`
	NotPlayedDays int  `json:"not_played_days,omitempty"`
}

// RuleAction: what to do once When matches.
type RuleAction struct {
	Kind    string `json:"kind"` // queue | queue_override | quick_fix | ignore
	Codec   string `json:"codec,omitempty"`
	Quality int    `json:"quality,omitempty"`
	// AudioRules overrides the global audio policy for files this rule
	// matches, same shape as Config.AudioRules/encode.Settings.AudioRules
	// (keyed by source codec_name, plus the virtual "pcm" key). Only applies
	// with Kind == "queue_override"; nil means "use the global policy".
	AudioRules map[string]encode.AudioRule `json:"audio_rules,omitempty"`
	// PruneLanguages applies language pruning to files this rule matches
	// using the effective LangPolicy (global + scope overrides)
	// even when the matching AudioMode/SubsMode isn't globally "apply" —
	// a rule can turn pruning on for just its own matched files. A side
	// whose effective keep list is empty is skipped (never drops
	// everything). Only applies with Kind == "queue_override".
	PruneLanguages bool `json:"prune_languages,omitempty"`
	// SidecarMode overrides subs_sidecar_mode for files this rule matches
	// same values as Config.SubsSidecarMode. "" means "use the
	// file/global resolution unchanged". Only applies with
	// Kind == "queue_override"; still loses to an explicit per-file
	// override (see EffectiveSidecarMode).
	SidecarMode string `json:"sidecar_mode,omitempty"`
	// ImageSubsMode overrides image_subs_mode for files this rule matches
	// same values as Config.ImageSubsMode. "" means "use the
	// file/global resolution unchanged". Only applies with
	// Kind == "queue_override"; still loses to an explicit per-file
	// override (see EffectiveImageSubsMode).
	ImageSubsMode string `json:"image_subs_mode,omitempty"`
}

// AutoRule is one ordered entry in Config.AutoRules.
type AutoRule struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Enabled bool      `json:"enabled"`
	When    RuleMatch `json:"when"`
	Then    RuleAction `json:"then"`
}

// ArrInstance is one connected Sonarr or Radarr.
type ArrInstance struct {
	// ID is a stable generated slug: it keys webhook URLs and arr_items
	// rows, so it must never change once assigned, even if Name does.
	ID   string `json:"id"`
	Name string `json:"name"` // "Radarr 4K"
	Kind string `json:"kind"` // "sonarr" | "radarr"
	URL  string `json:"url"`
	// APIKey is a secret: masked by the same convention as
	// JellyfinAPIKey (never returned by GET /config; a blank value on
	// PUT keeps the stored key).
	APIKey  string `json:"api_key"`
	PathMap string `json:"path_map,omitempty"` // pathmap format; "" = identical paths
	Enabled *bool  `json:"enabled,omitempty"`  // default on; see (ArrInstance).On()
	// PenaltyAck records that the user acknowledged this instance's
	// codec-penalty warning and wants autopilot to proceed anyway
	// once autopilot exists. Never set automatically.
	PenaltyAck bool `json:"penalty_ack,omitempty"`

	// SkipUpgradePending: when a monitored item hasn't met its quality
	// cutoff, this instance probably expects a better release soon, so
	// don't spend GPU time on it. Default on; see .SkipUpgradePendingOn().
	SkipUpgradePending *bool `json:"skip_upgrade_pending,omitempty"`
	// TagPolicyEnabled: read the tag names below from items synced from
	// this instance. Default on; see .TagPolicyOn(). Only ever reads
	// tags the user already set in Sonarr/Radarr — never writes one
	// (write-back is opt-in).
	TagPolicyEnabled *bool `json:"tag_policy_enabled,omitempty"`
	// SkipTag/AV1Tag/HEVCTag/RemuxOnlyTag name the tags that steer a
	// file's recommendation (see recs.Policy). Blank means "use the
	// default name"; the defaults are Distillarr-specific ("mt-" for
	// this project's original name) so they can't collide with a tag the
	// user already uses for something else in Sonarr/Radarr.
	SkipTag      string `json:"skip_tag,omitempty"`
	AV1Tag       string `json:"av1_tag,omitempty"`
	HEVCTag      string `json:"hevc_tag,omitempty"`
	RemuxOnlyTag string `json:"remux_only_tag,omitempty"`

	// RescanAfterReplace: tell this instance to rescan a series/movie
	// right after Distillarr replaces one of its files. Default
	// on; see .RescanAfterReplaceOn(). A write, but a harmless one
	// (Sonarr/Radarr do this on their own schedule anyway) — still
	// something the user should knowingly enable, so it stays a toggle
	// rather than being silently assumed.
	RescanAfterReplace *bool `json:"rescan_after_replace,omitempty"`

	// TagAfterReencode: apply ReencodeTag to the series/movie after a
	// successful encode/remux/upscale-replace, creating the tag
	// on the instance if it doesn't exist yet. Off by default, unlike
	// the toggles above — write-back is opt-in, and the API layer
	// refuses to enable this with a blank ReencodeTag (no silent
	// default tag name).
	TagAfterReencode *bool  `json:"tag_after_reencode,omitempty"`
	ReencodeTag      string `json:"reencode_tag,omitempty"`

	// UnmonitorAfterReencode: unmonitor the specific episode/movie (not
	// the whole series) after a successful re-encode. Off by
	// default.
	UnmonitorAfterReencode *bool `json:"unmonitor_after_reencode,omitempty"`

	// WebhookToken authenticates POST /api/v1/hooks/arr/{id}: Sonarr/Radarr
	// send it as the HTTP Basic password (any username). Generated once
	// on instance create; masked like APIKey, regenerated only via its
	// own endpoint (arrRegenerateWebhookToken) — a blank value on a
	// plain settings save must never erase it, same convention as the key.
	WebhookToken string `json:"webhook_token,omitempty"`
	// WebhookIntake: a Download event creates a settling intake row
	// instead of just refreshing the file's recommendation. Off
	// by default — connecting the webhook at all is opt-in, and letting
	// it queue unattended is a second, separate opt-in on top of that.
	WebhookIntake *bool `json:"webhook_intake,omitempty"`
	// WebhookSettleMinutes is how long a Download-triggered intake row
	// waits before it's eligible to promote; see .WebhookSettleMinutesOn().
	WebhookSettleMinutes int `json:"webhook_settle_minutes,omitempty"`
}

// WebhookIntakeOn reports the effective setting (default OFF).
func (a ArrInstance) WebhookIntakeOn() bool {
	return a.WebhookIntake != nil && *a.WebhookIntake
}

// WebhookSettleMinutesOn reports the effective delay (default 30).
func (a ArrInstance) WebhookSettleMinutesOn() int {
	if a.WebhookSettleMinutes > 0 {
		return a.WebhookSettleMinutes
	}
	return 30
}

// RescanAfterReplaceOn reports the effective setting (default on).
func (a ArrInstance) RescanAfterReplaceOn() bool {
	return a.RescanAfterReplace == nil || *a.RescanAfterReplace
}

// TagAfterReencodeOn reports the effective setting (default OFF —
// write-back is opt-in, unlike the read-only toggles above).
func (a ArrInstance) TagAfterReencodeOn() bool {
	return a.TagAfterReencode != nil && *a.TagAfterReencode
}

// UnmonitorAfterReencodeOn reports the effective setting (default off).
func (a ArrInstance) UnmonitorAfterReencodeOn() bool {
	return a.UnmonitorAfterReencode != nil && *a.UnmonitorAfterReencode
}

// Default tag names for the tag policy; see ArrInstance's tag
// fields — each is blank until the user overrides it.
const (
	DefaultSkipTag      = "mt-skip"
	DefaultAV1Tag       = "mt-av1"
	DefaultHEVCTag      = "mt-hevc"
	DefaultRemuxOnlyTag = "mt-remux-only"
)

// SkipUpgradePendingOn reports the effective setting (default on).
func (a ArrInstance) SkipUpgradePendingOn() bool {
	return a.SkipUpgradePending == nil || *a.SkipUpgradePending
}

// TagPolicyOn reports the effective setting (default on).
func (a ArrInstance) TagPolicyOn() bool {
	return a.TagPolicyEnabled == nil || *a.TagPolicyEnabled
}

func (a ArrInstance) skipTagName() string {
	if a.SkipTag != "" {
		return a.SkipTag
	}
	return DefaultSkipTag
}
func (a ArrInstance) av1TagName() string {
	if a.AV1Tag != "" {
		return a.AV1Tag
	}
	return DefaultAV1Tag
}
func (a ArrInstance) hevcTagName() string {
	if a.HEVCTag != "" {
		return a.HEVCTag
	}
	return DefaultHEVCTag
}
func (a ArrInstance) remuxOnlyTagName() string {
	if a.RemuxOnlyTag != "" {
		return a.RemuxOnlyTag
	}
	return DefaultRemuxOnlyTag
}

// TagNames returns the four policy tag names in a fixed order (skip,
// av1, hevc, remux-only), for a caller that needs to check membership by
// name without importing each accessor individually.
func (a ArrInstance) TagNames() (skip, av1, hevc, remuxOnly string) {
	return a.skipTagName(), a.av1TagName(), a.hevcTagName(), a.remuxOnlyTagName()
}

// On reports whether the instance is enabled (default true, so existing
// stored instances without the field keep working).
func (a ArrInstance) On() bool { return a.Enabled == nil || *a.Enabled }

// Default returns the initial configuration for a new install.
func Default() Config {
	// A fresh install assumes nothing about paths: no libraries,
	// no path maps, no media-server URL, and the queue paused until the
	// user has reviewed what it would do. Stored configs are loaded over
	// this, so existing installs keep every value they saved.
	return Config{
		Libraries:          []Library{},
		Paused:             true,
		Workers:            1,
		Schedules:          []Schedule{},
		DefaultCodec:       "hevc",
		DefaultQuality:     60,
		PreferredBackend:   "auto",
		MinSavingsPct:      30,
		AudioPCMTarget:     "flac",
		TrashEnabled:       true,
		TrashDays:          14,
		DefaultSpeed:       "medium",
		MaxAttempts:        3,
		RecompressHEVC:     false,
	}
}

const kvKey = "config"

// Manager owns the current config, persisting every change and
// notifying subscribers.
type Manager struct {
	mu   sync.RWMutex
	cfg  Config
	st   *store.Store
	subs []chan struct{}
}

func NewManager(st *store.Store) *Manager {
	m := &Manager{cfg: Default(), st: st}
	if ok, err := store.KVLoad(st, kvKey, &m.cfg); err != nil {
		log.Printf("config: load failed, using defaults: %v", err)
	} else if ok {
		m.normalize()
	}
	return m
}

func (m *Manager) normalize() {
	if m.cfg.Libraries == nil {
		m.cfg.Libraries = []Library{}
	}
	if m.cfg.Workers < 1 {
		m.cfg.Workers = 1
	}
	if m.cfg.Workers > 8 {
		m.cfg.Workers = 8
	}
	switch m.cfg.DefaultCodec {
	case "hevc", "av1", "h264":
	default:
		m.cfg.DefaultCodec = "hevc"
	}
	if m.cfg.DefaultQuality < 0 || m.cfg.DefaultQuality > 100 {
		m.cfg.DefaultQuality = 60
	}
	switch m.cfg.PreferredBackend {
	case "qsv", "vaapi", "nvenc", "sw", "auto":
	default:
		m.cfg.PreferredBackend = "auto"
	}
	if m.cfg.MinSavingsPct <= 0 {
		m.cfg.MinSavingsPct = 30
	}
	switch m.cfg.AudioPCMTarget {
	case "flac", "aac", "eac3", "copy":
	default:
		m.cfg.AudioPCMTarget = "flac"
	}
	if m.cfg.PreferMP4 == nil {
		t := true
		m.cfg.PreferMP4 = &t
	}
	switch m.cfg.ContainerGoal {
	case "prefer_mp4", "mp4_required", "keep":
	default:
		if m.cfg.PreferMP4 != nil && !*m.cfg.PreferMP4 {
			m.cfg.ContainerGoal = "keep"
		} else {
			m.cfg.ContainerGoal = "prefer_mp4"
		}
	}
	if m.cfg.ContainerGoal == "mp4_required" {
		// Suggested defaults: only fill a rule that doesn't exist
		// yet, so switching to mp4_required once seeds sane behaviour but
		// never overwrites something the user already configured.
		if m.cfg.AudioRules == nil {
			m.cfg.AudioRules = map[string]encode.AudioRule{}
		}
		def := func(k string, r encode.AudioRule) {
			if _, ok := m.cfg.AudioRules[k]; !ok {
				m.cfg.AudioRules[k] = r
			}
		}
		def("truehd", encode.AudioRule{Action: "convert", Target: "eac3", BitrateByChannels: map[int]int{6: 640, 8: 1024}})
		def("dts", encode.AudioRule{Action: "convert", Target: "eac3", BitrateByChannels: map[int]int{6: 640, 8: 1024}})
		def("flac", encode.AudioRule{Action: "convert", Target: "alac"})
		def("pcm", encode.AudioRule{Action: "convert", Target: "alac"})
		def("opus", encode.AudioRule{Action: "convert", Target: "aac"})
		def("vorbis", encode.AudioRule{Action: "convert", Target: "aac"})
	}
	if m.cfg.VMAFTarget == nil {
		t := 93.0
		m.cfg.VMAFTarget = &t
	}
	if m.cfg.PlexKeepAddedAt == nil {
		t := true
		m.cfg.PlexKeepAddedAt = &t
	}
	if m.cfg.DeferWhileTranscoding == nil {
		t := true
		m.cfg.DeferWhileTranscoding = &t
	}
	if m.cfg.HoldReplaceWhilePlaying == nil {
		t := true
		m.cfg.HoldReplaceWhilePlaying = &t
	}
	switch m.cfg.DefaultSpeed {
	case "faster", "fast", "medium", "slow", "slower":
	default:
		m.cfg.DefaultSpeed = "medium"
	}
	switch m.cfg.UpscaleOutput {
	case "copy", "replace":
	default:
		m.cfg.UpscaleOutput = "copy"
	}
	if m.cfg.TrashDays <= 0 {
		m.cfg.TrashDays = 14
	}
	if m.cfg.MaxAttempts <= 0 {
		m.cfg.MaxAttempts = 3
	}
	if m.cfg.Schedules == nil {
		m.cfg.Schedules = []Schedule{}
	}
	if m.cfg.UpscaleSchedules == nil {
		m.cfg.UpscaleSchedules = []Schedule{{ID: 1, Label: "Overnight", Days: 0x7F, Start: 22 * 60, End: 7 * 60}}
	}
	if m.cfg.MeasureSchedules == nil {
		m.cfg.MeasureSchedules = []Schedule{{ID: 1, Label: "Overnight", Days: 0x7F, Start: 60, End: 420}}
	}
}

// Measure reports whether overnight measuring is on (default on).
func (c Config) Measure() bool { return c.MeasureEnabled == nil || *c.MeasureEnabled }

// MeasureOpen reports whether a measurement may start at t. Unlike the
// encode queue, no schedules means never (measuring is opt-in by window).
// UpscaleOpen reports whether a neural upscale may start or continue at t.
func (m *Manager) UpscaleOpen(t time.Time) bool {
	c := m.Get()
	return !c.Paused && inWindows(c.UpscaleSchedules, t)
}

func (m *Manager) MeasureOpen(t time.Time) bool {
	c := m.Get()
	return c.Measure() && inWindows(c.MeasureSchedules, t)
}

// Get returns a copy of the current config.
func (m *Manager) Get() Config {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cfg
}

// Update applies fn to the config, persists, and notifies.
func (m *Manager) Update(fn func(*Config)) error {
	m.mu.Lock()
	fn(&m.cfg)
	m.normalize()
	cfg := m.cfg
	err := store.KVJSON(m.st, kvKey, &cfg)
	subs := append([]chan struct{}(nil), m.subs...)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	for _, ch := range subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
	return nil
}

// Subscribe returns a channel notified on every config change.
func (m *Manager) Subscribe() chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	ch := make(chan struct{}, 1)
	m.subs = append(m.subs, ch)
	return ch
}

// VMAF returns the effective quality target (0 = off).
func (c Config) VMAF() float64 {
	if c.VMAFTarget == nil {
		return 93
	}
	return *c.VMAFTarget
}

// PressureBudgetX reports the effective disk-pressure budget
// multiplier (default 2x).
func (c Config) PressureBudgetX() float64 {
	if c.DiskPressureBudgetX == nil {
		return 2
	}
	return *c.DiskPressureBudgetX
}

// MapJellyfinPath rewrites a Jellyfin-side path to the local view.
// JellyfinPathMap is "from=to" where from is Jellyfin's path, i.e. pathmap's
// "remote=local", so this is ToLocal.
func (c Config) MapJellyfinPath(p string) string {
	m, _ := pathmap.Parse(c.JellyfinPathMap)
	return m.ToLocal(p)
}

// ToJellyfinPath is MapJellyfinPath's inverse: our path as Jellyfin sees it
// (/srv/media/movies/x.mp4 becomes /data/movies/x.mp4).
func (c Config) ToJellyfinPath(p string) string {
	m, _ := pathmap.Parse(c.JellyfinPathMap)
	return m.ToRemote(p)
}

// MapPlexPath rewrites a Plex-side path to the local view; see
// MapJellyfinPath.
func (c Config) MapPlexPath(p string) string {
	m, _ := pathmap.Parse(c.PlexPathMap)
	return m.ToLocal(p)
}

// ToPlexPath is MapPlexPath's inverse: our path as Plex sees it.
func (c Config) ToPlexPath(p string) string {
	m, _ := pathmap.Parse(c.PlexPathMap)
	return m.ToRemote(p)
}

// Notifier is one notification target (a shoutrrr service URL).
type Notifier struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Enabled bool   `json:"enabled"`
	// Events lists the event keys delivered to this target; empty
	// means none.
	Events   []string `json:"events"`
	MinLevel string   `json:"min_level"` // info|warning|error; "" = info
	// QuietStart/QuietEnd are minutes after local midnight; info events
	// inside the window wait until it ends. Equal values = no quiet hours.
	QuietStart int `json:"quiet_start"`
	QuietEnd   int `json:"quiet_end"`
}

// PublicURL is the base notification links are built from: the
// webhook base URL override, since both name the address other
// systems reach this app at. "" means links are left relative.
func (c Config) PublicURL() string {
	return strings.TrimRight(c.WebhookBaseURL, "/")
}

// MapBazarrPath rewrites a Bazarr-side path to the local view.
func (c Config) MapBazarrPath(p string) string {
	m, _ := pathmap.Parse(c.BazarrPathMap)
	return m.ToLocal(p)
}

// PlexKeepAddedAtOn reports the effective setting (default on).
func (c Config) PlexKeepAddedAtOn() bool {
	return c.PlexKeepAddedAt == nil || *c.PlexKeepAddedAt
}

// DeferWhileTranscodingOn reports the effective setting (default on).
func (c Config) DeferWhileTranscodingOn() bool {
	return c.DeferWhileTranscoding == nil || *c.DeferWhileTranscoding
}

// HoldReplaceWhilePlayingOn reports the effective setting (default on).
func (c Config) HoldReplaceWhilePlayingOn() bool {
	return c.HoldReplaceWhilePlaying == nil || *c.HoldReplaceWhilePlaying
}

// WindowOpen reports whether the queue may start new jobs right now.
// Paused always closes the window; zero schedules = always open.
func (m *Manager) WindowOpen(t time.Time) bool {
	c := m.Get()
	if c.Paused {
		return false
	}
	if len(c.Schedules) == 0 {
		return true
	}
	return inWindows(c.Schedules, t)
}

// Auth modes.
const (
	AuthRequired    = "required"
	AuthLANBypass   = "lan_bypass"
	AuthProxyHeader = "proxy_header"
	AuthDisabled    = "disabled"
)

// DefaultAuthCIDRs are the private ranges lan_bypass trusts by default.
var DefaultAuthCIDRs = []string{"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "::1/128", "fc00::/7", "fe80::/10"}

// AuthModeOn returns the effective mode ("" = required).
func (c Config) AuthModeOn() string {
	switch c.AuthMode {
	case AuthLANBypass, AuthProxyHeader, AuthDisabled:
		return c.AuthMode
	}
	return AuthRequired
}

// AuthCIDRsOn returns the lan_bypass ranges (default: private ranges).
func (c Config) AuthCIDRsOn() []string {
	if len(c.AuthCIDRs) == 0 {
		return DefaultAuthCIDRs
	}
	return c.AuthCIDRs
}

// ProxyHeaderOn returns the trusted username header (default Remote-User).
func (c Config) ProxyHeaderOn() string {
	if c.ProxyHeader == "" {
		return "Remote-User"
	}
	return c.ProxyHeader
}

// UpgradeLoopDaysOn returns the effective upgrade-loop window.
func (c Config) UpgradeLoopDaysOn() int {
	if c.UpgradeLoopDays <= 0 {
		return 14
	}
	return c.UpgradeLoopDays
}

// InSchedules reports whether t is inside the processing schedules,
// ignoring Paused (zero schedules = always).
func InSchedules(scheds []Schedule, t time.Time) bool {
	return len(scheds) == 0 || inWindows(scheds, t)
}

// inWindows reports whether t falls inside any schedule.
func inWindows(scheds []Schedule, t time.Time) bool {
	min := t.Hour()*60 + t.Minute()
	// weekday: Monday=0 … Sunday=6 → bitmap bit
	bit := 1 << int((int(t.Weekday())+6)%7)
	for _, s := range scheds {
		if s.Days&bit == 0 {
			continue
		}
		if s.Start <= s.End {
			if min >= s.Start && min < s.End {
				return true
			}
		} else { // wraps midnight: active if >= start OR < end
			if min >= s.Start || min < s.End {
				return true
			}
		}
	}
	return false
}
