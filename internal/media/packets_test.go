// SPDX-License-Identifier: GPL-3.0-or-later

package media

import (
	"context"
	"testing"
)

// A nonexistent path makes ffprobe fail cleanly.
func TestPacketSizesMissingFile(t *testing.T) {
	if _, err := PacketSizes(context.Background(), "/no/such/file"); err == nil {
		t.Error("expected an error for a missing file")
	}
}

// ffprobe returns packets in decode order, not presentation order:
// B-frames make the two differ. A real capture opens 0.0, 0.167,
// 0.083, 0.042... - if parsePackets didn't sort, any time-based
// caller would silently misbehave.
func TestParsePacketsSortsByPTS(t *testing.T) {
	raw := []byte(`{"packets":[
		{"pts_time":"0.000000","size":"1048","flags":"K__"},
		{"pts_time":"0.167000","size":"58","flags":"___"},
		{"pts_time":"0.083000","size":"55","flags":"___"},
		{"pts_time":"0.042000","size":"60","flags":"___"},
		{"pts_time":"0.125000","size":"52","flags":"___"}
	]}`)
	got, err := parsePackets(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 {
		t.Fatalf("expected 5 packets, got %d", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].PTS < got[i-1].PTS {
			t.Fatalf("not sorted by pts: %+v", got)
		}
	}
	if !got[0].Key || got[0].Size != 1048 {
		t.Errorf("first packet by pts should be the keyframe at 0.0: %+v", got[0])
	}
	if got[1].Key {
		t.Errorf("second packet by pts (0.042) should not be flagged a keyframe: %+v", got[1])
	}
}

// A packet ffprobe couldn't timestamp ("N/A") is dropped, not a
// crash - malformed input degrades gracefully.
func TestParsePacketsSkipsUnparseable(t *testing.T) {
	raw := []byte(`{"packets":[
		{"pts_time":"N/A","size":"10","flags":"___"},
		{"pts_time":"1.0","size":"20","flags":"K__"}
	]}`)
	got, err := parsePackets(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].PTS != 1.0 {
		t.Errorf("expected exactly the one parseable packet: %+v", got)
	}
}
