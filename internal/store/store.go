// Package store provides the SQLite persistence layer.
//
// Concurrency model: two *sql.DB handles over the same file.
// dbW has MaxOpenConns(1) — every write in the app flows through it,
// serialized, so the app never competes with itself for the write lock.
// dbR serves API reads (WAL readers never block on the writer).
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS meta(
  key TEXT PRIMARY KEY, value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS files(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  path TEXT NOT NULL UNIQUE,
  library TEXT NOT NULL,               -- 'movies' | 'tvshows'
  title TEXT NOT NULL DEFAULT '',
  year INTEGER NOT NULL DEFAULT 0,
  season INTEGER NOT NULL DEFAULT -1,  -- -1 = movie
  episode INTEGER NOT NULL DEFAULT -1, -- -1 = movie
  ep_title TEXT NOT NULL DEFAULT '',
  quality_tag TEXT NOT NULL DEFAULT '',
  size INTEGER NOT NULL DEFAULT 0,
  mtime_ns INTEGER NOT NULL DEFAULT 0,
  container TEXT NOT NULL DEFAULT '',
  duration REAL NOT NULL DEFAULT 0,
  video_codec TEXT NOT NULL DEFAULT '',
  width INTEGER NOT NULL DEFAULT 0,
  height INTEGER NOT NULL DEFAULT 0,
  bit_depth INTEGER NOT NULL DEFAULT 0,
  fps REAL NOT NULL DEFAULT 0,
  hdr TEXT NOT NULL DEFAULT '',         -- '', 'hdr10', 'hlg', 'dolby_vision'
  video_bitrate INTEGER NOT NULL DEFAULT 0,
  total_bitrate INTEGER NOT NULL DEFAULT 0,
  audio_json TEXT NOT NULL DEFAULT '[]',
  sub_count INTEGER NOT NULL DEFAULT 0,
  sidecars_json TEXT NOT NULL DEFAULT '[]',
  transcode_score REAL NOT NULL DEFAULT 0,
  rec_json TEXT NOT NULL DEFAULT '',
  missing INTEGER NOT NULL DEFAULT 0,
  scanned_at TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS files_browse ON files(library, missing, title COLLATE NOCASE, id);
CREATE INDEX IF NOT EXISTS files_codec ON files(library, video_codec, id);
CREATE INDEX IF NOT EXISTS files_series ON files(library, title COLLATE NOCASE, season, episode);
CREATE INDEX IF NOT EXISTS files_score ON files(transcode_score, id);

CREATE TABLE IF NOT EXISTS streams(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
  kind TEXT NOT NULL,                  -- 'video' | 'audio' | 'subtitle'
  stream_index INTEGER NOT NULL,
  codec TEXT NOT NULL DEFAULT '',
  lang TEXT NOT NULL DEFAULT '',
  title TEXT NOT NULL DEFAULT '',
  channels INTEGER NOT NULL DEFAULT 0,
  bit_rate INTEGER NOT NULL DEFAULT 0,
  is_default INTEGER NOT NULL DEFAULT 0,
  is_forced INTEGER NOT NULL DEFAULT 0,
  is_text INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS streams_file ON streams(file_id);

CREATE TABLE IF NOT EXISTS jobs(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  file_id INTEGER REFERENCES files(id) ON DELETE SET NULL,
  src_path TEXT NOT NULL,
  temp_path TEXT NOT NULL DEFAULT '',
  status TEXT NOT NULL DEFAULT 'queued'
      CHECK(status IN ('queued','running','verifying','replacing','done','failed','canceled')),
  priority INTEGER NOT NULL DEFAULT 100000,
  run_now INTEGER NOT NULL DEFAULT 0,
  backend TEXT NOT NULL,
  codec TEXT NOT NULL,
  quality INTEGER NOT NULL,
  settings_json TEXT NOT NULL DEFAULT '{}',
  attempts INTEGER NOT NULL DEFAULT 0,
  max_attempts INTEGER NOT NULL DEFAULT 3,
  error TEXT NOT NULL DEFAULT '',
  error_tail TEXT NOT NULL DEFAULT '',
  progress_json TEXT NOT NULL DEFAULT '',
  src_probe_json TEXT NOT NULL DEFAULT '',
  src_stat_json TEXT NOT NULL DEFAULT '',
  dst_probe_json TEXT NOT NULL DEFAULT '',
  src_size INTEGER NOT NULL DEFAULT 0,
  output_size INTEGER NOT NULL DEFAULT 0,
  started_at TEXT NOT NULL DEFAULT '',
  finished_at TEXT NOT NULL DEFAULT '',
  created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);
CREATE INDEX IF NOT EXISTS jobs_pick ON jobs(status, priority, id);
CREATE INDEX IF NOT EXISTS jobs_active ON jobs(status);
CREATE INDEX IF NOT EXISTS jobs_file ON jobs(file_id);

CREATE TABLE IF NOT EXISTS kv(
  key TEXT PRIMARY KEY, value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS jellyfin(
  path TEXT PRIMARY KEY,
  item_id TEXT NOT NULL,
  series_id TEXT NOT NULL DEFAULT '',
  season_id TEXT NOT NULL DEFAULT '',
  name TEXT NOT NULL DEFAULT '',
  image_tag TEXT NOT NULL DEFAULT '',
  overview TEXT NOT NULL DEFAULT '',
  updated_at TEXT NOT NULL DEFAULT ''
);
`

// Store wraps the two database handles.
type Store struct {
	dbW *sql.DB // single-conn writer
	dbR *sql.DB // read pool
}

// Open opens (creating if needed) and migrates the database at path.
func Open(path string) (*Store, error) {
	dsn := func() string {
		q := "?_pragma=busy_timeout(5000)" +
			"&_pragma=journal_mode(WAL)" +
			"&_pragma=synchronous(NORMAL)" +
			"&_pragma=foreign_keys(1)"
		if strings.HasPrefix(path, "file:") || strings.Contains(path, "?") {
			return path
		}
		return "file:" + path + q
	}()

	dbW, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open writer: %w", err)
	}
	dbW.SetMaxOpenConns(1)

	dbR, err := sql.Open("sqlite", dsn)
	if err != nil {
		dbW.Close()
		return nil, fmt.Errorf("open reader: %w", err)
	}
	dbR.SetMaxOpenConns(8)

	s := &Store{dbW: dbW, dbR: dbR}
	if err := s.migrate(); err != nil {
		dbW.Close()
		dbR.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	err1 := s.dbW.Close()
	err2 := s.dbR.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

// v2 additions; ALTERs are idempotent (duplicate-column errors ignored).
var alters = []string{
	`ALTER TABLE files ADD COLUMN interlaced INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE jobs ADD COLUMN cmd TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE jobs ADD COLUMN dest_path TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE jellyfin ADD COLUMN genres TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE jellyfin ADD COLUMN item_type TEXT NOT NULL DEFAULT ''`,
	`CREATE TABLE IF NOT EXISTS trash(
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		orig_path TEXT NOT NULL,
		trash_path TEXT NOT NULL UNIQUE,
		current_path TEXT NOT NULL DEFAULT '',
		size INTEGER NOT NULL DEFAULT 0,
		job_id INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')))`,
	`CREATE INDEX IF NOT EXISTS jellyfin_item ON jellyfin(item_id)`,
	// Black-bar detection: active picture rectangle (0 = none/unknown).
	`ALTER TABLE files ADD COLUMN crop_w INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE files ADD COLUMN crop_h INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE files ADD COLUMN crop_x INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE files ADD COLUMN crop_y INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE files ADD COLUMN crop_checked INTEGER NOT NULL DEFAULT 0`,
	// Per-file VMAF quality search result (JSON, "" = not tuned).
	`ALTER TABLE files ADD COLUMN tune_json TEXT NOT NULL DEFAULT ''`,
	// Container/stream facts for the issues list, and the issues found.
	`ALTER TABLE files ADD COLUMN video_tag TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE files ADD COLUMN faststart INTEGER NOT NULL DEFAULT -1`, // -1 n/a or unknown
	`ALTER TABLE files ADD COLUMN meta_checked INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE files ADD COLUMN issues TEXT NOT NULL DEFAULT ''`,        // ",no_hvc1,pcm_audio,"
	`CREATE INDEX IF NOT EXISTS files_meta ON files(meta_checked)`,
	// Set when a job's output was later restored from trash: history/savings
	// stats exclude it (the work wasn't kept), while status stays 'done'
	// since the job itself did complete.
	`ALTER TABLE jobs ADD COLUMN reverted_at TEXT NOT NULL DEFAULT ''`,
	// Why a job exists: manual (user queued it), issue-fix (a quick-fix
	// button), upscale, or (later phases) webhook / autopilot / playback.
	// reason is a short free-text note shown in the UI.
	`ALTER TABLE jobs ADD COLUMN origin TEXT NOT NULL DEFAULT 'manual'`,
	`ALTER TABLE jobs ADD COLUMN reason TEXT NOT NULL DEFAULT ''`,
	// Hardlink count: >1 means another link shares this file's data
	// (usually a seeding torrent). Checked on every scan pass, since it
	// can change without size or mtime changing.
	`ALTER TABLE files ADD COLUMN nlink INTEGER NOT NULL DEFAULT 1`,
	// One Sonarr/Radarr's view of a file, keyed by file_id (see ArrItem's
	// doc comment for why not path). At most one instance owns a file;
	// ON DELETE CASCADE drops the row once its file row is actually
	// purged (missing=1 for 7+ days), not merely marked missing.
	`CREATE TABLE IF NOT EXISTS arr_items(
		file_id INTEGER PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
		instance_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		item_id INTEGER NOT NULL,
		file_rec_id INTEGER NOT NULL,
		monitored INTEGER NOT NULL DEFAULT 0,
		tags TEXT NOT NULL DEFAULT '',
		quality_profile_id INTEGER NOT NULL DEFAULT 0,
		cutoff_not_met INTEGER NOT NULL DEFAULT 0,
		cf_score INTEGER NOT NULL DEFAULT 0,
		original_language TEXT NOT NULL DEFAULT '',
		series_status TEXT NOT NULL DEFAULT '',
		scene_name TEXT NOT NULL DEFAULT '',
		synced_at TEXT NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS arr_items_instance ON arr_items(instance_id)`,
	`CREATE TABLE IF NOT EXISTS intake(
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
		origin TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '',
		settings_json TEXT NOT NULL DEFAULT '',
		state TEXT NOT NULL,
		not_before TEXT NOT NULL DEFAULT '',
		hold_reason TEXT NOT NULL DEFAULT '',
		job_id INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL, updated_at TEXT NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS intake_file ON intake(file_id)`,
	`CREATE INDEX IF NOT EXISTS intake_state ON intake(state)`,
	`CREATE TABLE IF NOT EXISTS speed_stats(
		key TEXT PRIMARY KEY,
		samples TEXT NOT NULL DEFAULT '[]',
		updated_at TEXT NOT NULL)`,
	// One Plex library item's mapping onto a local file, keyed by
	// file_id like arr_items (not path like the jellyfin table): a
	// file's id survives a replace even when its container/extension
	// changes, so no rename bookkeeping is needed the way
	// Store.RenamePath does for Jellyfin's path-keyed cache.
	`CREATE TABLE IF NOT EXISTS plex_items(
		file_id INTEGER PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
		rating_key TEXT NOT NULL,
		section_id TEXT NOT NULL,
		added_at INTEGER NOT NULL DEFAULT 0)`,
	`CREATE INDEX IF NOT EXISTS plex_items_section ON plex_items(section_id)`,
	// Plex's own metadata type for the item (1 movie, 4 episode): needed
	// to build the addedAt-restore PUT, which requires the
	// section's type param same as the original sync WalkSection call.
	`ALTER TABLE plex_items ADD COLUMN item_type INTEGER NOT NULL DEFAULT 1`,
	// One row per (file, server, hour): the sessions poller upserts here
	// every time it sees a session for a tracked file, so a session
	// polled every 15s for an hour still produces one row.
	// direct=1 means the whole session played without any transcoding;
	// reasons is a comma-joined list of issues.ReasonCategory keys,
	// empty when direct=1.
	`CREATE TABLE IF NOT EXISTS playback_events(
		file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
		server TEXT NOT NULL,
		at TEXT NOT NULL,
		direct INTEGER NOT NULL DEFAULT 0,
		reasons TEXT NOT NULL DEFAULT '',
		PRIMARY KEY(file_id, server, at))`,
	`CREATE INDEX IF NOT EXISTS playback_events_lookback ON playback_events(direct, at)`,
	// Per-subtitle-track summary (lang/title/forced/commentary/SDH/text),
	// mirroring audio_json, for the langprune report issue. sub_count
	// stays as the plain count other code already relies on.
	`ALTER TABLE files ADD COLUMN subs_json TEXT NOT NULL DEFAULT ''`,
	// User opt-out of language pruning; never touched by
	// scan/probe, so it survives reprobes and rescans (see UpsertFile).
	`ALTER TABLE files ADD COLUMN lang_prune_exempt INTEGER NOT NULL DEFAULT 0`,
	// Per-file override of subs_sidecar_mode; "" = inherit. Same
	// reprobe-safety reasoning as lang_prune_exempt.
	`ALTER TABLE files ADD COLUMN sidecar_mode TEXT NOT NULL DEFAULT ''`,
	// Sidecar files a job created, JSON string array; RestoreTrash
	// deletes exactly these and only these.
	`ALTER TABLE jobs ADD COLUMN sidecars_json TEXT NOT NULL DEFAULT '[]'`,
	// Per-file override of image_subs_mode; "" = inherit. Same
	// reprobe-safety reasoning as sidecar_mode.
	`ALTER TABLE files ADD COLUMN image_subs_mode TEXT NOT NULL DEFAULT ''`,
	// Cached OCR attempt result, same no-retry-every-cycle
	// reasoning as tune_json.
	`ALTER TABLE files ADD COLUMN ocr_json TEXT NOT NULL DEFAULT ''`,
}

func (s *Store) migrate() error {
	if _, err := s.dbW.Exec(schema); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	for _, a := range alters {
		if _, err := s.dbW.Exec(a); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return fmt.Errorf("migrate %q: %w", a[:40], err)
		}
	}
	var v string
	err := s.dbW.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&v)
	if err == sql.ErrNoRows {
		_, err = s.dbW.Exec(`INSERT INTO meta(key, value) VALUES('schema_version','1')`)
		return err
	}
	return err
}

func nowRFC() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// ---- kv ----

func (s *Store) KVGet(key string) (string, bool, error) {
	var v string
	err := s.dbR.QueryRow(`SELECT value FROM kv WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func (s *Store) KVSet(key, value string) error {
	_, err := s.dbW.Exec(`INSERT INTO kv(key, value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// KVDelete removes key (no error when it's absent).
func (s *Store) KVDelete(key string) error {
	_, err := s.dbW.Exec(`DELETE FROM kv WHERE key=?`, key)
	return err
}

// KVJSON stores arbitrary JSON-serializable state under key.
func KVJSON[T any](s *Store, key string, v *T) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.KVSet(key, string(b))
}

func KVLoad[T any](s *Store, key string, v *T) (bool, error) {
	raw, ok, err := s.KVGet(key)
	if err != nil || !ok {
		return false, err
	}
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		return true, fmt.Errorf("kv %s: %w", key, err)
	}
	return true, nil
}
