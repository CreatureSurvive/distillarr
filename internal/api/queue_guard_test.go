// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"errors"
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

func TestEnqueueGuards(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/tv/S/e1.mkv", Library: "tvshows", Title: "S", Size: 1 << 30})
	enc := encode.Settings{Codec: encode.HEVC, Backend: encode.SW, Quality: 60}

	j, err := s.enqueue(f, enc, false, "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.enqueue(f, enc, false, "manual", ""); !errors.Is(err, store.ErrActiveJob) {
		t.Fatalf("second active job: got %v", err)
	}
	// Retrying a failed copy while one is active must not slip past either.
	if err := s.st.CreateJob(&store.Job{FileID: f.ID, SrcPath: f.Path, Backend: "sw", MaxAttempts: 1}); !errors.Is(err, store.ErrActiveJob) {
		t.Fatalf("raw CreateJob: got %v", err)
	}

	_ = s.st.FinishJob(j.ID, store.StatusDone, 1<<29, "", "")
	if _, err := s.enqueue(f, enc, false, "manual", ""); !errors.Is(err, ErrAlreadyReencoded) {
		t.Fatalf("re-encode of our own output: got %v", err)
	}
	// A remux of the re-encoded file is still fine.
	remux := enc
	remux.VideoCopy = true
	if _, err := s.enqueue(f, remux, false, "issue-fix", "Quick fix"); err != nil {
		t.Fatalf("remux after re-encode: %v", err)
	}
}
