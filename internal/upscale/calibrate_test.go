package upscale

import "testing"

func TestEstimateUsesCalibration(t *testing.T) {
	var p Preset
	for _, x := range All() {
		if x.Neural() {
			p = x
			break
		}
	}
	if p.ID == "" {
		t.Skip("no neural preset")
	}
	ref := p.EstimateSeconds(1000, 720, 400)
	if want := 1000 * p.SPF; ref != want {
		t.Fatalf("reference estimate %v, want %v", ref, want)
	}
	old := Calibrated
	t.Cleanup(func() { Calibrated = old })
	Calibrated = func(key string) (float64, int, bool) {
		if key == NeuralKey(p.Model()) {
			return p.SPF / 2, 5, true
		}
		return 0, 0, false
	}
	if got := p.EstimateSeconds(1000, 720, 400); got != ref/2 {
		t.Errorf("calibrated estimate %v, want %v", got, ref/2)
	}
	if _, measured := p.SPFOn(); !measured {
		t.Error("SPFOn should report measured")
	}
}
