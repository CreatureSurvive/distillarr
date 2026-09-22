package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"mediatrans/internal/res"
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

	// Active picture inside the frame when black bars are encoded in it
	// (0 = no bars found or not checked yet).
	CropW       int  `json:"crop_w"`
	CropH       int  `json:"crop_h"`
	CropX       int  `json:"crop_x"`
	CropY       int  `json:"crop_y"`
	CropChecked bool `json:"crop_checked"`

	// TuneJSON is the last VMAF quality search for this file.
	TuneJSON string `json:"-"`

	VideoTag    string `json:"video_tag"`    // codec_tag_string: hvc1 / hev1 / avc1 ...
	Faststart   int    `json:"faststart"`    // MP4 only: 1 moov first, 0 not, -1 n/a or unknown
	MetaChecked bool   `json:"-"`
	Issues      string `json:"issues"` // ",no_hvc1,pcm_audio," (see internal/issues)
}

// HasBars reports detected black bars inside the encoded frame.
func (f *File) HasBars() bool {
	return f.CropW > 0 && f.CropH > 0 && (f.CropW < f.Width || f.CropH < f.Height)
}

// Active returns the real picture size (frame minus encoded black bars).
func (f *File) Active() (int, int) {
	if f.HasBars() {
		return f.CropW, f.CropH
	}
	return f.Width, f.Height
}

// CropRect is the ffmpeg crop "w:h:x:y" for the bars ("" = none).
func (f *File) CropRect() string {
	if !f.HasBars() {
		return ""
	}
	return fmt.Sprintf("%d:%d:%d:%d", f.CropW, f.CropH, f.CropX, f.CropY)
}

const fileCols = `id, path, library, title, year, season, episode, ep_title, quality_tag,
	size, mtime_ns, container, duration, video_codec, width, height, bit_depth, fps, hdr,
	video_bitrate, total_bitrate, audio_json, sub_count, sidecars_json, transcode_score,
	rec_json, missing, scanned_at, updated_at, interlaced, crop_w, crop_h, crop_x, crop_y, crop_checked, tune_json,
	video_tag, faststart, meta_checked, issues`

