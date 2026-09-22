// Package upscale defines the super-resolution presets and builds the
// libplacebo (Vulkan) filter fragment that runs them. It is a leaf package
// (it imports nothing from the app) so encode.Build can call it without an
// import cycle; recommending a preset for a file lives with the code that
// already knows about files and libraries.
package upscale

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// Tiers. The shader tier runs inside the one ffmpeg command; the neural
// tier is a separate chunked runner and is not built by encode.Build.
const (
	TierShader = "shader"
	TierNeural = "neural"
)

// Content classes a preset is designed for.
const (
	Film  = "film"  // live action
	Anime = "anime" // animation / cel art
	Any   = "any"   // content-agnostic
)

// DefaultPreset is used when a job asks for an upscale without naming one.
const DefaultPreset = "film-lanczos"

// Param is one tunable, exposed to the UI as a slider or toggle.
type Param struct {
	Key   string  `json:"key"`
	Label string  `json:"label"`
	Min   float64 `json:"min"`
	Max   float64 `json:"max"`
	Step  float64 `json:"step"`
	Def   float64 `json:"def"`
}

// Preset is one upscaling method.
type Preset struct {
	ID      string  `json:"id"`
	Label   string  `json:"label"`
	Desc    string  `json:"desc"`
	Tier    string  `json:"tier"`
	Content string  `json:"content"` // film | anime | any
	Params  []Param `json:"params"`
	// SPF is a neural preset's measured seconds per frame at RefPixels of input
	// (0 for shader presets, which run at encode speed). Clients scale it by the
	// file's own size to show a time estimate before anything is queued.
	SPF float64 `json:"sec_per_frame,omitempty"`

	scaler  string   // libplacebo upscaler for the residual scale
	shaders []string // embedded .glsl files, concatenated in order
	// opt are extra shader files a preset adds when its param is on. They are
	// put BEFORE the base shaders: RAVU keeps its weight tables at the end of its
	// file, and libplacebo reads a table until a blank line or end of input.
	opt    []optShader
	model  string // neural: Real-ESRGAN model name
	scales []int  // neural: the model's native scale factors
	// tune rewrites the shader source for the resolved params (nil = as shipped),
	// for shaders whose tunables are #defines rather than uniforms.
	tune func(src string, v map[string]float64) string
}

// optShader is a shader file enabled by a boolean param.
type optShader struct{ param, file string }

// Params shared by every shader-tier preset.
var commonParams = []Param{
	{Key: "sigmoid", Label: "Sigmoid light", Min: 0, Max: 1, Step: 1, Def: 1},
	{Key: "deband", Label: "Deband", Min: 0, Max: 1, Step: 1, Def: 0},
}

// refineParam turns on SSimSuperRes after a prescaler: it removes the ringing and
// line bloat a sharp scaler leaves, the usual mpv recipe (FSRCNNX/RAVU + SSim).
var refineParam = Param{Key: "ssim", Label: "SSimSuperRes refine (less ringing)", Min: 0, Max: 1, Step: 1, Def: 0}

var refineOpt = []optShader{{param: "ssim", file: "SSimSuperRes.glsl"}}

// sharpnessRe matches the strength #define in FSR (RCAS) and NIS.
var sharpnessRe = regexp.MustCompile(`(?m)^#define SHARPNESS [0-9.]+`)

// fsrTune sets RCAS sharpening. The shader's SHARPNESS counts stops of
// *reduction* (0 = strongest), so the slider is inverted: higher = sharper.
// nisTune sets NIS sharpening (0..1, higher = sharper: no inversion needed).
func nisTune(src string, v map[string]float64) string {
	return sharpnessRe.ReplaceAllString(src, fmt.Sprintf("#define SHARPNESS %.2f", v["sharpness"]))
}

func fsrTune(src string, v map[string]float64) string {
	return sharpnessRe.ReplaceAllString(src, fmt.Sprintf("#define SHARPNESS %.2f", 2-v["sharpness"]))
}

// RefPixels is the input size SPF was measured at (720x400, a 480p film).
const RefPixels = 720 * 400

// MaxNeuralHours is the longest estimated run the API will queue. Beyond it the
// job would monopolise the GPU for days; use a shader preset instead.
const MaxNeuralHours = 48

