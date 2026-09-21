// Package upscale defines the super-resolution presets and builds the
// libplacebo (Vulkan) filter fragment that runs them. It is a leaf package
// (it imports nothing from the app) so encode.Build can call it without an
// import cycle; recommending a preset for a file lives with the code that
// already knows about files and libraries.
package upscale

import (
	"fmt"
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

	scaler  string   // libplacebo upscaler for the residual scale
	shaders []string // embedded .glsl files, concatenated in order
	// tune rewrites the shader source for the resolved params (nil = as shipped),
	// for shaders whose tunables are #defines rather than uniforms.
	tune func(src string, v map[string]float64) string
}

// Params shared by every shader-tier preset.
var commonParams = []Param{
	{Key: "sigmoid", Label: "Sigmoid light", Min: 0, Max: 1, Step: 1, Def: 1},
	{Key: "deband", Label: "Deband", Min: 0, Max: 1, Step: 1, Def: 0},
}

// sharpnessRe matches FSR's RCAS strength define (see fsrTune).
var sharpnessRe = regexp.MustCompile(`(?m)^#define SHARPNESS [0-9.]+`)

// fsrTune sets RCAS sharpening. The shader's SHARPNESS counts stops of
// *reduction* (0 = strongest), so the slider is inverted: higher = sharper.
func fsrTune(src string, v map[string]float64) string {
	return sharpnessRe.ReplaceAllString(src, fmt.Sprintf("#define SHARPNESS %.2f", 2-v["sharpness"]))
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
		Desc: "AMD FidelityFX Super Resolution: edge-directed upscale plus contrast-adaptive sharpening. Crisp on any content, cheap to run.",
		Params: append([]Param{{Key: "sharpness", Label: "Sharpening", Min: 0, Max: 2, Step: 0.1, Def: 1.8}}, commonParams...),
		scaler: "ewa_lanczos", shaders: []string{"FSR.glsl"}, tune: fsrTune,
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
}

// All returns every preset in registry order (film first, then anime).
func All() []Preset { return append([]Preset(nil), presets...) }

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
