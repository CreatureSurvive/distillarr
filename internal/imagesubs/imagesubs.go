// SPDX-License-Identifier: GPL-3.0-or-later

// Package imagesubs handles PGS and VobSub image subtitle tracks:
// extracting them to sidecar files, and OCR'ing one representative track
// to text via tesseract (driven directly, not through pgsrip's CLI —
// testing found that CLI silently no-ops on a filename it doesn't
// recognize). for the research this
// package's design follows, including the finding that external image
// sidecars are not known to be selectable subtitle tracks in Jellyfin or
// Plex — "sidecar" mode is offered anyway, behind a UI warning, per user
// direction rather than dropping it.
package imagesubs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/CreatureSurvive/distillarr/internal/encode"
	"github.com/CreatureSurvive/distillarr/internal/media"
	"github.com/CreatureSurvive/distillarr/internal/store"
)

// Modes for Config.ImageSubsMode.
const (
	Keep    = ""
	Sidecar = "sidecar"
	OCR     = "ocr"
)

// Image subtitle codec names, as ffprobe reports them.
const (
	CodecPGS    = "hdmv_pgs_subtitle"
	CodecVobSub = "dvd_subtitle"
)

// IsImageSubtitle reports whether codec is a bitmap subtitle format.
func IsImageSubtitle(codec string) bool {
	return codec == CodecPGS || codec == CodecVobSub
}

// PrimaryTrack picks the one image subtitle track OCR mode
// processes: real-world PGS packs carry a dozen-plus foreign-language
// duplicates (one real sample had 16 tracks),
// so OCR — a real per-track cost — targets a single representative
// track rather than all of them. Preference: a track whose language
// matches origLangCode (arr's original_language, already normalized by
// the caller), else the disposition-default track, else the first image
// subtitle stream in source order.
func PrimaryTrack(subs []store.SubStream, origLangCode string) (store.SubStream, bool) {
	var byLang, byDefault, first *store.SubStream
	for i := range subs {
		s := &subs[i]
		if !IsImageSubtitle(s.Codec) {
			continue
		}
		if first == nil {
			first = s
		}
		if origLangCode != "" && Normalize(s.Lang) == origLangCode && byLang == nil {
			byLang = s
		}
		if s.Default && byDefault == nil {
			byDefault = s
		}
	}
	switch {
	case byLang != nil:
		return *byLang, true
	case byDefault != nil:
		return *byDefault, true
	case first != nil:
		return *first, true
	default:
		return store.SubStream{}, false
	}
}

// DropEntries returns drop entries for image subtitle tracks // modes remove from the output, skipping indices already present in
// existing so callers can append without duplicates:
//   - "sidecar" mode, keepOriginal false: every image subtitle track
//     (sidecar extraction covers all of them, so none need to stay
//     embedded).
//   - "ocr" mode, keepOriginal false: only ocrTrackIndex, and only when
//     the OCR attempt actually succeeded — the other, un-OCR'd image
//     tracks on the file stay untouched regardless (there is no text
//     replacement for them, so dropping them would just lose data).
func DropEntries(subs []store.SubStream, mode string, keepOriginal bool, ocrTrackIndex int, ocrSucceeded bool, existing []encode.SubTrack) []encode.SubTrack {
	if keepOriginal {
		return nil
	}
	already := map[int]bool{}
	for _, t := range existing {
		already[t.Index] = true
	}
	var out []encode.SubTrack
	switch mode {
	case Sidecar:
		for _, s := range subs {
			if IsImageSubtitle(s.Codec) && !already[s.Index] {
				out = append(out, encode.SubTrack{Index: s.Index, Action: "drop"})
			}
		}
	case OCR:
		if ocrSucceeded && !already[ocrTrackIndex] {
			out = append(out, encode.SubTrack{Index: ocrTrackIndex, Action: "drop"})
		}
	}
	return out
}

// ExtractedSidecar is one file ExtractSidecars created.
type ExtractedSidecar struct {
	Path  string
	Index int
	Lang  string
}

// tempPrefix mirrors internal/sidecar's own: a hidden, non-subtitle
// extension so a scan mid-extraction never mistakes it for a finished
// sidecar.
const tempPrefix = ".distillarr-imagesub-"

