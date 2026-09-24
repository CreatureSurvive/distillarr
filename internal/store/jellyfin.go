// SPDX-License-Identifier: GPL-3.0-or-later

package store

import (
	"strings"
	"sync"
)

// JellyfinRow caches one Jellyfin item keyed by its (local) file path.
type JellyfinRow struct {
	Path      string `json:"path"`
	ItemID    string `json:"item_id"`
	SeriesID  string `json:"series_id,omitempty"`
	SeasonID  string `json:"season_id,omitempty"`
	Name      string `json:"name,omitempty"`
	ImageTag  string `json:"image_tag,omitempty"`
	Overview  string `json:"overview,omitempty"`
	Genres    string `json:"genres,omitempty"` // comma-joined
	ItemType  string `json:"item_type,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

const jfCols = `path, item_id, series_id, season_id, name, image_tag, overview, genres, item_type`

func scanJF(row interface{ Scan(...any) error }) (JellyfinRow, error) {
	var r JellyfinRow
	err := row.Scan(&r.Path, &r.ItemID, &r.SeriesID, &r.SeasonID, &r.Name, &r.ImageTag,
		&r.Overview, &r.Genres, &r.ItemType)
	return r, err
}

// UpsertJF inserts/updates jellyfin cache rows in one transaction.
func (s *Store) UpsertJF(rows []JellyfinRow) error {
	tx, err := s.dbW.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range rows {
		if r.Path == "" || r.ItemID == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO jellyfin(path, item_id, series_id, season_id, name,
			image_tag, overview, genres, item_type, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(path) DO UPDATE SET item_id=excluded.item_id, series_id=excluded.series_id,
			season_id=excluded.season_id, name=excluded.name, image_tag=excluded.image_tag,
			overview=excluded.overview, genres=excluded.genres, item_type=excluded.item_type,
			updated_at=excluded.updated_at`,
			r.Path, r.ItemID, r.SeriesID, r.SeasonID, r.Name, r.ImageTag, r.Overview,
			r.Genres, r.ItemType, nowRFC()); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	genreCache.Lock()
	genreCache.m = nil
	genreCache.Unlock()
	return nil
}

// JellyfinMap looks up cache rows for a set of paths.
func (s *Store) JellyfinMap(paths []string) (map[string]JellyfinRow, error) {
	out := map[string]JellyfinRow{}
	const chunk = 200
	for i := 0; i < len(paths); i += chunk {
		end := min(i+chunk, len(paths))
		args := make([]any, 0, end-i)
		for _, p := range paths[i:end] {
			args = append(args, p)
		}
		rows, err := s.dbR.Query(`SELECT `+jfCols+` FROM jellyfin WHERE path IN (`+
			strings.TrimSuffix(strings.Repeat("?,", end-i), ",")+`)`, args...)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			r, err := scanJF(rows)
			if err != nil {
				rows.Close()
				return out, err
			}
			out[r.Path] = r
		}
		rows.Close()
	}
	return out, nil
}

// JellyfinByPath finds a cache row by media path.
func (s *Store) JellyfinByPath(path string) (*JellyfinRow, error) {
	r, err := scanJF(s.dbR.QueryRow(`SELECT `+jfCols+` FROM jellyfin WHERE path=?`, path))
	if err != nil {
		return nil, nil
	}
	return &r, nil
}

// RenamePath re-keys a cached row after a local replace moves the file
// (a container/extension change): Jellyfin itself won't report the new
// path until its own next scan, so without this the row goes stale -
// season/series joins on path, genre lookups and the post-replace
// refresh all miss until the next full sync. Any row already sitting at
// newPath (Jellyfin got there first) is dropped so the rename can't
// collide with the path PRIMARY KEY.
func (s *Store) RenamePath(oldPath, newPath string) error {
	if oldPath == "" || newPath == "" || oldPath == newPath {
		return nil
	}
	tx, err := s.dbW.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM jellyfin WHERE path=?`, newPath); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE jellyfin SET path=? WHERE path=?`, newPath, oldPath); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	genreCache.Lock()
	genreCache.m = nil
	genreCache.Unlock()
	return nil
}

// JellyfinCount returns how many items are cached.
func (s *Store) JellyfinCount() int {
	var n int
	_ = s.dbR.QueryRow(`SELECT COUNT(*) FROM jellyfin`).Scan(&n)
	return n
}

var genreCache struct {
	sync.Mutex
	m map[string]string
}

// GenresFor returns Jellyfin genres for a file path (cached in memory).
func (s *Store) GenresFor(path string) []string {
	genreCache.Lock()
	if genreCache.m == nil {
		genreCache.m = map[string]string{}
		rows, err := s.dbR.Query(`SELECT path, genres FROM jellyfin WHERE genres != ''`)
		if err == nil {
			for rows.Next() {
				var p, g string
				if rows.Scan(&p, &g) == nil {
					genreCache.m[p] = g
				}
			}
			rows.Close()
		}
	}
	g := genreCache.m[path]
	genreCache.Unlock()
	if g == "" {
		return nil
	}
	return strings.Split(g, ",")
}

// SeriesFor finds the Jellyfin Series row whose folder contains path.
func (s *Store) SeriesFor(path string) *JellyfinRow {
	r, err := scanJF(s.dbR.QueryRow(`SELECT `+jfCols+` FROM jellyfin
		WHERE item_type='Series' AND ? LIKE path || '/%' ORDER BY length(path) DESC LIMIT 1`, path))
	if err != nil {
		return nil
	}
	return &r
}

// FirstPathForShow returns any file path of a show (for folder lookups).
func (s *Store) FirstPathForShow(show string) string {
	var p string
	_ = s.dbR.QueryRow(`SELECT path FROM files WHERE library='tvshows' AND missing=0
		AND title=? COLLATE NOCASE LIMIT 1`, show).Scan(&p)
	return p
}
