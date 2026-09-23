package store

// PlexRow caches one Plex library item's mapping onto a local file,
// keyed by file_id (see the plex_items schema comment for why).
type PlexRow struct {
	FileID    int64  `json:"file_id"`
	RatingKey string `json:"rating_key"`
	SectionID string `json:"section_id"`
	AddedAt   int64  `json:"added_at,omitempty"`
	// ItemType is Plex's own metadata type (1 movie, 4 episode); needed
	// to build the addedAt-restore PUT.
	ItemType int `json:"item_type,omitempty"`
}

// UpsertPlex inserts/updates plex cache rows in one transaction.
func (s *Store) UpsertPlex(rows []PlexRow) error {
	tx, err := s.dbW.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, r := range rows {
		if r.FileID == 0 || r.RatingKey == "" {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO plex_items(file_id, rating_key, section_id, added_at, item_type)
			VALUES(?,?,?,?,?)
			ON CONFLICT(file_id) DO UPDATE SET rating_key=excluded.rating_key,
			section_id=excluded.section_id, added_at=excluded.added_at, item_type=excluded.item_type`,
			r.FileID, r.RatingKey, r.SectionID, r.AddedAt, r.ItemType); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PlexByFileID looks up the cached Plex item for a file, if any.
func (s *Store) PlexByFileID(fileID int64) (*PlexRow, error) {
	var r PlexRow
	err := s.dbR.QueryRow(`SELECT file_id, rating_key, section_id, added_at, item_type
		FROM plex_items WHERE file_id=?`, fileID).Scan(&r.FileID, &r.RatingKey, &r.SectionID, &r.AddedAt, &r.ItemType)
	if err != nil {
		return nil, nil
	}
	return &r, nil
}

// SetPlexAddedAt updates the cached added_at for a file after a
// successful restore write, so the next refresh compares against
// the value we just wrote rather than a stale one.
func (s *Store) SetPlexAddedAt(fileID int64, addedAt int64) error {
	_, err := s.dbW.Exec(`UPDATE plex_items SET added_at=? WHERE file_id=?`, addedAt, fileID)
	return err
}

// PlexCount returns how many items are cached.
func (s *Store) PlexCount() int {
	var n int
	_ = s.dbR.QueryRow(`SELECT COUNT(*) FROM plex_items`).Scan(&n)
	return n
}
