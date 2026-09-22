package tune

import (
	"context"
	"testing"

	"mediatrans/internal/encode"
	"mediatrans/internal/media"
)

func TestCRFStep(t *testing.T) {
	cases := []struct {
		gap  float64
		want int
	}{
		{0, 1},        // exactly on target: must still move, toward more quality
		{4, 2},        // 4 VMAF below target -> 2 CRF lower (more quality)
		{-4, -2},      // 4 VMAF above target -> 2 CRF higher (more compression)
		{0.9, 1},      // rounds to the nearest whole CRF, never zero
		{-0.9, -1},
		{100, 4},      // clamped: a huge gap never leaps more than 4
		{-100, -4},
	}
	for _, c := range cases {
		if got := crfStep(c.gap); got != c.want {
			t.Errorf("crfStep(%v) = %d, want %d", c.gap, got, c.want)
		}
	}
	// The step must always move crf in the right direction: below
	// target -> lower crf (crf - step < crf); above -> higher.
	if step := crfStep(5); step <= 0 {
		t.Errorf("below target must lower crf: step=%d", step)
	}
	if step := crfStep(-5); step >= 0 {
		t.Errorf("above target must raise crf: step=%d", step)
	}
}

func TestQualityCRFRoundTrip(t *testing.T) {
	for crf := 14; crf <= 32; crf++ {
		if got := encode.CRFForQuality(qualityForCRF(crf)); got != crf {
			t.Errorf("crf %d → quality %d → crf %d", crf, qualityForCRF(crf), got)
		}
	}
}

// A nonexistent path makes the packet-index read fail, so Spans falls
// back to fixedSpans.
func TestSpansFallback(t *testing.T) {
	s := Spans(context.Background(), "/no/such/file", 6000)
	if len(s) != 3 || s[0] >= s[1] || s[1] >= s[2] || s[2]+SampleLen > 6000 {
		t.Errorf("spans %v", s)
	}
	if s := Spans(context.Background(), "/no/such/file", 20); s[len(s)-1]+SampleLen > 20.01 || s[0] < 0 {
		t.Errorf("short file spans must stay inside: %v", s)
	}
}

func TestFixedSpans(t *testing.T) {
	s := fixedSpans(6000)
	if len(s) != 3 || s[0] >= s[1] || s[1] >= s[2] || s[2]+SampleLen > 6000 {
		t.Errorf("spans %v", s)
	}
	if s := fixedSpans(20); s[len(s)-1]+SampleLen > 20.01 || s[0] < 0 {
		t.Errorf("short file spans must stay inside: %v", s)
	}
}

// The whole point: each slice should pick the window closest to the
// file's own median byte density, skipping anything below the
// near-black floor - not just the first or the biggest.
func TestWindowScanPicksNearMedianPerSlice(t *testing.T) {
	dur := 300.0 // 3 equal slices of 100s; sampleLen=15, so isolated
	// keyframes 30s apart never sum into each other's window.
	key := func(pts float64, size int64) media.Packet { return media.Packet{PTS: pts, Size: size, Key: true} }
	pkts := []media.Packet{
		// slice 0 [0,100): global bytes 10 (below the floor), 50, 90
		key(10, 10), key(40, 50), key(70, 90),
		// slice 1 [100,200): global bytes 20 (== the floor, kept), 60, 80
		key(110, 20), key(140, 60), key(170, 80),
		// slice 2 [200,300): global bytes 30, 40, 70
		key(210, 30), key(240, 40), key(270, 70),
	}
	// Global sorted bytes: 10,20,30,40,50,60,70,80,90 -> floor (20%) = 20, median = 50.
	got := windowScan(pkts, dur)
	want := []float64{40, 140, 240} // closest to 50 in each slice, excluding the sub-floor 10
	if len(got) != 3 {
		t.Fatalf("expected 3 spans, got %v", got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("slice %d: got %v, want %v (full: %v)", i, got[i], w, got)
		}
	}
}

// A slice with no keyframe at all leaves nothing to choose: the whole
// scan gives up rather than mixing a real pick with a guess.
func TestWindowScanEmptySliceFails(t *testing.T) {
	dur := 300.0
	key := func(pts float64, size int64) media.Packet { return media.Packet{PTS: pts, Size: size, Key: true} }
	pkts := []media.Packet{
		key(10, 100), key(40, 200), // slice 0 only
		key(210, 100), key(240, 200), // slice 2 only - nothing in slice 1
	}
	if got := windowScan(pkts, dur); got != nil {
		t.Errorf("expected nil (fall back to fixed spans), got %v", got)
	}
}

// A non-keyframe packet is never a candidate window start, even if its
// byte size would otherwise win - seeking there would make ffmpeg
// decode backward to the real keyframe.
func TestWindowScanIgnoresNonKeyframes(t *testing.T) {
	dur := 300.0
	pkts := []media.Packet{
		{PTS: 10, Size: 999999, Key: false}, // huge, but not a keyframe
		{PTS: 40, Size: 50, Key: true},
		{PTS: 110, Size: 50, Key: true},
		{PTS: 210, Size: 50, Key: true},
	}
	got := windowScan(pkts, dur)
	if len(got) != 3 || got[0] != 40 {
		t.Errorf("must skip the non-keyframe candidate: %v", got)
	}
}
