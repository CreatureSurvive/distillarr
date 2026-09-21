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
