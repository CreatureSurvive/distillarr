// Package still renders single-frame A/B comparisons for tuning an upscale:
// A is a standard Lanczos resize of a source frame, B runs the real upscale
// chain on the same frame. Results are cached by content key, so dragging a
// slider back to an earlier value is instant.
package still

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/media"
	"github.com/CreatureSurvive/distillarr/internal/neural"
)

const (
	maxCached  = 60 // stills kept on disk; oldest are pruned
	runTimeout = 90 * time.Second
)

// Manager renders and caches stills under root.
type Manager struct {
	root    string
	acquire func(key string) func() // GPU slot, shared with jobs and previews
}

// New returns a Manager storing stills under root. acquire may be nil.
func New(root string, acquire func(string) func()) *Manager {
	return &Manager{root: root, acquire: acquire}
}

// Request describes one still.
type Request struct {
	FileID   int64
	Path     string
	Size     int64 // source size and mtime: a replaced file must not hit the cache
	MtimeNS  int64
	Duration float64
	At       float64 // seconds into the source
	Settings encode.Settings
}

// Result is a rendered (or cached) still pair.
type Result struct {
	Key    string  `json:"key"`
	W      int     `json:"w"`
	H      int     `json:"h"`
	At     float64 `json:"at"`
	MS     int64   `json:"ms"`
	Cached bool    `json:"cached"`
}

var keyRe = regexp.MustCompile(`^[0-9a-f]{16}$`)

// Path resolves a served still. Only a valid key and a.png/b.png are allowed,
// so the URL can never name anything outside root.
func (m *Manager) Path(key, name string) (string, bool) {
	if !keyRe.MatchString(key) || (name != "a.png" && name != "b.png") {
		return "", false
	}
	p := filepath.Join(m.root, key, name)
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	return p, true
}

// keyFor hashes what determines the pixels: the source and frame, the GPU,
// and the exact filter chains that render both sides. Hashing the chains, not
// the settings that produced them, means the cache invalidates itself when the
// pipeline changes (a fixed bug, a new shader revision) instead of serving
// stale images from before the change. Quality, codec and container never
// affect a still, so they are not part of it.
func keyFor(r Request, at float64, a, b []string) string {
	k, _ := json.Marshal([]any{r.FileID, r.Path, r.Size, r.MtimeNS, at, r.Settings.VulkanDevice, a, b})
	return fmt.Sprintf("%x", sha256.Sum256(k))[:16]
}

