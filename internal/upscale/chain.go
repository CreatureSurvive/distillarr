package upscale

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"os"
	"path/filepath"
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
func (p Preset) shaderPath() (string, error) {
	if len(p.shaders) == 0 {
		return "", nil
	}
	var buf strings.Builder
	for _, name := range p.shaders {
		b, err := shaderFS.ReadFile("shaders/" + name)
		if err != nil {
			return "", fmt.Errorf("upscale preset %s: %w", p.ID, err)
		}
		buf.Write(b)
		buf.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(buf.String()))
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
	if err := os.WriteFile(tmp, []byte(buf.String()), 0o644); err != nil {
		return "", err
	}
	return path, os.Rename(tmp, path)
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
	path, err := p.shaderPath()
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

// Chain is the filter list that takes software frames in `inFmt`, upscales
// them on the Vulkan device named by -filter_hw_device, and returns software
// frames in outFmt. toVAAPI appends the upload a VA-API encoder needs;
// every other encoder accepts software frames directly.
//
// Vulkan frames can't be mapped back to VA-API or QSV surfaces on current
// Mesa (hwmap fails with ENOSYS), so this pays one system-memory round trip
// around the upscaler. It measured 4x realtime for 480p to 1080p on an Arc
// A380 with that round trip included.
func (sp Spec) Chain(inFmt, outFmt string, toVAAPI bool) ([]string, error) {
	f, err := sp.Filter(outFmt)
	if err != nil {
		return nil, err
	}
	c := []string{"format=" + inFmt, "hwupload", f, "hwdownload", "format=" + outFmt}
	if toVAAPI {
		c = append(c, "hwupload_vaapi")
	}
	return c, nil
}
