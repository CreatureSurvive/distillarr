package store

import (
	"database/sql"
	"encoding/json"
	"strings"
)

// AudioStream is the summary persisted in files.audio_json.
type AudioStream struct {
	Index    int    `json:"index"`
	Codec    string `json:"codec"`
	Lang     string `json:"lang"`
	Title    string `json:"title"`
	Channels int    `json:"channels"`
	BitRate  int64  `json:"bit_rate"`
	Default  bool   `json:"default,omitempty"`
	Forced   bool   `json:"forced,omitempty"`
}

// Sidecar is an external subtitle file next to the media file.
type Sidecar struct {
	Name string `json:"name"`
	Lang string `json:"lang,omitempty"`
	Kind string `json:"kind"` // srt, ass, ...
}

// File is one media file row.
type File struct {
	ID          int64  `json:"id"`
	Path        string `json:"path"`
	Library     string `json:"library"`
	Title       string `json:"title"`
	Year        int    `json:"year"`
	Season      int    `json:"season"`
	Episode     int    `json:"episode"`
	EpTitle     string `json:"ep_title"`
	QualityTag  string `json:"quality_tag"`
	Size        int64  `json:"size"`
	MtimeNS     int64  `json:"mtime_ns"`
	Container   string `json:"container"`
	Duration    float64 `json:"duration"`
	VideoCodec  string `json:"video_codec"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	BitDepth    int    `json:"bit_depth"`
	FPS         float64 `json:"fps"`
	HDR         string `json:"hdr"`
	VideoBitrate int64 `json:"video_bitrate"`
	TotalBitrate int64 `json:"total_bitrate"`
	Audio       []AudioStream `json:"audio"`
	SubCount    int    `json:"sub_count"`
	Sidecars    []Sidecar `json:"sidecars"`
	TranscodeScore float64 `json:"transcode_score"`
	RecJSON     string `json:"rec_json,omitempty"`
	Missing     bool   `json:"missing"`
	ScannedAt   string `json:"scanned_at"`
	UpdatedAt   string `json:"updated_at"`
	Interlaced  bool   `json:"interlaced"`
}

const fileCols = `id, path, library, title, year, season, episode, ep_title, quality_tag,
	size, mtime_ns, container, duration, video_codec, width, height, bit_depth, fps, hdr,
	video_bitrate, total_bitrate, audio_json, sub_count, sidecars_json, transcode_score,
	rec_json, missing, scanned_at, updated_at, interlaced`

func scanFile(row interface{ Scan(...any) error }) (*File, error) {
	f := &File{}
	var audio, sidecars string
	var missing, interlaced int
	err := row.Scan(&f.ID, &f.Path, &f.Library, &f.Title, &f.Year, &f.Season, &f.Episode,
		&f.EpTitle, &f.QualityTag, &f.Size, &f.MtimeNS, &f.Container, &f.Duration,
		&f.VideoCodec, &f.Width, &f.Height, &f.BitDepth, &f.FPS, &f.HDR,
		&f.VideoBitrate, &f.TotalBitrate, &audio, &f.SubCount, &sidecars,
		&f.TranscodeScore, &f.RecJSON, &missing, &f.ScannedAt, &f.UpdatedAt, &interlaced)
	if err != nil {
		return nil, err
	}
	f.Missing = missing != 0
	f.Interlaced = interlaced != 0
	_ = json.Unmarshal([]byte(audio), &f.Audio)
	if f.Audio == nil {
		f.Audio = []AudioStream{}
	}
	_ = json.Unmarshal([]byte(sidecars), &f.Sidecars)
	if f.Sidecars == nil {
		f.Sidecars = []Sidecar{}
	}
	return f, nil
}

// UpsertFile inserts or updates a file row (keyed by path) and replaces
// its stream rows. Called from the scanner inside one transaction.
func (s *Store) UpsertFile(f *File, streams []Stream) error {
	audio, _ := json.Marshal(f.Audio)
	sidecars, _ := json.Marshal(f.Sidecars)
	tx, err := s.dbW.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRow(`SELECT id FROM files WHERE path=?`, f.Path).Scan(&id)
	switch {
	case err == sql.ErrNoRows:
		// 25 ?s (path…rec_json), literal 0 for missing, 2 ?s for timestamps.
		res, err := tx.Exec(`INSERT INTO files(path, library, title, year, season, episode,
			ep_title, quality_tag, size, mtime_ns, container, duration, video_codec, width, height,
			bit_depth, fps, hdr, video_bitrate, total_bitrate, audio_json, sub_count, sidecars_json,
			transcode_score, rec_json, missing, scanned_at, updated_at)
			VALUES(`+strings.Repeat("?,", 25)+"0,?,?)",
			f.Path, f.Library, f.Title, f.Year, f.Season, f.Episode, f.EpTitle, f.QualityTag,
			f.Size, f.MtimeNS, f.Container, f.Duration, f.VideoCodec, f.Width, f.Height,
			f.BitDepth, f.FPS, f.HDR, f.VideoBitrate, f.TotalBitrate, string(audio), f.SubCount,
			string(sidecars), f.TranscodeScore, f.RecJSON, nowRFC(), nowRFC())
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
	case err != nil:
		return err
	default:
		_, err = tx.Exec(`UPDATE files SET library=?, title=?, year=?, season=?, episode=?,
			ep_title=?, quality_tag=?, size=?, mtime_ns=?, container=?, duration=?, video_codec=?,
			width=?, height=?, bit_depth=?, fps=?, hdr=?, video_bitrate=?, total_bitrate=?,
			audio_json=?, sub_count=?, sidecars_json=?, transcode_score=?, rec_json=?,
			missing=0, scanned_at=?, updated_at=? WHERE id=?`,
			f.Library, f.Title, f.Year, f.Season, f.Episode, f.EpTitle, f.QualityTag,
			f.Size, f.MtimeNS, f.Container, f.Duration, f.VideoCodec, f.Width, f.Height,
			f.BitDepth, f.FPS, f.HDR, f.VideoBitrate, f.TotalBitrate, string(audio), f.SubCount,
			string(sidecars), f.TranscodeScore, f.RecJSON, nowRFC(), nowRFC(), id)
		if err != nil {
			return err
		}
		_, err = tx.Exec(`DELETE FROM streams WHERE file_id=?`, id)
		if err != nil {
			return err
		}
	}
	f.ID = id
	if _, err := tx.Exec(`UPDATE files SET interlaced=? WHERE id=?`, b2i(f.Interlaced), id); err != nil {
		return err
	}
	for i := range streams {
		streams[i].FileID = id
		if _, err := tx.Exec(`INSERT INTO streams(file_id, kind, stream_index, codec, lang, title,
			channels, bit_rate, is_default, is_forced, is_text)
			VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			streams[i].FileID, streams[i].Kind, streams[i].StreamIndex, streams[i].Codec,
			streams[i].Lang, streams[i].Title, streams[i].Channels, streams[i].BitRate,
			b2i(streams[i].IsDefault), b2i(streams[i].IsForced), b2i(streams[i].IsText)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MarkMissingThisScan flags every file not seen in the latest scan pass.
// The scan pass first clears the flag on files it visits; rows still
// flagged at the end are gone from disk.
func (s *Store) BeginScanPass() error {
	_, err := s.dbW.Exec(`UPDATE files SET missing=1`)
	return err
}

func (s *Store) ClearMissing(path string) error {
	_, err := s.dbW.Exec(`UPDATE files SET missing=0 WHERE path=?`, path)
	return err
}

// SweepMissing deletes file rows (and streams via cascade) that stayed
// missing through the pass and returns how many were removed.
func (s *Store) SweepMissing() (int64, error) {
	res, err := s.dbW.Exec(`DELETE FROM files WHERE missing=1 AND scanned_at < datetime('now', '-7 days')`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// FileStat is the minimal row used for incremental scan detection.
type FileStat struct {
	Path    string
	Size    int64
	MtimeNS int64
}

// ListFileStat loads path/size/mtime for one library.
func (s *Store) ListFileStat(library string) ([]FileStat, error) {
	rows, err := s.dbR.Query(`SELECT path, size, mtime_ns FROM files WHERE library=? AND missing=0`, library)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FileStat{}
	for rows.Next() {
		var f FileStat
		if err := rows.Scan(&f.Path, &f.Size, &f.MtimeNS); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// ExecDdl runs an arbitrary statement (temp-table management for scans).
func (s *Store) ExecDdl(q string) error {
	_, err := s.dbW.Exec(q)
	return err
}

// InsertSeen bulk-inserts paths into the temp seen table.
func (s *Store) InsertSeen(paths []string) error {
	tx, err := s.dbW.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range paths {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO seen_paths(path) VALUES(?)`, p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MarkNotSeen flags every file not present in the temp seen table.
func (s *Store) MarkNotSeen() error {
	_, err := s.dbW.Exec(`UPDATE files SET missing=1, updated_at=? WHERE path NOT IN (SELECT path FROM seen_paths)`, nowRFC())
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// FileFilter describes a library browse query.
type FileFilter struct {
	Library   string // movies | tvshows | ""
	Title     string // LIKE substring
	Show      string // exact show title (tv)
	Season    int    // with Show; -1 = every season
	Codec     string
	HDR       string
	MinHeight int
	Candidates bool // transcode_score > 0 only
	MinSize   int64
	Path      string // exact path
	SeriesKey string // title for grouping
}

// Sort orders accepted by ListFiles.
var sortSQL = map[string]string{
	"title":   "title COLLATE NOCASE ASC, year ASC, season ASC, episode ASC, id ASC",
	"size":    "size DESC, id ASC",
	"savings": "(CASE WHEN json_extract(rec_json,'$.worth') THEN size - json_extract(rec_json,'$.est_out_bytes') ELSE 0 END) DESC, size DESC, id ASC",
	"bitrate": "video_bitrate DESC, id ASC",
	"added":   "mtime_ns DESC, id ASC",
	"episode": "season ASC, episode ASC, id ASC",
}

// ListFiles returns one page of files matching filter plus the total.
func (s *Store) ListFiles(f FileFilter, sort string, offset, limit int) ([]*File, int, error) {
	where, args := f.where()
	orderBy, ok := sortSQL[sort]
	if !ok {
		orderBy = sortSQL["title"]
		if f.Show != "" {
			orderBy = sortSQL["episode"]
		}
	}
	if limit <= 0 || limit > 2000 {
		limit = 60
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := s.dbR.QueryRow(`SELECT COUNT(*) FROM files WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := s.dbR.Query(`SELECT `+fileCols+` FROM files WHERE `+where+`
		ORDER BY `+orderBy+` LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []*File{}
	for rows.Next() {
		fl, err := scanFile(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, fl)
	}
	return out, total, rows.Err()
}

// EachFile streams every present file (recommendation refresh).
func (s *Store) EachFile(fn func(*File) error) error {
	rows, err := s.dbR.Query(`SELECT ` + fileCols + ` FROM files WHERE missing=0`)
	if err != nil {
		return err
	}
	var all []*File
	for rows.Next() {
		fl, err := scanFile(rows)
		if err != nil {
			rows.Close()
			return err
		}
		all = append(all, fl)
	}
	rows.Close()
	for _, fl := range all {
		if err := fn(fl); err != nil {
			return err
		}
	}
	return nil
}

// UpdateRecs writes refreshed recommendation caches in one transaction.
func (s *Store) UpdateRecs(recs map[int64][2]any) error {
	tx, err := s.dbW.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, v := range recs {
		if _, err := tx.Exec(`UPDATE files SET transcode_score=?, rec_json=? WHERE id=?`, v[0], v[1], id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (f FileFilter) where() (string, []any) {
	w := []string{"missing=0"}
	var a []any
	if f.Library != "" {
		w = append(w, "library=?")
		a = append(a, f.Library)
	}
	if f.Title != "" {
		w = append(w, "title LIKE ? COLLATE NOCASE")
		a = append(a, "%"+escapeLike(f.Title)+"%")
	}
	if f.Show != "" {
		w = append(w, "library='tvshows' AND title=? COLLATE NOCASE")
		a = append(a, f.Show)
		if f.Season >= 0 {
			w = append(w, "season=?")
			a = append(a, f.Season)
		}
	}
	if f.Codec != "" {
		w = append(w, "video_codec=?")
		a = append(a, f.Codec)
	}
	if f.HDR != "" {
		w = append(w, "hdr=?")
		a = append(a, f.HDR)
	}
	if f.MinHeight > 0 {
		w = append(w, "height>=?")
		a = append(a, f.MinHeight)
	}
	if f.Candidates {
		w = append(w, "transcode_score>0")
	}
	if f.MinSize > 0 {
		w = append(w, "size>=?")
		a = append(a, f.MinSize)
	}
	if f.Path != "" {
		w = append(w, "path=?")
		a = append(a, f.Path)
	}
	return strings.Join(w, " AND "), a
}

func escapeLike(s string) string { return strings.ReplaceAll(s, "%", "\\%") }

// CountFiles counts matches for a filter.
func (s *Store) CountFiles(f FileFilter) (int, error) {
	where, args := f.where()
	var n int
	err := s.dbR.QueryRow(`SELECT COUNT(*) FROM files WHERE `+where, args...).Scan(&n)
	return n, err
}

// GetFile fetches one file by id.
func (s *Store) GetFile(id int64) (*File, error) {
	row := s.dbR.QueryRow(`SELECT `+fileCols+` FROM files WHERE id=?`, id)
	f, err := scanFile(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return f, err
}

// GetFileByPath fetches one file by absolute path.
func (s *Store) GetFileByPath(p string) (*File, error) {
	row := s.dbR.QueryRow(`SELECT `+fileCols+` FROM files WHERE path=?`, p)
	f, err := scanFile(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return f, err
}

// Streams returns the stream rows for a file.
func (s *Store) Streams(fileID int64) ([]Stream, error) {
	rows, err := s.dbR.Query(`SELECT id, file_id, kind, stream_index, codec, lang, title,
		channels, bit_rate, is_default, is_forced, is_text FROM streams WHERE file_id=?
		ORDER BY stream_index`, fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Stream{}
	for rows.Next() {
		var st Stream
		var d, fo, tx int
		if err := rows.Scan(&st.ID, &st.FileID, &st.Kind, &st.StreamIndex, &st.Codec, &st.Lang,
			&st.Title, &st.Channels, &st.BitRate, &d, &fo, &tx); err != nil {
			return nil, err
		}
		st.IsDefault, st.IsForced, st.IsText = d != 0, fo != 0, tx != 0
		out = append(out, st)
	}
	return out, rows.Err()
}

// Stream is one embedded stream row.
type Stream struct {
	ID          int64  `json:"id"`
	FileID      int64  `json:"file_id"`
	Kind        string `json:"kind"`
	StreamIndex int    `json:"stream_index"`
	Codec       string `json:"codec"`
	Lang        string `json:"lang"`
	Title       string `json:"title"`
	Channels    int    `json:"channels"`
	BitRate     int64  `json:"bit_rate"`
	IsDefault   bool   `json:"default"`
	IsForced    bool   `json:"forced"`
	IsText      bool   `json:"is_text"`
}

// ---- TV aggregation ----

const worthExpr = `CASE WHEN json_extract(f.rec_json,'$.worth') THEN 1 ELSE 0 END`
const savedExpr = `CASE WHEN json_extract(f.rec_json,'$.worth') THEN f.size - json_extract(f.rec_json,'$.est_out_bytes') ELSE 0 END`

// Series is an aggregated show rollup for browsing.
type Series struct {
	Title       string `json:"title"`
	Year        int    `json:"year"`
	Episodes    int    `json:"episodes"`
	Seasons     int    `json:"seasons"`
	TotalSize   int64  `json:"total_size"`
	AvgBitrate  int64  `json:"avg_bitrate"`
	Codecs      string `json:"codecs"`
	Height      int    `json:"height"`
	WorthCount  int    `json:"worth_count"`
	Reclaimable int64  `json:"reclaimable"`
	SeriesID    string `json:"series_id,omitempty"`
	Overview    string `json:"overview,omitempty"`
}

var seriesSort = map[string]string{
	"title":       "f.title COLLATE NOCASE",
	"reclaimable": "11 DESC, f.title COLLATE NOCASE",
	"size":        "6 DESC, f.title COLLATE NOCASE",
	"episodes":    "4 DESC, f.title COLLATE NOCASE",
}

// ListSeries returns per-show aggregates for the tvshows library.
func (s *Store) ListSeries(titleLike, sort string, onlyWorth bool) ([]Series, error) {
	where, args := "f.missing=0 AND f.library='tvshows'", []any{}
	if titleLike != "" {
		where += " AND f.title LIKE ? COLLATE NOCASE"
		args = append(args, "%"+escapeLike(titleLike)+"%")
	}
	order, ok := seriesSort[sort]
	if !ok {
		order = seriesSort["title"]
	}
	having := ""
	if onlyWorth {
		having = " HAVING SUM(" + worthExpr + ") > 0"
	}
	rows, err := s.dbR.Query(`SELECT f.title, MAX(f.year), COUNT(DISTINCT f.season), COUNT(*),
		COALESCE(MAX(j.series_id),''), SUM(f.size),
		CAST(COALESCE(AVG(NULLIF(f.video_bitrate,0)),0) AS INTEGER),
		COALESCE(GROUP_CONCAT(DISTINCT f.video_codec),''), MAX(f.height),
		SUM(`+worthExpr+`), CAST(SUM(`+savedExpr+`) AS INTEGER)
		FROM files f LEFT JOIN jellyfin j ON j.path=f.path
		WHERE `+where+` GROUP BY f.title COLLATE NOCASE`+having+` ORDER BY `+order, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Series{}
	for rows.Next() {
		var se Series
		if err := rows.Scan(&se.Title, &se.Year, &se.Seasons, &se.Episodes, &se.SeriesID,
			&se.TotalSize, &se.AvgBitrate, &se.Codecs, &se.Height, &se.WorthCount, &se.Reclaimable); err != nil {
			return nil, err
		}
		out = append(out, se)
	}
	return out, rows.Err()
}

// SeasonStat is one season of a show.
type SeasonStat struct {
	Season      int    `json:"season"`
	Episodes    int    `json:"episodes"`
	TotalSize   int64  `json:"total_size"`
	AvgBitrate  int64  `json:"avg_bitrate"`
	Height      int    `json:"height"`
	Codecs      string `json:"codecs"`
	WorthCount  int    `json:"worth_count"`
	Reclaimable int64  `json:"reclaimable"`
	SeasonID    string `json:"season_id,omitempty"`
}

// ListSeasons returns per-season aggregates for a show.
func (s *Store) ListSeasons(show string) ([]SeasonStat, error) {
	rows, err := s.dbR.Query(`SELECT f.season, COUNT(*), SUM(f.size),
		CAST(COALESCE(AVG(NULLIF(f.video_bitrate,0)),0) AS INTEGER), MAX(f.height),
		COALESCE(GROUP_CONCAT(DISTINCT f.video_codec),''),
		SUM(`+worthExpr+`), CAST(SUM(`+savedExpr+`) AS INTEGER), COALESCE(MAX(j.season_id),'')
		FROM files f LEFT JOIN jellyfin j ON j.path=f.path
		WHERE f.missing=0 AND f.library='tvshows' AND f.title=? COLLATE NOCASE
		GROUP BY f.season ORDER BY f.season`, show)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SeasonStat{}
	for rows.Next() {
		var se SeasonStat
		if err := rows.Scan(&se.Season, &se.Episodes, &se.TotalSize, &se.AvgBitrate, &se.Height,
			&se.Codecs, &se.WorthCount, &se.Reclaimable, &se.SeasonID); err != nil {
			return nil, err
		}
		out = append(out, se)
	}
	return out, rows.Err()
}

// LibraryStat summarizes a library for the dashboard.
type LibraryStat struct {
	Files int `json:"files"`
	TotalSize int64 `json:"total_size"`
	TranscodableSize int64 `json:"transcodable_size"`
	ProjectedSaved int64 `json:"projected_saved"`
}

// LibraryStats computes whole-library stats including projected savings
// from cached recommendations.
func (s *Store) LibraryStats(lib string) (LibraryStat, error) {
	var st LibraryStat
	err := s.dbR.QueryRow(`SELECT COUNT(*), COALESCE(SUM(size),0) FROM files
		WHERE missing=0 AND library=?`, lib).Scan(&st.Files, &st.TotalSize)
	if err != nil {
		return st, err
	}
	// Projected savings from cached recs: sum(size - est_out) where rec says worth it.
	rows, err := s.dbR.Query(`SELECT size, rec_json FROM files
		WHERE missing=0 AND library=? AND rec_json != ''`, lib)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var size int64
		var rec string
		if err := rows.Scan(&size, &rec); err != nil {
			return st, err
		}
		var r struct {
			Worth bool  `json:"worth"`
			EstOut int64 `json:"est_out_bytes"`
		}
		if json.Unmarshal([]byte(rec), &r) == nil && r.Worth && r.EstOut > 0 && r.EstOut < size {
			st.TranscodableSize += size
			st.ProjectedSaved += size - r.EstOut
		}
	}
	return st, rows.Err()
}
