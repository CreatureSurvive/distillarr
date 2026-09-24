// SPDX-License-Identifier: GPL-3.0-or-later

package store

// TrashItem is one retained original.
type TrashItem struct {
	ID          int64  `json:"id"`
	OrigPath    string `json:"orig_path"`
	TrashPath   string `json:"trash_path"`
	CurrentPath string `json:"current_path"`
	Size        int64  `json:"size"`
	JobID       int64  `json:"job_id"`
	CreatedAt   string `json:"created_at"`
}

// AddTrash records a retained original (idempotent on trash_path).
func (s *Store) AddTrash(t TrashItem) error {
	_, err := s.dbW.Exec(`INSERT INTO trash(orig_path, trash_path, current_path, size, job_id)
		VALUES(?,?,?,?,?) ON CONFLICT(trash_path) DO UPDATE SET current_path=excluded.current_path,
		job_id=excluded.job_id`, t.OrigPath, t.TrashPath, t.CurrentPath, t.Size, t.JobID)
	return err
}

func (s *Store) trashQuery(where string, args ...any) ([]TrashItem, error) {
	rows, err := s.dbR.Query(`SELECT id, orig_path, trash_path, current_path, size, job_id, created_at
		FROM trash WHERE `+where+` ORDER BY id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TrashItem{}
	for rows.Next() {
		var t TrashItem
		if err := rows.Scan(&t.ID, &t.OrigPath, &t.TrashPath, &t.CurrentPath, &t.Size, &t.JobID, &t.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListTrash returns every retained original, newest first.
func (s *Store) ListTrash() ([]TrashItem, error) { return s.trashQuery("1=1") }

// ExpiredTrash returns items older than days.
func (s *Store) ExpiredTrash(days int) ([]TrashItem, error) {
	return s.trashQuery(`created_at < strftime('%Y-%m-%dT%H:%M:%fZ','now', ?)`, "-"+itoaDays(days)+" days")
}

// GetTrash fetches one item.
func (s *Store) GetTrash(id int64) (*TrashItem, error) {
	items, err := s.trashQuery("id=?", id)
	if err != nil || len(items) == 0 {
		return nil, err
	}
	return &items[0], nil
}

// DeleteTrash removes a record.
func (s *Store) DeleteTrash(id int64) error {
	_, err := s.dbW.Exec(`DELETE FROM trash WHERE id=?`, id)
	return err
}

func itoaDays(d int) string {
	if d < 0 {
		d = 0
	}
	b := []byte{}
	if d == 0 {
		return "0"
	}
	for d > 0 {
		b = append([]byte{byte('0' + d%10)}, b...)
		d /= 10
	}
	return string(b)
}
