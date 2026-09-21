// Package hwprobe detects usable hardware encoders by running real
// test encodes — only encoders that demonstrably work are advertised.
package hwprobe

import (
	"context"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"mediatrans/internal/encode"
	"mediatrans/internal/store"
)

// EncoderResult is the probe outcome for one backend×codec×node.
type EncoderResult struct {
	Backend encode.Backend `json:"backend"`
	Codec   encode.Codec   `json:"codec"`
	Node    string         `json:"node,omitempty"` // render node used
	OK      bool           `json:"ok"`
	MS      int64          `json:"ms"`
	Error   string         `json:"error,omitempty"`
}

// Report is the full probe snapshot, persisted under kv "hw_probe".
type Report struct {
	TestedAt    time.Time       `json:"tested_at"`
	FFmpeg      string          `json:"ffmpeg_version"`
	RenderNodes []string        `json:"render_nodes"`
	HasNVENC    bool            `json:"has_nvenc"`
	Results     []EncoderResult `json:"results"`
}

// Available reports whether backend+codec passed its test encode.
func (r *Report) Available(b encode.Backend, c encode.Codec) *EncoderResult {
	for i := range r.Results {
		if r.Results[i].Backend == b && r.Results[i].Codec == c && r.Results[i].OK {
			return &r.Results[i]
		}
	}
	return nil
}

// BackendsFor returns preference-ordered working backends for a codec.
func (r *Report) BackendsFor(c encode.Codec) []encode.Backend {
	var out []encode.Backend
	for _, b := range encode.AllBackends {
		if r.Available(b, c) != nil {
			out = append(out, b)
		}
	}
	return out
}

