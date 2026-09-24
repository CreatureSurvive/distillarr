package notify

// EventType describes one event key for the settings UI.
type EventType struct {
	Key   string `json:"key"`
	Level string `json:"level"`
	Label string `json:"label"`
	Help  string `json:"help"`
	// DefaultOn is whether a new target starts with this event checked.
	DefaultOn bool `json:"default_on"`
}

// Event keys.
const (
	NightlySummary     = "nightly_summary"
	JobDone            = "job_done"
	JobFailed          = "job_failed"
	NeedsConfirmation  = "needs_confirmation"
	BackendDegraded    = "backend_degraded"
	DiskPressure       = "disk_pressure"
	ArrPathNotUpdated  = "arr_path_not_updated"
	UpgradeLoop        = "upgrade_loop"
	OCRLowConfidence   = "ocr_low_confidence"
	WebhookAuthFailed  = "webhook_auth_failed"
)

// EventTypes lists every event in display order.
var EventTypes = []EventType{
	{NightlySummary, Info, "Nightly summary", "After the day's last processing window closes (08:00 when no schedule is set): jobs done, space saved, failures, what's queued next.", true},
	{JobDone, Info, "Job finished", "Every finished encode, remux or upscale. Noisy; bursts are combined into one message.", false},
	{JobFailed, Error, "Job failed", "A job failed and has no retries left.", true},
	{NeedsConfirmation, Warning, "Needs confirmation", "New files are waiting for your go-ahead in the Queue page's intake panel.", true},
	{BackendDegraded, Warning, "Encoder degraded", "A hardware encoder failed twice within 30 minutes and is no longer used until you reset it.", true},
	{DiskPressure, Warning, "Low disk space", "A library's filesystem dropped to the free-space threshold.", true},
	{ArrPathNotUpdated, Warning, "Sonarr/Radarr didn't pick up a new path", "After a replace changed the file extension, the instance still reported the old path 10 minutes later.", true},
	{UpgradeLoop, Warning, "Upgrade loop", "Sonarr/Radarr replaced a file Distillarr re-encoded recently. The new file is skipped so the two don't keep undoing each other.", true},
	{OCRLowConfidence, Info, "Low-confidence OCR", "Image subtitle OCR scored below your threshold, so the track was left alone.", true},
	{WebhookAuthFailed, Warning, "Webhook auth failed", "A Sonarr/Radarr webhook call arrived with the wrong token.", true},
}

// LevelOf returns key's default level (info when unknown).
func LevelOf(key string) string {
	for _, t := range EventTypes {
		if t.Key == key {
			return t.Level
		}
	}
	return Info
}
