// Package replace verifies encoded output and swaps it into place,
// preserving original timestamps. Ordering is crash-safe on the
// mergerfs pool: the original is duplicated into trash BEFORE the
// rename, so a crash mid-rename can never lose data.
//
// Creation-time honesty: Linux offers no API to write ext4 crtime, so
// the replaced file's creation time becomes the encode completion
// time. mtime and atime are fully restored. (The optional Jellyfin
// integration patches DateCreated back via API.)
package replace

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"mediatrans/internal/media"
)

// SrcStat is the pre-encode statx snapshot persisted with the job.
type SrcStat struct {
	MtimeSec  int64  `json:"mtime_sec"`
	MtimeNsec int64  `json:"mtime_nsec"`
	AtimeSec  int64  `json:"atime_sec"`
	AtimeNsec int64  `json:"atime_nsec"`
	BtimeSec  int64  `json:"btime_sec"`
	BtimeNsec int64  `json:"btime_nsec"`
	Size      int64  `json:"size"`
	Ino       uint64 `json:"ino"`
}

// Snapshot captures statx info including birth time when available.
func Snapshot(path string) (*SrcStat, error) {
	var st unix.Statx_t
	err := unix.Statx(0, path, 0, unix.STATX_ALL, &st)
	if err != nil {
		// Fallback to plain stat (no birth time).
		fi, err2 := os.Stat(path)
		if err2 != nil {
			return nil, fmt.Errorf("stat %s: %w (statx: %v)", path, err2, err)
		}
		mt := fi.ModTime()
		return &SrcStat{
			MtimeSec: mt.Unix(), MtimeNsec: int64(mt.Nanosecond()),
			AtimeSec: mt.Unix(), AtimeNsec: int64(mt.Nanosecond()),
			Size: fi.Size(),
		}, nil
	}
	s := &SrcStat{Size: int64(st.Size), Ino: st.Ino}
	s.MtimeSec, s.MtimeNsec = int64(st.Mtime.Sec), int64(st.Mtime.Nsec)
	s.AtimeSec, s.AtimeNsec = int64(st.Atime.Sec), int64(st.Atime.Nsec)
	s.BtimeSec, s.BtimeNsec = int64(st.Btime.Sec), int64(st.Btime.Nsec)
	return s, nil
}

// RestoreTimes puts the original atime+mtime back on path.
func RestoreTimes(path string, s *SrcStat) error {
	times := []unix.Timespec{
		{Sec: s.AtimeSec, Nsec: s.AtimeNsec},
		{Sec: s.MtimeSec, Nsec: s.MtimeNsec},
	}
	return unix.UtimesNanoAt(unix.AT_FDCWD, path, times, 0)
}

// VerifySpec describes what a good output looks like.
type VerifySpec struct {
	WantVideoCodec string  // "hevc" | "av1"
	Want10Bit      bool
	SrcDuration    float64
	SrcAudioCount  int
	WantSubCount   int // exact expected (container-aware)
	SrcSize        int64
}

// Verify probes the temp file and enforces output sanity. Returns
// warnings (non-fatal) — e.g. output nearly as large as source.
func Verify(ctx context.Context, tempPath string, spec VerifySpec) ([]string, error) {
	p, err := media.ProbeFile(ctx, tempPath)
	if err != nil {
		return nil, fmt.Errorf("output probe failed: %w", err)
	}
	var warns []string

	dur := p.DurationSec()
	delta := dur - spec.SrcDuration
	if delta < 0 {
		delta = -delta
	}
	tol := spec.SrcDuration * 0.01
	if tol < 2 {
		tol = 2
	}
	if delta > tol {
		return nil, fmt.Errorf("duration mismatch: %.1fs vs source %.1fs (tol %.1fs)",
			dur, spec.SrcDuration, tol)
	}

	v := p.Video()
	if v == nil {
		return nil, fmt.Errorf("no video stream in output")
	}
	if v.CodecName != spec.WantVideoCodec {
		return nil, fmt.Errorf("output video is %s, wanted %s", v.CodecName, spec.WantVideoCodec)
	}
	if spec.Want10Bit && v.BitDepth() < 10 {
		return nil, fmt.Errorf("output is %d-bit, wanted 10-bit (pix_fmt %s)", v.BitDepth(), v.PixFmt)
	}
	if n := len(p.Audios()); n < spec.SrcAudioCount {
		return nil, fmt.Errorf("audio streams %d < source %d", n, spec.SrcAudioCount)
	}
	if n := len(p.Subtitles()); n != spec.WantSubCount {
		return nil, fmt.Errorf("subtitle streams %d != expected %d", n, spec.WantSubCount)
	}

	fi, err := os.Stat(tempPath)
	if err != nil {
		return nil, err
	}
	if fi.Size() < 2*1024*1024 || fi.Size() < spec.SrcSize*5/100 {
		return nil, fmt.Errorf("output implausibly small: %d bytes", fi.Size())
	}
	if fi.Size() >= spec.SrcSize*95/100 {
		warns = append(warns, fmt.Sprintf("output is %.1f%% of source size — barely worth it",
			100*float64(fi.Size())/float64(spec.SrcSize)))
	}

	// Mid-file decode spot check catches truncated middles.
	if err := spotCheck(ctx, tempPath); err != nil {
		return nil, fmt.Errorf("mid-file decode check failed: %w", err)
	}
	return warns, nil
}

