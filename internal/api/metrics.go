package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// metrics serves Prometheus text exposition format, written by
// hand to avoid the client library. Whether it needs an API key is
// decided by the metrics_public setting.
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	metric := func(name, typ, help string) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
	}
	sortedKeys := func(m map[string]int) []string {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		return keys
	}

	counts, _ := s.st.CountJobsByStatus()
	metric("distillarr_jobs", "gauge", "Jobs by status.")
	for _, k := range sortedKeys(counts) {
		fmt.Fprintf(&b, "distillarr_jobs{status=%q} %d\n", k, counts[k])
	}
	metric("distillarr_queue_depth", "gauge", "Jobs waiting to run.")
	fmt.Fprintf(&b, "distillarr_queue_depth %d\n", counts["queued"])

	saved, n, _ := s.st.RealizedSavings()
	metric("distillarr_saved_bytes_total", "counter", "Bytes saved by completed, unreverted jobs.")
	fmt.Fprintf(&b, "distillarr_saved_bytes_total %d\n", saved)
	metric("distillarr_saving_jobs_total", "counter", "Completed, unreverted jobs that saved space.")
	fmt.Fprintf(&b, "distillarr_saving_jobs_total %d\n", n)

	sec, _ := s.st.EncodeSeconds()
	metric("distillarr_encode_seconds_total", "counter", "Wall time of finished jobs.")
	fmt.Fprintf(&b, "distillarr_encode_seconds_total %.0f\n", sec)

	fps := s.eng.LastFPS()
	metric("distillarr_encode_fps", "gauge", "Most recent encode speed per backend.")
	backends := make([]string, 0, len(fps))
	for k := range fps {
		backends = append(backends, k)
	}
	sort.Strings(backends)
	for _, k := range backends {
		fmt.Fprintf(&b, "distillarr_encode_fps{backend=%q} %g\n", k, fps[k])
	}

	open := 0
	if s.cfg.WindowOpen(time.Now()) {
		open = 1
	}
	metric("distillarr_window_open", "gauge", "1 while the processing window is open.")
	fmt.Fprintf(&b, "distillarr_window_open %d\n", open)

	st := s.scan.Stats()
	running := 0
	if st.Running {
		running = 1
	}
	metric("distillarr_scan_running", "gauge", "1 while a library scan runs.")
	fmt.Fprintf(&b, "distillarr_scan_running %d\n", running)
	metric("distillarr_scan_files", "gauge", "Files seen by the current or last scan.")
	fmt.Fprintf(&b, "distillarr_scan_files %d\n", st.Seen)
	metric("distillarr_scan_probed", "gauge", "Files probed by the current or last scan.")
	fmt.Fprintf(&b, "distillarr_scan_probed %d\n", st.Probed)
	if !st.StartedAt.IsZero() && !st.EndedAt.IsZero() && st.EndedAt.After(st.StartedAt) {
		metric("distillarr_scan_duration_seconds", "gauge", "Duration of the last completed scan.")
		fmt.Fprintf(&b, "distillarr_scan_duration_seconds %.1f\n", st.EndedAt.Sub(st.StartedAt).Seconds())
	}

	intake, _ := s.st.CountIntakeByState()
	metric("distillarr_intake", "gauge", "Intake rows by state.")
	for _, k := range sortedKeys(intake) {
		fmt.Fprintf(&b, "distillarr_intake{state=%q} %d\n", k, intake[k])
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = w.Write([]byte(b.String()))
}
