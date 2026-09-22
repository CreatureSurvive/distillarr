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
