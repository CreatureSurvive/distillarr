// Package media wraps ffprobe and models probe results.
package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Probe is the parsed `ffprobe -show_streams -show_format` JSON.
type Probe struct {
	Streams []Stream    `json:"streams"`
	Format  Format      `json:"format"`
}

// Stream is one probed stream.
type Stream struct {
	Index          int               `json:"index"`
	CodecName      string            `json:"codec_name"`
	CodecType      string            `json:"codec_type"` // video|audio|subtitle
	CodecTagString string            `json:"codec_tag_string"`
	Width          int               `json:"width,omitempty"`
	Height         int               `json:"height,omitempty"`
	PixFmt         string            `json:"pix_fmt,omitempty"`
	ColorTransfer  string            `json:"color_transfer,omitempty"`
	ColorPrimaries string            `json:"color_primaries,omitempty"`
	ColorSpace     string            `json:"color_space,omitempty"`
	FieldOrder     string            `json:"field_order,omitempty"` // progressive|tt|bb|tb|bt
	Channels       int               `json:"channels,omitempty"`
	Profile        string            `json:"profile,omitempty"` // e.g. "DTS-HD MA", "Dolby TrueHD + Dolby Atmos"
	BitRate        string            `json:"bit_rate,omitempty"`   // ffprobe emits a string
	AvgFrameRate   string            `json:"avg_frame_rate,omitempty"` // "24000/1001"
	Duration       string            `json:"duration,omitempty"`
	NBFrames       string            `json:"nb_frames,omitempty"`
	Disposition    map[string]int    `json:"disposition"`
	Tags           map[string]string `json:"tags"`
	SideDataList   []SideData        `json:"side_data_list"`
}

// SideData is stream side data (HDR metadata, DV config, ...).
type SideData struct {
	SideDataType string `json:"side_data_type"`
}

// Format is the container-level probe result.
type Format struct {
	Filename       string            `json:"filename"`
	FormatName     string            `json:"format_name"`
	Duration       string            `json:"duration"`
	Size           string            `json:"size"`
	BitRate        string            `json:"bit_rate"`
	NBStreams      int               `json:"nb_streams"`
	Tags           map[string]string `json:"tags"`
}

// FFprobe runs the real binary.
var FFprobe = "ffprobe"

// ProbeFile runs ffprobe on path and parses the JSON.
func ProbeFile(ctx context.Context, path string) (*Probe, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, FFprobe,
		"-v", "error",
		"-print_format", "json",
		"-show_format", "-show_streams",
		path).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			msg := strings.TrimSpace(string(ee.Stderr))
			if len(msg) > 300 {
				msg = msg[len(msg)-300:]
			}
			return nil, fmt.Errorf("ffprobe: %s", msg)
		}
		return nil, fmt.Errorf("ffprobe exec: %w", err)
	}
	var p Probe
	if err := json.Unmarshal(out, &p); err != nil {
		return nil, fmt.Errorf("ffprobe json: %w", err)
	}
	return &p, nil
}

// Video returns the first video stream.
func (p *Probe) Video() *Stream {
	for i := range p.Streams {
		if p.Streams[i].CodecType == "video" && !p.Streams[i].IsAttachedPic() {
			return &p.Streams[i]
		}
	}
	return nil
}

// Audios returns all audio streams.
func (p *Probe) Audios() []Stream {
	out := []Stream{}
	for _, s := range p.Streams {
		if s.CodecType == "audio" {
			out = append(out, s)
		}
	}
	return out
}

// Subtitles returns all subtitle streams.
func (p *Probe) Subtitles() []Stream {
	out := []Stream{}
	for _, s := range p.Streams {
		if s.CodecType == "subtitle" {
			out = append(out, s)
		}
	}
	return out
}

// IsAttachedPic reports mjpeg/png cover-art "video" streams.
func (s *Stream) IsAttachedPic() bool {
	return s.Disposition != nil && s.Disposition["attached_pic"] == 1
}

// Lang returns the stream language tag.
func (s *Stream) Lang() string {
	if s.Tags == nil {
		return ""
	}
	return strings.TrimSpace(s.Tags["language"])
}

// Title returns the stream title tag.
func (s *Stream) Title() string {
	if s.Tags == nil {
		return ""
	}
	return strings.TrimSpace(s.Tags["title"])
}

// BitRateInt parses the string bitrate.
func (s *Stream) BitRateInt() int64 {
	n, _ := strconv.ParseInt(s.BitRate, 10, 64)
	return n
}

// DurationSec parses the container duration.
func (p *Probe) DurationSec() float64 {
	f, _ := strconv.ParseFloat(p.Format.Duration, 64)
	return f
}

// SizeBytes parses the container size.
func (p *Probe) SizeBytes() int64 {
	n, _ := strconv.ParseInt(p.Format.Size, 10, 64)
	return n
}

