// SPDX-License-Identifier: GPL-3.0-or-later

package encode

import (
	"math"
	"strings"
	"testing"
)

func sdSource(fps string) (p *NeuralPlan, err error) {
	src := probe("/m/a.mkv", "h264", "yuv420p", "", []string{"ac3"}, []string{"subrip"})
	src.Streams[0].Width, src.Streams[0].Height = 854, 480
	src.Streams[0].AvgFrameRate = fps
	src.Streams[0].ColorSpace, src.Streams[0].ColorPrimaries = "", ""
	return PlanNeural(Settings{UpscaleTo: 1080, UpscalePreset: "neural-anime"}, &src.Streams[0], 1440)
}

// Every frame of the source must land in exactly one chunk. Windows are
// half-frame offset so no frame sits on a boundary; if any frame were dropped or
// doubled at a seam, audio would drift out of sync a frame per chunk.
func TestChunkWindowsPartitionEveryFrame(t *testing.T) {
	for _, fps := range []string{"24000/1001", "25/1", "30000/1001", "60/1", "24/1", "50/1"} {
		n, err := sdSource(fps)
		if err != nil {
			t.Fatal(err)
		}
		total := n.Frames
		owner := make([]int, total)
		for i := 0; i < total; i++ {
			ts := float64(i) / n.FPSVal
			for k := 0; k < n.Chunks(); k++ {
				start, dur := n.ChunkWindow(k)
				if ts >= start && (dur < 0 || ts < start+dur) {
					owner[i]++
					if k > 0 && i < k*n.ChunkFrames-1 || i > (k+1)*n.ChunkFrames {
						t.Fatalf("%s: frame %d landed in chunk %d, far from where it belongs", fps, i, k)
					}
				}
			}
		}
		for i, c := range owner {
			if c != 1 {
				t.Fatalf("%s: frame %d belongs to %d chunks, want exactly 1", fps, i, c)
			}
		}
		// Chunks are the planned size, so estimates and progress are honest.
		if want := int(math.Round(n.FPSVal * ChunkSeconds)); n.ChunkFrames != want {
			t.Errorf("%s: chunk is %d frames, want %d", fps, n.ChunkFrames, want)
		}
	}
}

func TestPlanNeural(t *testing.T) {
	n, err := sdSource("24000/1001")
	if err != nil {
		t.Fatal(err)
	}
	// 854 wide to 1920: the 3x model reaches it, and the result is resized down.
	if n.Scale != 3 || n.ModelW != 2562 || n.OutW != 1920 || n.OutH != 1080 || n.Model != "realesr-animevideov3" {
		t.Errorf("plan: %+v", n)
	}
	// 24 minutes at 854x480: 34,517 frames x 0.40 s x 1.42 (pixels vs the 720x400
	// reference) = ~19,700 s, about 5.5 hours.
	if h := n.EstimateSecs / 3600; h < 5.2 || h > 5.8 {
		t.Errorf("estimate %.1fh, want ~5.5h for a 24-minute 854x480 source", h)
	}
	if _, err := PlanNeural(Settings{UpscaleTo: 1080, UpscalePreset: "film-lanczos"}, &probe("/m/a.mkv", "h264", "yuv420p", "", nil, nil).Streams[0], 100); err == nil {
		t.Error("a shader preset is not a neural plan")
	}
	if _, err := sdSource(""); err == nil {
		t.Error("no frame rate means no way to chunk")
	}
}

func TestNeuralChunkCommand(t *testing.T) {
	n, _ := sdSource("24000/1001")
	for _, b := range []Backend{SW, QSV, VAAPI} {
		spec, err := BuildNeuralChunk(Settings{Codec: HEVC, Backend: b, RenderNode: "/dev/dri/renderD129",
			UpscaleTo: 1080, UpscalePreset: "neural-anime", VMAFTarget: 93}, n, "/w/in/%06d.png", "/w/chunk-0000.mkv")
		if err != nil {
			t.Fatal(err)
		}
		a := strings.Join(spec.Args, " ")
		for _, want := range []string{"-framerate 24000/1001 -start_number 1 -i /w/in/%06d.png", "-f matroska", "-an", "/w/chunk-0000.mkv",
			"scale=out_color_matrix=bt709:out_range=tv,format=yuv444p", "-colorspace bt709"} {
			if !strings.Contains(a, want) {
				t.Errorf("%s chunk missing %q:\n%s", b, want, a)
			}
		}
		if strings.Contains(a, "-ss ") || strings.Contains(a, " -t ") || strings.Contains(a, "libplacebo") || strings.Contains(a, "-hwaccel") {
			t.Errorf("%s chunk must read whole PNG sequence in software, no seek/upscale filter: %s", b, a)
		}
		// The size is forced exactly (res.Up), not left to a class fit.
		switch b {
		case SW:
			if !strings.Contains(a, "scale=1920:1080:flags=lanczos") {
				t.Errorf("SW must resize the model output to exactly 1920x1080: %s", a)
			}
		case QSV:
			if !strings.Contains(a, "w=1920:h=1080") {
				t.Errorf("QSV must resize to exactly 1920x1080: %s", a)
			}
		}
	}
}

