// SPDX-License-Identifier: GPL-3.0-or-later

// Package neural runs the neural upscale tier: Real-ESRGAN (ncnn/Vulkan) on a
// video, chunk by chunk. See encode.PlanNeural for how a job is cut up. Every
// finished chunk is kept in the job's work directory, so a run stopped by a
// closed schedule window, a restart or a crash resumes where it left off
// instead of starting hours of GPU work over.
package neural

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/media"
	"github.com/CreatureSurvive/distillarr/internal/upscale"
)

// ErrPaused means the run stopped between chunks because its schedule window
// closed. The work directory is intact and the job should be requeued.
var ErrPaused = errors.New("paused: the upscale window closed")

// Job is one neural upscale to run.
type Job struct {
	Src      *media.Probe
	Settings encode.Settings
	Plan     *encode.NeuralPlan
	Work     string // scratch directory; kept across pauses
	Out      string // where the finished (still unverified) file goes
	GPU      int    // Vulkan device index (ncnn numbers devices the same way ffmpeg does)

	Acquire  func(key string) func()         // GPU slot, shared with other GPU work; nil = none
	Keep     func() bool                     // false: stop after the current chunk (ErrPaused); nil = never stop
	Progress func(done float64, note string) // 0..1 across the whole job
}

// chunkPath is where a finished chunk lives. It only ever appears under this
// name once complete (written as a .part file, then renamed).
func chunkPath(work string, k int) string {
	return filepath.Join(work, fmt.Sprintf("chunk-%05d.mkv", k))
}

// endMarker records that the source ran out of frames after n chunks, so a
// resume doesn't try to decode past the end.
func endMarker(work string) string { return filepath.Join(work, "chunks.end") }

