package api

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"mediatrans/internal/config"
	"mediatrans/internal/pathmap"
)

// arrWebhookEnvelope covers the fields shared or event-specific across
// Sonarr/Radarr's Download, Rename, {Episode,Movie}FileDelete and Test
// webhooks. Field names are taken from the Sonarr wiki's documented
// examples and the ArrAPI client but were NOT captured from a live payload — that needs the user's
// go-ahead to connect the webhook for real. episodeFile/movieFile are
// left as json.RawMessage because their shape differs by event (Download
// carries id/relativePath/path/quality; Rename carries path/previousPath).
type arrWebhookEnvelope struct {
	EventType           string           `json:"eventType"`
	IsUpgrade           bool             `json:"isUpgrade,omitempty"`
	EpisodeFile         json.RawMessage  `json:"episodeFile,omitempty"`
	MovieFile           json.RawMessage  `json:"movieFile,omitempty"`
	RenamedEpisodeFiles []arrRenameEntry `json:"renamedEpisodeFiles,omitempty"`
	DeleteReason        string           `json:"deleteReason,omitempty"`
}

type arrWebhookFile struct {
	ID           int64  `json:"id"`
	RelativePath string `json:"relativePath"`
	Path         string `json:"path"`
}

type arrRenameEntry struct {
	Path         string `json:"path"`
	PreviousPath string `json:"previousPath"`
}

// arrWebhookReprobe does the real (ffprobe-backed) work of noticing a
// Download event's file; overridable in tests so they don't need a real,
// ffprobe-able file on disk to exercise the rest of the handler.
var arrWebhookReprobe = func(s *Server, path string) error { return s.scan.ProbeSingle(path) }

// arrWebhookAuthFails is an in-memory, per-instance failed-auth counter
// for the Settings card — reset on restart, which is fine: it's a
// "something's misconfigured right now" signal, not an audit log.
var arrWebhookAuthFails = struct {
	m map[string]int
}{m: map[string]int{}}

