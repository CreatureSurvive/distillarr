package upscale

import (
	"os"
	"strings"
	"testing"
)

func TestResolveClampsAndDefaults(t *testing.T) {
	p, _ := Get("film-lanczos")
	got := p.Resolve(map[string]float64{"deband": 7, "bogus": 1})
	if got["deband"] != 1 || got["sigmoid"] != 1 {
		t.Errorf("deband clamps to 1, sigmoid defaults to 1: %v", got)
	}
	if _, ok := got["bogus"]; ok {
		t.Error("unknown params must be dropped")
	}
}

func TestFilterBuiltinScaler(t *testing.T) {
	f, err := Spec{W: 1920, H: 1080, Preset: "film-lanczos-sharp", Params: map[string]float64{"deband": 1}}.Filter("nv12")
	if err != nil {
		t.Fatal(err)
	}
	want := "libplacebo=w=1920:h=1080:upscaler=ewa_lanczossharp:sigmoid=1:deband=1:format=nv12"
	if f != want {
		t.Errorf("got  %s\nwant %s", f, want)
	}
}

func TestFilterShaderPreset(t *testing.T) {
	ShaderDir = t.TempDir()
	f, err := Spec{W: 1920, H: 1080, Preset: "anime-fast"}.Filter("p010le")
	if err != nil {
		t.Fatal(err)
	}
	i := strings.Index(f, "custom_shader_path=")
	if i < 0 || !strings.HasSuffix(f, ":format=p010le") {
		t.Fatalf("anime preset needs a shader path and the output format: %s", f)
	}
	path := f[i+len("custom_shader_path="):strings.Index(f, ":format=")]
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("shader not materialised: %v", err)
	}
	// Mode A chain: highlights clamp, restore CNN, then the 2x upscale.
	s := string(b)
	a, r, u := strings.Index(s, "De-Ring-Clamp"), strings.Index(s, "Restore-CNN-(S)"), strings.Index(s, "Upscale-CNN-x2-(S)")
	if a < 0 || r < 0 || u < 0 || !(a < r && r < u) {
		t.Errorf("passes missing or out of order: clamp=%d restore=%d upscale=%d", a, r, u)
	}
	// A second call must reuse the same file (content-hash naming).
	f2, _ := Spec{W: 1280, H: 720, Preset: "anime-fast"}.Filter("nv12")
	if !strings.Contains(f2, path) {
		t.Error("identical shader content should map to the same file")
	}
}

func TestChain(t *testing.T) {
	ShaderDir = t.TempDir()
	c, err := Spec{W: 1920, H: 1080, Preset: "film-lanczos"}.Chain("nv12", "nv12", true)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(c, ",")
	// libplacebo must render RGB (its YUV readback is corrupt through Vulkan)
	// and the CPU converts to BT.709 YUV, which also tags the frames bt709.
	if !strings.HasPrefix(got, "format=nv12,hwupload,libplacebo=") ||
		!strings.Contains(got, ":format=rgba,hwdownload,format=rgba,scale=out_color_matrix=bt709:out_range=tv,format=nv12") ||
		!strings.HasSuffix(got, ",hwupload_vaapi") {
		t.Errorf("chain shape wrong: %s", got)
	}
	if strings.Contains(got, "libplacebo=") && strings.Contains(strings.SplitN(got, "hwdownload", 2)[0], "format=nv12:") {
		t.Errorf("libplacebo must not output a YUV format: %s", got)
	}

	c, _ = Spec{W: 3840, H: 2160, Preset: "film-lanczos"}.Chain("p010le", "p010le", false)
	got = strings.Join(c, ",")
	if strings.Contains(got, "hwupload_vaapi") {
		t.Error("hwupload_vaapi belongs only on the VA-API path")
	}
	if !strings.Contains(got, ":format=x2bgr10le,hwdownload,format=x2bgr10le,scale=out_color_matrix=bt709:out_range=tv,format=p010le") {
		t.Errorf("10-bit must keep 10-bit RGB through the round trip: %s", got)
	}
}

func TestRGBChainEndsInRGB(t *testing.T) {
	c, err := Spec{W: 1920, H: 1080, Preset: "film-lanczos"}.RGBChain("nv12", "rgba")
	if err != nil {
		t.Fatal(err)
	}
	if last := c[len(c)-1]; last != "format=rgba" || c[len(c)-2] != "hwdownload" {
		t.Errorf("RGBChain must end hwdownload,format=rgba: %v", c)
	}
}

func TestFilterErrors(t *testing.T) {
	if _, err := (Spec{W: 1920, H: 1080, Preset: "nope"}).Filter("nv12"); err == nil {
		t.Error("unknown preset must error")
	}
	if _, err := (Spec{W: 0, H: 1080, Preset: "film-lanczos"}).Filter("nv12"); err == nil {
		t.Error("zero-size target must error")
	}
	ShaderDir = "/tmp/has space"
	if _, err := (Spec{W: 1920, H: 1080, Preset: "anime-fast"}).Filter("nv12"); err == nil {
		t.Error("a shader path needing filter escaping must be refused")
	}
}

