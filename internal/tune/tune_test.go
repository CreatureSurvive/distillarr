package tune

import (
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

func TestSpans(t *testing.T) {
	s := Spans(6000)
	if len(s) != 3 || s[0] >= s[1] || s[1] >= s[2] || s[2]+SampleLen > 6000 {
		t.Errorf("spans %v", s)
	}
	if s := Spans(20); s[len(s)-1]+SampleLen > 20.01 || s[0] < 0 {
		t.Errorf("short file spans must stay inside: %v", s)
	}
}
