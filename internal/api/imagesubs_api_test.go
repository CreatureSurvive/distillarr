// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"encoding/json"
	"testing"

	"github.com/CreatureSurvive/distillarr/internal/imagesubs"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

func TestSetImageSubsMode(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A",
		VideoCodec: "hevc", Container: "mkv", Width: 1920, Height: 1080, Duration: 100})

	rec := doJSON(t, s, "POST", "/api/v1/files/"+itoa64(f.ID)+"/image-subs-mode", map[string]any{"mode": "ocr"})
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	got, _ := s.st.GetFile(f.ID)
	if got.ImageSubsMode != "ocr" {
		t.Errorf("ImageSubsMode = %q, want ocr", got.ImageSubsMode)
	}

	rec = doJSON(t, s, "POST", "/api/v1/files/"+itoa64(f.ID)+"/image-subs-mode", map[string]any{"mode": "bogus"})
	if rec.Code != 400 {
		t.Fatalf("bogus mode: status = %d, want 400", rec.Code)
	}
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
}

func TestOCRFileQueuesJob(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/a.mkv", Library: "movies", Title: "A",
		VideoCodec: "hevc", Container: "mkv", Width: 1920, Height: 1080, Duration: 100,
		Subs: []store.SubStream{{Index: 8, Codec: imagesubs.CodecPGS, Lang: "eng", Default: true}}})

	rec := doJSON(t, s, "POST", "/api/v1/files/"+itoa64(f.ID)+"/ocr", map[string]any{})
	if rec.Code != 200 {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
	var job store.Job
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.Backend != "ocr" {
		t.Errorf("job.Backend = %q, want ocr", job.Backend)
	}
	if queued, _ := s.st.HasQueuedForFile(f.Path); !queued {
		t.Error("expected a queued job for the file")
	}
}

func TestOCRFileRejectsNoImageTrack(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/b.mkv", Library: "movies", Title: "B",
		VideoCodec: "hevc", Container: "mkv", Width: 1920, Height: 1080, Duration: 100})

	rec := doJSON(t, s, "POST", "/api/v1/files/"+itoa64(f.ID)+"/ocr", map[string]any{})
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400 (no image subtitle track)", rec.Code)
	}
}

func TestOCRFileRejectsVobSubPrimaryTrack(t *testing.T) {
	s := newTestServer(t)
	f := mustUpsert(t, s.st, &store.File{Path: "/m/c.mkv", Library: "movies", Title: "C",
		VideoCodec: "hevc", Container: "mkv", Width: 1920, Height: 1080, Duration: 100,
		Subs: []store.SubStream{{Index: 8, Codec: imagesubs.CodecVobSub, Lang: "eng", Default: true}}})

	rec := doJSON(t, s, "POST", "/api/v1/files/"+itoa64(f.ID)+"/ocr", map[string]any{})
	if rec.Code != 400 {
		t.Fatalf("status = %d, want 400 (VobSub OCR isn't supported)", rec.Code)
	}
}