// FPS parses an "num/den" frame rate.
func (s *Stream) FPS() float64 {
	parts := strings.SplitN(s.AvgFrameRate, "/", 2)
	num, err1 := strconv.ParseFloat(parts[0], 64)
	den := 1.0
	var err2 error
	if len(parts) == 2 {
		den, err2 = strconv.ParseFloat(parts[1], 64)
	}
	if err1 != nil || err2 != nil || den == 0 {
		return 0
	}
	return num / den
}

// BitDepth extracts bits-per-component from pix_fmt (yuv420p10le → 10,
// yuv420p → 8, p010le → 10). Digits that are part of the chroma
// subsampling (the 420 in yuv420p) are NOT the depth.
func (s *Stream) BitDepth() int {
	pf := s.PixFmt
	if pf == "" {
		return 0
	}
	// p010/p016 style planar 10-bit
	if strings.HasPrefix(pf, "p01") {
		return 10
	}
	// depth digits follow the planar 'p': yuv420p10le, gbrp12le...
	if i := strings.LastIndexByte(pf, 'p'); i >= 0 && i+3 <= len(pf) {
		two := pf[i+1 : i+3]
		if isDigits(two) {
			n, _ := strconv.Atoi(two)
			if n >= 8 && n <= 16 {
				return n
			}
		}
	}
	// gray10le, gray16be
	if strings.HasPrefix(pf, "gray") && len(pf) > 4 {
		d := strings.TrimRight(strings.TrimPrefix(pf, "gray"), "lebe")
		if n, err := strconv.Atoi(d); err == nil {
			return n
		}
	}
	return 8
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) == 2
}

// IsCommentary reports director/cast commentary tracks (audio or
// subtitle), detected from the disposition flag or a "commentary" title
// match (used by langprune).
func (s *Stream) IsCommentary() bool {
	if s.Disposition != nil && s.Disposition["comment"] == 1 {
		return true
	}
	return strings.Contains(strings.ToLower(s.Title()), "commentary")
}

// IsSDH reports hearing-impaired subtitle tracks, detected from the
// disposition flag or an "sdh"/"hearing impaired" title match (used by
// langprune).
func (s *Stream) IsSDH() bool {
	if s.Disposition != nil && s.Disposition["hearing_impaired"] == 1 {
		return true
	}
	t := strings.ToLower(s.Title())
	return strings.Contains(t, "sdh") || strings.Contains(t, "hearing impaired")
}

// IsTextSubtitle reports text-based (movable/editable) subtitle codecs.
func (s *Stream) IsTextSubtitle() bool {
	switch s.CodecName {
	case "subrip", "srt", "ass", "ssa", "webvtt", "text", "vtt", "mov_text", "microdvd", "subviewer":
		return true
	}
	return false
}

// HDRType detects the HDR flavor of the video stream.
// "", "hdr10", "hlg", "dolby_vision" (DV wins when both DV and HDR10).
func (s *Stream) HDRType() string {
	for _, sd := range s.SideDataList {
		if strings.Contains(sd.SideDataType, "Dolby Vision") {
			return "dolby_vision"
		}
	}
	switch s.CodecTagString {
	case "dvh1", "dvhe", "dva1", "dvav":
		return "dolby_vision"
	}
	switch s.ColorTransfer {
	case "smpte2084":
		return "hdr10"
	case "arib-std-b67":
		return "hlg"
	}
	return ""
}

// Interlaced reports field-coded video.
func (s *Stream) Interlaced() bool {
	switch s.FieldOrder {
	case "tt", "bb", "tb", "bt":
		return true
	}
	return false
}

// IsPCM reports uncompressed/linear audio codecs worth converting.
func (s *Stream) IsPCM() bool {
	switch {
	case strings.HasPrefix(s.CodecName, "pcm_"),
		s.CodecName == "lpcm",
		strings.Contains(s.CodecName, "adpcm"):
		return true
	}
	return false
}

// VideoBitrate computes the video stream bitrate: stream bit_rate when
// present, else (total bits − audio bits − subtitle bits) / duration,
// else total bitrate.
func (p *Probe) VideoBitrate() int64 {
	v := p.Video()
	if v == nil {
		return 0
	}
	if br := v.BitRateInt(); br > 0 {
		return br
	}
	total, _ := strconv.ParseInt(p.Format.BitRate, 10, 64)
	var audioBits int64
	for _, a := range p.Audios() {
		audioBits += a.BitRateInt()
	}
	if total > audioBits && audioBits > 0 {
		return total - audioBits
	}
	return total
}

// TotalBitrate parses the container bitrate.
func (p *Probe) TotalBitrate() int64 {
	n, _ := strconv.ParseInt(p.Format.BitRate, 10, 64)
	return n
}
