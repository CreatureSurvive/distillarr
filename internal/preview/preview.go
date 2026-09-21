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

	"mediatrans/internal/encode"
	"mediatrans/internal/media"
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
}

// Manager tracks previews; clips live under rootDir.
type Manager struct {
	root     string
	mu       sync.RWMutex
	items    map[string]*Preview
	acquire  func(key string) func() // engine GPU slot
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
// segment placement.
func (m *Manager) Create(fileID int64, path string, dur float64, s encode.Settings, manual []float64) (*Preview, error) {
	if dur <= 0 {
		return nil, fmt.Errorf("unknown duration")
	}
	id := fmt.Sprintf("%d-%d", fileID, time.Now().Unix())
	p := &Preview{
		ID: id, FileID: fileID, Path: path, CreatedAt: time.Now(),
		Status: "running", Settings: s, Duration: dur,
	}
	dir := filepath.Join(m.root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}

	starts := manual
	if len(starts) == 0 {
		starts = autoStarts(dur, 3)
	}
	if len(starts) > maxSegs {
		starts = starts[:maxSegs]
	}
	sort.Float64s(starts)
	for i, st := range starts {
		if st < 0 {
			st = 0
		}
		if st > dur-segLen/2 {
			st = dur - segLen
		}
		if st < 0 {
			st = 0
		}
		p.Segments = append(p.Segments, Segment{Index: i, Start: st, Len: segLen})
	}

	m.mu.Lock()
	m.items[id] = p
	m.mu.Unlock()
	m.notify(evPreview, p)

	go m.run(p)
	return p, nil
}

// autoStarts picks evenly spaced segment midpoints.
func autoStarts(dur float64, n int) []float64 {
	var out []float64
	for i := 1; i <= n; i++ {
		out = append(out, dur*float64(i)/float64(n+1)-segLen/2)
	}
	return out
}

func (m *Manager) run(p *Preview) {
	defer func() {
		m.mu.Lock()
		m.persist(p)
		m.mu.Unlock()
		m.notify(evPreview, p)
	}()

	src, err := media.ProbeFile(context.Background(), p.Path)
	if err != nil {
		p.Status = "failed"
		p.Error = err.Error()
		return
	}
	proxy := encode.SourceNeedsProxy(src)

	prim, fb, err := encode.BuildPreview(p.Settings, src, "", 0, segLen)
	if err != nil {
		p.Status = "failed"
		p.Error = err.Error()
		return
	}
	semKey := prim.SemKey

	for i := range p.Segments {
		seg := &p.Segments[i]
		dir := filepath.Join(m.root, p.ID)
		seg.Proxy = proxy
		seg.SrcPath = fmt.Sprintf("src-%d.mp4", i)
		seg.EncPath = fmt.Sprintf("enc-%d.mp4", i)

		// Left side: source cut (copy, or x264 proxy when needed).
		srcArgs := encode.BuildSourceCut(src, filepath.Join(dir, seg.SrcPath), seg.Start, seg.Len, proxy)
		if err := runSimple(srcArgs); err != nil {
			p.Status = "failed"
			p.Error = fmt.Sprintf("source cut: %v", err)
			return
		}
		if fi, err := os.Stat(filepath.Join(dir, seg.SrcPath)); err == nil {
			seg.SrcSize = fi.Size()
		}

		// Right side: proposed encode on the same span.
		release := m.acquire(semKey)
		encArgs := withOutputAndSpan(prim.Args, filepath.Join(dir, seg.EncPath), seg.Start, seg.Len)
		err := runSimple(encArgs)
		if err != nil && fb != nil {
			log.Printf("preview: hw decode failed (%v); software decode", err)
			encArgs = withOutputAndSpan(fb.Args, filepath.Join(dir, seg.EncPath), seg.Start, seg.Len)
			err = runSimple(encArgs)
		}
		release()
		if err != nil {
			p.Status = "failed"
			p.Error = fmt.Sprintf("encode sample: %v", err)
			return
		}
		if fi, err := os.Stat(filepath.Join(dir, seg.EncPath)); err == nil {
			seg.EncSize = fi.Size()
		}
	}
	p.Status = "ready"
}

// withOutputAndSpan rewrites a BuildPreview argv for a concrete span
// and output. BuildPreview baked placeholder values we now replace.
func withOutputAndSpan(args []string, out string, start, dur float64) []string {
	out2 := append([]string{}, args...)
	for i := 0; i+1 < len(out2); i++ {
		switch out2[i] {
		case "-ss":
			out2[i+1] = fmt.Sprintf("%.3f", start)
		case "-t":
			out2[i+1] = fmt.Sprintf("%.3f", dur)
		}
	}
	// last element is the output path
	out2[len(out2)-1] = out
	return out2
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