// Make renders the still pair, or returns the cached one.
func (m *Manager) Make(ctx context.Context, r Request) (*Result, error) {
	if r.Settings.UpscaleTo <= 0 {
		return nil, fmt.Errorf("no upscale target set")
	}
	r.Settings.Normalize()
	at := math.Round(r.At*10) / 10 // 0.1s grid: deterministic seeks, better cache hits
	if hi := r.Duration - 0.5; hi > 0 && at > hi {
		at = hi
	}
	if at < 0 {
		at = 0
	}
	start := time.Now()

	probe, err := media.ProbeFile(ctx, r.Path)
	if err != nil {
		return nil, err
	}
	v := probe.Video()
	if v == nil {
		return nil, fmt.Errorf("no video stream")
	}
	fa, fb, w, h, err := encode.StillFilters(r.Settings, v)
	if err != nil {
		return nil, err
	}
	// A nil B chain means the neural tier: B comes from the upscaler, not a filter.
	var nModel string
	var nScale int
	var nPre []string
	if fb == nil {
		if nModel, nScale, nPre, err = encode.NeuralStill(r.Settings, v); err != nil {
			return nil, err
		}
		fb = []string{"neural", nModel, strconv.Itoa(nScale)} // what distinguishes one neural render from another
	}
	key := keyFor(r, at, fa, fb)
	dir := filepath.Join(m.root, key)
	res := &Result{Key: key, W: w, H: h, At: at}

	_, errA := os.Stat(filepath.Join(dir, "a.png"))
	_, errB := os.Stat(filepath.Join(dir, "b.png"))
	if errA == nil && errB == nil {
		res.Cached = true
		return res, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	seek := fmt.Sprintf("%.1f", at)
	base := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	grab := func(out string, filters []string) []string {
		return []string{"-ss", seek, "-i", r.Path, "-map", "0:v:0", "-frames:v", "1",
			"-vf", strings.Join(filters, ","), "-y", out}
	}

	// A: plain software resize, no GPU.
	if err := m.run(ctx, dir, "a.png", func(o string) []string { return cat(base, grab(o, fa)) }); err != nil {
		return nil, fmt.Errorf("baseline frame: %w", err)
	}
	if nModel != "" {
		if err := m.neuralB(ctx, r, dir, seek, nModel, nScale, nPre, w, h); err != nil {
			return nil, fmt.Errorf("upscaled frame: %w", err)
		}
		res.MS = time.Since(start).Milliseconds()
		m.prune()
		return res, nil
	}
	// B: the real chain, holding the shared Vulkan slot.
	vk := []string{"-init_hw_device", "vulkan=vk:" + fmt.Sprint(r.Settings.VulkanDevice), "-filter_hw_device", "vk"}
	if m.acquire != nil {
		defer m.acquire("vulkan")()
	}
	if err := m.run(ctx, dir, "b.png", func(o string) []string { return cat(base, vk, grab(o, fb)) }); err != nil {
		return nil, fmt.Errorf("upscaled frame: %w", err)
	}

	res.MS = time.Since(start).Milliseconds()
	m.prune()
	return res, nil
}

// neuralB renders the upscaled side with Real-ESRGAN: decode the frame with the
// same prefilters a job uses, upscale it, and resize to the exact target.
func (m *Manager) neuralB(ctx context.Context, r Request, dir, seek, model string, scale int, pre []string, w, h int) error {
	frame := filepath.Join(dir, fmt.Sprintf(".frame-%d.png", time.Now().UnixNano()))
	up := filepath.Join(dir, fmt.Sprintf(".up-%d.png", time.Now().UnixNano()))
	defer os.Remove(frame)
	defer os.Remove(up)

	dctx, cancel := context.WithTimeout(ctx, runTimeout)
	args := []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-ss", seek, "-i", r.Path, "-map", "0:v:0",
		"-frames:v", "1", "-vf", strings.Join(pre, ","), "-y", frame}
	b, err := exec.CommandContext(dctx, encode.FFmpeg, args...).CombinedOutput()
	cancel()
	if err != nil {
		return fmt.Errorf("decode: %s", tail(string(b), err))
	}
	// A single frame at a heavy model can take a while; give it longer than a shader.
	nctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if err := neural.Frame(nctx, m.acquire, frame, up, model, scale, r.Settings.VulkanDevice); err != nil {
		return err
	}
	return m.run(ctx, dir, "b.png", func(o string) []string {
		return []string{"-hide_banner", "-loglevel", "error", "-nostdin", "-i", up,
			"-vf", fmt.Sprintf("scale=%d:%d:flags=lanczos,format=rgb24", w, h), "-frames:v", "1", "-y", o}
	})
}

// run executes ffmpeg writing to a temp file, then renames it to name so a
// half-written or failed render is never served. args builds the command
// for the temp output path it is given.
func (m *Manager) run(ctx context.Context, dir, name string, args func(out string) []string) error {
	tmp := filepath.Join(dir, fmt.Sprintf(".%d.%s", time.Now().UnixNano(), name))
	ctx, cancel := context.WithTimeout(ctx, runTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, encode.FFmpeg, args(tmp)...).CombinedOutput()
	if err != nil {
		os.Remove(tmp)
		return fmt.Errorf("%s", tail(string(out), err))
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}

// prune keeps the newest maxCached still directories.
func (m *Manager) prune() {
	ents, err := os.ReadDir(m.root)
	if err != nil || len(ents) <= maxCached {
		return
	}
	type d struct {
		name string
		mod  time.Time
	}
	var ds []d
	for _, e := range ents {
		if info, err := e.Info(); err == nil && e.IsDir() && keyRe.MatchString(e.Name()) {
			ds = append(ds, d{e.Name(), info.ModTime()})
		}
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].mod.After(ds[j].mod) })
	for i := maxCached; i < len(ds); i++ {
		os.RemoveAll(filepath.Join(m.root, ds[i].name))
	}
}

func cat(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func tail(out string, err error) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) > 3 {
		lines = lines[len(lines)-3:]
	}
	if s := strings.TrimSpace(strings.Join(lines, " | ")); s != "" {
		return s
	}
	return err.Error()
}
