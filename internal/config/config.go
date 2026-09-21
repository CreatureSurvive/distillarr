// Package config holds runtime configuration, persisted as JSON in the
// kv table. Every mutation broadcasts a change so the scheduler and
// dispatcher pick up new windows / worker counts without a restart.
package config

import (
	"log"
	"strings"
	"sync"
	"time"

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

	JellyfinURL    string `json:"jellyfin_url"`
	JellyfinAPIKey string `json:"jellyfin_api_key"`
	// JellyfinPathMap rewrites Jellyfin's view of paths to ours,
	// "from=to" (e.g. "/data=/srv/media" when Jellyfin mounts the library as /data).
	JellyfinPathMap string `json:"jellyfin_path_map"`
}

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
		TrashDir:           "/srv/media/.mediatrans-trash",
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
	if m.cfg.TrashDays <= 0 {
		m.cfg.TrashDays = 14
	}
	if m.cfg.MaxAttempts <= 0 {
		m.cfg.MaxAttempts = 3
	}
	if m.cfg.Schedules == nil {
		m.cfg.Schedules = []Schedule{}
	}
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
func (c Config) MapJellyfinPath(p string) string {
	from, to, ok := strings.Cut(c.JellyfinPathMap, "=")
	from, to = strings.TrimRight(from, "/"), strings.TrimRight(to, "/")
	if !ok || from == "" {
		return p
	}
	if p == from || strings.HasPrefix(p, from+"/") {
		return to + strings.TrimPrefix(p, from)
	}
	return p
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
	min := t.Hour()*60 + t.Minute()
	// weekday: Monday=0 … Sunday=6 → bitmap bit
	bit := 1 << int((int(t.Weekday())+6)%7)
	for _, s := range c.Schedules {
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
