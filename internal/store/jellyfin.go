package store

// JellyfinRow caches one Jellyfin item keyed by its file path.
type JellyfinRow struct {
	Path     string `json:"path"`
	ItemID   string `json:"item_id"`
	SeriesID string `json:"series_id,omitempty"`
	SeasonID string `json:"season_id,omitempty"`
	Name     string `json:"name,omitempty"`
	ImageTag string `json:"image_tag,omitempty"`
	Overview string `json:"overview,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
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
			image_tag, overview, updated_at) VALUES(?,?,?,?,?,?,?,?)
			ON CONFLICT(path) DO UPDATE SET item_id=excluded.item_id, series_id=excluded.series_id,
			season_id=excluded.season_id, name=excluded.name, image_tag=excluded.image_tag,
			overview=excluded.overview, updated_at=excluded.updated_at`,
			r.Path, r.ItemID, r.SeriesID, r.SeasonID, r.Name, r.ImageTag, r.Overview, nowRFC()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// JellyfinMap looks up cache rows for a set of paths.
func (s *Store) JellyfinMap(paths []string) (map[string]JellyfinRow, error) {
	out := map[string]JellyfinRow{}
	if len(paths) == 0 {
		return out, nil
	}
	tx, err := s.dbR.Begin()
	if err != nil {
		return out, err
	}
	defer tx.Rollback()
	const chunk = 100
	for i := 0; i < len(paths); i += chunk {
		end := i + chunk
		if end > len(paths) {
			end = len(paths)
		}
		q := `SELECT path, item_id, series_id, season_id, name, image_tag, overview FROM jellyfin WHERE path IN (`
		args := make([]any, 0, end-i)
		for j := i; j < end; j++ {
			if j > i {
				q += ","
			}
			q += "?"
			args = append(args, paths[j])
		}
		q += `)`
		rows, err := tx.Query(q, args...)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var r JellyfinRow
			if err := rows.Scan(&r.Path, &r.ItemID, &r.SeriesID, &r.SeasonID, &r.Name,
				&r.ImageTag, &r.Overview); err != nil {
				rows.Close()
				return out, err
			}
			out[r.Path] = r
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return out, err
		}
	}
	return out, nil
}

// JellyfinByItem finds a cache row by item id.
func (s *Store) JellyfinByItem(itemID string) (*JellyfinRow, error) {
	var r JellyfinRow
	err := s.dbR.QueryRow(`SELECT path, item_id, series_id, season_id, name, image_tag, overview
		FROM jellyfin WHERE item_id=?`, itemID).Scan(&r.Path, &r.ItemID, &r.SeriesID, &r.SeasonID,
		&r.Name, &r.ImageTag, &r.Overview)
	if err != nil {
		return nil, nil
	}
	return &r, nil
}

// JellyfinByPath finds a cache row by media path.
func (s *Store) JellyfinByPath(path string) (*JellyfinRow, error) {
	var r JellyfinRow
	err := s.dbR.QueryRow(`SELECT path, item_id, series_id, season_id, name, image_tag, overview
		FROM jellyfin WHERE path=?`, path).Scan(&r.Path, &r.ItemID, &r.SeriesID, &r.SeasonID,
		&r.Name, &r.ImageTag, &r.Overview)
	if err != nil {
		return nil, nil
	}
	return &r, nil
}