// spotCheck decodes ~200 frames from ~5% into the file.
func spotCheck(ctx context.Context, path string) error {
	cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, media.FFprobe, "-v", "error",
		"-read_intervals", "5%+#200", "-select_streams", "v",
		"-show_frames", "-show_entries", "frame=pts_time", "-of", "csv=p=0", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 200 {
			msg = msg[len(msg)-200:]
		}
		return fmt.Errorf("%v: %s", err, msg)
	}
	return nil
}

// Replace performs the crash-safe swap:
// fsync temp → trash-link original → rename → restore times → fsync dir.
// destPath is normally srcPath; it differs only when the container
// changed (e.g. .avi → .mkv), in which case the original path is
// removed after the new file is in place (the trash copy already
// preserves it). trashDir == "" skips retention (not recommended).
func Replace(tempPath, srcPath, destPath string, st *SrcStat, trashDir string) error {
	if destPath == "" {
		destPath = srcPath
	}
	// 1. Make the encoded file durable.
	f, err := os.Open(tempPath)
	if err != nil {
		return err
	}
	_ = unix.Fsync(int(f.Fd()))
	f.Close()

	// 2. Retain the original first (crash-safety anchor).
	if trashDir != "" {
		if err := retain(srcPath, trashDir, st); err != nil {
			return fmt.Errorf("trash retention: %w", err)
		}
	}

	// 3. Rename the temp onto its final name. (On mergerfs with
	// create=mfs this may be a copy-then-unlink across branches —
	// data-safe because step 2 already duplicated the original.)
	if err := os.Rename(tempPath, destPath); err != nil {
		return fmt.Errorf("rename: %w", err)
	}

	// 4. When the container changed, drop the old name (trash has it).
	if destPath != srcPath {
		if err := os.Remove(srcPath); err != nil {
			return fmt.Errorf("remove old name %s: %w", srcPath, err)
		}
	}

	// 5. Restore original atime+mtime.
	if st != nil {
		if err := RestoreTimes(destPath, st); err != nil {
			return fmt.Errorf("restore timestamps: %w", err)
		}
	}

	// 6. Make the directory entry durable.
	if d, err := os.Open(filepath.Dir(destPath)); err == nil {
		_ = unix.Fsync(int(d.Fd()))
		d.Close()
	}
	return nil
}

// retain hard-links the original into the trash tree; falls back to a
// streamed copy when the link crosses mergerfs branches (EXDEV).
func retain(srcPath, trashDir string, st *SrcStat) error {
	rel := strings.TrimPrefix(srcPath, "/")
	dst := filepath.Join(trashDir, rel)
	if _, err := os.Stat(dst); err == nil {
		return nil // already retained (job retry)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := os.Link(srcPath, dst); err == nil {
		return nil
	}
	// Cross-branch link (or any link failure) → durable copy.
	return copyFile(srcPath, dst, st)
}

func copyFile(src, dst string, st *SrcStat) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if st != nil {
		_ = RestoreTimes(tmp, st)
	}
	return os.Rename(tmp, dst)
}

// LinkSidecars hard-links external subtitle files to a new stem when
// the media file's extension changed (e.g. .mp4 → .mkv), so players
// keep finding them for BOTH filenames. Originals are untouched.
func LinkSidecars(oldPath, newPath string) error {
	oldExt := strings.ToLower(filepath.Ext(oldPath))
	newExt := strings.ToLower(filepath.Ext(newPath))
	if oldExt == newExt {
		return nil
	}
	dir := filepath.Dir(oldPath)
	stem := strings.TrimSuffix(filepath.Base(oldPath), filepath.Ext(oldPath))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		switch ext {
		case ".srt", ".ass", ".ssa", ".sub", ".idx", ".vtt", ".sup", ".smi":
		default:
			continue
		}
		s := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		if s != stem && !strings.HasPrefix(s, stem+".") {
			continue
		}
		newName := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name())) + newExt
		dst := filepath.Join(dir, newName)
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		_ = os.Link(filepath.Join(dir, e.Name()), dst) // best effort
	}
	return nil
}
