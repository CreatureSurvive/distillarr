package store



// IssueCount is how many present files carry one issue key.
type IssueCount struct {
	Key      string `json:"key"`
	Movies   int    `json:"movies"`
	Episodes int    `json:"episodes"`
	Bytes    int64  `json:"bytes"`
}

// IssueCounts tallies every issue key across the library.
func (s *Store) IssueCounts(keys []string) (map[string]*IssueCount, error) {
	out := map[string]*IssueCount{}
	for _, k := range keys {
		c := &IssueCount{Key: k}
		if err := s.dbR.QueryRow(`SELECT COALESCE(SUM(library='movies'),0), COALESCE(SUM(library='tvshows'),0),
			COALESCE(SUM(size),0) FROM files WHERE missing=0 AND issues LIKE ?`, "%,"+k+",%").
			Scan(&c.Movies, &c.Episodes, &c.Bytes); err != nil {
			return nil, err
		}
		out[k] = c
	}
	return out, nil
}

// MixedSeason is a season whose episodes don't share one format.
type MixedSeason struct {
	Show       string `json:"show"`
	Season     int    `json:"season"`
	Episodes   int    `json:"episodes"`
	Codecs     string `json:"codecs"`
	Containers string `json:"containers"`
	Classes    string `json:"classes"`
	Size       int64  `json:"size"`
}

// MixedSeasons lists seasons mixing video codecs, containers or
// resolution classes. Specials (season 0) are ignored — they're mixed
// by nature.
func (s *Store) MixedSeasons(show string) ([]MixedSeason, error) {
	where, args := "missing=0 AND library='tvshows' AND season>0", []any{}
	if show != "" {
		where += " AND title LIKE ? COLLATE NOCASE"
		args = append(args, "%"+escapeLike(show)+"%")
	}
	rows, err := s.dbR.Query(`SELECT title, season, COUNT(*),
		COALESCE(GROUP_CONCAT(DISTINCT video_codec),''), COALESCE(GROUP_CONCAT(DISTINCT container),''),
		COALESCE(GROUP_CONCAT(DISTINCT `+resClassSQL+`),''), SUM(size)
		FROM files WHERE `+where+`
		GROUP BY title COLLATE NOCASE, season
		HAVING COUNT(DISTINCT video_codec)>1 OR COUNT(DISTINCT container)>1 OR COUNT(DISTINCT `+resClassSQL+`)>1
		ORDER BY title COLLATE NOCASE, season`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MixedSeason{}
	for rows.Next() {
		var m MixedSeason
		if err := rows.Scan(&m.Show, &m.Season, &m.Episodes, &m.Codecs, &m.Containers, &m.Classes, &m.Size); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Facet is one value of a library dimension with its file count.
type Facet struct {
	Value string `json:"value"`
	Files int    `json:"files"`
	Bytes int64  `json:"bytes"`
}

// Composition breaks the library down by container, video codec,
// audio codec and resolution class (filter options + stats).
type Composition struct {
	Containers []Facet `json:"containers"`
	Video      []Facet `json:"video"`
	Audio      []Facet `json:"audio"`
	Res        []Facet `json:"res"`
}

// LibraryComposition computes facets, optionally for one library.
func (s *Store) LibraryComposition(library string) (Composition, error) {
	var c Composition
	where, args := "f.missing=0", []any{}
	if library != "" {
		where += " AND f.library=?"
		args = append(args, library)
	}
	q := func(expr, from string) ([]Facet, error) {
		rows, err := s.dbR.Query(`SELECT `+expr+` AS v, COUNT(DISTINCT f.id), COALESCE(SUM(f.size),0) FROM `+from+
			` WHERE `+where+` GROUP BY v ORDER BY 2 DESC`, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []Facet{}
		for rows.Next() {
			var fc Facet
			if err := rows.Scan(&fc.Value, &fc.Files, &fc.Bytes); err != nil {
				return nil, err
			}
			out = append(out, fc)
		}
		return out, rows.Err()
	}
	var err error
	if c.Containers, err = q("f.container", "files f"); err != nil {
		return c, err
	}
	if c.Video, err = q("f.video_codec", "files f"); err != nil {
		return c, err
	}
	if c.Res, err = q("CAST("+resClassSQL+" AS TEXT)", "files f"); err != nil {
		return c, err
	}
	// Audio: count each file once per distinct codec family.
	fam := `CASE WHEN json_extract(a.value,'$.codec') LIKE 'pcm%' THEN 'pcm'
		WHEN json_extract(a.value,'$.codec') LIKE 'dts%' THEN 'dts'
		ELSE json_extract(a.value,'$.codec') END`
	if c.Audio, err = q(fam, "files f, json_each(f.audio_json) a"); err != nil {
		return c, err
	}
	return c, nil
}
