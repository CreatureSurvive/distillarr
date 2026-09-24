// SPDX-License-Identifier: GPL-3.0-or-later

package neural

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/media"
	"github.com/CreatureSurvive/distillarr/internal/upscale"
)

// fixture installs stand-in ffmpeg and upscaler scripts and returns a Job.
// FAKE_FRAMES lists how many frames each successive decode produces, so a test
// controls exactly what the "source" contains; every call is logged.
type fixture struct {
	dir, log string
	job      Job
}

func newFixture(t *testing.T, frames string) *fixture {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "calls.log")
	ff := filepath.Join(dir, "ffmpeg")
	os.WriteFile(ff, []byte(`#!/bin/bash
echo "$*" >> "$FAKE_LOG"
last="${@: -1}"
case "$*" in
*"-fps_mode passthrough"*)   # decode: n frames from FAKE_FRAMES, by call index
  n=$(grep -c -e '-fps_mode passthrough' "$FAKE_LOG"); IFS=, read -ra F <<< "$FAKE_FRAMES"; c=${F[$((n-1))]:-0}
  d=$(dirname "$last"); for ((i=1;i<=c;i++)); do printf x > "$d/$(printf %06d $i).png"; done ;;
*"-framerate"*) printf chunk > "$last" ;;
*"-f concat"*)  li=$(sed -E 's/.*-f concat -safe 0 -i ([^ ]+) .*/\1/' <<< "$*"); cp "$li" "$last" ;;
esac
exit 0
`), 0o755)
	nd := filepath.Join(dir, "ncnn")
	os.MkdirAll(filepath.Join(nd, "models"), 0o755)
	os.WriteFile(filepath.Join(nd, "realesrgan-ncnn-vulkan"), []byte(`#!/bin/bash
echo "ncnn $*" >> "$FAKE_LOG"
while [ $# -gt 0 ]; do case "$1" in -i) in="$2";; -o) out="$2";; esac; shift; done
[ -n "$FAKE_NCNN_FAIL" ] && { echo "vkCreateInstance failed" >&2; exit 3; }
printf '0.00%%\r50.00%%\r' >&2
n=0; for f in "$in"/*.png; do n=$((n+1)); [ -n "$FAKE_NCNN_SHORT" ] && [ $n -gt 2 ] && break; cp "$f" "$out/$(basename $f)"; done
printf '100.00%%\n' >&2
`), 0o755)

	t.Setenv("FAKE_LOG", log)
	t.Setenv("FAKE_FRAMES", frames)
	oldFF, oldND := encode.FFmpeg, upscale.NeuralDir
	encode.FFmpeg, upscale.NeuralDir = ff, nd
	t.Cleanup(func() { encode.FFmpeg, upscale.NeuralDir = oldFF, oldND })

	src := &media.Probe{
		Format: media.Format{Filename: "/m/a.mkv", Duration: "0.5"},
		Streams: []media.Stream{
			{Index: 0, CodecType: "video", CodecName: "h264", Width: 854, Height: 480, PixFmt: "yuv420p", AvgFrameRate: "24/1"},
			{Index: 1, CodecType: "audio", CodecName: "ac3", Channels: 2},
		},
	}
	s := encode.Settings{Codec: encode.HEVC, Backend: encode.SW, UpscaleTo: 1080, UpscalePreset: "neural-anime"}
	s.Normalize()
	work := filepath.Join(dir, "work")
	return &fixture{dir: dir, log: log, job: Job{
		Src: src, Settings: s, Work: work, Out: filepath.Join(dir, "out.mkv"),
		Plan: &encode.NeuralPlan{Scale: 3, InW: 854, InH: 480, ModelW: 2562, ModelH: 1440, OutW: 1920, OutH: 1080,
			FPS: "24/1", FPSVal: 24, Frames: 12, ChunkFrames: 5, Model: "realesr-animevideov3"},
	}}
}

