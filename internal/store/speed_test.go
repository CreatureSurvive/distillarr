// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"path/filepath"
	"testing"
)

func TestSpeedMedianNoHistory(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if _, _, ok := st.SpeedMedian(SpeedKey("qsv", "Arc A380", "hevc", 1080)); ok {
		t.Error("no samples recorded yet: ok must be false")
	}
}

func TestSpeedMedianOddAndEvenCounts(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := SpeedKey("qsv", "Arc A380", "hevc", 1080)

	for _, v := range []float64{2, 4, 6} { // odd count: median is the middle value
		if err := st.RecordSpeedSample(key, v); err != nil {
			t.Fatal(err)
		}
	}
	if med, n, ok := st.SpeedMedian(key); !ok || med != 4 || n != 3 {
		t.Fatalf("median = %v, n = %d, ok = %v; want 4, 3, true", med, n, ok)
	}

	if err := st.RecordSpeedSample(key, 100); err != nil { // even count: average the middle two
		t.Fatal(err)
	}
	if med, n, ok := st.SpeedMedian(key); !ok || med != 5 || n != 4 {
		t.Fatalf("median = %v, n = %d, ok = %v; want 5 ((4+6)/2), 4, true", med, n, ok)
	}
}

func TestSpeedMedianIgnoresOutliersViaMedianNotMean(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := SpeedKey("qsv", "Arc A380", "av1", 2160)

	for _, v := range []float64{1, 1, 1, 1, 1000} { // one wild outlier
		if err := st.RecordSpeedSample(key, v); err != nil {
			t.Fatal(err)
		}
	}
	if med, _, ok := st.SpeedMedian(key); !ok || med != 1 {
		t.Fatalf("median = %v, want 1 (median, not mean, resists the outlier)", med)
	}
}

func TestSpeedSamplesCapAtRollingWindow(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := SpeedKey("vaapi", "UHD 630", "hevc", 720)

	// Fill well past the cap with an ascending sequence, then confirm the
	// median reflects only the most recent window, not the whole history.
	for i := 1; i <= speedSampleCap+10; i++ {
		if err := st.RecordSpeedSample(key, float64(i)); err != nil {
			t.Fatal(err)
		}
	}
	med, n, ok := st.SpeedMedian(key)
	if !ok || n != speedSampleCap {
		t.Fatalf("n = %d, ok = %v; want exactly the cap (%d)", n, ok, speedSampleCap)
	}
	// The most recent speedSampleCap values are (10+1)..(cap+10); median
	// of that ascending run is its midpoint.
	wantMed := float64(11+speedSampleCap+10) / 2
	if med != wantMed {
		t.Fatalf("median = %v, want %v (old samples must have rolled off)", med, wantMed)
	}
}

func TestSpeedKeyDistinguishesDevicesNotJustBackend(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	uhd := SpeedKey("qsv", "UHD 630", "hevc", 1080)
	arc := SpeedKey("qsv", "Arc A380", "hevc", 1080)
	if uhd == arc {
		t.Fatal("two different devices on the same backend must not share a key")
	}
	if err := st.RecordSpeedSample(uhd, 2); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordSpeedSample(arc, 8); err != nil {
		t.Fatal(err)
	}
	if med, _, _ := st.SpeedMedian(uhd); med != 2 {
		t.Errorf("uhd median = %v, want 2 (unaffected by the arc sample)", med)
	}
	if med, _, _ := st.SpeedMedian(arc); med != 8 {
		t.Errorf("arc median = %v, want 8", med)
	}
}

func TestRecordSpeedSampleIgnoresNonPositive(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	key := SpeedKey("sw", "CPU (software)", "hevc", 480)

	if err := st.RecordSpeedSample(key, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordSpeedSample(key, -5); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := st.SpeedMedian(key); ok {
		t.Error("zero/negative speed samples must never be recorded")
	}
}
