// SPDX-License-Identifier: GPL-3.0-or-later

// Package preview generates A/B sample clips: short source cuts plus
// the same segments encoded with proposed settings.
package preview

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/media"
	"github.com/CreatureSurvive/distillarr/internal/tune"
)

// Segment is one A/B pair.
type Segment struct {
	Index   int     `json:"index"`
	Start   float64 `json:"start"`
	Len     float64 `json:"len"`
	SrcPath string  `json:"src_path"`
	EncPath string  `json:"enc_path"`
	SrcSize int64   `json:"src_size"`
	EncSize int64   `json:"enc_size"`
	Proxy   bool    `json:"proxy"` // left side re-encoded for browser compat
	VMAF    *media.VMAFResult `json:"vmaf,omitempty"` // B vs the original span
}

// Preview is one preview job (several segments).
type Preview struct {
	ID        string          `json:"id"`
	FileID    int64           `json:"file_id"`
	Path      string          `json:"path"`
	CreatedAt time.Time       `json:"created_at"`
	Status    string          `json:"status"` // running | ready | failed
	Error     string          `json:"error,omitempty"`
	Settings  encode.Settings `json:"settings"`
	Duration  float64         `json:"duration"`
	Segments  []Segment       `json:"segments"`
	Command   string          `json:"command,omitempty"`
	// MeasuredRatio is encoded/source VIDEO bytes across all samples
	// (0 when the source needed a proxy and can't be compared).
	MeasuredRatio float64 `json:"measured_ratio,omitempty"`
	// Tune is the quality search, when the settings had a VMAF target.
	Tune  *tune.Result `json:"tune,omitempty"`
	Stage string       `json:"stage,omitempty"` // what it is doing right now
	// deferSpans: segment starts are the autoStarts placeholder for the
	// initial broadcast; run() replaces them with packet-scanned spans
	// once it has a context to read the file's own index on.
	deferSpans bool
	// cambi: also score banding severity (set by the caller, who knows
	// whether this file is animation or HDR-tonemap content).
	cambi bool
}

// Manager tracks previews; clips live under rootDir.
type Manager struct {
	root     string
	mu       sync.RWMutex
	items    map[string]*Preview
	acquire  func(key string) func() // engine GPU slot
	// OnMeasured feeds sample results into the size model.
	OnMeasured func(fileID int64, s encode.Settings, ratio float64)
	// OnTuned stores a quality search result on the file.
	OnTuned func(fileID int64, s encode.Settings, r tune.Result)
	notify   func(event string, payload any)
}

const (
	segLen     = 20.0
	maxSegs    = 5
	evPreview  = "preview"
)

func NewManager(root string, acquire func(string) func(),
	notify func(string, any)) *Manager {
	m := &Manager{root: root, items: map[string]*Preview{}, acquire: acquire, notify: notify}
	_ = os.MkdirAll(root, 0o755)
	m.loadPersisted()
	return m
}

func (m *Manager) loadPersisted() {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(m.root, e.Name(), "manifest.json"))
		if err != nil {
			continue
		}
		var p Preview
		if json.Unmarshal(b, &p) == nil {
			if p.Status == "running" {
				p.Status = "failed"
				p.Error = "interrupted by restart"
			}
			m.items[p.ID] = &p
		}
	}
}

func (m *Manager) persist(p *Preview) {
	b, _ := json.MarshalIndent(p, "", " ")
	_ = os.WriteFile(filepath.Join(m.root, p.ID, "manifest.json"), b, 0o644)
}

// Create spawns a preview job. manual timestamps override automatic
// segment placement. cambi also scores banding severity - pass true for
// animation or HDR-tonemap content, where VMAF is known to under-
// penalize banding.
func (m *Manager) Create(fileID int64, path string, dur float64, s encode.Settings, manual []float64, cambi bool) (*Preview, error) {
	if dur <= 0 {
		return nil, fmt.Errorf("unknown duration")
	}
	id := fmt.Sprintf("%d-%d", fileID, time.Now().Unix())
	p := &Preview{
		ID: id, FileID: fileID, Path: path, CreatedAt: time.Now(),
		Status: "running", Settings: s, Duration: dur, cambi: cambi,
	}
	dir := filepath.Join(m.root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	starts := manual
	length := segLen
	if len(starts) == 0 {
		starts = autoStarts(dur, 3)
		if s.VMAFTarget > 0 && media.VMAFAvailable() {
			// Same spans the quality search samples, so B is its output.
			// autoStarts above is just the placeholder for the initial
			// broadcast below - run() swaps in the packet-scanned starts
			// once it has a context to read the file's own index on
			// (this method must return immediately).
			length = tune.SampleLen
			p.deferSpans = true
		}
	}
	if len(starts) > maxSegs {
		starts = starts[:maxSegs]
	}
	sort.Float64s(starts)
	for i, st := range starts {
		if st < 0 {
			st = 0
		}
		if st > dur-length/2 {
			st = dur - length
		}
		if st < 0 {
			st = 0
		}
		p.Segments = append(p.Segments, Segment{Index: i, Start: st, Len: length})
	}

	m.mu.Lock()
	m.items[id] = p
	m.pruneLocked(12)
	m.mu.Unlock()
	m.notify(evPreview, p)

	go m.run(p)
	return p, nil
}

// pruneLocked keeps the newest keep previews and deletes the rest.
func (m *Manager) pruneLocked(keep int) {
	all := make([]*Preview, 0, len(m.items))
	for _, p := range m.items {
		all = append(all, p)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.After(all[j].CreatedAt) })
	for _, p := range all[min(keep, len(all)):] {
		if p.Status == "running" {
			continue
		}
		_ = os.RemoveAll(filepath.Join(m.root, p.ID))
		delete(m.items, p.ID)
	}
}

