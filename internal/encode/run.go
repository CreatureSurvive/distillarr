// SPDX-License-Identifier: GPL-3.0-or-later

package encode

import (
	"bufio"
	"context"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Progress is the live encode state parsed from -progress output.
type Progress struct {
	Frame     int64   `json:"frame"`
	FPS       float64 `json:"fps"`
	Speed     float64 `json:"speed"` // e.g. 2.31 (× realtime)
	OutSec    float64 `json:"out_sec"`
	TotalSize int64   `json:"total_size"`
	DurSec    float64 `json:"dur_sec"` // source duration for pct math
}

// Pct returns completion 0..1.
func (p Progress) Pct() float64 {
	if p.DurSec <= 0 {
		return 0
	}
	c := p.OutSec / p.DurSec
	if c < 0 {
		return 0
	}
	if c > 1 {
		return 1
	}
	return c
}

// ETA returns seconds-to-finish (−1 unknown).
func (p Progress) ETA() float64 {
	if p.Speed <= 0 {
		return -1
	}
	rem := p.DurSec - p.OutSec
	if rem < 0 {
		return 0
	}
	return rem / p.Speed
}

// Tail is a bounded stderr collector (last N lines).
type Tail struct {
	mu    sync.Mutex
	lines []string
	max   int
}

func NewTail(max int) *Tail { return &Tail{max: max} }

func (t *Tail) Write(p []byte) (int, error) {
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line == "" {
			continue
		}
		t.mu.Lock()
		t.lines = append(t.lines, line)
		if len(t.lines) > t.max {
			t.lines = t.lines[len(t.lines)-t.max:]
		}
		t.mu.Unlock()
	}
	return len(p), nil
}

func (t *Tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, "\n")
}

// Runner executes one CmdSpec with progress + graceful cancellation
// (SIGINT → wait → SIGKILL via CommandContext+WaitDelay). onProgress
// is invoked from the reader goroutine on every progress block.
func Runner(ctx context.Context, spec CmdSpec, srcDuration float64,
	onProgress func(Progress)) (*Tail, error) {

	tail := NewTail(100)
	cmd := exec.CommandContext(ctx, FFmpeg, spec.Args...)
	cmd.Stderr = tail
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return tail, err
	}
	cmd.WaitDelay = 5 * time.Second
	if cmd.Cancel == nil {
		cmd.Cancel = func() error {
			if cmd.Process != nil {
				_ = cmd.Process.Signal(syscall.SIGINT) // let ffmpeg flush + finalize
			}
			time.Sleep(3 * time.Second)
			return context.Cause(ctx)
		}
	}

	if err := cmd.Start(); err != nil {
		return tail, err
	}

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	kv := map[string]string{}
	for sc.Scan() {
		line := sc.Text()
		// Block separator line "progress=continue|end" ends one stats
		// block — it must be checked before the generic key=value store.
		if strings.HasPrefix(line, "progress=") {
			// NOTE: ffmpeg's out_time_ms value is actually microseconds.
			outUs, _ := strconv.ParseFloat(kv["out_time_us"], 64)
			if outUs == 0 {
				outUs, _ = strconv.ParseFloat(kv["out_time_ms"], 64)
			}
			fps, _ := strconv.ParseFloat(kv["fps"], 64)
			frame, _ := strconv.ParseInt(kv["frame"], 10, 64)
			size, _ := strconv.ParseInt(kv["total_size"], 10, 64)
			if onProgress != nil {
				onProgress(Progress{
					Frame: frame, FPS: fps, Speed: parseSpeed(kv["speed"]),
					OutSec: outUs / 1e6, TotalSize: size, DurSec: srcDuration,
				})
			}
			continue
		}
		if i := strings.IndexByte(line, '='); i > 0 {
			kv[line[:i]] = line[i+1:]
		}
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return tail, context.Cause(ctx)
	}
	return tail, err
}

func parseSpeed(s string) float64 {
	f, _ := strconv.ParseFloat(strings.TrimSuffix(s, "x"), 64)
	return f
}

var hwFailureRe = regexp.MustCompile(
	`(?i)(vaapi|/dev/dri|drm|hwaccel|hwupload|hwdevice|device|qsv|mfx|vpl|onevpl|cuda|nvdec|vulkan|libplacebo|shader|no capable|failed (to|creating)|error initializing)`)

// LooksLikeHWFailure classifies an encode failure as hardware-ish
// (zero frames encoded + hardware/device keywords) and therefore worth
// one software-decode retry.
func LooksLikeHWFailure(tail string, sawFrames bool) bool {
	if sawFrames {
		return false
	}
	return hwFailureRe.MatchString(tail)
}