// NeuralDir holds the realesrgan-ncnn-vulkan binary and its models/ (see the
// Dockerfile). A variable so tests can point it elsewhere.
var NeuralDir = "/opt/realesrgan"

// NeuralBin is the ncnn upscaler executable.
func NeuralBin() string { return filepath.Join(NeuralDir, "realesrgan-ncnn-vulkan") }

// NeuralAvailable reports whether the neural tier is installed.
func NeuralAvailable() bool {
	if fi, err := os.Stat(NeuralBin()); err != nil || fi.IsDir() {
		return false
	}
	fi, err := os.Stat(filepath.Join(NeuralDir, "models"))
	return err == nil && fi.IsDir()
}

// Neural reports whether the preset runs on the neural tier.
func (p Preset) Neural() bool { return p.Tier == TierNeural }

// Model is the Real-ESRGAN model a neural preset runs.
func (p Preset) Model() string { return p.model }

// PickScale chooses the model's native scale for taking srcW to targetW: the
// smallest one that reaches it (the result is then resized down to the exact
// size), or the largest available when none does.
func (p Preset) PickScale(srcW, targetW int) int {
	best := 0
	for _, s := range p.scales {
		best = s
		if srcW*s >= targetW {
			return s
		}
	}
	return best
}

// EstimateSeconds is the expected run time for frames of w×h input.
func (p Preset) EstimateSeconds(frames float64, w, h int) float64 {
	return frames * p.SPF * float64(w*h) / RefPixels
}

