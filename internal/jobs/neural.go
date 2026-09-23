package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mediatrans/internal/encode"
	"mediatrans/internal/media"
	"mediatrans/internal/neural"
	"mediatrans/internal/store"
)

// NeuralRoot holds each neural job's chunks between runs. It sits on the config
// volume: chunks must survive a container restart, that being the point.
var NeuralRoot = func() string {
	if v := os.Getenv("DISTILLARR_UPSCALE_WORK"); v != "" {
		return v
	}
	if v := os.Getenv("MEDIIATRANS_UPSCALE_WORK"); v != "" {
		return v
	}
	return "/config/upscale"
}()

func neuralWorkDir(id int64) string { return filepath.Join(NeuralRoot, fmt.Sprintf("job-%d", id)) }

// isNeuralJob reports whether a stored job runs on the neural tier.
func isNeuralJob(j *store.Job) bool {
	return strings.Contains(j.SettingsJSON, `"upscale_tier":"neural"`)
}

// neuralCmd describes a neural job for the job page, which shows a command:
// there isn't one ffmpeg line to show.
func neuralCmd(p *encode.NeuralPlan, s encode.Settings) string {
	return fmt.Sprintf("realesrgan-ncnn-vulkan -n %s -s %d -g %d over %d chunks of %d frames (resized to %dx%d), then ffmpeg joins the chunks with the source's audio and subtitles",
		p.Model, p.Scale, s.VulkanDevice, p.Chunks(), p.ChunkFrames, p.OutW, p.OutH)
}

// runNeural runs the chunked neural upscale for a job, reporting progress the
// way an encode does. A job started inside its window stops (neural.ErrPaused)
// when the window closes; one claimed outside it was started by "Upscale now"
// and runs to completion.
func (e *Engine) runNeural(ctx context.Context, j *store.Job, s encode.Settings, src *media.Probe, plan *encode.NeuralPlan, out string) error {
	forced := !e.neuralOpen.Load()
	dur := src.DurationSec()
	began := time.Now()
	first := -1.0
	var lastPct float64
	var lastNote string
	lastPersist := time.Now().Add(-time.Hour)

	// A clean shutdown must pause the job with its chunks, not cancel it.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-e.stopCh:
			if c, ok := e.cancels.Load(j.ID); ok {
				c.(context.CancelFunc)()
			}
		case <-stop:
		}
	}()

	return neural.Run(ctx, neural.Job{
		Src: src, Settings: s, Plan: plan, Work: neuralWorkDir(j.ID), Out: out, GPU: s.VulkanDevice,
		Acquire: e.AcquireSem,
		Keep:    func() bool { return forced || e.neuralOpen.Load() },
		Progress: func(done float64, note string) {
			if first < 0 {
				first = done
			}
			eta := 0.0
			if done > first {
				eta = time.Since(began).Seconds() * (1 - done) / (done - first)
			}
			if done >= lastPct+0.002 || done >= 1 {
				lastPct = done
				e.notify(EvProgress, map[string]any{"job_id": j.ID, "pct": done, "fps": 0, "speed": 0, "eta_sec": eta, "size": 0})
			}
			if note != lastNote {
				lastNote = note
				e.notify(EvJob, map[string]any{"id": j.ID, "status": store.StatusRunning,
					"note": fmt.Sprintf("neural upscale: %s (%.0f%%)", note, done*100)})
			}
			if time.Since(lastPersist) > 5*time.Second {
				lastPersist = time.Now()
				pj, _ := json.Marshal(encode.Progress{OutSec: done * dur, DurSec: dur})
				_ = e.st.SetJobProgress(j.ID, string(pj))
			}
		},
	})
}

// sweepNeuralWork removes the chunks of jobs that will never resume (finished,
// failed, canceled, or gone): they hold gigabytes and nothing else deletes them.
func (e *Engine) sweepNeuralWork() {
	ents, err := os.ReadDir(NeuralRoot)
	if err != nil {
		return
	}
	for _, d := range ents {
		id, err := strconv.ParseInt(strings.TrimPrefix(d.Name(), "job-"), 10, 64)
		if err != nil || !strings.HasPrefix(d.Name(), "job-") {
			continue
		}
		j, err := e.st.GetJob(id)
		if err != nil {
			continue
		}
		switch {
		case j == nil, j.Status == store.StatusDone, j.Status == store.StatusFailed, j.Status == store.StatusCanceled:
			log.Printf("jobs: removing stale neural work for job %d", id)
			neural.Cleanup(filepath.Join(NeuralRoot, d.Name()))
		}
	}
}