func TestFSRSharpnessParam(t *testing.T) {
	ShaderDir = t.TempDir()
	read := func(params map[string]float64) string {
		f, err := Spec{W: 1920, H: 1080, Preset: "fsr", Params: params}.Filter("rgba")
		if err != nil {
			t.Fatal(err)
		}
		i := strings.Index(f, "custom_shader_path=") + len("custom_shader_path=")
		b, err := os.ReadFile(f[i:strings.Index(f[i:], ":")+i])
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	def, max, min := read(nil), read(map[string]float64{"sharpness": 2}), read(map[string]float64{"sharpness": 0})
	// The slider is inverted: the shader counts stops of REDUCTION, so a higher
	// slider must mean a LOWER define. The default (1.8) is the shader's own 0.2.
	for _, c := range []struct{ name, src, want string }{
		{"default", def, "#define SHARPNESS 0.20"},
		{"max", max, "#define SHARPNESS 0.00"},
		{"min", min, "#define SHARPNESS 2.00"},
	} {
		if !strings.Contains(c.src, c.want) {
			t.Errorf("%s: want %q in the shader", c.name, c.want)
		}
	}
	// Only that one line may differ: the rest is AMD's shader, untouched.
	strip := func(s string) string { return sharpnessRe.ReplaceAllString(s, "") }
	if strip(def) != strip(max) || strip(def) != strip(min) {
		t.Error("tuning must change nothing but the SHARPNESS define")
	}
	if !strings.Contains(def, "FidelityFX Super Resolution v1.0.2 (EASU)") || !strings.Contains(def, "(RCAS)") {
		t.Error("both FSR passes must be present")
	}
}

func TestShaderCacheIsBounded(t *testing.T) {
	ShaderDir = t.TempDir()
	for i := 0; i < 90; i++ { // a slider drag: one distinct file per value
		if _, err := (Spec{W: 1920, H: 1080, Preset: "fsr", Params: map[string]float64{"sharpness": float64(i) / 45}}).Filter("rgba"); err != nil {
			t.Fatal(err)
		}
	}
	if ents, _ := os.ReadDir(ShaderDir); len(ents) > 62 {
		t.Errorf("shader cache grew to %d files", len(ents))
	}
}

func TestNeuralRegistry(t *testing.T) {
	a, ok := Get("neural-anime")
	if !ok || !a.Neural() || a.Model() != "realesr-animevideov3" {
		t.Fatalf("neural-anime: %+v %v", a, ok)
	}
	// Smallest native scale that reaches the target, else the largest.
	for _, c := range []struct{ src, dst, want int }{
		{720, 1920, 3}, {854, 1920, 3}, {1280, 1920, 2}, {1920, 3840, 2}, {320, 1920, 4}, {160, 3840, 4},
	} {
		if got := a.PickScale(c.src, c.dst); got != c.want {
			t.Errorf("PickScale(%d→%d) = %d, want %d", c.src, c.dst, got, c.want)
		}
	}
	if h, _ := Get("neural-anime-hq"); h.PickScale(720, 1920) != 4 {
		t.Error("a single-scale model always uses its scale")
	}
	// Measured 2.4 fps at 720x400: a 24-minute episode is about 4 hours.
	if hours := a.EstimateSeconds(24*60*24, 720, 400) / 3600; hours < 3.5 || hours > 4.5 {
		t.Errorf("24-minute episode estimated %.1fh, measured ~4h", hours)
	}
	// Cost scales with input area: 1080p input is 7.2x the pixels of 720x400.
	if r := a.EstimateSeconds(100, 1920, 1080) / a.EstimateSeconds(100, 720, 400); r < 7 || r > 7.4 {
		t.Errorf("cost should scale with pixels, ratio %.2f", r)
	}
	// Neural presets are not filtergraph presets.
	if _, err := (Spec{W: 1920, H: 1080, Preset: "neural-anime"}).Filter("rgba"); err == nil {
		t.Error("a neural preset has no libplacebo filter")
	}
	for _, p := range All() {
		if p.Neural() != (p.SPF > 0) {
			t.Errorf("%s: only neural presets carry a per-frame cost", p.ID)
		}
	}
}

func TestNeuralAvailable(t *testing.T) {
	old := NeuralDir
	defer func() { NeuralDir = old }()
	NeuralDir = t.TempDir()
	if NeuralAvailable() {
		t.Error("an empty directory is not an install")
	}
	os.WriteFile(NeuralBin(), []byte("x"), 0o755)
	if NeuralAvailable() {
		t.Error("the binary without models/ is not usable")
	}
	os.Mkdir(NeuralDir+"/models", 0o755)
	if !NeuralAvailable() {
		t.Error("binary plus models/ is an install")
	}
}
