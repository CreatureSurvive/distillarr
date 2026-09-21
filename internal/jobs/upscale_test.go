package jobs

import "testing"

func TestUpscaleDest(t *testing.T) {
	for _, c := range []struct {
		src, container string
		class          int
		want           string
	}{
		{"/srv/media/movies/A (2007)/A (2007) WEBDL-480p.mp4", "mp4", 1080, "/srv/media/movies/A (2007)/A (2007) WEBDL-480p - 1080p upscale.mp4"},
		{"/m/B.mkv", "mkv", 2160, "/m/B - 4K upscale.mkv"},
		{"/m/C.avi", "mkv", 720, "/m/C - 720p upscale.mkv"}, // container changes with the encode, not the source name
	} {
		if got := upscaleDest(c.src, c.container, c.class); got != c.want {
			t.Errorf("upscaleDest(%q,%q,%d) = %q, want %q", c.src, c.container, c.class, got, c.want)
		}
	}
}