func (f *fixture) calls() []string {
	b, _ := os.ReadFile(f.log)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func (f *fixture) count(sub string) int {
	n := 0
	for _, c := range f.calls() {
		if strings.Contains(c, sub) {
			n++
		}
	}
	return n
}

func TestRunFullJob(t *testing.T) {
	f := newFixture(t, "5,5,2")
	var mu sync.Mutex
	var prog []float64
	f.job.Progress = func(d float64, _ string) { mu.Lock(); prog = append(prog, d); mu.Unlock() }
	if err := Run(context.Background(), f.job); err != nil {
		t.Fatal(err)
	}
	if f.count("-fps_mode passthrough") != 3 || f.count("ncnn ") != 3 || f.count("-framerate") != 3 || f.count("-f concat") != 1 {
		t.Errorf("want 3 decode/upscale/encode and 1 mux:\n%s", strings.Join(f.calls(), "\n"))
	}
	list, _ := os.ReadFile(filepath.Join(f.job.Work, "list.txt"))
	if got := strings.Count(string(list), "file '"); got != 3 {
		t.Errorf("concat list has %d chunks, want 3:\n%s", got, list)
	}
	if _, err := os.Stat(f.job.Out); err != nil {
		t.Errorf("no output: %v", err)
	}
	// The upscaler was pointed at the planned model, scale and GPU.
	if !strings.Contains(strings.Join(f.calls(), "\n"), "-n realesr-animevideov3 -s 3 -g 0") {
		t.Errorf("upscaler args: %v", f.calls())
	}
	// Progress climbs to 1 and never goes backwards (the UI shows it as a bar).
	for i := 1; i < len(prog); i++ {
		if prog[i]+1e-9 < prog[i-1]-0.34 { // within-chunk sub-steps may reset by < one chunk
			t.Errorf("progress jumped back: %v", prog)
			break
		}
	}
	if len(prog) == 0 || prog[len(prog)-1] < 0.99 {
		t.Errorf("progress should end at 1: %v", prog)
	}
	// Per-chunk frame directories don't pile up on disk.
	for _, d := range []string{"in", "out"} {
		if _, err := os.Stat(filepath.Join(f.job.Work, d)); err == nil {
			t.Errorf("%s/ should be removed after each chunk", d)
		}
	}
}

// The reason chunks exist: a restart must not redo finished work.
func TestRunResumesFromFinishedChunks(t *testing.T) {
	f := newFixture(t, "2") // only the last chunk will be decoded
	os.MkdirAll(f.job.Work, 0o755)
	for k := 0; k < 2; k++ {
		os.WriteFile(chunkPath(f.job.Work, k), []byte("done"), 0o644)
	}
	if err := Run(context.Background(), f.job); err != nil {
		t.Fatal(err)
	}
	if got := f.count("-fps_mode passthrough"); got != 1 {
		t.Errorf("only chunk 2 should be decoded on resume, got %d decodes", got)
	}
	// ...and the chunk that was decoded is the third one, seeking to its window.
	start, _ := f.job.Plan.ChunkWindow(2)
	if !strings.Contains(f.calls()[0], fmt.Sprintf("-ss %.4f", start)) {
		t.Errorf("resume decoded the wrong window: %s", f.calls()[0])
	}
	if list, _ := os.ReadFile(filepath.Join(f.job.Work, "list.txt")); strings.Count(string(list), "file '") != 3 {
		t.Errorf("all three chunks must be joined:\n%s", list)
	}
}

func TestRunPausesBetweenChunks(t *testing.T) {
	f := newFixture(t, "5,5,2")
	calls := 0
	f.job.Keep = func() bool { calls++; return calls == 1 } // the window closes after the first chunk
	err := Run(context.Background(), f.job)
	if !errors.Is(err, ErrPaused) {
		t.Fatalf("want ErrPaused, got %v", err)
	}
	if _, e := os.Stat(chunkPath(f.job.Work, 0)); e != nil {
		t.Error("the finished chunk must survive a pause")
	}
	if _, e := os.Stat(chunkPath(f.job.Work, 1)); e == nil {
		t.Error("no second chunk should exist")
	}
	if _, e := os.Stat(f.job.Out); e == nil {
		t.Error("a paused job has no output yet")
	}
	// Resuming picks up at chunk 1 and completes.
	f.job.Keep = nil
	t.Setenv("FAKE_FRAMES", "5,2")
	before := f.count("-fps_mode passthrough")
	if err := Run(context.Background(), f.job); err != nil {
		t.Fatal(err)
	}
	if got := f.count("-fps_mode passthrough") - before; got != 2 {
		t.Errorf("resume should decode the 2 remaining chunks, decoded %d", got)
	}
}

// The plan estimates the frame count; the file is the truth.
func TestRunSourceEndsEarly(t *testing.T) {
	f := newFixture(t, "5,0") // planned 3 chunks, but the source has one chunk of frames
	if err := Run(context.Background(), f.job); err != nil {
		t.Fatal(err)
	}
	if list, _ := os.ReadFile(filepath.Join(f.job.Work, "list.txt")); strings.Count(string(list), "file '") != 1 {
		t.Errorf("only the chunk with frames belongs in the output:\n%s", list)
	}
	if b, _ := os.ReadFile(endMarker(f.job.Work)); strings.TrimSpace(string(b)) != "1" {
		t.Errorf("end marker: %q", b)
	}
	// A resume must trust the marker and not decode past the end again.
	before := f.count("-fps_mode passthrough")
	if err := Run(context.Background(), f.job); err != nil {
		t.Fatal(err)
	}
	if f.count("-fps_mode passthrough") != before {
		t.Error("a resume with the end marker must not decode again")
	}
}

func TestRunFailureModes(t *testing.T) {
	t.Run("no frames at all", func(t *testing.T) {
		f := newFixture(t, "0")
		if err := Run(context.Background(), f.job); err == nil || !strings.Contains(err.Error(), "no frames") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("upscaler fails", func(t *testing.T) {
		f := newFixture(t, "5,5,2")
		t.Setenv("FAKE_NCNN_FAIL", "1")
		err := Run(context.Background(), f.job)
		if err == nil || !strings.Contains(err.Error(), "vkCreateInstance failed") || !strings.Contains(err.Error(), "chunk 1/3") {
			t.Errorf("the upscaler's own message and the chunk must surface: %v", err)
		}
		if _, e := os.Stat(filepath.Join(f.job.Work, "in")); e == nil {
			t.Error("frame directories must be cleaned up after a failure")
		}
	})
	t.Run("upscaler exits 0 but writes too few frames", func(t *testing.T) {
		f := newFixture(t, "5,5,2")
		t.Setenv("FAKE_NCNN_SHORT", "1")
		if err := Run(context.Background(), f.job); err == nil || !strings.Contains(err.Error(), "wrote 2 of 5") {
			t.Errorf("a silent short write must fail the chunk: %v", err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		f := newFixture(t, "5,5,2")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := Run(ctx, f.job); !errors.Is(err, context.Canceled) {
			t.Errorf("got %v", err)
		}
	})
	t.Run("not installed", func(t *testing.T) {
		f := newFixture(t, "5")
		upscale.NeuralDir = t.TempDir()
		if err := Run(context.Background(), f.job); err == nil || !strings.Contains(err.Error(), "isn't installed") {
			t.Errorf("got %v", err)
		}
	})
}

func TestConcatListQuoting(t *testing.T) {
	got := concatList("/w/it's here", 2)
	if !strings.Contains(got, `file '/w/it'\''s here/chunk-00000.mkv'`) || strings.Count(got, "\n") != 2 {
		t.Errorf("a quote in the path must be escaped for the concat demuxer:\n%s", got)
	}
}

func TestScanLinesOrCR(t *testing.T) {
	var got []string
	for buf := []byte("0.00%\r12.50%\r25.00%\n"); len(buf) > 0; {
		adv, tok, _ := scanLinesOrCR(buf, false)
		if adv == 0 {
			break
		}
		got = append(got, string(tok))
		buf = buf[adv:]
	}
	if strings.Join(got, ",") != "0.00%,12.50%,25.00%" {
		t.Errorf("ncnn redraws its percentage with \\r: %v", got)
	}
	if m := pctRe.FindStringSubmatch("  37.50% "); m == nil || m[1] != "37.50" {
		t.Error("percentage line not recognised")
	}
	if pctRe.MatchString("[0 Intel(R) Arc(tm) A380] queueC=0") {
		t.Error("device lines are not progress")
	}
}
