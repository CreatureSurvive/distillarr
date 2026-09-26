// SPDX-License-Identifier: GPL-3.0-or-later

package media

import "testing"

func TestStreamStatsTags(t *testing.T) {
	mkv := Stream{Tags: map[string]string{"BPS-eng": "7974105", "NUMBER_OF_BYTES-eng": "64522474"}}
	if got := mkv.BitRateInt(); got != 7974105 {
		t.Errorf("BitRateInt from BPS-eng = %d", got)
	}
	if got := mkv.Bytes(); got != 64522474 {
		t.Errorf("Bytes from NUMBER_OF_BYTES-eng = %d", got)
	}
	plain := Stream{BitRate: "128000", Tags: map[string]string{"BPS": "1"}}
	if got := plain.BitRateInt(); got != 128000 {
		t.Errorf("bit_rate should win over the tag, got %d", got)
	}
	none := Stream{Tags: map[string]string{"BPSX": "5"}}
	if none.BitRateInt() != 0 || none.Bytes() != 0 {
		t.Error("unrelated tag read as a statistic")
	}

	p := Probe{Streams: []Stream{
		{CodecType: "video", Tags: map[string]string{"BPS-eng": "7974105"}},
		{CodecType: "audio", Tags: map[string]string{"BPS-eng": "127997"}},
	}}
	p.Format.BitRate = "10070000"
	if got := p.VideoBitrate(); got != 7974105 {
		t.Errorf("VideoBitrate = %d, want the video track's own BPS", got)
	}
}
