// SPDX-License-Identifier: GPL-3.0-or-later

package api

import (
	"fmt"
	"log"
	"syscall"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/config"
	"github.com/CreatureSurvive/distillarr/internal/notify"
)

// statFreePct is overridable in tests so a fake filesystem's free-space
// percentage can be injected without a real disk (mirrors the
// arrWebhookReprobe pattern in arr_webhook.go).
var statFreePct = func(path string) float64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil || st.Blocks == 0 {
		return -1
	}
	return float64(st.Bavail) / float64(st.Blocks) * 100
}

// diskPressureFor reports whether cfg is under disk-pressure mode
// DiskPressurePct is set, and at least one library's own
// filesystem (not a hardcoded path — each Library.Path is checked
// directly) is at or below it. api.system's own disk-usage display
// still hardcodes /srv/media; that's a separate, pre-existing thing
// left for "remove host defaults" pass, which already plans a
// per-library rework there.
func diskPressureFor(cfg config.Config) bool {
	if cfg.DiskPressurePct <= 0 {
		return false
	}
	for _, lib := range cfg.Libraries {
		if pct := statFreePct(lib.Path); pct >= 0 && pct <= cfg.DiskPressurePct {
			return true
		}
	}
	return false
}

func (s *Server) diskPressureNow() bool {
	return diskPressureFor(s.cfg.Get())
}

// DiskPressureLoop polls free space every 5 minutes — free space
// doesn't move fast enough to need tighter polling — and broadcasts
// SSE "system" {"pressure":...} only when the state actually changes.
// It checks once immediately so a threshold that's already crossed at
// boot doesn't wait for the first tick. It never opens a processing
// window itself; that stays entirely config.WindowOpen's call.
func (s *Server) DiskPressureLoop(stop <-chan struct{}) {
	check := func() {
		now := s.diskPressureNow()
		if now == s.diskPressure.Load() {
			return
		}
		s.diskPressure.Store(now)
		s.hub.Broadcast("system", map[string]any{"pressure": now})
		if now {
			s.Notify.Send(notify.Event{Key: notify.DiskPressure, Level: notify.Warning, Title: "Low disk space",
				Body: fmt.Sprintf("A library's filesystem is at or below %.0f%% free. Autopilot now favours the biggest savings.", s.cfg.Get().DiskPressurePct),
				Link: "#/queue"})
		}
		log.Printf("disk pressure: %v", now)
	}
	check()
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case <-t.C:
			check()
		}
	}
}