// POST /api/v1/hooks/arr/{instanceID} — HTTP Basic, any username,
// password = that instance's webhook_token. Kept outside whatever
// app-wide auth applies (that middleware must exclude this
// prefix): this route authenticates itself, per-instance, already.
func (s *Server) arrWebhook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("instanceID")
	inst := s.arrInstanceByID(id)
	if inst == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	_, pass, ok := r.BasicAuth()
	if !ok || inst.WebhookToken == "" || subtle.ConstantTimeCompare([]byte(pass), []byte(inst.WebhookToken)) != 1 {
		arrWebhookAuthFails.m[id]++
		log.Printf("arr webhook %s: auth failed (%d total)", id, arrWebhookAuthFails.m[id])
		w.Header().Set("WWW-Authenticate", `Basic realm="distillarr"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		fail(w, 400, err)
		return
	}
	var ev arrWebhookEnvelope
	if err := json.Unmarshal(body, &ev); err != nil {
		fail(w, 400, err)
		return
	}

	_ = s.st.KVSet("arr_webhook_last_"+id, time.Now().UTC().Format(time.RFC3339))
	s.hub.Broadcast("arr", map[string]any{"instance": id, "webhook": ev.EventType})

	switch ev.EventType {
	case "Test":
		// any 2xx is success; nothing else to do
	case "Download":
		s.handleArrWebhookDownload(*inst, ev)
	case "Rename":
		s.handleArrWebhookRename(*inst, ev)
	case "EpisodeFileDelete", "MovieFileDelete":
		s.handleArrWebhookDelete(*inst, ev)
	default:
		// unhandled events (Grab, Health, ManualInteractionRequired, …): 200, ignore
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func decodeWebhookFile(raw json.RawMessage) (*arrWebhookFile, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("no file in payload")
	}
	var f arrWebhookFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, err
	}
	if f.Path == "" {
		return nil, fmt.Errorf("no path in payload")
	}
	return &f, nil
}

// handleArrWebhookDownload maps the imported file to a local path,
// re-probes it, and — only if this instance opted into webhook intake —
// opens a settling intake row instead of leaving it to the next full
// sync/scan to notice.
func (s *Server) handleArrWebhookDownload(inst config.ArrInstance, ev arrWebhookEnvelope) {
	raw := ev.EpisodeFile
	if raw == nil {
		raw = ev.MovieFile
	}
	wf, err := decodeWebhookFile(raw)
	if err != nil {
		log.Printf("arr webhook %s: Download: %v", inst.ID, err)
		return
	}
	pm, _ := pathmap.Parse(inst.PathMap)
	local := pm.ToLocal(wf.Path)
	if err := arrWebhookReprobe(s, local); err != nil {
		log.Printf("arr webhook %s: probe %s: %v", inst.ID, local, err)
		return
	}
	if !inst.WebhookIntakeOn() {
		return
	}
	f, err := s.st.GetFileByPath(local)
	if err != nil || f == nil {
		log.Printf("arr webhook %s: Download: no file row for %s after probing", inst.ID, local)
		return
	}
	reason := "Imported by " + inst.Name
	if ev.IsUpgrade {
		reason += " (upgrade)"
	}
	settle := time.Duration(inst.WebhookSettleMinutesOn()) * time.Minute
	if _, err := s.st.UpsertIntake(f.ID, "webhook", reason, "", time.Now().Add(settle)); err != nil {
		log.Printf("arr webhook %s: intake for %s: %v", inst.ID, local, err)
	}
}

// handleArrWebhookRename re-keys files/jellyfin rows so joins on path
// don't go stale until the next full sync. Sonarr sends every renamed
// episode file in one payload; Radarr's Rename webhook carries a single
// movieFile with previousPath/path instead of an array.
func (s *Server) handleArrWebhookRename(inst config.ArrInstance, ev arrWebhookEnvelope) {
	pm, _ := pathmap.Parse(inst.PathMap)
	entries := ev.RenamedEpisodeFiles
	if len(entries) == 0 && ev.MovieFile != nil {
		var one arrRenameEntry
		if err := json.Unmarshal(ev.MovieFile, &one); err == nil && one.Path != "" {
			entries = []arrRenameEntry{one}
		}
	}
	for _, e := range entries {
		if e.Path == "" || e.PreviousPath == "" {
			continue
		}
		oldLocal, newLocal := pm.ToLocal(e.PreviousPath), pm.ToLocal(e.Path)
		if err := s.st.RenameFilePath(oldLocal, newLocal); err != nil {
			log.Printf("arr webhook %s: rename %s -> %s: %v", inst.ID, oldLocal, newLocal, err)
		}
	}
}

// handleArrWebhookDelete cancels anything queued for the file, dismisses
// its open intake row, and marks it missing the way the scanner does —
// deliberately not a hard delete, so history/trash bookkeeping tied to
// its file id survives.
func (s *Server) handleArrWebhookDelete(inst config.ArrInstance, ev arrWebhookEnvelope) {
	raw := ev.EpisodeFile
	if raw == nil {
		raw = ev.MovieFile
	}
	wf, err := decodeWebhookFile(raw)
	if err != nil {
		log.Printf("arr webhook %s: %s: %v", inst.ID, "delete", err)
		return
	}
	pm, _ := pathmap.Parse(inst.PathMap)
	local := pm.ToLocal(wf.Path)
	f, err := s.st.GetFileByPath(local)
	if err != nil || f == nil {
		return // never scanned, or already gone — nothing to clean up
	}
	if ids, err := s.st.OpenJobIDsForFile(f.ID); err == nil {
		for _, id := range ids {
			if err := s.eng.Cancel(id); err != nil {
				log.Printf("arr webhook %s: cancel job %d: %v", inst.ID, id, err)
			}
		}
	}
	if err := s.st.DismissOpenIntakeForFile(f.ID); err != nil {
		log.Printf("arr webhook %s: dismiss intake for file %d: %v", inst.ID, f.ID, err)
	}
	if err := s.st.MarkMissingByPath(local); err != nil {
		log.Printf("arr webhook %s: mark missing %s: %v", inst.ID, local, err)
	}
}

// arrWebhookInfoOut is what Settings shows on each instance's card.
type arrWebhookInfoOut struct {
	LastReceived string `json:"last_received,omitempty"`
	AuthFails    int    `json:"auth_fails"`
}

func (s *Server) arrWebhookInfo(id string) arrWebhookInfoOut {
	last, _, _ := s.st.KVGet("arr_webhook_last_" + id)
	return arrWebhookInfoOut{LastReceived: last, AuthFails: arrWebhookAuthFails.m[id]}
}
