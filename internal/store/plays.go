// SPDX-License-Identifier: GPL-3.0-or-later

package store

import "time"

// PlayStat is how often and how recently a file was watched.
type PlayStat struct {
	Plays      int
	LastPlayed time.Time // zero when never
}

// PlayStats combines the two play sources per file: playback_events
// (one row per session-hour, from the live poller and Jellystat
// history) for Jellyfin, and Plex's own viewCount/lastViewedAt, taking
// the larger of Plex's count and the poller's Plex rows so a session
// isn't counted twice.
func (s *Store) PlayStats() (map[int64]PlayStat, error) {
	out := map[int64]PlayStat{}
	rows, err := s.dbR.Query(`SELECT file_id,
		SUM(server='jellyfin'), SUM(server='plex'), MAX(at) FROM playback_events GROUP BY file_id`)
	if err != nil {
		return nil, err
	}
	plexEvents := map[int64]int{}
	for rows.Next() {
		var id int64
		var jf, px int
		var at string
		if err := rows.Scan(&id, &jf, &px, &at); err != nil {
			rows.Close()
			return nil, err
		}
		t, _ := time.Parse(time.RFC3339, at)
		out[id] = PlayStat{Plays: jf + px, LastPlayed: t}
		plexEvents[id] = px
	}
	rows.Close()
	prow, err := s.dbR.Query(`SELECT file_id, view_count, last_viewed_at FROM plex_items WHERE view_count > 0 OR last_viewed_at > 0`)
	if err != nil {
		return nil, err
	}
	defer prow.Close()
	for prow.Next() {
		var id, vc, lv int64
		if err := prow.Scan(&id, &vc, &lv); err != nil {
			return nil, err
		}
		st := out[id]
		if int(vc) > plexEvents[id] {
			st.Plays += int(vc) - plexEvents[id]
		}
		if t := time.Unix(lv, 0); lv > 0 && t.After(st.LastPlayed) {
			st.LastPlayed = t
		}
		out[id] = st
	}
	return out, prow.Err()
}

// PlayStatFor is PlayStats for one file.
func (s *Store) PlayStatFor(fileID int64) PlayStat {
	var st PlayStat
	var jf, px int
	var at string
	_ = s.dbR.QueryRow(`SELECT COALESCE(SUM(server='jellyfin'),0), COALESCE(SUM(server='plex'),0), COALESCE(MAX(at),'')
		FROM playback_events WHERE file_id=?`, fileID).Scan(&jf, &px, &at)
	st.Plays = jf + px
	st.LastPlayed, _ = time.Parse(time.RFC3339, at)
	var vc, lv int64
	if s.dbR.QueryRow(`SELECT view_count, last_viewed_at FROM plex_items WHERE file_id=?`, fileID).Scan(&vc, &lv) == nil {
		if int(vc) > px {
			st.Plays += int(vc) - px
		}
		if t := time.Unix(lv, 0); lv > 0 && t.After(st.LastPlayed) {
			st.LastPlayed = t
		}
	}
	return st
}

// JellyfinPathsByItem maps every cached Jellyfin item id (lowercase, no
// dashes, the form Jellystat uses) to its local path.
func (s *Store) JellyfinPathsByItem() (map[string]string, error) {
	rows, err := s.dbR.Query(`SELECT LOWER(REPLACE(item_id,'-','')), path FROM jellyfin`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, p string
		if err := rows.Scan(&id, &p); err != nil {
			return nil, err
		}
		out[id] = p
	}
	return out, rows.Err()
}