func scanFile(row interface{ Scan(...any) error }) (*File, error) {
	f := &File{}
	var audio, sidecars string
	var missing, interlaced, cropChecked, metaChecked int
	err := row.Scan(&f.ID, &f.Path, &f.Library, &f.Title, &f.Year, &f.Season, &f.Episode,
		&f.EpTitle, &f.QualityTag, &f.Size, &f.MtimeNS, &f.Container, &f.Duration,
		&f.VideoCodec, &f.Width, &f.Height, &f.BitDepth, &f.FPS, &f.HDR,
		&f.VideoBitrate, &f.TotalBitrate, &audio, &f.SubCount, &sidecars,
		&f.TranscodeScore, &f.RecJSON, &missing, &f.ScannedAt, &f.UpdatedAt, &interlaced,
		&f.CropW, &f.CropH, &f.CropX, &f.CropY, &cropChecked, &f.TuneJSON,
		&f.VideoTag, &f.Faststart, &metaChecked, &f.Issues)
	if err != nil {
		return nil, err
	}
	f.Missing = missing != 0
	f.Interlaced = interlaced != 0
	f.CropChecked = cropChecked != 0
	f.MetaChecked = metaChecked != 0
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
	// A (re)probed file is a new picture: bars must be detected again.
	if _, err := tx.Exec(`UPDATE files SET interlaced=?, crop_w=0, crop_h=0, crop_x=0, crop_y=0,
		crop_checked=0, tune_json='', video_tag=?, faststart=?, meta_checked=?, issues=? WHERE id=?`,
		b2i(f.Interlaced), f.VideoTag, f.Faststart, b2i(f.MetaChecked), f.Issues, id); err != nil {
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

// CropTodo is a file awaiting black-bar detection.
type CropTodo struct {
	ID              int64
	Path            string
	Width, Height   int
	Duration        float64
}

// FilesNeedingCrop lists unchecked files, re-encode candidates first
// (their estimates and quality depend on it most).
func (s *Store) FilesNeedingCrop(limit int) ([]CropTodo, error) {
	rows, err := s.dbR.Query(`SELECT id, path, width, height, duration FROM files
		WHERE missing=0 AND crop_checked=0 AND width>0 AND duration>60
		ORDER BY (video_codec IN ('hevc','av1')), transcode_score DESC, id LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CropTodo
	for rows.Next() {
		var t CropTodo
		if err := rows.Scan(&t.ID, &t.Path, &t.Width, &t.Height, &t.Duration); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetCrop stores detection results (zeros = no bars).
func (s *Store) SetCrop(id int64, w, h, x, y int) error {
	_, err := s.dbW.Exec(`UPDATE files SET crop_w=?, crop_h=?, crop_x=?, crop_y=?, crop_checked=1 WHERE id=?`,
		w, h, x, y, id)
	return err
}

// SetTune stores a file's VMAF quality search result.
func (s *Store) SetTune(id int64, tuneJSON string) error {
	_, err := s.dbW.Exec(`UPDATE files SET tune_json=? WHERE id=?`, tuneJSON, id)
	return err
}

// ClearMeasurements resets every file's quality search result, so the
// overnight loop and future encodes re-measure with the current
// sampling/search method.
func (s *Store) ClearMeasurements() (int64, error) {
	return execChanges(s.dbW, `UPDATE files SET tune_json='' WHERE tune_json!=''`)
}

// ClearCrop re-queues every checked file for black-bar detection.
func (s *Store) ClearCrop() (int64, error) {
	return execChanges(s.dbW, `UPDATE files SET crop_checked=0, crop_w=0, crop_h=0, crop_x=0, crop_y=0 WHERE crop_checked!=0`)
}

// ClearIssueTags re-queues every checked file for container-tag
// re-probing (hvc1/faststart) and clears the cached issue list so it
// doesn't keep showing stale results until the backfill catches up.
func (s *Store) ClearIssueTags() (int64, error) {
	return execChanges(s.dbW, `UPDATE files SET meta_checked=0, video_tag='', faststart=-1, issues='' WHERE meta_checked!=0`)
}

func execChanges(w *sql.DB, query string) (int64, error) {
	res, err := w.Exec(query)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CropProgress reports checked / total present files.
func (s *Store) CropProgress() (checked, total, withBars int) {
	_ = s.dbR.QueryRow(`SELECT COALESCE(SUM(crop_checked),0), COUNT(*),
		COALESCE(SUM(crop_w>0),0) FROM files WHERE missing=0`).Scan(&checked, &total, &withBars)
	return
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

	Container  string // exact container (mkv, mp4, avi ...); "legacy" = old containers
	AudioCodec string // any audio track: pcm, aac, ac3, eac3, dts, truehd, flac, opus ...
	Issue      string // issue key (see internal/issues)
	ResClass   int    // nominal class (res.Class): 480, 576, 720, 1080, 2160
	Upscale    string // "upscaled": made by an upscale job; "upscalable": below 4K and not one
}

// resClassSQL mirrors res.Class in SQL.
const resClassSQL = `(CASE WHEN width<=0 AND height<=0 THEN 0
	WHEN width>=3200 OR height>=1800 THEN 2160
	WHEN width>=1700 OR height>=1000 THEN 1080
	WHEN width>=1180 OR height>=700 THEN 720
	WHEN height>500 OR width>860 THEN 576 ELSE 480 END)`

// LegacyContainers / LegacyVideoCodecs are filter groups ("legacy").
var LegacyContainers = []string{"avi", "wmv", "asf", "flv", "mpg", "mpeg", "ts", "m2ts", "vob", "divx", "ogm", "rm", "rmvb", "3gp"}
var LegacyVideoCodecs = []string{"mpeg1video", "mpeg2video", "mpeg4", "msmpeg4v1", "msmpeg4v2", "msmpeg4v3",
	"wmv1", "wmv2", "wmv3", "vc1", "h263", "rv30", "rv40", "vp6", "vp6f", "vp8", "theora", "flv1"}

func inList(col string, vals []string) (string, []any) {
	a := make([]any, len(vals))
	for i, v := range vals {
		a[i] = v
	}
	return col + " IN (" + strings.TrimSuffix(strings.Repeat("?,", len(vals)), ",") + ")", a
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

// RecUpdate is a refreshed recommendation + issues for one file.
type RecUpdate struct {
	Score  float64
	Rec    string
	Issues string
}

// UpdateRecs writes refreshed recommendation caches in one transaction.
func (s *Store) UpdateRecs(recs map[int64]RecUpdate) error {
	tx, err := s.dbW.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, v := range recs {
		if _, err := tx.Exec(`UPDATE files SET transcode_score=?, rec_json=?, issues=? WHERE id=?`,
			v.Score, v.Rec, v.Issues, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// MetaTodo is a file whose container facts haven't been read yet.
type MetaTodo struct {
	ID        int64
	Path      string
	Container string
}

// FilesNeedingMeta lists files without video tag / faststart facts.
func (s *Store) FilesNeedingMeta(limit int) ([]MetaTodo, error) {
	rows, err := s.dbR.Query(`SELECT id, path, container FROM files WHERE missing=0 AND meta_checked=0 LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MetaTodo
	for rows.Next() {
		var t MetaTodo
		if err := rows.Scan(&t.ID, &t.Path, &t.Container); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetMeta stores container facts.
func (s *Store) SetMeta(id int64, tag string, faststart int) error {
	_, err := s.dbW.Exec(`UPDATE files SET video_tag=?, faststart=?, meta_checked=1 WHERE id=?`, tag, faststart, id)
	return err
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
	switch f.Codec {
	case "":
	case "legacy":
		q, qa := inList("video_codec", LegacyVideoCodecs)
		w, a = append(w, q), append(a, qa...)
	default:
		w = append(w, "video_codec=?")
		a = append(a, f.Codec)
	}
	switch f.Container {
	case "":
	case "legacy":
		q, qa := inList("container", LegacyContainers)
		w, a = append(w, q), append(a, qa...)
	case "mp4":
		w = append(w, "container IN ('mp4','m4v','mov')")
	default:
		w = append(w, "container=?")
		a = append(a, f.Container)
	}
	if f.AudioCodec != "" {
		// audio_json holds "codec":"x"; pcm/dts families match by prefix.
		if f.AudioCodec == "pcm" || f.AudioCodec == "dts" {
			w = append(w, "audio_json LIKE ?")
			a = append(a, `%"codec":"`+f.AudioCodec+`%`)
		} else {
			w = append(w, "audio_json LIKE ?")
			a = append(a, `%"codec":"`+f.AudioCodec+`"%`)
		}
	}
	if f.Issue != "" {
		w = append(w, "issues LIKE ?")
		a = append(a, "%,"+f.Issue+",%")
	}
	if f.ResClass > 0 {
		w = append(w, resClassSQL+"=?")
		a = append(a, f.ResClass)
	}
	switch f.Upscale {
	case "upscaled":
		w = append(w, "path IN ("+upscaleOutputs+")")
	case "upscalable":
		w = append(w, "path NOT IN ("+upscaleOutputs+")", resClassSQL+"<2160", "height>0")
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
		COALESCE(GROUP_CONCAT(DISTINCT f.video_codec),''), MAX(f.width), MAX(f.height),
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
		var w, h int
		if err := rows.Scan(&se.Title, &se.Year, &se.Seasons, &se.Episodes, &se.SeriesID,
			&se.TotalSize, &se.AvgBitrate, &se.Codecs, &w, &h, &se.WorthCount, &se.Reclaimable); err != nil {
			return nil, err
		}
		se.Height = res.Class(w, h)
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
		CAST(COALESCE(AVG(NULLIF(f.video_bitrate,0)),0) AS INTEGER), MAX(f.width), MAX(f.height),
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
		var w, h int
		if err := rows.Scan(&se.Season, &se.Episodes, &se.TotalSize, &se.AvgBitrate, &w, &h,
			&se.Codecs, &se.WorthCount, &se.Reclaimable, &se.SeasonID); err != nil {
			return nil, err
		}
		se.Height = res.Class(w, h)
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

// MeasureCandidates lists never-measured files whose estimate is at or
// near the savings threshold (minPct), biggest first — the files where
// a real measurement changes decisions the most.
func (s *Store) MeasureCandidates(minPct float64, limit int) ([]*File, error) {
	rows, err := s.dbR.Query(`SELECT `+fileCols+` FROM files
		WHERE missing=0 AND tune_json='' AND rec_json!='' AND duration>=120
		AND json_extract(rec_json,'$.action')!='caution'
		AND json_extract(rec_json,'$.savings_pct')>=?
		ORDER BY size DESC LIMIT ?`, minPct, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*File{}
	for rows.Next() {
		fl, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, fl)
	}
	return out, rows.Err()
}

// MeasureProgress counts measured files and remaining candidates.
func (s *Store) MeasureProgress(minPct float64) (measured, remaining int) {
	_ = s.dbR.QueryRow(`SELECT COALESCE(SUM(tune_json!=''),0),
		COALESCE(SUM(tune_json='' AND rec_json!='' AND duration>=120
			AND json_extract(rec_json,'$.action')!='caution'
			AND json_extract(rec_json,'$.savings_pct')>=?),0)
		FROM files WHERE missing=0`, minPct).Scan(&measured, &remaining)
	return
}
