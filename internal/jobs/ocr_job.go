package jobs

import (
	"context"
	"fmt"
	"log"
	"path/filepath"

	"github.com/CreatureSurvive/distillarr/internal/imagesubs"
	"github.com/CreatureSurvive/distillarr/internal/notify"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// OCRTessdataDir is where per-language tesseract traineddata files are
// cached, downloaded on first use. On the config volume so a
// language downloaded once survives a container restart.
const OCRTessdataDir = "/config/tessdata"

// runOCRJob is dedicated job kind (Backend == "ocr"): unlike
// every other job, it never touches the video file at all — no
// probe/tune/verify/replace — it only reads the source's image
// subtitle track, OCRs it, and (on a confident result) writes an SRT
// sidecar next to it. The CPU-heavy OCR step is gated by its own
// semaphore key, not a GPU backend, so it never competes with encode
// jobs for a hardware encoder slot.
func (e *Engine) runOCRJob(ctx context.Context, j *store.Job) {
	f, err := e.st.GetFile(j.FileID)
	if err != nil || f == nil {
		e.fail(j, "file not found", "")
		return
	}
	track, ok := imagesubs.PrimaryTrack(f.Subs, "")
	if !ok {
		e.fail(j, "no image subtitle track", "")
		return
	}

	e.st.UpdateJobStatus(j.ID, store.StatusRunning, "")
	release := e.AcquireSem("ocr")
	res, err := imagesubs.RunOCR(ctx, j.SrcPath, track, e.cfg.Get().OCRMinConfidenceOn(), OCRTessdataDir)
	release()
	if err != nil {
		e.fail(j, err.Error(), "")
		return
	}

	if err := e.st.SetOCRResult(f.ID, imagesubs.MarshalResult(res)); err != nil {
		log.Printf("jobs: ocr job %d: caching result: %v", j.ID, err)
	}
	if res.Failed {
		Alert(notify.Event{Key: notify.OCRLowConfidence, Title: "Low-confidence OCR: " + filepath.Base(j.SrcPath),
			Body: fmt.Sprintf("Confidence %.0f, below your threshold; the image subtitle track was left as is.", res.Confidence),
			Link: fmt.Sprintf("#/file/%d", f.ID), Group: "files with low-confidence OCR"})
	}
	if !res.Failed && res.SRTName != "" {
		sidecarPath := filepath.Join(filepath.Dir(j.SrcPath), res.SRTName)
		if err := e.st.SetJobSidecars(j.ID, []string{sidecarPath}); err != nil {
			log.Printf("jobs: ocr job %d: recording sidecar: %v", j.ID, err)
		}
	}

	e.st.FinishJob(j.ID, store.StatusDone, 0, "", "")
	e.notify(EvJob, map[string]any{"id": j.ID, "status": store.StatusDone})
	if e.scan != nil {
		_ = e.scan.ProbeSingle(j.SrcPath)
	}
}
