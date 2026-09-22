package media

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Packet is one demuxed video packet: timestamp, byte size and whether
// it's a keyframe, straight from the container's own index - no decode.
type Packet struct {
	PTS  float64
	Size int64
	Key  bool
}

// PacketSizes dumps every video packet's timestamp, byte size and
// keyframe flag via ffprobe. This reads the container index only - no
// frame is decoded - so it's dramatically cheaper than any filter that
// needs to see pixels: ~9s for a 2.5-hour Bluray remux in testing,
// against minutes for a full decode pass. ffprobe returns packets in
// decode order, not presentation order (B-frames reorder the two), so
// this sorts by pts before returning.
func PacketSizes(ctx context.Context, path string) ([]Packet, error) {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, FFprobe, "-v", "quiet", "-select_streams", "v:0",
		"-show_entries", "packet=pts_time,size,flags", "-of", "json", path).Output()
	if err != nil {
		return nil, fmt.Errorf("packet sizes: %w", err)
	}
	return parsePackets(out)
}

func parsePackets(raw []byte) ([]Packet, error) {
	var doc struct {
		Packets []struct {
			PTSTime string `json:"pts_time"`
			Size    string `json:"size"`
			Flags   string `json:"flags"`
		} `json:"packets"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("packet sizes: %w", err)
	}
	pkts := make([]Packet, 0, len(doc.Packets))
	for _, p := range doc.Packets {
		pts, err := strconv.ParseFloat(p.PTSTime, 64)
		if err != nil {
			continue // "N/A" for a packet ffprobe couldn't timestamp
		}
		size, _ := strconv.ParseInt(p.Size, 10, 64)
		pkts = append(pkts, Packet{PTS: pts, Size: size, Key: strings.Contains(p.Flags, "K")})
	}
	// ffprobe lists packets in decode order; B-frames make that differ
	// from presentation order (seen live: 0.0, 0.167, 0.083, 0.042...).
	// Callers need presentation order to do anything time-based.
	sort.Slice(pkts, func(i, j int) bool { return pkts[i].PTS < pkts[j].PTS })
	return pkts, nil
}
