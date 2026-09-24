// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestForcesTranscode(t *testing.T) {
	st, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	f := &File{Path: "/movies/A.mkv", Library: "movies"}
	if err := st.UpsertFile(f, nil); err != nil {
		t.Fatal(err)
	}
	other := &File{Path: "/movies/B.mkv", Library: "movies"}
	if err := st.UpsertFile(other, nil); err != nil {
		t.Fatal(err)
	}

	if got, _ := st.ForcesTranscode(f.ID, 30); got {
		t.Error("no events yet: must not force transcode")
	}

	// 10 past the previous hour: always in the past, and now+5min stays
	// in the same hour whatever the wall clock says.
	now := time.Now().Add(-time.Hour).Truncate(time.Hour).Add(10 * time.Minute)
	if err := st.RecordPlaybackEvent(f.ID, "jellyfin", now, false, []string{"video_codec", "audio_codec"}); err != nil {
		t.Fatal(err)
	}
	// A second poll within the same hour must not add a second row.
	if err := st.RecordPlaybackEvent(f.ID, "jellyfin", now.Add(5*time.Minute), false, []string{"video_codec"}); err != nil {
		t.Fatal(err)
	}
	// A direct-play observation for the other file must not count.
	if err := st.RecordPlaybackEvent(other.ID, "jellyfin", now, true, nil); err != nil {
		t.Fatal(err)
	}

	if got, _ := st.ForcesTranscode(f.ID, 30); !got {
		t.Error("non-direct session in window: must force transcode")
	}
	if got, _ := st.ForcesTranscode(other.ID, 30); got {
		t.Error("direct-play only: must not force transcode")
	}
	if got, _ := st.ForcesTranscode(f.ID, 0); got {
		t.Error("days=0 window excludes everything: must not force transcode")
	}

	ids, err := st.ForcesTranscodeFileIDs(30)
	if err != nil {
		t.Fatal(err)
	}
	if !ids[f.ID] || ids[other.ID] {
		t.Errorf("wrong file id set: %v", ids)
	}

	detail, err := st.ForcesTranscodeDetail(f.ID, 30)
	if err != nil {
		t.Fatal(err)
	}
	if detail == nil {
		t.Fatal("expected a detail record")
	}
	if detail.Servers["jellyfin"] != 1 {
		t.Errorf("expected the same-hour poll to dedup to one row, got servers=%v", detail.Servers)
	}
	if detail.Reasons["video_codec"] != 1 {
		t.Errorf("expected the later poll's reasons to win, got %v", detail.Reasons)
	}
	if detail.Reasons["audio_codec"] != 0 {
		t.Errorf("first poll's reasons must be replaced, not merged: %v", detail.Reasons)
	}

	if detail, _ := st.ForcesTranscodeDetail(other.ID, 30); detail != nil {
		t.Errorf("direct-play only file must have no detail: %+v", detail)
	}
}
