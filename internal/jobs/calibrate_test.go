package jobs

import (
	"path/filepath"
	"testing"

	"mediatrans/internal/config"
	"mediatrans/internal/scan"
	"mediatrans/internal/store"
	"mediatrans/internal/upscale"
)

func TestUpscaleCalibratedNeedsThreeSamples(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.NewManager(st)
	e := New(st, cfg, scan.New(st, cfg))
	key := upscale.NeuralKey("m")
	for i, v := range []float64{0.5, 0.7} {
		_ = st.RecordSpeedSample(e.upscaleSpeedKey(key), v)
		if _, _, ok := e.UpscaleCalibrated(key); ok {
			t.Fatalf("calibrated after %d samples", i+1)
		}
	}
	_ = st.RecordSpeedSample(e.upscaleSpeedKey(key), 0.6)
	if m, n, ok := e.UpscaleCalibrated(key); !ok || n != 3 || m != 0.6 {
		t.Errorf("got %v %d %v", m, n, ok)
	}
}
