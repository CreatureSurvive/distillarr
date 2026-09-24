// SPDX-License-Identifier: GPL-3.0-or-later

package still

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/media"
	"github.com/CreatureSurvive/distillarr/internal/upscale"
)

func req() Request {
	return Request{FileID: 7, Path: "/m/a.mkv", Size: 100, MtimeNS: 5, At: 30,
		Settings: encode.Settings{UpscaleTo: 1080, UpscalePreset: "film-lanczos"}}
}

// key mirrors what Make does: the key comes from the rendered filter chains.
func key(t *testing.T, r Request, at float64) string {
	t.Helper()
	r.Settings.Normalize()
	fa, fb, _, _, err := encode.StillFilters(r.Settings, &media.Stream{Width: 720, Height: 400})
	if err != nil {
		t.Fatal(err)
	}
	return keyFor(r, at, fa, fb)
}

func TestKeyTracksThePicture(t *testing.T) {
	upscale.ShaderDir = t.TempDir()
	base := key(t, req(), 30)
	if base != key(t, req(), 30) || len(base) != 16 {
		t.Fatalf("key must be stable and 16 hex chars: %q", base)
	}
	same := req()
	same.Settings.Quality, same.Settings.Codec, same.Settings.Container = 90, encode.AV1, "mkv"
	same.Settings.UpscaleParams = map[string]float64{"sigmoid": 1} // an explicit default is not a change
	if key(t, same, 30) != base {
		t.Error("quality, codec, container and default-valued params never change a still")
	}
	for name, mut := range map[string]func(*Request){
		"preset":     func(r *Request) { r.Settings.UpscalePreset = "anime-hq" },
		"param":      func(r *Request) { r.Settings.UpscaleParams = map[string]float64{"deband": 1} },
		"target":     func(r *Request) { r.Settings.UpscaleTo = 2160 },
		"gpu":        func(r *Request) { r.Settings.VulkanDevice = 1 },
		"crop":       func(r *Request) { r.Settings.Crop = "720:300:0:50" },
		"source":     func(r *Request) { r.MtimeNS = 6 },
		"source-len": func(r *Request) { r.Size = 101 },
	} {
		r := req()
		mut(&r)
		if key(t, r, 30) == base {
			t.Errorf("changing %s must change the key", name)
		}
	}
	if key(t, req(), 31) == base {
		t.Error("a different frame must change the key")
	}
}

// A bug fixed in the filter chain must not leave the old, wrong images being
// served from the cache: the key follows the chain itself.
func TestKeyFollowsTheChain(t *testing.T) {
	r := req()
	a, b := []string{"scale=1920:1066"}, []string{"format=nv12", "hwupload"}
	k := keyFor(r, 30, a, b)
	if keyFor(r, 30, a, append(append([]string{}, b...), "format=rgba")) == k {
		t.Error("changing the upscaled chain must change the key")
	}
	if keyFor(r, 30, []string{"scale=1920:1066:flags=bicubic"}, b) == k {
		t.Error("changing the baseline chain must change the key")
	}
}

func TestPathGuard(t *testing.T) {
	root := t.TempDir()
	key := "0123456789abcdef"
	os.MkdirAll(filepath.Join(root, key), 0o755)
	os.WriteFile(filepath.Join(root, key, "a.png"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "secret.png"), []byte("x"), 0o644)
	m := New(root, nil)

	if _, ok := m.Path(key, "a.png"); !ok {
		t.Error("a real still must be served")
	}
	if _, ok := m.Path(key, "b.png"); ok {
		t.Error("a missing file must 404")
	}
	for _, bad := range [][2]string{
		{"../" + key, "a.png"}, {key, "../secret.png"}, {key, "a.png/../../secret.png"},
		{"..", "a.png"}, {"0123456789ABCDEF", "a.png"}, {key + "0", "a.png"}, {key, ".a.tmp.png"},
	} {
		if p, ok := m.Path(bad[0], bad[1]); ok {
			t.Errorf("Path(%q, %q) must be refused, got %s", bad[0], bad[1], p)
		}
	}
}
