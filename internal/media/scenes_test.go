package media

import "testing"

func TestParseSceneTimes(t *testing.T) {
	// A trimmed real showinfo transcript: two detected cuts, one
	// duplicate line (ffmpeg can log a frame twice across filters).
	out := `[Parsed_showinfo_2 @ 0x1] n:0 pts:123 pts_time:5.130000 fmt:yuv420p
[Parsed_showinfo_2 @ 0x1] n:1 pts:456 pts_time:5.130000 fmt:yuv420p
[Parsed_showinfo_2 @ 0x1] n:2 pts:789 pts_time:612.500000 fmt:yuv420p
frame=  120 fps=0.0 q=-0.0 Lsize=N/A time=00:00:05.00 bitrate=N/A speed=  40x`
	got := parseSceneTimes(out)
	want := []float64{5.13, 612.5}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("got %v, want %v", got, want)
		}
	}
}

func TestParseSceneTimesNoCuts(t *testing.T) {
	if got := parseSceneTimes("frame=1 fps=0.0 speed=1x\n"); got != nil {
		t.Errorf("expected no cuts, got %v", got)
	}
}
