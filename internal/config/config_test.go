// SPDX-License-Identifier: GPL-3.0-or-later

package config

import "testing"

func TestJellyfinPathMapBothWays(t *testing.T) {
	c := Config{JellyfinPathMap: "/data=/srv/media"} // Jellyfin mounts /srv/media as /data
	for _, tc := range []struct{ jf, local string }{
		{"/data/movies/A (2001)/A.mp4", "/srv/media/movies/A (2001)/A.mp4"},
		{"/data", "/srv/media"},
	} {
		if got := c.MapJellyfinPath(tc.jf); got != tc.local {
			t.Errorf("Jellyfin->local %q = %q, want %q", tc.jf, got, tc.local)
		}
		if got := c.ToJellyfinPath(tc.local); got != tc.jf {
			t.Errorf("local->Jellyfin %q = %q, want %q", tc.local, got, tc.jf)
		}
	}
	// A path outside the mapped tree, and a sibling that merely shares a prefix, are left alone.
	for _, p := range []string{"/mnt/other/x.mp4", "/srv/media2/x.mp4"} {
		if got := c.ToJellyfinPath(p); got != p {
			t.Errorf("%q is not under the mapped root, got %q", p, got)
		}
	}
	if got := (Config{}).ToJellyfinPath("/srv/media/x.mp4"); got != "/srv/media/x.mp4" {
		t.Errorf("no mapping configured means no rewrite, got %q", got)
	}
}

func TestEffectiveSidecarMode(t *testing.T) {
	c := Config{SubsSidecarMode: "extract_keep"}
	if got := c.EffectiveSidecarMode("", ""); got != "extract_keep" {
		t.Errorf("no overrides: got %q, want global extract_keep", got)
	}
	if got := c.EffectiveSidecarMode("", "extract_remove"); got != "extract_remove" {
		t.Errorf("rule override should win over global: got %q", got)
	}
	if got := c.EffectiveSidecarMode("off", "extract_remove"); got != "off" {
		t.Errorf("file override should win over both rule and global: got %q", got)
	}
	if got := (Config{}).EffectiveSidecarMode("", ""); got != "off" {
		t.Errorf("nothing set anywhere: got %q, want off", got)
	}
}

func TestEffectiveImageSubsMode(t *testing.T) {
	c := Config{ImageSubsMode: "sidecar"}
	if got := c.EffectiveImageSubsMode("", ""); got != "sidecar" {
		t.Errorf("no overrides: got %q, want global sidecar", got)
	}
	if got := c.EffectiveImageSubsMode("", "ocr"); got != "ocr" {
		t.Errorf("rule override should win over global: got %q", got)
	}
	if got := c.EffectiveImageSubsMode("ocr", "sidecar"); got != "ocr" {
		t.Errorf("file override should win over both rule and global: got %q", got)
	}
	if got := (Config{}).EffectiveImageSubsMode("", ""); got != "keep" {
		t.Errorf("nothing set anywhere: got %q, want keep", got)
	}
}

func TestImageSubsKeepOriginalOnDefault(t *testing.T) {
	if !(Config{}).ImageSubsKeepOriginalOn() {
		t.Error("default should be on (the safer choice)")
	}
	off := false
	if (Config{ImageSubsKeepOriginal: &off}).ImageSubsKeepOriginalOn() {
		t.Error("explicit false should be respected")
	}
}

func TestOCRMinConfidenceOnDefault(t *testing.T) {
	if got := (Config{}).OCRMinConfidenceOn(); got != 80 {
		t.Errorf("default = %v, want 80", got)
	}
	v := 65.0
	if got := (Config{OCRMinConfidence: &v}).OCRMinConfidenceOn(); got != 65 {
		t.Errorf("explicit value = %v, want 65", got)
	}
}

func TestPlexPathMapBothWays(t *testing.T) {
	c := Config{PlexPathMap: "/media=/srv/media"} // Plex mounts /srv/media as /media
	for _, tc := range []struct{ plex, local string }{
		{"/media/movies/A (2001)/A.mkv", "/srv/media/movies/A (2001)/A.mkv"},
		{"/media", "/srv/media"},
	} {
		if got := c.MapPlexPath(tc.plex); got != tc.local {
			t.Errorf("Plex->local %q = %q, want %q", tc.plex, got, tc.local)
		}
		if got := c.ToPlexPath(tc.local); got != tc.plex {
			t.Errorf("local->Plex %q = %q, want %q", tc.local, got, tc.plex)
		}
	}
	if got := (Config{}).ToPlexPath("/srv/media/x.mkv"); got != "/srv/media/x.mkv" {
		t.Errorf("no mapping configured means no rewrite, got %q", got)
	}
}

func TestPressureBudgetXDefault(t *testing.T) {
	if got := (Config{}).PressureBudgetX(); got != 2 {
		t.Errorf("default PressureBudgetX() = %v, want 2", got)
	}
	x := 3.5
	c := Config{DiskPressureBudgetX: &x}
	if got := c.PressureBudgetX(); got != 3.5 {
		t.Errorf("PressureBudgetX() = %v, want 3.5", got)
	}
}
