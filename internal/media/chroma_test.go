package media

import "testing"

func TestMeanChroma(t *testing.T) {
	out := "frame:0\nlavfi.signalstats.YAVG=90\nlavfi.signalstats.UAVG=120\nlavfi.signalstats.VAVG=130\n" +
		"frame:1\nlavfi.signalstats.UAVG=124\nlavfi.signalstats.VAVG=134\n"
	u, v, ok := MeanChroma(out)
	if !ok || u != 122 || v != 132 {
		t.Errorf("want 122/132, got %v/%v %v", u, v, ok)
	}
	if _, _, ok := MeanChroma("Error opening input\n"); ok {
		t.Error("output with no stats must not parse")
	}
	if _, _, ok := MeanChroma("lavfi.signalstats.UAVG=120\n"); ok {
		t.Error("stats missing V must not parse")
	}
}
