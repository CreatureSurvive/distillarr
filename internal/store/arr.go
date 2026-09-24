package store

import (
	"encoding/json"
	"strconv"
	"strings"
)

// ArrItem is one file's Sonarr/Radarr view, keyed by file_id (not path):
// a file's id survives an in-place replace, so no rename re-keying is
// needed here the way the Jellyfin cache needs it. A rename that also
// changes the container extension gets a brand-new files row (see
// ClearOldPath) and so a brand-new file_id; the old arr_items row goes
// with the old file row into the missing-file purge, and the new file
// picks up its own row on the next sync — self-healing within one sync
// pass, deliberately not chased immediately.
type ArrItem struct {
	FileID           int64  `json:"file_id"`
	InstanceID       string `json:"instance_id"`
	Kind             string `json:"kind"` // "sonarr" | "radarr"
	ItemID           int64  `json:"item_id"`
	FileRecID        int64  `json:"file_rec_id"`
	Monitored        bool   `json:"monitored"`
	Tags             string `json:"-"` // ",3,7," — resolved to names by the API layer
	QualityProfileID int64  `json:"quality_profile_id"`
	CutoffNotMet     bool   `json:"cutoff_not_met"`
	CFScore          int    `json:"cf_score"`
	OriginalLanguage string `json:"original_language"`
	SeriesStatus     string `json:"series_status"`
	SceneName        string `json:"scene_name"`
	SyncedAt         string `json:"synced_at,omitempty"`
}

