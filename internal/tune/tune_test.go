package tune

import (
	"context"
	"testing"

	"mediatrans/internal/encode"
)

func TestQualityCRFRoundTrip(t *testing.T) {
	for crf := 14; crf <= 32; crf++ {
		if got := encode.CRFForQuality(qualityForCRF(crf)); got != crf {
			t.Errorf("crf %d → quality %d → crf %d", crf, qualityForCRF(crf), got)
		}
	}
}

// A nonexistent path makes scene detection fail, so Spans falls back
// to fixedSpans - the case this test cared about before scene-aware
// placement existed.
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

// The whole point: a cut sitting near the target should pull the
// sample onto it, landing on real content instead of an arbitrary
// timestamp.
func TestNearestCutSnaps(t *testing.T) {
	dur, target := 3000.0, 652.5
	cuts := []float64{40, 650.2, 900}
	got, ok := nearestCut(cuts, target, dur, spanRadius(dur), nil)
	if !ok {
		t.Fatal("expected a snap")
	}
	if want := 650.2 + 0.5; got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

// No cuts close enough to the target: falls through to the caller's
// fixed-fraction position (nearestCut just reports ok=false).
func TestNearestCutNoneNearby(t *testing.T) {
	dur, target := 3000.0, 652.5
	if _, ok := nearestCut([]float64{5, 6, 7}, target, dur, spanRadius(dur), nil); ok {
		t.Error("expected no cut within range")
	}
}

// A cut close to an already-chosen sample must not be picked again -
// otherwise two samples could cover almost the same content.
func TestNearestCutAvoidsOverlap(t *testing.T) {
	dur, target := 3000.0, 652.5
	chosen := []float64{650.0} // an earlier sample already sits right here
	if _, ok := nearestCut([]float64{650.3}, target, dur, spanRadius(dur), chosen); ok {
		t.Error("cut inside an already-chosen sample's window must be excluded")
	}
}