// autoStarts picks evenly spaced segment midpoints.
func autoStarts(dur float64, n int) []float64 {
	var out []float64
	for i := 1; i <= n; i++ {
		out = append(out, dur*float64(i)/float64(n+1)-segLen/2)
	}
	return out
}

func (m *Manager) setStage(p *Preview, stage string) {
	m.mu.Lock()
	p.Stage = stage
	m.mu.Unlock()
	m.notify(evPreview, p)
}

func (m *Manager) run(p *Preview) {
	defer func() {
		m.mu.Lock()
		p.Stage = ""
		m.persist(p)
		m.mu.Unlock()
		m.notify(evPreview, p)
	}()
	ctx := context.Background()
	src, err := media.ProbeFile(ctx, p.Path)
	if err != nil {
		p.Status, p.Error = "failed", err.Error()
		return
	}
	v := src.Video()
	dir := filepath.Join(m.root, p.ID)
	proxy := true // the A side is always rendered from the lossless cut
	// An upscale is never scored: the metric scales the result back down to the
	// source's size, where any competent upscale looks near-perfect (VMAF ~99).
	canScore := media.VMAFAvailable() && !(p.Settings.TonemapHDR && v.HDRType() != "") && p.Settings.UpscaleTo == 0
	refW, refH, crop := v.Width, v.Height, ""
	if w, h, ok := cropSize(p.Settings.Crop, v.Width, v.Height); ok {
		refW, refH, crop = w, h, p.Settings.Crop
	}
	deint := p.Settings.Deinterlace == "on" || (p.Settings.Deinterlace == "auto" && v.Interlaced())

	if p.deferSpans {
		m.setStage(p, "scanning for sample spots")
		starts := tune.Spans(ctx, p.Path, p.Duration)
		for i := range p.Segments {
			if i < len(starts) {
				p.Segments[i].Start = starts[i]
			}
		}
	}

	// Cut each span once; A, B and the VMAF reference all come from the
	// same cut, so they start on exactly the same frame.
	m.setStage(p, "cutting samples")
	starts := make([]float64, len(p.Segments))
	for i, sg := range p.Segments {
		starts[i] = sg.Start
	}
	cuts, err := tune.CutSamples(ctx, src, starts, p.Segments[0].Len, dir, true)
	if err != nil {
		p.Status, p.Error = "failed", err.Error()
		return
	}
	defer func() {
		for _, c := range cuts {
			_ = os.Remove(c)
		}
	}()

	// With a quality target, the search picks the quality and its final
	// samples become the B side.
	tuned := false
	if p.Settings.VMAFTarget > 0 && canScore && p.Segments[0].Len == tune.SampleLen {
		m.setStage(p, "measuring quality")
		res, keep, err := tune.Search(ctx, tune.Options{
			Settings: p.Settings, Probe: src, Target: p.Settings.VMAFTarget, WorkDir: dir,
			Acquire: m.acquire, KeepFinal: true, Cuts: cuts, Starts: starts, Cambi: p.cambi,
			Progress: func(msg string) { m.setStage(p, msg) },
		})
		if err != nil {
			log.Printf("preview: quality search failed, using fixed quality: %v", err)
		} else if len(keep) == len(p.Segments) {
			tuned = true
			p.Tune = &res
			p.Settings.Quality = res.Quality
			p.MeasuredRatio = res.Ratio
			for i := range p.Segments {
				p.Segments[i].EncPath = fmt.Sprintf("enc-%d.mp4", i)
				_ = os.Rename(keep[i], filepath.Join(dir, p.Segments[i].EncPath))
			}
			if m.OnTuned != nil {
				m.OnTuned(p.FileID, p.Settings, res)
			}
		}
	}

	var srcVideo, encVideo int64
	for i := range p.Segments {
		seg := &p.Segments[i]
		seg.Proxy = proxy
		seg.SrcPath = fmt.Sprintf("src-%d.mp4", i)
		m.setStage(p, fmt.Sprintf("sample %d of %d", i+1, len(p.Segments)))
		cp, err := media.ProbeFile(ctx, cuts[i])
		if err != nil {
			p.Status, p.Error = "failed", err.Error()
			return
		}

		// A side: the lossless cut as a visually lossless browser proxy,
		// frame-aligned with B. Its size shown is the original's own.
		if err := runSimple(encode.BuildSourceCut(cp, filepath.Join(dir, seg.SrcPath), 0, seg.Len+1, true)); err != nil {
			p.Status, p.Error = "failed", fmt.Sprintf("source sample: %v", err)
			return
		}
		if n, err := media.SpanVideoBytes(ctx, p.Path, seg.Start, seg.Len); err == nil {
			seg.SrcSize = n
			srcVideo += n
		}

		// B side: the exact pipeline a real job would run, on this cut.
		if !tuned {
			seg.EncPath = fmt.Sprintf("enc-%d.mp4", i)
			prim, fb, err := encode.Build(p.Settings, cp, filepath.Join(dir, seg.EncPath),
				&encode.Clip{Start: 0, Dur: seg.Len + 1})
			if err != nil {
				p.Status, p.Error = "failed", err.Error()
				return
			}
			if i == 0 {
				p.Command = encode.CommandString(prim.Args)
			}
			spec := prim
			if fb != nil {
				spec = fb // lossless intermediate: decode on the CPU
			}
			release := m.acquire(prim.SemKey)
			err = runSimple(spec.Args)
			release()
			if err != nil {
				p.Status, p.Error = "failed", fmt.Sprintf("encode sample: %v", err)
				return
			}
		}
		encPath := filepath.Join(dir, seg.EncPath)
		if n, err := media.SpanVideoBytes(ctx, encPath, 0, seg.Len+1); err == nil {
			seg.EncSize = n
			encVideo += n
		}
		if canScore {
			if vr, err := media.VMAF(ctx, encPath, media.VMAFRef{Path: cuts[i], Start: 0, Dur: seg.Len + 1,
				Crop: crop, W: refW, H: refH, Deinterlace: deint, Cambi: p.cambi}); err == nil {
				seg.VMAF = &vr
			} else {
				log.Printf("preview: vmaf: %v", err)
			}
		}
	}
	if tuned && p.Command == "" {
		if prim, _, err := encode.Build(p.Settings, src, "/path/to/output", nil); err == nil {
			p.Command = encode.CommandString(prim.Args)
		}
	}
	if !tuned && srcVideo > 0 && encVideo > 0 {
		p.MeasuredRatio = float64(encVideo) / float64(srcVideo)
		if m.OnMeasured != nil {
			m.OnMeasured(p.FileID, p.Settings, p.MeasuredRatio)
		}
	}
	p.Status = "ready"
}

