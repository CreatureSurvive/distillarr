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
	Mode      uint32 `json:"mode"` // permission bits, restored on the output
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
	s := &SrcStat{Size: int64(st.Size), Ino: st.Ino, Mode: uint32(st.Mode)}
	s.MtimeSec, s.MtimeNsec = int64(st.Mtime.Sec), int64(st.Mtime.Nsec)
	s.AtimeSec, s.AtimeNsec = int64(st.Atime.Sec), int64(st.Atime.Nsec)
	s.BtimeSec, s.BtimeNsec = int64(st.Btime.Sec), int64(st.Btime.Nsec)
	return s, nil
}

// SameSource reports whether two snapshots of the same path describe the
// same file content: an untouched source has the same size, mtime and
// inode. atime is excluded on purpose — reading the file (ffprobe, a
// preview) changes atime without changing the file. Used right before a
// replace to catch a source that Sonarr/Radarr (or anything else)
// overwrote while the encode was running.
func SameSource(orig, now *SrcStat) bool {
	if orig == nil || now == nil {
		return false
	}
	return orig.Size == now.Size &&
		orig.MtimeSec == now.MtimeSec && orig.MtimeNsec == now.MtimeNsec &&
		orig.Ino == now.Ino
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
	WantAudioCount int    // exact, from the stream plan
	WantSubCount   int    // exact, from the stream plan
	WantTransfer   string // e.g. smpte2084 for HDR10 passthrough ("" = don't check)
	Container      string // "mp4" → also require moov-before-mdat and hvc1 for HEVC
	SrcSize        int64

	// Upscale jobs. WantWidth/WantHeight is the exact expected picture size (0 =
	// don't check): a silently skipped upscale filter still yields a valid file.
	// ColorRef is the source whose mean chroma the output must match: a broken
	// GPU readback returns clean, correctly sized video with the colour gone.
	WantWidth, WantHeight int
	ColorRef              string
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
	if spec.WantWidth > 0 && (v.Width != spec.WantWidth || v.Height != spec.WantHeight) {
		return nil, fmt.Errorf("output is %dx%d, wanted %dx%d (the upscale did not apply)",
			v.Width, v.Height, spec.WantWidth, spec.WantHeight)
	}
	if spec.ColorRef != "" {
		if err := checkChroma(ctx, spec.ColorRef, tempPath, dur); err != nil {
			return nil, err
		}
	}
	if n := len(p.Audios()); n != spec.WantAudioCount {
		return nil, fmt.Errorf("audio streams %d != expected %d", n, spec.WantAudioCount)
	}
	if spec.WantTransfer != "" && v.ColorTransfer != spec.WantTransfer {
		return nil, fmt.Errorf("colour transfer is %q, expected %q (HDR signalling lost)", v.ColorTransfer, spec.WantTransfer)
	}
	if n := len(p.Subtitles()); n != spec.WantSubCount {
		return nil, fmt.Errorf("subtitle streams %d != expected %d", n, spec.WantSubCount)
	}

	if spec.Container == "mp4" {
		if spec.WantVideoCodec == "hevc" && v.CodecTagString != "hvc1" {
			return nil, fmt.Errorf("HEVC in MP4 is tagged %q, Apple players need hvc1", v.CodecTagString)
		}
		if ok, err := media.MoovFirst(tempPath); err != nil {
			return nil, err
		} else if !ok {
			return nil, fmt.Errorf("moov atom is after the media data (faststart failed)")
		}
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
func Replace(tempPath, srcPath, destPath string, st *SrcStat, trashDir string) (trashPath string, err error) {
	if destPath == "" {
		destPath = srcPath
	}
	// 1. Make the encoded file durable.
	f, err := os.Open(tempPath)
	if err != nil {
		return "", err
	}
	_ = unix.Fsync(int(f.Fd()))
	f.Close()

	// 2. Retain the original first (crash-safety anchor).
	if trashDir != "" {
		trashPath = TrashPath(trashDir, srcPath)
		if err := retain(srcPath, trashPath, st); err != nil {
			return "", fmt.Errorf("trash retention: %w", err)
		}
	}

	// 3. Rename the temp onto its final name. (On mergerfs with
	// create=mfs this may be a copy-then-unlink across branches —
	// data-safe because step 2 already duplicated the original.)
	if err := os.Rename(tempPath, destPath); err != nil {
		return trashPath, fmt.Errorf("rename: %w", err)
	}

	// 3b. Restore the original permission bits (Samba-writable group
	// bits etc.); ffmpeg creates the temp with default umask.
	if st != nil && st.Mode != 0 {
		_ = os.Chmod(destPath, os.FileMode(st.Mode&0o7777))
	}

	// 4. When the container changed, drop the old name (trash has it).
	if destPath != srcPath {
		if err := os.Remove(srcPath); err != nil {
			return trashPath, fmt.Errorf("remove old name %s: %w", srcPath, err)
		}
	}

	// 5. Restore original atime+mtime.
	if st != nil {
		if err := RestoreTimes(destPath, st); err != nil {
			return trashPath, fmt.Errorf("restore timestamps: %w", err)
		}
	}

	// 6. Make the directory entry durable.
	if d, err := os.Open(filepath.Dir(destPath)); err == nil {
		_ = unix.Fsync(int(d.Fd()))
		d.Close()
	}
	return trashPath, nil
}

// TrashPath maps an original to its place in the trash tree, keeping
// the path relative to the trash dir's parent (the media pool root)
// so the link stays on the same mergerfs branch and is instant.
func TrashPath(trashDir, srcPath string) string {
	root := filepath.Dir(filepath.Clean(trashDir))
	rel := strings.TrimPrefix(srcPath, "/")
	if r, err := filepath.Rel(root, srcPath); err == nil && !strings.HasPrefix(r, "..") {
		rel = r
	}
	return filepath.Join(trashDir, rel)
}

// Restore swaps a trashed original back to origPath, removing the
// encoded file at currentPath (when it differs, e.g. .mp4 → .mkv).
func Restore(trashPath, origPath, currentPath string) error {
	if _, err := os.Stat(trashPath); err != nil {
		return fmt.Errorf("original not in trash: %w", err)
	}
	if err := os.Rename(trashPath, origPath); err != nil {
		return fmt.Errorf("restore: %w", err)
	}
	if currentPath != "" && currentPath != origPath {
		_ = os.Remove(currentPath)
	}
	return nil
}

// retain hard-links the original into the trash tree; falls back to a
// streamed copy when the link crosses mergerfs branches (EXDEV).
func retain(srcPath, dst string, st *SrcStat) error {
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

// chromaTolerance is how far (of 255) the output's mean chroma may sit from
// the source's. Different matrices and scalers move it by a couple of levels;
// a corrupt readback moves it by ~128.
const chromaTolerance = 25.0

// chromaClose reports whether the output's mean chroma agrees with the source's.
func chromaClose(su, sv, ou, ov float64) bool {
	return abs(su-ou) <= chromaTolerance && abs(sv-ov) <= chromaTolerance
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// checkChroma samples the same three points of the source and the output and
// fails when the colour differs. Three points keep one scene change between the
// two decodes from being mistaken for corruption.
func checkChroma(ctx context.Context, srcPath, outPath string, dur float64) error {
	if dur <= 0 {
		return nil
	}
	at := []float64{dur * 0.2, dur * 0.5, dur * 0.8}
	su, sv, err := media.SampleChroma(ctx, srcPath, at, 8)
	if err != nil {
		return nil // can't judge from an unreadable source: don't fail the job over it
	}
	ou, ov, err := media.SampleChroma(ctx, outPath, at, 8)
	if err != nil {
		return fmt.Errorf("colour check: %w", err)
	}
	if !chromaClose(su, sv, ou, ov) {
		return fmt.Errorf("output colour is wrong (mean U/V %.0f/%.0f, source %.0f/%.0f): the upscaler produced corrupt frames",
			ou, ov, su, sv)
	}
	return nil
}

// AddCopy installs a finished encode beside its source instead of replacing
// it. The original is untouched and nothing goes to trash. The link makes it
// atomic and no-clobber: an existing destination is an error, never
// overwritten.
func AddCopy(tempPath, destPath string, st *SrcStat) error {
	if f, err := os.Open(tempPath); err == nil {
		_ = unix.Fsync(int(f.Fd()))
		f.Close()
	}
	if err := os.Link(tempPath, destPath); err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("%s already exists", destPath)
		}
		return err
	}
	if err := os.Remove(tempPath); err != nil {
		return err
	}
	if st != nil && st.Mode != 0 {
		_ = os.Chmod(destPath, os.FileMode(st.Mode&0o7777))
	}
	if d, err := os.Open(filepath.Dir(destPath)); err == nil {
		_ = unix.Fsync(int(d.Fd()))
		d.Close()
	}
	return nil
}
