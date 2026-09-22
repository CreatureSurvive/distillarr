package upscale

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

//go:embed shaders/*.glsl
var shaderFS embed.FS

// ShaderDir is where preset shaders are written for libplacebo, whose
// custom_shader_path needs a real file. Files are named by content hash, so
// they are written once and reused across jobs and restarts.
var ShaderDir = filepath.Join(os.TempDir(), "mediatrans-shaders")

var writeMu sync.Mutex

// shaderPath materialises a preset's concatenated shaders and returns the
// file path, or "" for presets that use only a built-in scaler.
func (p Preset) shaderPath(v map[string]float64) (string, error) {
	if len(p.shaders) == 0 {
		return "", nil
	}
	var names []string
	for _, o := range p.opt { // optional passes first (see Preset.opt)
		if v[o.param] >= 1 {
			names = append(names, o.file)
		}
	}
	names = append(names, p.shaders...)
	var buf strings.Builder
	for i, name := range names {
		b, err := shaderFS.ReadFile("shaders/" + name)
		if err != nil {
			return "", fmt.Errorf("upscale preset %s: %w", p.ID, err)
		}
		if i > 0 {
			buf.WriteString("\n\n") // a blank line ends a shader's data table
		}
		buf.Write(b)
	}
	buf.WriteByte('\n')
	src := buf.String()
	if p.tune != nil {
		src = p.tune(src, v)
	}
	sum := sha256.Sum256([]byte(src))
	path := filepath.Join(ShaderDir, fmt.Sprintf("%s-%x.glsl", p.ID, sum[:4]))

	writeMu.Lock()
	defer writeMu.Unlock()
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := os.MkdirAll(ShaderDir, 0o755); err != nil {
		return "", err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(src), 0o644); err != nil {
		return "", err
	}
	pruneShaders()
	return path, os.Rename(tmp, path)
}

// pruneShaders keeps the shader cache bounded: dragging a tuning slider writes
// one file per value. The oldest go first; anything still in use is re-created
// on demand, since files are named by content.
func pruneShaders() {
	const keep = 60
	ents, err := os.ReadDir(ShaderDir)
	if err != nil || len(ents) <= keep {
		return
	}
	sort.Slice(ents, func(i, j int) bool {
		a, _ := ents[i].Info()
		b, _ := ents[j].Info()
		return a != nil && b != nil && a.ModTime().Before(b.ModTime())
	})
	for _, e := range ents[:len(ents)-keep] {
		os.Remove(filepath.Join(ShaderDir, e.Name()))
	}
}

// Spec is one upscale stage: the output size, the preset and its params.
type Spec struct {
	W, H   int
	Preset string
	Params map[string]float64
}

// Filter returns the libplacebo filter for the spec, working and producing
// pixel format `format` (nv12 or p010le).
func (sp Spec) Filter(format string) (string, error) {
	p, ok := Get(sp.Preset)
	if !ok {
		return "", fmt.Errorf("unknown upscale preset %q", sp.Preset)
	}
	if p.Tier != TierShader {
		return "", fmt.Errorf("upscale preset %q is not a shader preset", sp.Preset)
	}
	if sp.W <= 0 || sp.H <= 0 {
		return "", fmt.Errorf("upscale target %dx%d", sp.W, sp.H)
	}
	v := p.Resolve(sp.Params)
	parts := []string{
		fmt.Sprintf("libplacebo=w=%d:h=%d", sp.W, sp.H),
		"upscaler=" + p.scaler,
		fmt.Sprintf("sigmoid=%d", int(v["sigmoid"])),
		fmt.Sprintf("deband=%d", int(v["deband"])),
	}
	path, err := p.shaderPath(v)
	if err != nil {
		return "", err
	}
	if path != "" {
		// The path sits inside a filtergraph: refuse anything that would
		// need escaping rather than build a broken or injectable string.
		if strings.ContainsAny(path, ":,;'\"\\ []=") {
			return "", fmt.Errorf("shader path %q needs filter escaping", path)
		}
		parts = append(parts, "custom_shader_path="+path)
	}
	parts = append(parts, "format="+format)
	return strings.Join(parts, ":"), nil
}

// rgbFor is the RGB surface libplacebo renders into. YUV output from
// libplacebo is corrupt through Vulkan here (both chroma planes read back as
// zero, luma is wrong too, even for 4:4:4), while RGB is exact. 10-bit
// targets use packed 10-bit RGB so precision survives the trip.
func rgbFor(outFmt string) string {
	if outFmt == "p010le" {
		return "x2bgr10le"
	}
	return "rgba"
}

// RGBChain takes software frames in inFmt, upscales them on the Vulkan device
// named by -filter_hw_device, and returns software RGB frames. libplacebo
// infers the source matrix (BT.601 for SD, BT.709 for HD) unless the frame is
// tagged, so the picture is decoded correctly on the way in.
func (sp Spec) RGBChain(inFmt, rgb string) ([]string, error) {
	f, err := sp.Filter(rgb)
	if err != nil {
		return nil, err
	}
	return []string{"format=" + inFmt, "hwupload", f, "hwdownload", "format=" + rgb}, nil
}

// Chain is RGBChain plus the conversion back to YUV: software frames in
// inFmt in, outFmt out. Every upscale target is 720p or above, so the RGB is
// encoded as BT.709 (scale tags the frames bt709) whatever the source used.
// toVAAPI appends the upload a VA-API encoder needs; every other encoder
// accepts software frames directly.
//
// Vulkan frames can't be mapped back to VA-API or QSV surfaces on current
// Mesa (hwmap fails with ENOSYS), and YUV can't be read back from Vulkan at
// all (see rgbFor), so the frame crosses system memory as RGB and is
// converted on the CPU.
func (sp Spec) Chain(inFmt, outFmt string, toVAAPI bool) ([]string, error) {
	c, err := sp.RGBChain(inFmt, rgbFor(outFmt))
	if err != nil {
		return nil, err
	}
	c = append(c, "scale=out_color_matrix=bt709:out_range=tv", "format="+outFmt)
	if toVAAPI {
		c = append(c, "hwupload_vaapi")
	}
	return c, nil
}
