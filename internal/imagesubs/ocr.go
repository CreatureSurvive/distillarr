// SPDX-License-Identifier: GPL-3.0-or-later

package imagesubs

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/CreatureSurvive/distillarr/internal/store"
)

// PgsRip is the CLI binary name (installed via pip in the Docker image).
var PgsRip = "pgsrip"

// Tesseract is the CLI binary name.
var Tesseract = "tesseract"

// tessdataRepo is Google's official trained-model repository — Apache
// 2.0 licensed.
const tessdataRepoBase = "https://github.com/tesseract-ocr/tessdata_best/raw/main/"

// confidenceSampleSize caps how many of pgsrip's kept intermediate PNGs
// get re-OCR'd with tesseract's own TSV output to compute a real
// word-confidence average — matches the sample size used when this was tuned.
// Running against every image in a long PGS track would double the
// work for no meaningful precision gain over a representative sample.
const confidenceSampleSize = 20

// OCRResult is what RunOCR produces, cached verbatim (as JSON) in
// store.File.OCRJSON.
type OCRResult struct {
	TrackIndex  int     `json:"track_index"`
	Lang        string  `json:"lang"`
	Confidence  float64 `json:"confidence"`
	SRTName     string  `json:"srt_name,omitempty"` // basename only; caller knows the directory
	Failed      bool    `json:"failed"`
	Error       string  `json:"error,omitempty"`
	AttemptedAt string  `json:"attempted_at"`
}

// RunOCR extracts track from srcPath, OCRs it via pgsrip/tesseract, and
// — if the resulting word confidence clears minConfidence — writes an
// SRT sidecar next to srcPath (same naming/clash-suffix convention as
// internal/sidecar), returning the confidence either way so the
// caller can cache it. tessdataDir is where per-language traineddata
// lives; a missing language is downloaded into it on demand (Apache
// 2.0) and reused on every later
// call. Only PGS is supported — VobSub has no working OCR tool
// integrated here (see the package doc and the research doc's caveat);
// a VobSub track returns a plain error.
func RunOCR(ctx context.Context, srcPath string, track store.SubStream, minConfidence float64, tessdataDir string) (OCRResult, error) {
	res := OCRResult{TrackIndex: track.Index, Lang: track.Lang, AttemptedAt: time.Now().UTC().Format(time.RFC3339)}
	if track.Codec != CodecPGS {
		return res, fmt.Errorf("OCR only supports PGS tracks (got %q)", track.Codec)
	}
	normalized := Normalize(track.Lang)
	ietf, tess := ietf2For(normalized), tess3For(normalized)

	if err := ensureTessdata(ctx, tessdataDir, tess); err != nil {
		return res, fmt.Errorf("tessdata for %s: %w", tess, err)
	}

	work, err := os.MkdirTemp("", "distillarr-ocr-*")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(work)

	// pgsrip's CLI filters input files by a "<name>.<lang>.<ext>" naming
	// convention (a filename it doesn't
	// recognize is silently skipped, exit 0, no error) — name the
	// extracted stream to match exactly.
	supPath := filepath.Join(work, "track."+ietf+".sup")
	extractArgs := []string{"-y", "-i", srcPath, "-map", fmt.Sprintf("0:%d", track.Index), "-c", "copy", "-f", "sup", supPath}
	if b, err := exec.CommandContext(ctx, ffmpegBin(), extractArgs...).CombinedOutput(); err != nil {
		return res, fmt.Errorf("extract sub #%d: %w: %s", track.Index, err, strings.TrimSpace(string(b)))
	}

	ripArgs := []string{"rip", supPath, "--language", ietf, "--keep-temp-files",
		"--tessdata-dir", tessdataDir, "--no-tessdata-download"}
	ripOut, ripErr := exec.CommandContext(ctx, PgsRip, ripArgs...).CombinedOutput()
	srtPath := strings.TrimSuffix(supPath, ".sup") + ".srt"
	if ripErr != nil || !fileExists(srtPath) {
		res.Failed = true
		res.Error = fmt.Sprintf("pgsrip: %v: %s", ripErr, strings.TrimSpace(string(ripOut)))
		return res, nil
	}

	conf, cerr := sampleConfidence(ctx, supPath)
	if cerr != nil {
		res.Failed = true
		res.Error = "confidence sampling: " + cerr.Error()
		return res, nil
	}
	res.Confidence = conf
	if conf < minConfidence {
		res.Failed = true
		return res, nil
	}

	dir := filepath.Dir(srcPath)
	stem := strings.TrimSuffix(filepath.Base(srcPath), filepath.Ext(srcPath))
	finalName := sidecarName(dir, stem, track.Lang, ".srt")
	finalPath := filepath.Join(dir, finalName)
	if err := copyFile(srtPath, finalPath); err != nil {
		res.Failed = true
		res.Error = "installing sidecar: " + err.Error()
		return res, nil
	}
	res.SRTName = finalName
	return res, nil
}