func TestNeuralDecodeArgs(t *testing.T) {
	src := probe("/m/a.mkv", "h264", "yuv420p", "", []string{"ac3"}, nil)
	src.Streams[0].Width, src.Streams[0].Height = 854, 480
	s := Settings{UpscaleTo: 1080, UpscalePreset: "neural-anime", Crop: "854:356:0:62"}
	a, err := NeuralDecodeArgs(s, src, 19.5, 20, "/w/in/%06d.png")
	if err != nil {
		t.Fatal(err)
	}
	j := strings.Join(a, " ")
	for _, want := range []string{"-ss 19.5000 -i /m/a.mkv", "-t 20.0000", "-vf crop=854:356:0:62", "-fps_mode passthrough", "-start_number 1", "/w/in/%06d.png"} {
		if !strings.Contains(j, want) {
			t.Errorf("decode missing %q: %s", want, j)
		}
	}
	first, _ := NeuralDecodeArgs(s, src, 0, 19.5, "/w/in/%06d.png")
	if strings.Contains(strings.Join(first, " "), "-ss") {
		t.Error("the first chunk starts at 0: no seek")
	}
	last, _ := NeuralDecodeArgs(s, src, 100, -1, "/w/in/%06d.png")
	if strings.Contains(strings.Join(last, " "), " -t ") {
		t.Error("the last chunk runs to the end of the file")
	}
	hdr := probe("/m/h.mkv", "hevc", "yuv420p10le", "smpte2084", nil, nil)
	if _, err := NeuralDecodeArgs(s, hdr, 0, 20, "/x/%06d.png"); err == nil {
		t.Error("untonemapped HDR must be refused")
	}
}

func TestNeuralMux(t *testing.T) {
	src := probe("/m/a.mkv", "h264", "yuv420p", "", []string{"ac3", "aac"}, []string{"subrip"})
	spec, err := BuildNeuralMux(Settings{Codec: HEVC, UpscaleTo: 1080, UpscalePreset: "neural-anime"}, src, "/w/list.txt", "/o.mkv")
	if err != nil {
		t.Fatal(err)
	}
	a := strings.Join(spec.Args, " ")
	for _, want := range []string{"-f concat -safe 0 -i /w/list.txt -i /m/a.mkv", "-map 0:v:0", "-map 1:1", "-map 1:2", "-map 1:3",
		"-c:v copy", "-map_metadata 1 -map_chapters 1", "-f matroska", "-y /o.mkv"} {
		if !strings.Contains(a, want) {
			t.Errorf("mux missing %q:\n%s", want, a)
		}
	}
	// The source's own video stream (0:0) must not be mapped: the video comes from the chunks.
	if strings.Contains(a, "-map 0:0") || strings.Contains(a, "-map 1:0") {
		t.Errorf("the source video must not be mapped into the output: %s", a)
	}
	if spec.ExpectAudio != 2 || spec.ExpectSubs != 1 || spec.Container != "mkv" {
		t.Errorf("verify expectations: %+v", spec)
	}
}

func TestUpscaleTierFollowsPreset(t *testing.T) {
	for id, want := range map[string]string{"neural-anime": "neural", "film-lanczos": "shader", "fsr": "shader", "nope": "shader"} {
		s := Settings{UpscaleTo: 1080, UpscalePreset: id, UpscaleTier: "shader"}
		s.Normalize()
		if s.UpscaleTier != want {
			t.Errorf("%s: tier %q, want %q", id, s.UpscaleTier, want)
		}
	}
}

// PNG frames are RGB, so the YUV->RGB matrix is chosen at decode. swscale's
// default (BT.601) would tint every HD source.
func TestNeuralDecodeUsesTheSourceMatrix(t *testing.T) {
	s := Settings{UpscaleTo: 2160, UpscalePreset: "neural-anime"}
	for _, c := range []struct {
		name          string
		w, h          int
		space, wantIn string
	}{
		{"untagged SD", 854, 480, "", "bt601"},
		{"untagged HD", 1920, 1080, "", "bt709"},
		{"tagged", 1920, 1080, "bt470bg", "auto"},
	} {
		src := probe("/m/a.mkv", "h264", "yuv420p", "", nil, nil)
		src.Streams[0].Width, src.Streams[0].Height, src.Streams[0].ColorSpace = c.w, c.h, c.space
		a, err := NeuralDecodeArgs(s, src, 0, 20, "/w/%06d.png")
		if err != nil {
			t.Fatal(err)
		}
		if j := strings.Join(a, " "); !strings.Contains(j, "scale=in_color_matrix="+c.wantIn+",format=rgb24") {
			t.Errorf("%s: want in_color_matrix=%s then rgb24: %s", c.name, c.wantIn, j)
		}
		_, _, pre, err := NeuralStill(Settings{UpscaleTo: 2160, UpscalePreset: "neural-anime"}, &src.Streams[0])
		if err != nil || !strings.Contains(strings.Join(pre, ","), "scale=in_color_matrix="+c.wantIn) {
			t.Errorf("%s: the still must decode the same way: %v %v", c.name, pre, err)
		}
	}
}