var presets = []Preset{
	{
		ID: "film-lanczos", Label: "Lanczos (EWA)", Tier: TierShader, Content: Film,
		Desc:   "Elliptical-weighted Lanczos: the clean, artefact-free baseline for live action.",
		Params: commonParams, scaler: "ewa_lanczos",
	},
	{
		ID: "film-lanczos-sharp", Label: "Lanczos Sharp (EWA)", Tier: TierShader, Content: Film,
		Desc:   "Lanczos with a sharper kernel: crisper edges, slightly more ringing on hard contrast.",
		Params: commonParams, scaler: "ewa_lanczossharp",
	},
	{
		ID: "film-ginseng", Label: "Ginseng (EWA)", Tier: TierShader, Content: Film,
		Desc:   "Softer, very low-ringing kernel: best for grainy or heavily compressed sources.",
		Params: commonParams, scaler: "ewa_ginseng",
	},
	{
		ID: "fsr", Label: "FSR 1.0 (EASU + RCAS)", Tier: TierShader, Content: Any,
		Desc:   "AMD FidelityFX Super Resolution: edge-directed upscale plus contrast-adaptive sharpening. Crisp on any content, cheap to run.",
		Params: append([]Param{{Key: "sharpness", Label: "Sharpening", Min: 0, Max: 2, Step: 0.1, Def: 1.8}}, commonParams...),
		scaler: "ewa_lanczos", shaders: []string{"FSR.glsl"}, tune: fsrTune,
	},
	{
		ID: "film-jinc", Label: "Jinc (EWA)", Tier: TierShader, Content: Film,
		Desc:   "Jinc-windowed elliptical scaler: avoids diagonal stair-stepping. Between Lanczos and Ginseng in sharpness.",
		Params: commonParams, scaler: "ewa_jinc",
	},
	{
		ID: "film-spline64", Label: "Spline64", Tier: TierShader, Content: Film,
		Desc:   "Separable spline: the safe, universally predictable choice. Sharper than bicubic, little ringing.",
		Params: commonParams, scaler: "spline64",
	},
	{
		ID: "ravu-lite", Label: "RAVU Lite (r3)", Tier: TierShader, Content: Film,
		Desc:   "Edge-directed prescaler with trained filter weights: sharp, natural edges without the painterly look of heavy networks. ~3x realtime to 1080p.",
		Params: append(append([]Param{}, commonParams...), refineParam), scaler: "ewa_lanczos",
		shaders: []string{"ravu-lite-r3.glsl"}, opt: refineOpt,
	},
	{
		ID: "fsrcnnx-fast", Label: "FSRCNNX 8-0-4-1", Tier: TierShader, Content: Film,
		Desc:   "A small convolutional network trained on live-action video: natural detail recovery. ~3x realtime to 1080p.",
		Params: append(append([]Param{}, commonParams...), refineParam), scaler: "ewa_lanczos",
		shaders: []string{"FSRCNNX_x2_8-0-4-1.glsl"}, opt: refineOpt,
	},
	{
		ID: "fsrcnnx-hq", Label: "FSRCNNX 16-0-4-1", Tier: TierShader, Content: Film,
		Desc:   "The larger FSRCNNX network: a little more detail, about 25% slower (~2.5x realtime to 1080p).",
		Params: append(append([]Param{}, commonParams...), refineParam), scaler: "ewa_lanczos",
		shaders: []string{"FSRCNNX_x2_16-0-4-1.glsl"}, opt: refineOpt,
	},
	{
		ID: "nis", Label: "NVIDIA Image Scaling", Tier: TierShader, Content: Any,
		Desc:   "NVIDIA's directional scaler and sharpener, a cross-vendor alternative to FSR. Very cheap; noticeably sharper than Lanczos.",
		Params: append([]Param{{Key: "sharpness", Label: "Sharpening", Min: 0, Max: 1, Step: 0.05, Def: 0.25}}, commonParams...),
		scaler: "ewa_lanczos", shaders: []string{"NVScaler.glsl"}, tune: nisTune,
	},
	{
		ID: "anime-fast", Label: "Anime4K Fast", Tier: TierShader, Content: Anime,
		Desc:   "Anime4K Mode A, small CNNs: restores line art, then doubles. Real-time on any Vulkan GPU.",
		Params: commonParams, scaler: "ewa_lanczos",
		shaders: []string{"Anime4K_Clamp_Highlights.glsl", "Anime4K_Restore_CNN_S.glsl", "Anime4K_Upscale_CNN_x2_S.glsl"},
	},
	{
		ID: "anime-hq", Label: "Anime4K HQ", Tier: TierShader, Content: Anime,
		Desc:   "Anime4K Mode A, medium CNNs: cleaner lines and less blocking, roughly 20% slower.",
		Params: commonParams, scaler: "ewa_lanczos",
		shaders: []string{"Anime4K_Clamp_Highlights.glsl", "Anime4K_Restore_CNN_M.glsl", "Anime4K_Upscale_CNN_x2_M.glsl"},
	},
	{
		ID: "neural-anime", Label: "Real-ESRGAN Anime Video", Tier: TierNeural, Content: Anime,
		Desc: "Neural super-resolution trained on anime video. Far better line art and texture than any shader, but only ~2.4 fps on an Arc A380 (about 4 hours per 24-minute episode): an overnight job.",
		SPF:  0.40, model: "realesr-animevideov3", scales: []int{2, 3, 4},
	},
	{
		ID: "neural-anime-hq", Label: "Real-ESRGAN Anime (heavy)", Tier: TierNeural, Content: Anime,
		Desc: "The large anime model: the best detail this app can produce, at ~0.24 fps. Practical for short clips only.",
		SPF:  4.2, model: "realesrgan-x4plus-anime", scales: []int{4},
	},
	{
		ID: "neural-general", Label: "Real-ESRGAN General", Tier: TierNeural, Content: Film,
		Desc: "The general model, for live action and photographic content: strong detail recovery but ~0.06 fps. Practical for stills and very short clips.",
		SPF:  15, model: "realesrgan-x4plus", scales: []int{4},
	},
}

// All returns every preset in registry order (film first, then anime).
func All() []Preset {
	out := append([]Preset(nil), presets...)
	for i := range out {
		if out[i].Params == nil {
			out[i].Params = []Param{} // JSON [] not null: clients index into it
		}
	}
	return out
}

// Get looks a preset up by id.
func Get(id string) (Preset, bool) {
	for _, p := range presets {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}

// ForContent returns the default preset for a content class.
func ForContent(content string) Preset {
	id := DefaultPreset
	if content == Anime {
		id = "anime-fast"
	}
	p, _ := Get(id)
	return p
}

// Resolve fills defaults for params the caller didn't set and clamps every
// value to its range, dropping keys the preset doesn't define.
func (p Preset) Resolve(in map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(p.Params))
	for _, d := range p.Params {
		v, ok := in[d.Key]
		if !ok {
			v = d.Def
		}
		out[d.Key] = min(max(v, d.Min), d.Max)
	}
	return out
}