// Run upscales the job, resuming from any chunks already finished.
func Run(ctx context.Context, j Job) error {
	if !upscale.NeuralAvailable() {
		return fmt.Errorf("the neural upscaler isn't installed in this image")
	}
	if err := os.MkdirAll(j.Work, 0o755); err != nil {
		return err
	}
	total := j.Plan.Chunks()
	if n, ok := readEnd(j.Work); ok {
		total = n
	}
	report := func(k int, within float64, note string) {
		if j.Progress != nil {
			j.Progress((float64(k)+within)/float64(max(1, total)), note)
		}
	}

	done := 0
	for k := 0; k < total; k++ {
		if _, err := os.Stat(chunkPath(j.Work, k)); err == nil {
			done++
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if j.Keep != nil && !j.Keep() {
			return ErrPaused
		}
		frames, err := j.chunk(ctx, k, func(within float64, note string) { report(k, within, note) })
		if err != nil {
			return fmt.Errorf("chunk %d/%d: %w", k+1, total, err)
		}
		if frames == 0 { // the source ended before the planned length
			if k == 0 {
				return fmt.Errorf("no frames decoded from the source")
			}
			total = k
			_ = os.WriteFile(endMarker(j.Work), []byte(strconv.Itoa(total)), 0o644)
			break
		}
		done++
	}
	if done < total {
		return fmt.Errorf("only %d of %d chunks finished", done, total)
	}
	report(total, 0, "joining chunks")
	return j.mux(ctx, total)
}

func readEnd(work string) (int, bool) {
	b, err := os.ReadFile(endMarker(work))
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	return n, err == nil && n > 0
}

// chunk decodes, upscales and encodes chunk k, returning how many frames it had.
func (j Job) chunk(ctx context.Context, k int, prog func(float64, string)) (int, error) {
	in, out := filepath.Join(j.Work, "in"), filepath.Join(j.Work, "out")
	for _, d := range []string{in, out} {
		if err := os.RemoveAll(d); err != nil {
			return 0, err
		}
		if err := os.MkdirAll(d, 0o755); err != nil {
			return 0, err
		}
	}
	defer os.RemoveAll(in)
	defer os.RemoveAll(out)

	// 1. Decode this chunk's frames.
	prog(0, "decoding")
	start, dur := j.Plan.ChunkWindow(k)
	args, err := encode.NeuralDecodeArgs(j.Settings, j.Src, start, dur, filepath.Join(in, "%06d.png"))
	if err != nil {
		return 0, err
	}
	if b, err := exec.CommandContext(ctx, encode.FFmpeg, args...).CombinedOutput(); err != nil {
		return 0, fmt.Errorf("decode: %v: %s", err, tail(string(b)))
	}
	ents, err := os.ReadDir(in)
	if err != nil {
		return 0, err
	}
	if len(ents) == 0 {
		return 0, nil
	}

	// 2. Upscale on the GPU, holding the shared slot.
	prog(0.05, "upscaling")
	if err := j.upscaleFrames(ctx, in, out, func(p float64) { prog(0.05+0.85*p, "upscaling") }); err != nil {
		return 0, err
	}

	// 3. Encode the upscaled frames to the final codec as a standalone chunk.
	prog(0.9, "encoding")
	part := filepath.Join(j.Work, fmt.Sprintf("part-%05d.mkv", k))
	spec, err := encode.BuildNeuralChunk(j.Settings, j.Plan, filepath.Join(out, "%06d.png"), part)
	if err != nil {
		return 0, err
	}
	if tl, err := encode.Runner(ctx, *spec, float64(len(ents))/j.Plan.FPSVal, nil); err != nil {
		os.Remove(part)
		return 0, fmt.Errorf("encode: %v: %s", err, tail(tl.String()))
	}
	if err := os.Rename(part, chunkPath(j.Work, k)); err != nil {
		return 0, err
	}
	prog(1, "chunk done")
	return len(ents), nil
}

var pctRe = regexp.MustCompile(`^\s*([0-9.]+)%\s*$`)

// upscaleFrames runs realesrgan-ncnn-vulkan over a directory of frames.
func (j Job) upscaleFrames(ctx context.Context, in, out string, prog func(float64)) error {
	if j.Acquire != nil {
		defer j.Acquire("neural")()
	}
	if err := runNCNN(ctx, in, out, j.Plan.Model, j.Plan.Scale, j.GPU, prog); err != nil {
		return err
	}
	// A run that exits 0 but wrote too few frames is a failure, not a success.
	a, _ := os.ReadDir(in)
	b, _ := os.ReadDir(out)
	if len(b) != len(a) {
		return fmt.Errorf("upscaler wrote %d of %d frames", len(b), len(a))
	}
	return nil
}

// Frame upscales one image (a single-frame preview), holding the shared GPU slot.
func Frame(ctx context.Context, acquire func(string) func(), in, out, model string, scale, gpu int) error {
	if !upscale.NeuralAvailable() {
		return fmt.Errorf("the neural upscaler isn't installed in this image")
	}
	if acquire != nil {
		defer acquire("neural")()
	}
	if err := runNCNN(ctx, in, out, model, scale, gpu, nil); err != nil {
		return err
	}
	if fi, err := os.Stat(out); err != nil || fi.Size() == 0 {
		return fmt.Errorf("the upscaler wrote no image")
	}
	return nil
}

// runNCNN runs realesrgan-ncnn-vulkan on a file or a directory (-i/-o take
// either), reporting its percentage lines to prog if given.
func runNCNN(ctx context.Context, in, out, model string, scale, gpu int, prog func(float64)) error {
	cmd := exec.CommandContext(ctx, upscale.NeuralBin(),
		"-i", in, "-o", out, "-n", model, "-s", strconv.Itoa(scale),
		"-g", strconv.Itoa(gpu), "-m", filepath.Join(upscale.NeuralDir, "models"),
		"-f", "png", "-j", "2:2:4")
	cmd.Env = append(os.Environ(), vulkanEnv()...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	var last []string
	sc := bufio.NewScanner(stderr)
	sc.Split(scanLinesOrCR)
	for sc.Scan() {
		line := sc.Text()
		if m := pctRe.FindStringSubmatch(line); m != nil {
			if f, err := strconv.ParseFloat(m[1], 64); err == nil && prog != nil {
				prog(f / 100)
			}
			continue
		}
		if line = strings.TrimSpace(line); line != "" {
			if last = append(last, line); len(last) > 6 {
				last = last[1:]
			}
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("upscaler: %v: %s", err, strings.Join(last, " | "))
	}
	return nil
}

// mux concatenates the finished chunks and joins them with the source's audio,
// subtitles and metadata.
func (j Job) mux(ctx context.Context, total int) error {
	list := filepath.Join(j.Work, "list.txt")
	if err := os.WriteFile(list, []byte(concatList(j.Work, total)), 0o644); err != nil {
		return err
	}
	spec, err := encode.BuildNeuralMux(j.Settings, j.Src, list, j.Out)
	if err != nil {
		return err
	}
	if tl, err := encode.Runner(ctx, *spec, j.Src.DurationSec(), nil); err != nil {
		os.Remove(j.Out)
		return fmt.Errorf("mux: %v: %s", err, tail(tl.String()))
	}
	return nil
}

// concatList is the ffmpeg concat-demuxer file for the chunks, in order.
func concatList(work string, n int) string {
	var b strings.Builder
	for k := 0; k < n; k++ {
		fmt.Fprintf(&b, "file '%s'\n", strings.ReplaceAll(chunkPath(work, k), "'", `'\''`))
	}
	return b.String()
}

// vulkanEnv points the upscaler at the Vulkan loader and drivers that ship with
// jellyfin-ffmpeg, which is where they live in this image; other layouts fall
// through to the system's.
func vulkanEnv() []string {
	const lib = "/usr/lib/jellyfin-ffmpeg/lib"
	var env []string
	if _, err := os.Stat(lib); err == nil {
		ld := lib
		if cur := os.Getenv("LD_LIBRARY_PATH"); cur != "" {
			ld += ":" + cur
		}
		env = append(env, "LD_LIBRARY_PATH="+ld)
	}
	if icds, _ := filepath.Glob("/usr/lib/jellyfin-ffmpeg/share/vulkan/icd.d/*.json"); len(icds) > 0 && os.Getenv("VK_ICD_FILENAMES") == "" {
		env = append(env, "VK_ICD_FILENAMES="+strings.Join(icds, ":"))
	}
	return env
}

// Cleanup removes a job's work directory (after success, failure or cancel; not
// after a pause).
func Cleanup(work string) { _ = os.RemoveAll(work) }

// scanLinesOrCR splits on \n or \r: ncnn redraws its percentage with \r.
func scanLinesOrCR(data []byte, atEOF bool) (int, []byte, error) {
	for i, c := range data {
		if c == '\n' || c == '\r' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}

func tail(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	return strings.Join(lines, " | ")
}