// ExtractSidecars pulls every image subtitle stream out of srcPath into
// sidecar files next to it: PGS as "Stem.<lang>.sup" (stream copy, no
// re-encode — a PGS bitmap can't be losslessly "copied" any other way),
// VobSub as a "Stem.<lang>.idx"/"Stem.<lang>.sub" pair (via ffmpeg's
// vobsub muxer). VobSub extraction is implemented per ffmpeg's
// documented muxer behavior but has no real VobSub source in this
// project's own test library to verify against. A name clash gets a numeric suffix;
// an existing sidecar is never overwritten. mode "" or anything other
// than "sidecar" is a no-op.
func ExtractSidecars(ctx context.Context, srcPath string, subs []media.Stream, mode string) ([]ExtractedSidecar, error) {
	if mode != Sidecar {
		return nil, nil
	}
	dir := filepath.Dir(srcPath)
	stem := strings.TrimSuffix(filepath.Base(srcPath), filepath.Ext(srcPath))
	var out []ExtractedSidecar
	for _, s := range subs {
		if !IsImageSubtitle(s.CodecName) {
			continue
		}
		if s.CodecName == CodecVobSub {
			paths, err := extractVobSub(ctx, srcPath, dir, stem, s)
			if err != nil {
				return out, err
			}
			out = append(out, paths...)
			continue
		}
		name := sidecarName(dir, stem, s.Lang(), ".sup")
		tmp := filepath.Join(dir, fmt.Sprintf("%s%d-%d.tmp", tempPrefix, os.Getpid(), s.Index))
		args := []string{"-y", "-i", srcPath, "-map", fmt.Sprintf("0:%d", s.Index), "-c", "copy", "-f", "sup", tmp}
		if b, err := exec.CommandContext(ctx, encode.FFmpeg, args...).CombinedOutput(); err != nil {
			os.Remove(tmp)
			return out, fmt.Errorf("extract sub #%d: %w: %s", s.Index, err, strings.TrimSpace(string(b)))
		}
		final := filepath.Join(dir, name)
		if err := os.Rename(tmp, final); err != nil {
			os.Remove(tmp)
			return out, err
		}
		out = append(out, ExtractedSidecar{Path: final, Index: s.Index, Lang: s.Lang()})
	}
	return out, nil
}

func extractVobSub(ctx context.Context, srcPath, dir, stem string, s media.Stream) ([]ExtractedSidecar, error) {
	base := sidecarBase(dir, stem, s.Lang())
	tmpBase := filepath.Join(dir, fmt.Sprintf("%s%d-%d.tmp", tempPrefix, os.Getpid(), s.Index))
	args := []string{"-y", "-i", srcPath, "-map", fmt.Sprintf("0:%d", s.Index), "-c", "copy", "-f", "vobsub", tmpBase + ".idx"}
	if b, err := exec.CommandContext(ctx, encode.FFmpeg, args...).CombinedOutput(); err != nil {
		os.Remove(tmpBase + ".idx")
		os.Remove(tmpBase + ".sub")
		return nil, fmt.Errorf("extract sub #%d: %w: %s", s.Index, err, strings.TrimSpace(string(b)))
	}
	idxFinal, subFinal := base+".idx", base+".sub"
	if err := os.Rename(tmpBase+".idx", idxFinal); err != nil {
		os.Remove(tmpBase + ".idx")
		os.Remove(tmpBase + ".sub")
		return nil, err
	}
	if err := os.Rename(tmpBase+".sub", subFinal); err != nil {
		os.Remove(idxFinal)
		os.Remove(tmpBase + ".sub")
		return nil, err
	}
	return []ExtractedSidecar{
		{Path: idxFinal, Index: s.Index, Lang: s.Lang()},
		{Path: subFinal, Index: s.Index, Lang: s.Lang()},
	}, nil
}

// sidecarBase returns dir/stem[.lang], clash-suffixed against both
// "<base>.idx" and "<base>.sub" so a VobSub pair never overwrites an
// existing one.
func sidecarBase(dir, stem, lang string) string {
	base := stem
	if lang != "" {
		base = stem + "." + lang
	}
	full := filepath.Join(dir, base)
	for n := 1; fileExists(full+".idx") || fileExists(full+".sub"); n++ {
		full = filepath.Join(dir, fmt.Sprintf("%s.%d", base, n))
	}
	return full
}

func sidecarName(dir, stem, lang, ext string) string {
	base := stem
	if lang != "" {
		base = stem + "." + lang
	}
	name := base + ext
	for n := 1; fileExists(filepath.Join(dir, name)); n++ {
		name = fmt.Sprintf("%s.%d%s", base, n, ext)
	}
	return name
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
