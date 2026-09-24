// SPDX-License-Identifier: GPL-3.0-or-later

package replace

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Runs the real colour guard against real ffmpeg output. The "corrupt" file is
// built the way the Vulkan readback bug looked: correct size, clean decode,
// both chroma planes zero. Skipped where ffmpeg isn't installed.
func TestCheckChromaCatchesCorruptFrames(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	make := func(name string, args ...string) string {
		out := filepath.Join(dir, name)
		a := append([]string{"-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi",
			"-i", "testsrc2=size=320x240:rate=10:duration=4"}, args...)
		if b, err := exec.Command(ff, append(a, "-pix_fmt", "yuv420p", out)...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg %s: %v\n%s", name, err, b)
		}
		return out
	}
	src := make("src.mp4")
	good := make("good.mp4", "-vf", "scale=640:480:flags=lanczos")
	green := make("green.mp4", "-vf", "scale=640:480:flags=lanczos,format=yuv420p,lutyuv=u=0:v=0")

	ctx := context.Background()
	if err := checkChroma(ctx, src, good, 4); err != nil {
		t.Errorf("a faithful upscale must pass: %v", err)
	}
	err = checkChroma(ctx, src, green, 4)
	if err == nil {
		t.Fatal("zero-chroma output must be rejected")
	}
	if !strings.Contains(err.Error(), "colour is wrong") {
		t.Errorf("unhelpful error: %v", err)
	}
}
