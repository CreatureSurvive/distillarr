// SPDX-License-Identifier: GPL-3.0-or-later

// Package sidecar extracts text subtitle tracks to external sidecar
// files. Off by default: embedded text subtitles already fit MP4
// as mov_text, so this only matters to users who specifically want
// sidecar files (e.g. for players that don't read embedded mov_text).
package sidecar

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

// Modes for Config.SubsSidecarMode / encode.Settings.SidecarMode.
const (
	Off           = ""
	ExtractKeep   = "extract_keep"
	ExtractRemove = "extract_remove"
)

// DropEntries returns new drop entries for embedded text subtitle
// streams, for extract_remove mode: those tracks move to sidecar files,
// so they come out of the output too. extract_keep leaves them embedded
// (the sidecar is an addition, not a replacement) and contributes
// nothing here. Indices already present in existing (e.g. a language
// pruning drop from the same fold) are skipped so callers can append the
// result without duplicate-index entries.
func DropEntries(subs []store.SubStream, mode string, existing []encode.SubTrack) []encode.SubTrack {
	if mode != ExtractRemove {
		return nil
	}
	already := map[int]bool{}
	for _, t := range existing {
		already[t.Index] = true
	}
	var out []encode.SubTrack
	for _, s := range subs {
		if s.IsText && !already[s.Index] {
			out = append(out, encode.SubTrack{Index: s.Index, Action: "drop"})
		}
	}
	return out
}

// ExtractedSidecar is one file Extract created.
type ExtractedSidecar struct {
	Path  string
	Index int
	Lang  string
}

// tempPrefix mirrors jobs.tempPrefixNew (unexported there): a hidden,
// non-subtitle-extension name so a scan mid-extraction never picks up
// the half-written file as a real sidecar (scan/parse.go's IsSubFile
// only checks the extension, and ".tmp" isn't in SubExts).
const tempPrefix = ".distillarr-sidecar-"

// Extract pulls every text subtitle stream out of srcPath into sidecar
// files next to it, named "Stem.<lang>.srt" (or ".ass" for ASS/SSA,
// whose styling is never flattened to SRT). A name clash with an
// existing file — including one this same call just created — gets a
// numeric suffix; an existing sidecar is never overwritten. Each stream
// is written to a temp name first, then renamed, so a crash never leaves
// a half-written file at its final name. mode "" or anything other than
// extract_keep/extract_remove is a no-op.
func Extract(ctx context.Context, srcPath string, subs []media.Stream, mode string) ([]ExtractedSidecar, error) {
	if mode != ExtractKeep && mode != ExtractRemove {
		return nil, nil
	}
	dir := filepath.Dir(srcPath)
	stem := strings.TrimSuffix(filepath.Base(srcPath), filepath.Ext(srcPath))
	var out []ExtractedSidecar
	for _, s := range subs {
		if !s.IsTextSubtitle() {
			continue
		}
		ext, muxer, codecArgs := ".srt", "srt", []string{"-c:s", "srt"}
		if s.CodecName == "ass" || s.CodecName == "ssa" {
			ext, muxer, codecArgs = ".ass", "ass", []string{"-c:s", "copy"}
		}
		name := sidecarName(dir, stem, s.Lang(), ext)
		// The temp name has no subtitle extension on purpose (see tempPrefix),
		// so ffmpeg can't infer the muxer from it: -f says so explicitly.
		tmp := filepath.Join(dir, fmt.Sprintf("%s%d-%d.tmp", tempPrefix, os.Getpid(), s.Index))
		args := []string{"-y", "-i", srcPath, "-map", fmt.Sprintf("0:%d", s.Index)}
		args = append(args, codecArgs...)
		args = append(args, "-f", muxer, tmp)
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