// ffmpegBin avoids importing internal/encode just for its FFmpeg var
// (which would pull encode's own, larger dependency graph in) — same
// binary, same PATH resolution ("ffmpeg" is symlinked in the runtime
// image; see Dockerfile).
func ffmpegBin() string { return "ffmpeg" }

// ensureTessdata downloads tess+".traineddata" into dir if not already
// present. Apache 2.0 licensed.
func ensureTessdata(ctx context.Context, dir, tess string) error {
	path := filepath.Join(dir, tess+".traineddata")
	if fileExists(path) {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, tessdataRepoBase+tess+".traineddata", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: status %d", tess, resp.StatusCode)
	}
	tmp := path + ".downloading"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	return os.Rename(tmp, path)
}

// sampleConfidence re-OCRs up to confidenceSampleSize of pgsrip's kept
// intermediate PNG frames with tesseract's own TSV output, averaging the
// per-word confidence column — the same measurement used when this was tuned
// manually. --keep-temp-files does NOT write next to supPath (found
// live: pgsrip uses Python's system default temp directory, unrelated
// to wherever the input file actually sits) — its dirs land directly
// under os.TempDir(), named "<basename-of-supPath><random>.pgsrip", so
// that's where this globs, and it removes every match it finds
// afterward (pgsrip itself never cleans these up).
func sampleConfidence(ctx context.Context, supPath string) (float64, error) {
	pattern := filepath.Join(os.TempDir(), filepath.Base(supPath)+"*.pgsrip")
	dirs, err := filepath.Glob(pattern)
	if err != nil {
		return 0, err
	}
	defer func() {
		for _, d := range dirs {
			os.RemoveAll(d)
		}
	}()
	var pngs []string
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !d.IsDir() && strings.HasSuffix(strings.ToLower(p), ".png") {
				pngs = append(pngs, p)
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
	}
	if len(pngs) == 0 {
		return 0, fmt.Errorf("no intermediate frames found under %s (no dialogue in this track's span, or pgsrip's --keep-temp-files layout changed)", pattern)
	}
	if len(pngs) > confidenceSampleSize {
		pngs = pngs[:confidenceSampleSize]
	}
	var sum float64
	var n int
	for _, png := range pngs {
		out, err := exec.CommandContext(ctx, Tesseract, png, "-", "--psm", "6", "tsv").Output()
		if err != nil {
			continue // one unreadable frame doesn't sink the whole sample
		}
		sc := bufio.NewScanner(strings.NewReader(string(out)))
		first := true
		for sc.Scan() {
			if first { // header row
				first = false
				continue
			}
			cols := strings.Split(sc.Text(), "\t")
			if len(cols) < 11 {
				continue
			}
			conf, err := strconv.ParseFloat(cols[10], 64)
			if err != nil || conf < 0 {
				continue
			}
			sum += conf
			n++
		}
	}
	if n == 0 {
		return 0, fmt.Errorf("no words recognized in sampled frames")
	}
	return sum / float64(n), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// MarshalResult and UnmarshalResult round-trip OCRResult through
// store.File.OCRJSON, mirroring TuneJSON's plain-JSON caching.
func MarshalResult(r OCRResult) string {
	b, _ := json.Marshal(r)
	return string(b)
}

func UnmarshalResult(s string) (OCRResult, bool) {
	if s == "" {
		return OCRResult{}, false
	}
	var r OCRResult
	if json.Unmarshal([]byte(s), &r) != nil {
		return OCRResult{}, false
	}
	return r, true
}