func cropSize(spec string, fw, fh int) (w, h int, ok bool) {
	var x, y int
	if spec == "" {
		return 0, 0, false
	}
	if n, err := fmt.Sscanf(spec, "%d:%d:%d:%d", &w, &h, &x, &y); err != nil || n != 4 || x+w > fw || y+h > fh {
		return 0, 0, false
	}
	return w, h, true
}

func runSimple(args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	tail, err := encode.Runner(ctx, encode.CmdSpec{Args: args, SemKey: "preview"}, 0, nil)
	if err != nil {
		msg := tail.String()
		if len(msg) > 300 {
			msg = msg[len(msg)-300:]
		}
		return fmt.Errorf("%v: %s", err, msg)
	}
	return nil
}

// Get returns a preview by id.
func (m *Manager) Get(id string) *Preview {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if p, ok := m.items[id]; ok {
		cp := *p
		return &cp
	}
	return nil
}

// List returns previews newest first.
func (m *Manager) List() []*Preview {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Preview, 0, len(m.items))
	for _, p := range m.items {
		cp := *p
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

// ClipPath resolves a preview clip filename safely.
func (m *Manager) ClipPath(id, name string) (string, bool) {
	if name == "manifest.json" {
		return "", false
	}
	if !validClipName(name) {
		return "", false
	}
	m.mu.RLock()
	p, ok := m.items[id]
	m.mu.RUnlock()
	if !ok {
		return "", false
	}
	want := filepath.Base(name)
	for _, s := range p.Segments {
		if s.SrcPath == want || s.EncPath == want {
			return filepath.Join(m.root, id, want), true
		}
	}
	return "", false
}

func validClipName(name string) bool {
	if name == "" || strings.ContainsAny(name, "/\\") {
		return false
	}
	return strings.HasPrefix(name, "src-") || strings.HasPrefix(name, "enc-")
}