// RenderNodes lists /dev/dri render device paths.
func RenderNodes() []string {
	entries, err := os.ReadDir("/dev/dri")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "renderD") {
			out = append(out, "/dev/dri/"+e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// ffmpegVersion runs ffmpeg -version and returns the first line.
func ffmpegVersion() string {
	out, err := execOutput("ffmpeg", "-version")
	if err != nil {
		return ""
	}
	if i := strings.IndexByte(out, '\n'); i > 0 {
		return out[:i]
	}
	return out
}

func execOutput(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	b, err := exec.CommandContext(ctx, name, args...).Output()
	return string(b), err
}

// execCommandCombined runs a command capturing stdout+stderr.
func execCommandCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

// cat concatenates string slices (append-with-spread helper).
func cat(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// testEncoders maps backend+codec → its test invocation. The test uses
// the software-decode → hwupload path: it exercises the encoder (what
// we care about) without depending on hw decode support.
func testArgs(b encode.Backend, c encode.Codec, node string) []string {
	src := []string{"-f", "lavfi", "-i", "testsrc2=duration=1:size=320x240:rate=10"}
	common := []string{"-hide_banner", "-loglevel", "error", "-nostdin"}
	switch b {
	case encode.SW:
		enc := "libx265"
		preset := "ultrafast"
		if c == encode.AV1 {
			enc = "libsvtav1"
			preset = "11" // SVT-AV1 presets are numeric
		}
		return cat(common, src, []string{"-c:v", enc, "-preset", preset,
			"-frames:v", "10", "-f", "null", "-"})
	case encode.QSV:
		enc := "hevc_qsv"
		extra := []string{"-rc_mode", "ICQ", "-global_quality", "30", "-profile:v", "main10"}
		if c == encode.AV1 {
			enc = "av1_qsv"
			extra = []string{"-rc_mode", "ICQ", "-global_quality", "30"}
		}
		return cat(common,
			[]string{"-init_hw_device", "vaapi=va:" + node,
				"-init_hw_device", "qsv=qsv@va", "-filter_hw_device", "qsv"},
			src,
			[]string{"-vf", "format=nv12|p010le,hwupload=extra_hw_frames=64,vpp_qsv=format=p010le",
				"-c:v", enc},
			extra,
			[]string{"-frames:v", "10", "-f", "null", "-"})
	case encode.VAAPI:
		enc := "hevc_vaapi"
		extra := []string{"-rc_mode", "CQP", "-qp", "30", "-profile:v", "main10"}
		if c == encode.AV1 {
			enc = "av1_vaapi"
			extra = []string{"-rc_mode", "CQP", "-qp", "30"}
		}
		return cat(common,
			[]string{"-init_hw_device", "vaapi=va:" + node, "-filter_hw_device", "va"},
			src,
			[]string{"-vf", "format=p010le,hwupload=extra_hw_frames=32,scale_vaapi=format=p010le",
				"-c:v", enc},
			extra,
			[]string{"-frames:v", "10", "-f", "null", "-"})
	case encode.NVENC:
		enc := "hevc_nvenc"
		if c == encode.AV1 {
			enc = "av1_nvenc"
		}
		return cat(common, src, []string{"-c:v", enc, "-preset", "p1",
			"-frames:v", "10", "-f", "null", "-"})
	}
	return nil
}

// Run probes every backend×codec across all render nodes.
func Run(st *store.Store) (*Report, error) {
	r := &Report{TestedAt: time.Now(), FFmpeg: ffmpegVersion(), RenderNodes: RenderNodes()}
	r.HasNVENC = detectNVGPU()

	codecs := []encode.Codec{encode.HEVC, encode.AV1}

	// Software.
	for _, c := range codecs {
		r.Results = append(r.Results, runOne(encode.SW, c, ""))
	}
	// Intel: every render node (A380 + iGPU differ in AV1 support).
	for _, node := range r.RenderNodes {
		for _, c := range codecs {
			for _, b := range []encode.Backend{encode.QSV, encode.VAAPI} {
				r.Results = append(r.Results, runOne(b, c, node))
			}
		}
	}
	// NVIDIA once.
	if r.HasNVENC {
		for _, c := range codecs {
			r.Results = append(r.Results, runOne(encode.NVENC, c, ""))
		}
	}

	if err := store.KVJSON(st, "hw_probe", r); err != nil {
		return r, err
	}
	return r, nil
}

func runOne(b encode.Backend, c encode.Codec, node string) EncoderResult {
	res := EncoderResult{Backend: b, Codec: c, Node: node}
	args := testArgs(b, c, node)
	if args == nil {
		res.Error = "unsupported"
		return res
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := execCommandCombined(ctx, "ffmpeg", args...)
	res.MS = time.Since(start).Milliseconds()
	if err != nil {
		res.Error = trimErr(string(out))
		if len(res.Error) == 0 {
			res.Error = err.Error()
		}
	} else {
		res.OK = true
	}
	return res
}

func trimErr(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i > 200 {
		s = s[max(0, len(s)-300):]
	}
	lines := strings.Split(s, "\n")
	if len(lines) > 4 {
		lines = lines[len(lines)-4:]
	}
	return strings.TrimSpace(strings.Join(lines, " | "))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func detectNVGPU() bool {
	if _, err := os.Stat("/dev/nvidia0"); err == nil {
		return true
	}
	if _, err := os.Stat("/proc/driver/nvidia/version"); err == nil {
		return true
	}
	_, err := execOutput("nvidia-smi", "-L")
	return err == nil
}

// Load returns the stored report, if any.
func Load(st *store.Store) (*Report, error) {
	var r Report
	_, err := store.KVLoad(st, "hw_probe", &r)
	if err != nil {
		return nil, err
	}
	if r.TestedAt.IsZero() {
		return nil, nil
	}
	return &r, nil
}

// ResolveBackend picks the working backend for a codec given the
// configured preference, falling back down the chain.
func ResolveBackend(rep *Report, pref string, c encode.Codec) encode.Backend {
	if rep == nil {
		return encode.SW
	}
	if pref != "" && pref != "auto" {
		b := encode.Backend(pref)
		if rep.Available(b, c) != nil {
			return b
		}
	}
	if bs := rep.BackendsFor(c); len(bs) > 0 {
		return bs[0]
	}
	return encode.SW
}

// NodeFor returns the render node to use for a backend+codec: the
// FASTEST passing test wins (prefers the discrete GPU when both pass).
func NodeFor(rep *Report, b encode.Backend, c encode.Codec) string {
	var best *EncoderResult
	for i := range rep.Results {
		r := &rep.Results[i]
		if r.Backend == b && r.Codec == c && r.OK && r.Node != "" {
			if best == nil || r.MS < best.MS {
				best = r
			}
		}
	}
	if best != nil {
		return best.Node
	}
	return encode.DefaultRenderNode
}
