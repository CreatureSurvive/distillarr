// SPDX-License-Identifier: GPL-3.0-or-later

package res

import "testing"

func TestClass(t *testing.T) {
	cases := []struct{ w, h, want int }{
		{1920, 1080, 1080}, {1920, 802, 1080}, {1920, 800, 1080}, {1440, 1080, 1080}, {1916, 796, 1080},
		{3840, 2160, 2160}, {3840, 1600, 2160}, {1280, 720, 720}, {1280, 534, 720},
		{720, 576, 576}, {720, 480, 480}, {640, 480, 480}, {854, 356, 480},
	}
	for _, c := range cases {
		if got := Class(c.w, c.h); got != c.want {
			t.Errorf("Class(%d×%d) = %d, want %d", c.w, c.h, got, c.want)
		}
	}
}

func TestFit(t *testing.T) {
	if w, h, ok := Fit(3840, 1600, 1080); !ok || w != 1920 || h != 800 {
		t.Errorf("scope 4K → 1080p should be 1920×800, got %d×%d %v", w, h, ok)
	}
	if w, h, ok := Fit(3840, 2160, 720); !ok || w != 1280 || h != 720 {
		t.Errorf("4K → 720p got %d×%d", w, h)
	}
	if _, _, ok := Fit(1920, 802, 1080); ok {
		t.Error("1920×802 is already 1080p; no downscale")
	}
	if w, h, ok := Fit(1920, 802, 720); !ok || w != 1280 || h != 534 {
		t.Errorf("1920×802 → 720p got %d×%d", w, h)
	}
}

func TestUp(t *testing.T) {
	cases := []struct {
		w, h, target int
		ow, oh       int
		ok           bool
	}{
		{854, 480, 1080, 1920, 1080, true},
		{854, 480, 720, 1280, 720, true},
		{1920, 1080, 2160, 3840, 2160, true},
		{1920, 800, 2160, 3840, 1600, true}, // scope film keeps its aspect ratio
		{640, 480, 1080, 1440, 1080, true},  // 4:3 pillarboxes inside the frame
		{1920, 1080, 1080, 1920, 1080, false},
		{1920, 802, 1080, 1920, 802, false}, // 1920×802 is already 1080p
		{3840, 2160, 1080, 3840, 2160, false},
		{854, 480, 0, 854, 480, false},
	}
	for _, c := range cases {
		w, h, ok := Up(c.w, c.h, c.target)
		if ok != c.ok || w != c.ow || h != c.oh {
			t.Errorf("Up(%d×%d, %d) = %d×%d %v, want %d×%d %v", c.w, c.h, c.target, w, h, ok, c.ow, c.oh, c.ok)
		}
	}
}