// TagIDs decodes the ",3,7," form into [3, 7].
func (a ArrItem) TagIDs() []int64 {
	var out []int64
	for _, s := range strings.Split(strings.Trim(a.Tags, ","), ",") {
		if s == "" {
			continue
		}
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// JoinTagIDs encodes tag ids the way ArrItem.Tags and the issues.Encode
// convention both use: ",3,7," so a LIKE '%,3,%' matches exactly.
func JoinTagIDs(ids []int64) string {
	if len(ids) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteByte(',')
	for _, id := range ids {
		b.WriteString(strconv.FormatInt(id, 10))
		b.WriteByte(',')
	}
	return b.String()
}

const arrItemCols = `file_id, instance_id, kind, item_id, file_rec_id, monitored, tags,
	quality_profile_id, cutoff_not_met, cf_score, original_language, series_status, scene_name, synced_at`

func scanArrItem(row interface{ Scan(...any) error }) (ArrItem, error) {
	var a ArrItem
	var monitored, cutoffNotMet int
	err := row.Scan(&a.FileID, &a.InstanceID, &a.Kind, &a.ItemID, &a.FileRecID, &monitored, &a.Tags,
		&a.QualityProfileID, &cutoffNotMet, &a.CFScore, &a.OriginalLanguage, &a.SeriesStatus,
		&a.SceneName, &a.SyncedAt)
	a.Monitored = monitored != 0
	a.CutoffNotMet = cutoffNotMet != 0
	return a, err
}

// UpsertArrItems inserts/updates arr_items rows in one transaction.
func (s *Store) UpsertArrItems(items []ArrItem) error {
	if len(items) == 0 {
		return nil
	}
	tx, err := s.dbW.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT INTO arr_items(` + arrItemCols + `)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(file_id) DO UPDATE SET instance_id=excluded.instance_id, kind=excluded.kind,
			item_id=excluded.item_id, file_rec_id=excluded.file_rec_id, monitored=excluded.monitored,
			tags=excluded.tags, quality_profile_id=excluded.quality_profile_id,
			cutoff_not_met=excluded.cutoff_not_met, cf_score=excluded.cf_score,
			original_language=excluded.original_language, series_status=excluded.series_status,
			scene_name=excluded.scene_name, synced_at=excluded.synced_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	now := nowRFC()
	for _, a := range items {
		if _, err := stmt.Exec(a.FileID, a.InstanceID, a.Kind, a.ItemID, a.FileRecID, b2i(a.Monitored),
			a.Tags, a.QualityProfileID, b2i(a.CutoffNotMet), a.CFScore, a.OriginalLanguage,
			a.SeriesStatus, a.SceneName, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteArrItemsForInstanceExcept removes an instance's rows whose
// file_id isn't in keep — items it no longer reports (deleted, or the
// file was matched to a different local path this pass). Called once
// per instance right after that instance's own sync succeeds, so a
// different instance's rows (including ones this instance lost a
// conflict on) are never touched by it.
func (s *Store) DeleteArrItemsForInstanceExcept(instanceID string, keep []int64) error {
	if len(keep) == 0 {
		return s.DeleteArrItemsForInstance(instanceID)
	}
	// Chunking a "NOT IN (subset)" delete is wrong: each chunk only sees
	// its own slice of keep, so it deletes every row not in THAT chunk —
	// including rows a different chunk was supposed to keep. Compute the
	// actual stale ids first (one instance_id's current rows, minus the
	// full keep set), then delete exactly those; chunking a plain
	// "IN (ids-to-delete)" is safe because each id is independent.
	keepSet := make(map[int64]bool, len(keep))
	for _, id := range keep {
		keepSet[id] = true
	}
	rows, err := s.dbR.Query(`SELECT file_id FROM arr_items WHERE instance_id=?`, instanceID)
	if err != nil {
		return err
	}
	var stale []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		if !keepSet[id] {
			stale = append(stale, id)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(stale) == 0 {
		return nil
	}

	tx, err := s.dbW.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	const chunk = 500
	for i := 0; i < len(stale); i += chunk {
		end := min(i+chunk, len(stale))
		args := make([]any, 0, end-i)
		for _, id := range stale[i:end] {
			args = append(args, id)
		}
		q := `DELETE FROM arr_items WHERE file_id IN (` +
			strings.TrimSuffix(strings.Repeat("?,", end-i), ",") + `)`
		if _, err := tx.Exec(q, args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DeleteArrItemsForInstance drops every row owned by an instance (it was
// removed from config, or disabled).
func (s *Store) DeleteArrItemsForInstance(instanceID string) error {
	_, err := s.dbW.Exec(`DELETE FROM arr_items WHERE instance_id=?`, instanceID)
	return err
}

// ArrItemByFileID looks up one file's arr_items row, if any.
func (s *Store) ArrItemByFileID(fileID int64) (*ArrItem, error) {
	a, err := scanArrItem(s.dbR.QueryRow(`SELECT `+arrItemCols+` FROM arr_items WHERE file_id=?`, fileID))
	if err != nil {
		return nil, nil
	}
	return &a, nil
}

// ArrItemsMap loads arr_items rows for a set of file ids.
func (s *Store) ArrItemsMap(fileIDs []int64) (map[int64]ArrItem, error) {
	out := map[int64]ArrItem{}
	const chunk = 400
	for i := 0; i < len(fileIDs); i += chunk {
		end := min(i+chunk, len(fileIDs))
		args := make([]any, 0, end-i)
		for _, id := range fileIDs[i:end] {
			args = append(args, id)
		}
		rows, err := s.dbR.Query(`SELECT `+arrItemCols+` FROM arr_items WHERE file_id IN (`+
			strings.TrimSuffix(strings.Repeat("?,", end-i), ",")+`)`, args...)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			a, err := scanArrItem(rows)
			if err != nil {
				rows.Close()
				return out, err
			}
			out[a.FileID] = a
		}
		rows.Close()
	}
	return out, nil
}

// ArrOriginalLanguages returns every managed file's original_language
// name, keyed by file id (files with no arr_items row, or a blank
// original_language, are simply absent). Used to resolve langprune's
// "original" language in bulk (extra_languages issue), the same
// way ForcesTranscodeFileIDs feeds forces_transcode.
func (s *Store) ArrOriginalLanguages() (map[int64]string, error) {
	out := map[int64]string{}
	rows, err := s.dbR.Query(`SELECT file_id, original_language FROM arr_items WHERE original_language != ''`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var lang string
		if err := rows.Scan(&id, &lang); err != nil {
			return out, err
		}
		out[id] = lang
	}
	return out, rows.Err()
}

// ArrOriginalLanguageFor is ArrOriginalLanguages for a single file, for
// callers (recs.OriginalLanguage) that resolve one file at a time rather
// than bulk-refreshing every row.
func (s *Store) ArrOriginalLanguageFor(fileID int64) string {
	item, err := s.ArrItemByFileID(fileID)
	if err != nil || item == nil {
		return ""
	}
	return item.OriginalLanguage
}

// ArrInstanceIDs returns every managed file's owning arr instance id,
// keyed by file id. Used to resolve per-file scope (// lang_instance_overrides) in bulk without one ArrItemByFileID query per
// file, the same way ArrOriginalLanguages avoids that for original_language.
func (s *Store) ArrInstanceIDs() (map[int64]string, error) {
	out := map[int64]string{}
	rows, err := s.dbR.Query(`SELECT file_id, instance_id FROM arr_items`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var inst string
		if err := rows.Scan(&id, &inst); err != nil {
			return out, err
		}
		out[id] = inst
	}
	return out, rows.Err()
}

// ArrManagedCount returns how many non-missing files have an arr_items
// row, for the "Managed by" facet's counts.
func (s *Store) ArrManagedCount() (int, error) {
	var n int
	err := s.dbR.QueryRow(`SELECT COUNT(*) FROM arr_items a JOIN files f ON f.id=a.file_id WHERE f.missing=0`).Scan(&n)
	return n, err
}

// SetArrTags caches an instance's tag id → label map (Settings → Tags),
// used to show tag names on a file instead of bare ids.
func (s *Store) SetArrTags(instanceID string, tags map[int64]string) error {
	b, err := json.Marshal(tags)
	if err != nil {
		return err
	}
	return s.KVSet("arr_tags_"+instanceID, string(b))
}

// ArrTags returns an instance's cached tag id → label map ({} if never synced).
func (s *Store) ArrTags(instanceID string) map[int64]string {
	out := map[int64]string{}
	if v, ok, _ := s.KVGet("arr_tags_" + instanceID); ok {
		_ = json.Unmarshal([]byte(v), &out)
	}
	return out
}
