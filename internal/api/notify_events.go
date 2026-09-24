package api

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"mediatrans/internal/arr"
	"mediatrans/internal/config"
	"mediatrans/internal/jobs"
	"mediatrans/internal/notify"
	"mediatrans/internal/store"
)

// notifyEventTypes lists the event keys for the settings UI.
func (s *Server) notifyEventTypes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, notify.EventTypes)
}

// JobDoneSubscriber sends job_done after every successful replace/copy.
func (s *Server) JobDoneSubscriber() func(jobs.ReplacedEvent) {
	return func(ev jobs.ReplacedEvent) {
		body := ""
		if ev.SizeBefore > 0 && ev.SizeAfter > 0 {
			body = fmt.Sprintf("%s → %s", humanBytes(ev.SizeBefore), humanBytes(ev.SizeAfter))
		}
		s.Notify.Send(notify.Event{Key: notify.JobDone, Title: "Finished: " + filepath.Base(ev.NewPath),
			Body: body, Link: fmt.Sprintf("#/file/%d", ev.FileID), Group: "jobs finished"})
	}
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// ---- upgrade loop ----

func upgradeLoopKey(fileID int64) string { return "upgrade_loop_skip:" + strconv.FormatInt(fileID, 10) }

// checkUpgradeLoop runs on a Download webhook that's an upgrade: if any
// file it replaced (or the same path, re-downloaded in place) was
// re-encoded by Distillarr within UpgradeLoopDays, skip the new file,
// tag it with the skip tag when this instance has tag write-back on, and
// notify. Returns whether a loop was found.
func (s *Server) checkUpgradeLoop(inst config.ArrInstance, newFile *store.File, replacedLocal []string, now time.Time) bool {
	cfg := s.cfg.Get()
	cutoff := now.Add(-time.Duration(cfg.UpgradeLoopDaysOn()) * 24 * time.Hour).UTC().Format(time.RFC3339)
	var candidates []int64
	for _, p := range replacedLocal {
		if f, err := s.st.GetFileByPath(p); err == nil && f != nil {
			candidates = append(candidates, f.ID)
		}
	}
	candidates = append(candidates, newFile.ID) // same path re-downloaded keeps its row
	found := false
	for _, id := range candidates {
		if at, err := s.st.LastDoneJobAt(id); err == nil && at != "" && at >= cutoff {
			found = true
			break
		}
	}
	if !found {
		return false
	}
	_ = s.st.KVSet(upgradeLoopKey(newFile.ID), now.UTC().Format(time.RFC3339))
	tagged := ""
	if inst.TagAfterReencodeOn() {
		if item, err := s.st.ArrItemByFileID(newFile.ID); err == nil && item != nil {
			skipTag, _, _, _ := inst.TagNames()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := tagArrItem(ctx, arr.New(inst.URL, inst.APIKey, arrKind(inst.Kind)), arrKind(inst.Kind), item.ItemID, skipTag); err != nil {
				log.Printf("upgrade loop: tag %s: %v", skipTag, err)
			} else {
				tagged = fmt.Sprintf(" Tagged %q in %s.", skipTag, inst.Name)
			}
			cancel()
		}
	}
	s.Notify.Send(notify.Event{Key: notify.UpgradeLoop, Level: notify.Warning,
		Title: "Upgrade loop: " + filepath.Base(newFile.Path),
		Body: fmt.Sprintf("%s replaced a file Distillarr re-encoded in the last %d days. The new file is skipped.%s If %s keeps grabbing releases, check its quality profile and custom formats against re-encoded files (see the FAQ entry \"Will Sonarr/Radarr re-download files Distillarr re-encodes?\").",
			inst.Name, cfg.UpgradeLoopDaysOn(), tagged, inst.Name),
		Link: fmt.Sprintf("#/file/%d", newFile.ID)})
	s.scan.RefreshRecsSoon()
	return true
}

// clearUpgradeLoop removes a file's upgrade-loop skip (the file page's
// button). Any skip tag already written to Sonarr/Radarr stays; remove it
// there.
func (s *Server) clearUpgradeLoop(w http.ResponseWriter, r *http.Request) {
	if err := s.st.KVDelete(upgradeLoopKey(pathID(r))); err != nil {
		fail(w, 500, err)
		return
	}
	s.scan.RefreshRecsSoon()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// ---- nightly summary ----

// summaryDue reports whether now is when the day's summary goes out: the
// minute the last processing window of the day closes, or 08:00 when the
// schedules never close (none set).
func summaryDue(scheds []config.Schedule, now time.Time) bool {
	now = now.Truncate(time.Minute)
	if !config.InSchedules(scheds, now.Add(-time.Minute)) || config.InSchedules(scheds, now) {
		if len(scheds) == 0 || alwaysOpenToday(scheds, now) {
			return now.Hour() == 8 && now.Minute() == 0
		}
		return false
	}
	midnight := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
	for t := now.Add(time.Minute); t.Before(midnight); t = t.Add(time.Minute) {
		if config.InSchedules(scheds, t) {
			return false // opens again later today
		}
	}
	return true
}

func alwaysOpenToday(scheds []config.Schedule, now time.Time) bool {
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	for t := day; t.Day() == day.Day(); t = t.Add(15 * time.Minute) {
		if !config.InSchedules(scheds, t) {
			return false
		}
	}
	return true
}

// buildSummary formats the nightly summary; nil when there's nothing to say.
func buildSummary(js store.JobSummary, queued int) *notify.Event {
	if js.Done == 0 && js.Failed == 0 && queued == 0 {
		return nil
	}
	var parts []string
	parts = append(parts, fmt.Sprintf("%d job(s) done", js.Done))
	if js.Saved > 0 {
		parts = append(parts, humanBytes(js.Saved)+" saved")
	}
	if js.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", js.Failed))
	}
	body := strings.Join(parts, ", ") + "."
	if queued > 0 {
		body += fmt.Sprintf(" %d waiting in the queue for the next window.", queued)
	}
	return &notify.Event{Key: notify.NightlySummary, Title: "Distillarr daily summary", Body: body, Link: "#/queue"}
}

// SummaryLoop checks once a minute whether the daily summary is due.
func (s *Server) SummaryLoop(stop <-chan struct{}) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			if !summaryDue(s.cfg.Get().Schedules, now) {
				continue
			}
			since, ok, _ := s.st.KVGet("notify_summary_at")
			if !ok {
				since = now.Add(-24 * time.Hour).UTC().Format(time.RFC3339)
			}
			js, err := s.st.JobSummarySince(since)
			if err != nil {
				log.Printf("notify: summary: %v", err)
				continue
			}
			counts, _ := s.st.CountJobsByStatus()
			if ev := buildSummary(js, counts[store.StatusQueued]); ev != nil {
				s.Notify.Send(*ev)
			}
			_ = s.st.KVSet("notify_summary_at", now.UTC().Format(time.RFC3339))
		}
	}
}
