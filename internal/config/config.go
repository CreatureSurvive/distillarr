// Package config holds runtime configuration, persisted as JSON in the
// kv table. Every mutation broadcasts a change so the scheduler and
// dispatcher pick up new windows / worker counts without a restart.
package config

import (
	"log"
	"sync"
	"time"

	"mediatrans/internal/pathmap"
	"mediatrans/internal/store"
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
	TrashDir           string `json:"trash_dir"`           // on the media pool → hardlinks, no copy
	// PreferMP4 makes "auto" output MP4 (HEVC tagged hvc1, moov first)
	// whenever every kept track fits, for Apple/direct-play clients.
	PreferMP4 *bool `json:"prefer_mp4"`
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

	JellyfinURL    string `json:"jellyfin_url"`
	JellyfinAPIKey string `json:"jellyfin_api_key"`
	// JellyfinPathMap rewrites Jellyfin's view of paths to ours,
	// "from=to" (e.g. "/data=/srv/media" when Jellyfin mounts the library as /data).
	JellyfinPathMap string `json:"jellyfin_path_map"`

	// ArrInstances: any number of Sonarr and Radarr connections. See
	// internal/arr for the client and for the design.
	ArrInstances []ArrInstance `json:"arr_instances"`

	// WebhookBaseURL overrides the host:port Settings shows in front of
	// each instance's webhook path. Blank means "use whatever
	// origin the browser is loaded from" (computed client-side) — only
	// needed when that's wrong for Sonarr/Radarr, e.g. they see this
	// container under a different Docker-network hostname.
	WebhookBaseURL string `json:"webhook_base_url,omitempty"`
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
	return Config{
		Libraries: []Library{
			{Name: "movies", Path: "/srv/media/movies"},
			{Name: "tvshows", Path: "/srv/media/tvshows"},
		},
		Workers:            1,
		Schedules:          []Schedule{},
		DefaultCodec:       "hevc",
		DefaultQuality:     60,
		PreferredBackend:   "auto",
		MinSavingsPct:      30,
		AudioPCMTarget:     "flac",
		TrashEnabled:       true,
		TrashDays:          14,
		TrashDir:           "/srv/media/.distillarr-trash",
		DefaultSpeed:       "medium",
		JellyfinPathMap:    "/data=/srv/media",
		MaxAttempts:        3,
		RecompressHEVC:     false,
		JellyfinURL:        "http://host.docker.internal:8096",
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
	d := Default()
	if len(m.cfg.Libraries) == 0 {
		m.cfg.Libraries = d.Libraries
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
	if m.cfg.TrashDir == "" {
		m.cfg.TrashDir = d.TrashDir
	}
	if m.cfg.VMAFTarget == nil {
		t := 93.0
		m.cfg.VMAFTarget = &t
	}
	if m.cfg.JellyfinPathMap == "" {
		m.cfg.JellyfinPathMap = d.JellyfinPathMap
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

// MP4 reports the effective prefer-MP4 setting (default on).
func (c Config) MP4() bool { return c.PreferMP4 == nil || *c.PreferMP4 }

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
